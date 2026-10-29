// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: LGPL-3.0-or-later

package gasestimator

import (
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// These unit fixtures feed synthetic events to the production hook consumer.
// Opcode prices and frame-hook delivery require native EVM qualification with
// the supported Geth version.
type certificateScopeFixture struct {
	stack []uint256.Int
	address common.Address
}
func (s certificateScopeFixture) MemoryData() []byte { return nil }
func (s certificateScopeFixture) StackData() []uint256.Int { return s.stack }
func (s certificateScopeFixture) Caller() common.Address { return common.Address{} }
func (s certificateScopeFixture) Address() common.Address { return s.address }
func (s certificateScopeFixture) CallValue() *uint256.Int { return new(uint256.Int) }
func (s certificateScopeFixture) CallInput() []byte { return nil }
func (s certificateScopeFixture) ContractCode() []byte { return []byte{0} }

func certificateFixture(entry uint64) *certificateObserver {
	o := newCertificateObserver()
	o.bound = true
	o.report.High, o.report.Intrinsic = entry+21000, 21000
	o.report.FirstRunExecuted = true
	o.origin = common.HexToAddress("0x01")
	o.enter(0, byte(vm.CALL), o.origin, common.HexToAddress("0x02"), nil, tracing.Gas{Execution: entry}, new(big.Int))
	return o
}
func certificateStep(o *certificateObserver, depth int, pc uint64, op vm.OpCode, gas, cost uint64, stack []uint256.Int) {
	o.opcode(pc, byte(op), tracing.Gas{Execution: gas}, tracing.Gas{Execution: cost}, certificateScopeFixture{stack: stack, address: common.HexToAddress("0x02")}, nil, depth+1, nil)
}
func certificateCallStack(kind vm.OpCode, request, value uint64) []uint256.Int {
	n := 6
	if kind == vm.CALL || kind == vm.CALLCODE { n = 7 }
	s := make([]uint256.Int, n)
	s[n-1].SetUint64(request)
	s[n-2].SetUint64(3)
	if n == 7 { s[n-3].SetUint64(value) }
	return s
}
func certificateFinish(o *certificateObserver, left uint64) CertificateReport {
	o.exit(0, nil, tracing.Gas{Execution: left}, nil, false)
	return o.finish(&core.ExecutionResult{}, nil, false)
}

func TestCertificatePhiBoundaries(t *testing.T) {
	for k := uint64(0); k <= 8192; k++ {
		x, ok := phiCertificate(k)
		if !ok || x-x/64 < k || (x > 0 && (x-1)-(x-1)/64 >= k) { t.Fatalf("nonminimal inverse k=%d x=%d", k, x) }
	}
	if _, ok := phiCertificate(math.MaxUint64); ok { t.Fatal("inverse overflow accepted") }
	if _, ok := addCertificate(math.MaxUint64, 1); ok { t.Fatal("addition overflow accepted") }
	if x, ok := addCertificate(math.MaxUint64, 0); !ok || x != math.MaxUint64 { t.Fatal("boundary addition rejected") }
}

func TestCertificateSstoreAndFinalDepletion(t *testing.T) {
	o := certificateFixture(5000)
	certificateStep(o, 0, 0, vm.SSTORE, 5000, 100, nil)
	certificateStep(o, 0, 1, vm.STOP, 4900, 0, nil)
	r := certificateFinish(o, 4900)
	if !r.Certified || r.Depletion != 100 || r.Minimum != 2301 || r.Candidate != 23301 { t.Fatalf("sentry report %+v", r) }
	o = certificateFixture(5000)
	certificateStep(o, 0, 0, vm.KECCAK256, 5000, 3000, nil)
	r = certificateFinish(o, 2000)
	if !r.Certified || r.Minimum != 3000 { t.Fatalf("final depletion omitted %+v", r) }
}

func TestCertificateCallTransfers(t *testing.T) {
	for _, kind := range []vm.OpCode{vm.CALL, vm.CALLCODE, vm.DELEGATECALL, vm.STATICCALL} {
		t.Run(kind.String(), func(t *testing.T) {
			o := certificateFixture(20000)
			// DELEGATECALL inherits the call value with a zero stipend.
			certificateStep(o, 0, 0, kind, 20000, 600, certificateCallStack(kind, 500, 0))
			v := new(big.Int)
			if kind == vm.DELEGATECALL { v.SetUint64(1) }
			o.enter(1, byte(kind), common.HexToAddress("0x02"), common.HexToAddress("0x03"), nil, tracing.Gas{Execution: 500}, v)
			certificateStep(o, 1, 0, vm.KECCAK256, 500, 300, nil)
			o.exit(1, nil, tracing.Gas{Execution: 200}, nil, false)
			certificateStep(o, 0, 1, vm.STOP, 19600, 0, nil)
			r := certificateFinish(o, 19600)
			if !r.Certified || r.Depletion != 400 || r.Minimum != 404 || r.CompletedCalls != 1 { t.Fatalf("returned gas report %+v", r) }
		})
	}
	for _, kind := range []vm.OpCode{vm.CALL, vm.CALLCODE} {
		o := certificateFixture(20000)
		certificateStep(o, 0, 0, kind, 20000, 10100, certificateCallStack(kind, 1000, 1))
		o.enter(1, byte(kind), common.HexToAddress("0x02"), common.HexToAddress("0x03"), nil, tracing.Gas{Execution: 3300}, big.NewInt(1))
		certificateStep(o, 1, 0, vm.SSTORE, 3300, 100, nil)
		o.exit(1, nil, tracing.Gas{Execution: 3200}, nil, false)
		r := certificateFinish(o, 13100)
		if !r.Certified || r.Depletion != 6900 || r.Minimum != 9101 { t.Fatalf("stipend report %+v", r) }
	}
	// The next CALL consumes GAS immediately, so forwarding follows the
	// EIP-150 cap as the execution budget changes.
	o := certificateFixture(20000)
	certificateStep(o, 0, 0, vm.GAS, 20000, 2, nil)
	f := uint64(19898-19898/64)
	certificateStep(o, 0, 1, vm.CALL, 19998, 100+f, certificateCallStack(vm.CALL, 19998, 0))
	o.enter(1, byte(vm.CALL), common.HexToAddress("0x02"), common.HexToAddress("0x03"), nil, tracing.Gas{Execution: f}, new(big.Int))
	certificateStep(o, 1, 0, vm.KECCAK256, f, 300, nil)
	o.exit(1, nil, tracing.Gas{Execution: f-300}, nil, false)
	r := certificateFinish(o, 19598)
	if !r.Certified || r.Minimum != 406 || r.GasCapCalls != 1 { t.Fatalf("GAS cap report %+v", r) }
	// Two sequential child calls reclaim returned gas before the next one.
	o = certificateFixture(20000)
	for i := 0; i < 2; i++ {
		before := uint64(20000-i*400)
		certificateStep(o, 0, uint64(i), vm.CALL, before, 600, certificateCallStack(vm.CALL, 500, 0))
		o.enter(1, byte(vm.CALL), common.HexToAddress("0x02"), common.HexToAddress("0x03"), nil, tracing.Gas{Execution: 500}, new(big.Int))
		certificateStep(o, 1, 0, vm.KECCAK256, 500, 300, nil)
		o.exit(1, nil, tracing.Gas{Execution: 200}, nil, false)
	}
	r = certificateFinish(o, 19200)
	if !r.Certified || r.Depletion != 800 || r.Minimum != 804 || r.CompletedCalls != 2 { t.Fatalf("sequential calls %+v", r) }
}

func TestCertificateRejectsUnqualifiedEvents(t *testing.T) {
	cases := []struct {name, reason string; run func(*certificateObserver)}{
		{"gas_use", "gas_not_immediate_call", func(o *certificateObserver) { certificateStep(o,0,0,vm.GAS,5000,2,nil); certificateStep(o,0,1,vm.POP,4998,2,nil) }},
		{"gas_word", "gas_word_mismatch", func(o *certificateObserver) { certificateStep(o,0,0,vm.GAS,5000,2,nil); certificateStep(o,0,1,vm.CALL,4998,600,certificateCallStack(vm.CALL,4997,0)) }},
		{"depletion", "frame_depletion_mismatch", func(o *certificateObserver) { certificateStep(o,0,0,vm.STOP,5000,0,nil); o.exit(0,nil,tracing.Gas{Execution:4999},nil,false) }},
		{"empty", "opaque_or_empty_frame", func(o *certificateObserver) { o.exit(0,nil,tracing.Gas{Execution:5000},nil,false) }},
		{"failed", "failed_frame", func(o *certificateObserver) { o.exit(0,nil,tracing.Gas{},errors.New("fixture"),true) }},
		{"failed_child", "failed_frame", func(o *certificateObserver) { certificateStep(o,0,0,vm.CALL,5000,600,certificateCallStack(vm.CALL,500,0)); o.enter(1,byte(vm.CALL),common.HexToAddress("0x02"),common.HexToAddress("0x03"),nil,tracing.Gas{Execution:500},new(big.Int)); o.exit(1,nil,tracing.Gas{Execution:500},errors.New("fixture"),true); o.exit(0,nil,tracing.Gas{Execution:4900},nil,false) }},
		{"state", "state_gas", func(o *certificateObserver) { o.opcode(0,byte(vm.STOP),tracing.Gas{Execution:5000,State:1},tracing.Gas{},certificateScopeFixture{},nil,1,nil) }},
		{"create", "creation_or_selfdestruct", func(o *certificateObserver) { certificateStep(o,0,0,vm.CREATE,5000,1,nil) }},
		{"selfdestruct", "creation_or_selfdestruct", func(o *certificateObserver) { certificateStep(o,0,0,vm.SELFDESTRUCT,5000,1,nil) }},
		{"scope", "origin_execution_scope", func(o *certificateObserver) { o.opcode(0,byte(vm.STOP),tracing.Gas{Execution:5000},tracing.Gas{},certificateScopeFixture{address:o.origin},nil,1,nil) }},
		{"origin_balance", "origin_balance_read", func(o *certificateObserver) { s:=make([]uint256.Int,1); s[0].SetUint64(1); certificateStep(o,0,0,vm.BALANCE,5000,700,s) }},
		{"unmatched", "incomplete_frame", func(o *certificateObserver) { certificateStep(o,0,0,vm.CALL,5000,600,certificateCallStack(vm.CALL,500,0)); o.exit(0,nil,tracing.Gas{Execution:4400},nil,false) }},
		{"fault", "opcode_fault", func(o *certificateObserver) { o.fault(0,0,tracing.Gas{},tracing.Gas{},nil,1,errors.New("fixture")) }},
		{"forward", "forward_mismatch", func(o *certificateObserver) { certificateStep(o,0,0,vm.CALL,5000,600,certificateCallStack(vm.CALL,500,0)); o.enter(1,byte(vm.CALL),common.HexToAddress("0x02"),common.HexToAddress("0x03"),nil,tracing.Gas{Execution:499},new(big.Int)) }},
	}
	for _, tc := range cases { t.Run(tc.name,func(t *testing.T) {
		o:=certificateFixture(5000); tc.run(o)
		r:=o.finish(&core.ExecutionResult{},nil,false)
		if r.Certified || r.ReasonCounts[tc.reason]!=1 { t.Fatalf("wrong rejection %+v",r) }
	}) }
}

func TestCertificateFeeRelationEligibility(t *testing.T) {
	for _, price := range []uint64{0,1,20000000000} {
		o:=certificateFixture(5000)
		call:=&core.Message{From:o.origin,GasPrice:uint256.NewInt(price)}
		if !o.bindFee(call,nil,0) || o.report.ZeroEffectiveGasPrice!=(price==0) || !o.report.SenderCodeEmpty { t.Fatal("fee eligibility rejected") }
		certificateStep(o,0,0,vm.STOP,5000,0,nil)
		if r:=certificateFinish(o,5000); !r.Certified { t.Fatalf("positive-price eligible trace rejected %+v",r) }
		o=newCertificateObserver()
		if o.bindFee(call,[]byte{0},0) || o.report.ReasonCounts["sender_code"]!=1 { t.Fatal("sender code accepted") }
		o=newCertificateObserver()
		if o.bindFee(call,nil,math.MaxUint64) || o.report.ReasonCounts["sender_nonce_overflow"]!=1 { t.Fatal("sender nonce wrap accepted") }
	}
}

func TestCertificateForkEligibility(t *testing.T) {
	r:=params.Rules{IsHomestead:true,IsEIP150:true,IsEIP158:true,IsByzantium:true,IsConstantinople:true,IsIstanbul:true}
	if f,e:=certificateFork(r); f!="istanbul" || e!="" { t.Fatal("Istanbul rejected") }
	r.IsBerlin=true
	if _,e:=certificateFork(r); e=="" { t.Fatal("Berlin mislabelled Istanbul") }
	r.IsLondon,r.IsMerge,r.IsShanghai=true,true,true
	if f,e:=certificateFork(r); f!="shanghai" || e!="" { t.Fatal("Shanghai rejected") }
	r.IsCancun=true
	if _,e:=certificateFork(r); e=="" { t.Fatal("Cancun mislabelled Shanghai") }
}
