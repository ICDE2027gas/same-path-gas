// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: LGPL-3.0-or-later

package gasestimator

import (
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// CertificateReport describes the optional check of the high-limit execution
// within one Estimate invocation. Its validity is bound to that invocation's
// request, state, and EVM context. Rejection strings use fixed checker categories.
type CertificateReport struct {
	SourcePin string `json:"source_pin"`
	Status string `json:"status"`
	Fork string `json:"fork"`
	High uint64 `json:"high"`
	Intrinsic uint64 `json:"intrinsic"`
	// Floor is the calldata floor under Osaka rules and zero on the earlier supported forks.
	Floor uint64 `json:"floor,omitempty"`
	RootEntry uint64 `json:"root_entry"`
	Depletion uint64 `json:"depletion"`
	Minimum uint64 `json:"minimum"`
	Candidate uint64 `json:"candidate"`
	UsedGas uint64 `json:"used_gas"`
	MaxUsedGas uint64 `json:"max_used_gas"`
	FirstRunExecuted bool `json:"first_run_executed"`
	Certified bool `json:"certified"`
	ZeroEffectiveGasPrice bool `json:"zero_effective_gas_price"`
	EffectiveGasPrice string `json:"effective_gas_price"`
	Origin common.Address `json:"origin"`
	SenderCodeEmpty bool `json:"sender_code_empty"`
	OriginalSenderNonce uint64 `json:"original_sender_nonce"`
	FeeRule string `json:"fee_rule"`
	ReasonCounts map[string]uint64 `json:"reason_counts"`
	// ReasonCounts records the first failed check, where semantic checking stops.
	// Raw event counters below continue through the remaining execution.
	FirstReasonOnly bool `json:"first_reason_only"`
	OpcodeEvents uint64 `json:"opcode_events"`
	OpcodeErrorEvents uint64 `json:"opcode_error_events"`
	EnterEvents uint64 `json:"enter_events"`
	ExitEvents uint64 `json:"exit_events"`
	FailedFrameEvents uint64 `json:"failed_frame_events"`
	FaultEvents uint64 `json:"fault_events"`
	CheckedOpcodes uint64 `json:"checked_opcodes"`
	CompletedFrames uint64 `json:"completed_frames"`
	CallOpcodes uint64 `json:"call_opcodes"`
	CompletedCalls uint64 `json:"completed_calls"`
	GasCapCalls uint64 `json:"gas_cap_calls"`
	GasOpcodes uint64 `json:"gas_opcodes"`
	Sstores uint64 `json:"sstores"`
	WideFixedGasRequests uint64 `json:"wide_fixed_gas_requests"`
	MaxFrameDepth int `json:"max_frame_depth"`
}

// TrialReport records the gas limit and outcome of one execution attempt for
// optional functional diagnostics. Elapsed-time measurement and installation
// of the diagnostic callback are controlled separately by the caller.
type TrialReport struct {
	GasLimit uint64 `json:"gas_limit"`
	Failed bool `json:"failed"`
	Outcome string `json:"outcome"`
}

func initialCertificateReport() CertificateReport {
	return CertificateReport{
		SourcePin: "a579077007b98217c3e253a66e4b452ca0c32b96",
		Status: "not_reached", FirstReasonOnly: true,
		ReasonCounts: make(map[string]uint64),
	}
}

type certificateGas struct { pc, value uint64 }
type certificateCall struct {
	kind byte
	address common.Address
	value uint256.Int // copied before the VM mutates its stack storage
	request uint64
	wide bool
	capPattern bool
	gas, total, stipend uint64
	entered bool
	intrinsic uint64
}
type certificateFrame struct {
	depth int
	entry, used, need, opcodes uint64
	gas *certificateGas
	call *certificateCall
}
type certificateObserver struct {
	report CertificateReport
	reason string
	origin common.Address
	bound, rootSeen, rootDone bool
	frames []*certificateFrame
}

func newCertificateObserver() *certificateObserver {
	return &certificateObserver{report: initialCertificateReport()}
}

func (o *certificateObserver) reject(reason string) {
	if o.reason == "" {
		o.reason = reason
		o.report.ReasonCounts[reason]++
		o.frames = nil
	}
}

// bind checks eligibility against the EVM context and state created by run.
// It returns hooks after all preflight checks pass and nil on fallback.
func (o *certificateObserver) bind(call *core.Message, evm *vm.EVM) *tracing.Hooks {
	o.bound = true
	o.report.High = call.GasLimit
	o.report.FirstRunExecuted = true
	if !o.bindFee(call, evm.StateDB.GetCode(call.From), evm.StateDB.GetNonce(call.From)) { return nil }
	if call.To == nil { o.reject("top_creation"); return nil }
	if call.SetCodeAuthorizations != nil { o.reject("authorization_list"); return nil }
	r := evm.ChainConfig().Rules(evm.Context.BlockNumber, evm.Context.Random != nil, evm.Context.Time)
	if len(evm.Config.ExtraEips) != 0 {
		o.reject("unsupported_fork_or_extra_eip"); return nil
	}
	fork, reason := certificateFork(r)
	if reason != "" { o.reject(reason); return nil }
	o.report.Fork = fork
	if fork == "osaka" && (call.BlobHashes != nil || call.BlobGasFeeCap != nil) {
		o.reject("blob_fields"); return nil
	}
	value := call.Value
	if value == nil { value = new(uint256.Int) }
	intrinsic, err := core.IntrinsicGas(call.Data, call.AccessList, call.SetCodeAuthorizations, call.From, call.To, value, r)
	if err != nil || call.GasLimit < intrinsic { o.reject("intrinsic_invalid"); return nil }
	o.report.Intrinsic = intrinsic
	if fork == "osaka" {
		floor, err := core.FloorDataGas(r, call.From, call.To, value, call.Data, call.AccessList)
		if err != nil || call.GasLimit < floor { o.reject("floor_invalid"); return nil }
		o.report.Floor = floor
	}
	return &tracing.Hooks{
		OnEnterV2: o.enter, OnExitV2: o.exit,
		OnOpcodeV2: o.opcode, OnFaultV2: o.fault,
	}
}

func certificateFork(r params.Rules) (string, string) {
	if r.IsBogota || r.IsAmsterdam || r.IsUBT || r.IsEIP4762 {
		return "", "unsupported_fork_or_extra_eip"
	}
	if !(r.IsHomestead && r.IsEIP150 && r.IsEIP158 && r.IsByzantium && r.IsConstantinople && r.IsIstanbul) {
		return "", "unsupported_predecessor_rules"
	}
	if r.IsOsaka && r.IsPrague && r.IsCancun && r.IsShanghai && r.IsMerge && r.IsLondon && r.IsBerlin {
		return "osaka", ""
	}
	if r.IsOsaka || r.IsPrague || r.IsCancun { return "", "unsupported_fork_or_extra_eip" }
	switch {
	case r.IsShanghai && r.IsMerge && r.IsLondon && r.IsBerlin:
		return "shanghai", ""
	case !r.IsShanghai && !r.IsMerge && !r.IsLondon && !r.IsBerlin:
		return "istanbul", ""
	default:
		return "", "unsupported_fork"
	}
}

// bindFee checks sender eligibility and is shared with focused tests. bind
// passes the sender's code and nonce from StateDB before ApplyMessage.
func (o *certificateObserver) bindFee(call *core.Message, senderCode []byte, senderNonce uint64) bool {
	if call.GasPrice == nil {
		o.reject("missing_effective_gas_price")
		return false
	}
	o.origin, o.report.Origin = call.From, call.From
	o.report.EffectiveGasPrice = call.GasPrice.ToBig().String()
	o.report.ZeroEffectiveGasPrice = call.GasPrice.IsZero()
	o.report.FeeRule = "sender_balance_offset_1713"
	o.report.OriginalSenderNonce = senderNonce
	if senderNonce == math.MaxUint64 { o.reject("sender_nonce_overflow"); return false }
	// Sender-code eligibility uses the original per-run state copy before
	// ApplyMessage buys gas, independently of the skipEOACheck message flag.
	if len(senderCode) != 0 { o.reject("sender_code"); return false }
	o.report.SenderCodeEmpty = true
	return true
}

func addCertificate(a, b uint64) (uint64, bool) {
	if a > math.MaxUint64-b { return 0, false }
	return a+b, true
}

func phiCertificate(k uint64) (uint64, bool) {
	if k == 0 { return 0, true }
	return addCertificate(k, (k-1)/63)
}

func isCertificateCall(op byte) bool {
	return op == byte(vm.CALL) || op == byte(vm.CALLCODE) || op == byte(vm.DELEGATECALL) || op == byte(vm.STATICCALL)
}

func (o *certificateObserver) enter(depth int, kind byte, from, to common.Address, input []byte, gas tracing.Gas, value *big.Int) {
	o.report.EnterEvents++
	if o.reason != "" { return }
	if gas.State != 0 { o.reject("state_gas"); return }
	if depth < 0 || depth > int(params.CallCreateDepth) { o.reject("frame_depth"); return }
	if len(o.frames) == 0 {
		if o.rootSeen || depth != 0 || kind != byte(vm.CALL) { o.reject("root_frame_shape"); return }
		if gas.Execution != o.report.High-o.report.Intrinsic { o.reject("root_entry_mismatch"); return }
		o.rootSeen = true
		o.report.RootEntry = gas.Execution
	} else {
		parent := o.frames[len(o.frames)-1]
		p := parent.call
		if depth != parent.depth+1 || p == nil || p.entered || p.kind != kind || p.address != to {
			o.reject("child_entry_shape"); return
		}
		if (kind == byte(vm.CALL) || kind == byte(vm.CALLCODE)) && (value == nil || value.Cmp(p.value.ToBig()) != 0) {
			o.reject("child_value_mismatch"); return
		}
		if gas.Execution < p.stipend { o.reject("stipend_underflow"); return }
		forward := gas.Execution-p.stipend
		if p.total < forward { o.reject("forward_exceeds_cost"); return }
		p.intrinsic = p.total-forward
		if p.gas < p.intrinsic { o.reject("call_cost_exceeds_gas"); return }
		available := p.gas-p.intrinsic
		cap := available-available/64
		expected := cap
		if !p.wide && p.request < expected { expected = p.request }
		if forward != expected { o.reject("forward_mismatch"); return }
		p.entered = true
	}
	o.frames = append(o.frames, &certificateFrame{depth: depth, entry: gas.Execution})
	if depth > o.report.MaxFrameDepth { o.report.MaxFrameDepth = depth }
}

func (o *certificateObserver) opcode(pc uint64, op byte, gas, cost tracing.Gas, scope tracing.OpContext, rData []byte, depth int, err error) {
	o.report.OpcodeEvents++
	if err != nil { o.report.OpcodeErrorEvents++ }
	if o.reason != "" { return }
	if err != nil { o.reject("opcode_error"); return }
	if gas.State != 0 || cost.State != 0 { o.reject("state_gas"); return }
	if scope == nil { o.reject("missing_scope"); return }
	if scope.Address() == o.origin { o.reject("origin_execution_scope"); return }
	if op == byte(vm.BALANCE) {
		stack := scope.StackData()
		if len(stack) == 0 { o.reject("balance_stack_shape"); return }
		if common.Address(stack[len(stack)-1].Bytes20()) == o.origin { o.reject("origin_balance_read"); return }
	}
	if len(o.frames) == 0 { o.reject("opcode_without_frame"); return }
	f := o.frames[len(o.frames)-1]
	if depth != f.depth+1 || f.call != nil { o.reject("opcode_frame_shape"); return }
	if f.used > f.entry || gas.Execution != f.entry-f.used || cost.Execution > gas.Execution {
		o.reject("opcode_budget_mismatch"); return
	}
	if f.gas != nil {
		if !isCertificateCall(op) || f.gas.pc == math.MaxUint64 || pc != f.gas.pc+1 {
			o.reject("gas_not_immediate_call"); return
		}
	}
	if op == byte(vm.CREATE) || op == byte(vm.CREATE2) || op == byte(vm.SELFDESTRUCT) {
		o.reject("creation_or_selfdestruct"); return
	}
	f.opcodes++
	o.report.CheckedOpcodes++
	if isCertificateCall(op) {
		o.report.CallOpcodes++
		stack := scope.StackData()
		n := len(stack)
		want := 6
		if op == byte(vm.CALL) || op == byte(vm.CALLCODE) { want = 7 }
		if n < want { o.reject("call_stack_shape"); return }
		request := stack[n-1]
		p := &certificateCall{kind: op, address: common.Address(stack[n-2].Bytes20()), gas: gas.Execution, total: cost.Execution, request: request.Uint64(), wide: !request.IsUint64()}
		if p.wide { o.report.WideFixedGasRequests++ }
		if op == byte(vm.CALL) || op == byte(vm.CALLCODE) {
			p.value = stack[n-3]
			if !p.value.IsZero() { p.stipend = params.CallStipend }
		}
		if f.gas != nil && (p.wide || p.request != f.gas.value) {
			o.reject("gas_word_mismatch"); return
		}
		p.capPattern = f.gas != nil
		if p.capPattern { o.report.GasCapCalls++ }
		f.gas = nil
		f.call = p
		return
	}
	required := cost.Execution
	if op == byte(vm.SSTORE) {
		o.report.Sstores++
		required = max(required, params.SstoreSentryGasEIP2200+1)
	}
	need, ok := addCertificate(f.used, required)
	if !ok { o.reject("arithmetic_overflow"); return }
	used, ok := addCertificate(f.used, cost.Execution)
	if !ok { o.reject("arithmetic_overflow"); return }
	f.need, f.used = max(f.need, need), used
	if op == byte(vm.GAS) {
		o.report.GasOpcodes++
		f.gas = &certificateGas{pc: pc, value: gas.Execution-cost.Execution}
	}
}

func (o *certificateObserver) fault(pc uint64, op byte, gas, cost tracing.Gas, scope tracing.OpContext, depth int, err error) {
	o.report.FaultEvents++
	o.reject("opcode_fault")
}

func (o *certificateObserver) exit(depth int, output []byte, left tracing.Gas, err error, reverted bool) {
	o.report.ExitEvents++
	if err != nil || reverted { o.report.FailedFrameEvents++ }
	if o.reason != "" { return }
	if err != nil || reverted { o.reject("failed_frame"); return }
	if left.State != 0 { o.reject("state_gas"); return }
	if len(o.frames) == 0 { o.reject("exit_without_frame"); return }
	f := o.frames[len(o.frames)-1]
	if f.depth != depth || f.call != nil || f.gas != nil { o.reject("incomplete_frame"); return }
	if f.opcodes == 0 { o.reject("opaque_or_empty_frame"); return }
	if left.Execution > f.entry || f.entry-left.Execution != f.used { o.reject("frame_depletion_mismatch"); return }
	f.need = max(f.need, f.used)
	if f.entry < f.need { o.reject("observed_requirement_mismatch"); return }
	o.report.CompletedFrames++
	o.frames = o.frames[:len(o.frames)-1]
	if len(o.frames) == 0 {
		o.rootDone = true
		o.report.Depletion, o.report.Minimum = f.used, f.need
		return
	}
	parent := o.frames[len(o.frames)-1]
	p := parent.call
	if p == nil || !p.entered { o.reject("child_exit_shape"); return }
	k := uint64(0)
	if f.need > p.stipend { k = f.need-p.stipend }
	if !p.capPattern && !p.wide && p.request < k { o.reject("fixed_request_below_requirement"); return }
	phi, ok := phiCertificate(k)
	if !ok { o.reject("arithmetic_overflow"); return }
	required, ok := addCertificate(p.intrinsic, phi)
	if !ok { o.reject("arithmetic_overflow"); return }
	depletion, ok := addCertificate(p.intrinsic, f.used)
	if !ok || depletion < p.stipend { o.reject("call_depletion_arithmetic"); return }
	depletion -= p.stipend
	need, ok := addCertificate(parent.used, required)
	if !ok { o.reject("arithmetic_overflow"); return }
	used, ok := addCertificate(parent.used, depletion)
	if !ok { o.reject("arithmetic_overflow"); return }
	parent.need, parent.used = max(parent.need, need, used), used
	parent.call = nil
	o.report.CompletedCalls++
}

func (o *certificateObserver) finish(result *core.ExecutionResult, executionErr error, failed bool) CertificateReport {
	if result != nil { o.report.UsedGas, o.report.MaxUsedGas = result.UsedGas, result.MaxUsedGas }
	if executionErr != nil || failed { o.reject("first_run_failed") }
	if !o.bound || !o.rootDone || len(o.frames) != 0 { o.reject("incomplete_observation") }
	if o.reason == "" {
		candidate, ok := addCertificate(o.report.Intrinsic, o.report.Minimum)
		candidate = max(candidate, o.report.Floor)
		if !ok || candidate > o.report.High { o.reject("candidate_out_of_bounds") } else {
			o.report.Candidate, o.report.Certified = candidate, true
		}
	}
	o.report.Status = "unsupported"
	if executionErr != nil || failed { o.report.Status = "first_run_failed" }
	if o.report.Certified { o.report.Status = "certified" }
	return o.report
}
