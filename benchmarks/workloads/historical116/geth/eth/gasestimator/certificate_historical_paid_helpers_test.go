// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: LGPL-3.0-or-later
package gasestimator
import (
 "context"
 "errors"
 "fmt"
 "reflect"
 "time"
 "github.com/ethereum/go-ethereum/core"
 "github.com/ethereum/go-ethereum/core/state"
 "github.com/ethereum/go-ethereum/core/vm"
)
type gasPaidFunctionalSummary struct {
    Binary gasFixtureSpec `json:"test_binary"`
    Schema string `json:"schema"`; Complete bool `json:"complete"`; Errors []string `json:"errors"`
    Counts map[string]uint64 `json:"counts"`; InputsPreserved bool `json:"inputs_preserved"`
    FullRows gasFixtureSpec `json:"full_rows"`; MaterializationReceipt *gasFixtureSpec `json:"materialization_receipt"`
    InputFiles map[string]gasFixtureSpec `json:"input_files_before_after"`
    CaseInputCounts []int `json:"case_input_counts"`
}
type gasPaidPrepared struct {
    send gasFixtureSend; expected gasFixtureRow
    chain *gasFixtureChain; base *state.StateDB; message *core.Message
}
type gasPaidRequest struct {
    RequestIndex int `json:"request_index"`; Phase string `json:"phase"`; PairID int `json:"pair_id"`
    BlockID int `json:"block_id"`; Position int `json:"position"`; Mode string `json:"mode"`
    ExecutionSeq int `json:"execution_seq"`; RPCSeq int `json:"rpc_seq"`; CaseID int `json:"case_id"`
    CaseTitle string `json:"case_title"`; OriginalPhase string `json:"original_phase"`; Ordinal int `json:"phase_send_ordinal"`
    TransactionHash string `json:"transaction_hash"`; Input gasFixtureSpec `json:"input"`
    ExpectedStatus int `json:"expected_status"`; FunctionallyCertified bool `json:"functionally_certified"`
    SuccessfulNonshortcut bool `json:"successful_nonshortcut"`
    ElapsedNS int64 `json:"elapsed_ns"`; Estimate gasFixtureEstimate `json:"estimate"`
    Certificate *CertificateReport `json:"certificate"`; Panic string `json:"panic"`; Errors []string `json:"errors"`
}
type gasPaidAggregate struct {
    Requests int `json:"requests"`; PaidNS int64 `json:"paid_ns"`; Errors int `json:"errors"`
}
type gasPaidGroup struct {
    Phase string `json:"phase"`; Mode string `json:"mode"`; Label string `json:"label"`
    gasPaidAggregate
}
type gasPaidBlock struct {
    Phase string `json:"phase"`; PairID int `json:"pair_id"`; BlockID int `json:"block_id"`; Mode string `json:"mode"`
    RequestFirst int `json:"request_first"`; RequestLast int `json:"request_last"`
    gasPaidAggregate
    BlockWallNS int64 `json:"block_wall_ns_including_outside_timer_validation_io"`
}

// message decoding is shared outside the timer. The single service interval
// includes the existing functional path's per-request snapshot/Options setup
// and the entire real Estimate call, including its own state copies and VM work.
func gasPaidEstimate(p *gasPaidPrepared, mode string) (r gasPaidRequest) {
    message := *p.message
    start := time.Now()
    defer func() {
        if value := recover(); value != nil {
            r.ElapsedNS = time.Since(start).Nanoseconds()
            r.Panic = fmt.Sprint(value)
        }
    }()
    if mode == "certificate" { r.Certificate = new(CertificateReport) }
    options := &Options{Config:p.chain.config, Chain:p.chain, Header:p.chain.current,
        State:p.base.Copy(), ErrorRatio:0.015, Certificate:r.Certificate, OnTrial:nil}
    limit, data, err := Estimate(context.Background(), &message, options, 0)
    r.ElapsedNS = time.Since(start).Nanoseconds()
    // These formatting and validation operations are outside the service timer.
    r.Estimate = gasFixtureEstimate{Estimate:limit, ReturnData:gasFixtureHexOut(data),
        Error:gasFixtureError(err), Reverted:errors.Is(err, vm.ErrExecutionReverted)}
    return r
}
func gasPaidCheck(r *gasPaidRequest, p *gasPaidPrepared) {
    r.Errors = make([]string,0)
    if r.Panic != "" { r.Errors = append(r.Errors,"estimator panic: "+r.Panic) }
    if r.ElapsedNS <= 0 { r.Errors = append(r.Errors,"nonpositive monotonic service interval") }
    expected := p.expected.Stock
    if r.Mode == "certificate" { expected = p.expected.CertificateEstimate }
    if r.Estimate.Estimate != expected.Estimate || r.Estimate.ReturnData != expected.ReturnData ||
        r.Estimate.Error != expected.Error || r.Estimate.Reverted != expected.Reverted {
        r.Errors = append(r.Errors,"estimate/error/revert/return differs from full functional row")
    }
    if len(r.Estimate.Trials) != 0 { r.Errors = append(r.Errors,"functional trial callback unexpectedly populated") }
    if r.Mode == "certificate" {
        if r.Certificate == nil || !reflect.DeepEqual(*r.Certificate,p.expected.Certificate) {
            r.Errors = append(r.Errors,"complete certificate report differs from full functional row")
        }
    } else if r.Certificate != nil { r.Errors = append(r.Errors,"stock unexpectedly enabled certificate") }
}
func gasPaidPrepare(send gasFixtureSend, expected gasFixtureRow) (*gasPaidPrepared,error) {
    var in gasFixtureInput; var result gasFixtureResult
    if err:=gasFixtureRead(send.Input,&in);err!=nil{return nil,err}
    if err:=gasFixtureRead(send.Result,&result);err!=nil{return nil,err}
    if len(expected.Errors)!=0 || expected.ExecutionSeq!=send.ExecutionSeq || expected.RPCSeq!=send.RPCSeq ||
        expected.CaseID!=send.CaseID || expected.CaseTitle!=send.CaseTitle || expected.Phase!=send.Phase ||
        expected.Ordinal!=send.Ordinal || expected.TransactionHash!=send.TransactionHash || expected.Input!=send.Input ||
        expected.Result!=send.Result || !reflect.DeepEqual(expected.Expected,send.Expected) ||
        !reflect.DeepEqual(expected.Ganache,send.Observed) {return nil,fmt.Errorf("functional row join differs")}
    if in.Schema!=gasFixtureSchema || result.Schema!=gasFixtureSchema || in.ExecutionSeq!=send.ExecutionSeq ||
        in.RPCSeq!=send.RPCSeq || result.ExecutionSeq!=send.ExecutionSeq || result.RPCSeq!=send.RPCSeq ||
        in.CaseID!=send.CaseID || in.CaseTitle!=send.CaseTitle || in.Phase!=send.Phase || in.Ordinal!=send.Ordinal ||
        in.RawTransaction!=send.RawTransaction || in.TransactionHash!=send.TransactionHash ||
        !reflect.DeepEqual(in.Expected,send.Expected) || !reflect.DeepEqual(result.Expected,send.Expected) ||
        !result.ExpectedMatches || !reflect.DeepEqual(result.Observed,send.Observed) {return nil,fmt.Errorf("input/result/receipt join differs")}
    chain,err:=gasFixtureContext(&in);if err!=nil{return nil,err}
    message,err:=gasFixtureMessage(&in,chain);if err!=nil{return nil,err}
    base,root,err:=gasFixtureImport(&in);if err!=nil{return nil,err}
    fail:=func(err error)(*gasPaidPrepared,error){_ = base.Database().TrieDB().Close();return nil,err}
    if root!=expected.GethPrestateRoot || base.GetNonce(message.From)!=message.Nonce ||
        message.Nonce!=expected.OriginalNonce || message.GasLimit!=expected.OriginalGasLimit ||
        message.GasPrice.ToBig().String()!=expected.OriginalGasPrice || chain.current.Hash()!=expected.HeaderHash {
        return fail(fmt.Errorf("imported immutable base/message/header differs from functional row"))
    }
    return &gasPaidPrepared{send:send,expected:expected,chain:chain,base:base,message:message},nil
}

