// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: LGPL-3.0-or-later

package gasestimator

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "reflect"
    "sort"
    "testing"

    "github.com/ethereum/go-ethereum/core/vm"
)

type gasControlManifest struct {
    Schema string `json:"schema"`
    Binary *gasFixtureSpec `json:"test_binary"`
    FunctionalSummary *gasFixtureSpec `json:"functional_summary"`
    OutputDirectory string `json:"output_directory"`
}
type gasControlReferenceSummary struct {
    Binary gasFixtureSpec `json:"test_binary"`
    Schema string `json:"schema"`; Complete bool `json:"complete"`; Errors []string `json:"errors"`
    Counts map[string]uint64 `json:"counts"`; InputsPreserved bool `json:"inputs_preserved"`
    FullRows gasFixtureSpec `json:"full_rows"`; MaterializationReceipt *gasFixtureSpec `json:"materialization_receipt"`
    InputFiles map[string]gasFixtureSpec `json:"input_files_before_after"`; CaseInputCounts []int `json:"case_input_counts"`
}
type gasControlRow struct {
    ExecutionSeq int `json:"execution_seq"`; RPCSeq int `json:"rpc_seq"`; CaseID int `json:"case_id"`
    CaseTitle string `json:"case_title"`; Phase string `json:"phase"`; Ordinal int `json:"phase_send_ordinal"`
    TransactionHash string `json:"transaction_hash"`; Input gasFixtureSpec `json:"input"`; Result gasFixtureSpec `json:"result"`
    Expected gasFixtureExpected `json:"expected"`
    StockAndCertificate gasFixtureRow `json:"stock_and_certificate"`
    StockCertificateExactReference bool `json:"stock_certificate_exact_reference"`
    BaselineAttempted bool `json:"baseline_attempted"`
    BaselineReturned bool `json:"baseline_returned"`
    Baseline gasFixtureEstimate `json:"baseline"`; BaselineReport GanachePolicyReport `json:"baseline_report"`
    BaselineReturnedReplay *gasFixtureReplay `json:"baseline_returned_replay"`
    BaselineReturnedSufficient bool `json:"baseline_returned_sufficient"`
    BaselineStateRootEqualToOriginal bool `json:"baseline_state_root_equal_to_original"`
    BaselineEffectsEqualToOriginal bool `json:"baseline_effects_equal_to_original"`
    BaselineReplayEquivalentToOriginal bool `json:"baseline_replay_equivalent_to_original"`
    Errors []string `json:"errors"`
}

func gasControlOne(send gasFixtureSend, reference gasFixtureRow) (row gasControlRow) {
    row=gasControlRow{ExecutionSeq:send.ExecutionSeq,RPCSeq:send.RPCSeq,CaseID:send.CaseID,
        CaseTitle:send.CaseTitle,Phase:send.Phase,Ordinal:send.Ordinal,TransactionHash:send.TransactionHash,
        Input:send.Input,Result:send.Result,Expected:send.Expected,Errors:make([]string,0)}
    fail:=func(stage string,err error){row.Errors=append(row.Errors,stage+": "+gasFixtureError(err))}
    defer func(){if value:=recover();value!=nil{fail("unexpected harness/control panic",fmt.Errorf("%v",value))}}()
    // Re-execute the unchanged full functional helper after the VM hook change:
    // both methods, trial sequences, independent fresh VM replays and all effects.
    row.StockAndCertificate=gasFixtureOne(send)
    row.StockCertificateExactReference=reflect.DeepEqual(row.StockAndCertificate,reference)
    if !row.StockCertificateExactReference{fail("stock/certificate regression",fmt.Errorf("complete functional row differs from whole1734 reference"))}
    for _,why:=range row.StockAndCertificate.Errors{fail("stock/certificate",fmt.Errorf("%s",why))}
    var in gasFixtureInput;var result gasFixtureResult
    if err:=gasFixtureRead(send.Input,&in);err!=nil{fail("input",err);return}
    if err:=gasFixtureRead(send.Result,&result);err!=nil{fail("original result",err);return}
    if in.Schema!=gasFixtureSchema||result.Schema!=gasFixtureSchema||in.ExecutionSeq!=send.ExecutionSeq||
        in.RPCSeq!=send.RPCSeq||result.ExecutionSeq!=send.ExecutionSeq||result.RPCSeq!=send.RPCSeq||
        in.CaseID!=send.CaseID||in.CaseTitle!=send.CaseTitle||in.Phase!=send.Phase||in.Ordinal!=send.Ordinal||
        in.RawTransaction!=send.RawTransaction||in.TransactionHash!=send.TransactionHash||
        !reflect.DeepEqual(in.Expected,send.Expected)||!reflect.DeepEqual(result.Expected,send.Expected)||
        !result.ExpectedMatches||!reflect.DeepEqual(result.Observed,send.Observed){fail("join",fmt.Errorf("input/result/receipt differs"));return}
    chain,err:=gasFixtureContext(&in);if err!=nil{fail("original context",err);return}
    message,err:=gasFixtureMessage(&in,chain);if err!=nil{fail("signed message",err);return}
    base,root,err:=gasFixtureImport(&in);if err!=nil{fail("independent state import",err);return}
    defer func(){if err:=base.Database().TrieDB().Close();err!=nil{fail("base close",err)}}()
    if root!=reference.GethPrestateRoot||base.GetNonce(message.From)!=message.Nonce||
        message.Nonce!=reference.OriginalNonce||message.GasLimit!=reference.OriginalGasLimit||
        message.GasPrice.ToBig().String()!=reference.OriginalGasPrice||chain.current.Hash()!=reference.HeaderHash{
        fail("original base/message",fmt.Errorf("functional identity differs"));return
    }
    copy:=*message
    options:=&Options{Config:chain.config,Chain:chain,Header:chain.current,State:base.Copy(),
        ErrorRatio:0.015,Certificate:nil,OnTrial:nil}
    row.BaselineAttempted=true
    limit,data,estimateErr,report:=ganachePolicyEstimate(context.Background(),&copy,options,0)
    row.BaselineReturned=true
    row.Baseline=gasFixtureEstimate{Estimate:limit,ReturnData:gasFixtureHexOut(data),
        Error:gasFixtureError(estimateErr),Reverted:errors.Is(estimateErr,vm.ErrExecutionReverted)}
    row.BaselineReport=report
    if !report.Complete{fail("baseline policy",fmt.Errorf("policy did not terminate completely"))}
    if report.NormalizationError!=""{fail("baseline normalization",fmt.Errorf("%s",report.NormalizationError))}
    if estimateErr==nil {
        // Check the policy's returned budget directly on a fresh VM and state.
        // Success is the required execution condition.
        hash,_:=gasFixtureHash(send.TransactionHash)
        replay,err:=gasFixtureExecute(chain,base,message,hash,limit)
        if err!=nil{fail("baseline returned budget replay",err)}else{
            row.BaselineReturnedReplay=replay;row.BaselineReturnedSufficient=replay.Status==1
            if !row.BaselineReturnedSufficient{fail("baseline sufficiency",fmt.Errorf("accepted returned budget did not succeed"))}
            original:=reference.OriginalReplay
            if original==nil{fail("reference original replay",fmt.Errorf("missing complete original execution"))}else{
                row.BaselineStateRootEqualToOriginal=replay.FeeSettledStateRoot==original.FeeSettledStateRoot
                row.BaselineEffectsEqualToOriginal=row.BaselineStateRootEqualToOriginal&&replay.Status==original.Status&&
                    replay.ExecutionError==original.ExecutionError&&replay.ReturnData==original.ReturnData&&
                    reflect.DeepEqual(replay.Logs,original.Logs)&&reflect.DeepEqual(replay.FullLogs,original.FullLogs)&&
                    reflect.DeepEqual(replay.CreatedAddress,original.CreatedAddress)
                row.BaselineReplayEquivalentToOriginal=gasFixtureEqual(original,replay)
                // Record equivalence as a descriptive observation while preserving the
                // policy's returned budget and search behavior.
            }
        }
        if send.Expected.Status!=1{fail("baseline expected rejection",fmt.Errorf("accepted an original K rejection"))}
    }else if send.Expected.Status!=0||!row.Baseline.Reverted||row.Baseline.ReturnData!=send.Observed.ReturnData{
        fail("baseline rejection",fmt.Errorf("does not reproduce original status/revert/return"))
    }
    if base.IntermediateRoot(reference.Rules)!=root{fail("immutable baseline state",fmt.Errorf("base root changed"))}
    if base.Error()!=nil{fail("immutable baseline state",base.Error())}
    return
}

func TestGasGanacheControlFixtureCohort(t *testing.T) {
    path:=os.Getenv("GETH_GANACHE_CONTROL_FIXTURE_MANIFEST")
    if path==""{t.Skip("requires whole-cohort reference manifest; skip is not qualification")}
    path,err:=gasFixturePath(path);if err!=nil{t.Fatal(err)}
    manifestID,err:=gasFixtureFileIdentity(path);if err!=nil{t.Fatal(err)}
    var manifest gasControlManifest;if err=gasFixtureRead(manifestID,&manifest);err!=nil{t.Fatal(err)}
    if manifest.Schema!="round1743-three-method-functional-input-v1"||manifest.FunctionalSummary==nil||manifest.Binary==nil{t.Fatal("unbound whole reference summary/binary")}
    var summary gasControlReferenceSummary;if err=gasFixtureRead(*manifest.FunctionalSummary,&summary);err!=nil{t.Fatal(err)}
    if summary.Schema!="round1719-gas-functional-summary-v1"||summary.Binary!=*manifest.Binary||!summary.Complete||len(summary.Errors)!=0||!summary.InputsPreserved||
        summary.MaterializationReceipt==nil||summary.Counts["attempted_inputs"]!=116||summary.Counts["inputs_with_errors"]!=0{
        t.Fatal("whole original functional reference not qualified")
    }
    var materialized gasFixtureReceipt;if err=gasFixtureRead(*summary.MaterializationReceipt,&materialized);err!=nil{t.Fatal(err)}
    if err=gasFixtureCohort(&materialized);err!=nil{t.Fatal(err)}
    protected:=make(map[string]gasFixtureSpec)
    protect:=func(s gasFixtureSpec)error{if prior,ok:=protected[s.Path];ok&&prior!=s{return fmt.Errorf("conflicting FileSpecs")};protected[s.Path]=s;return gasFixtureCheck(s)}
    for _,s:=range []gasFixtureSpec{manifestID,*manifest.FunctionalSummary,*manifest.Binary,summary.FullRows,*summary.MaterializationReceipt}{if err=protect(s);err!=nil{t.Fatal(err)}}
    for key,s:=range summary.InputFiles{if key!=s.Path{t.Fatal("reference inventory key differs")};if err=protect(s);err!=nil{t.Fatal(err)}}
    for _,s:=range append(append([]gasFixtureSpec(nil),materialized.Files...),materialized.Inputs...){if prior,ok:=protected[s.Path];!ok||prior!=s{t.Fatal("reference does not bind full original inventory")}}
    sends:=append([]gasFixtureSend(nil),materialized.RawSends...);sort.Slice(sends,func(i,j int)bool{return sends[i].ExecutionSeq<sends[j].ExecutionSeq})
    f,err:=os.Open(summary.FullRows.Path);if err!=nil{t.Fatal(err)}
    decoder:=json.NewDecoder(f);reference:=make([]gasFixtureRow,0,116)
    for{var row gasFixtureRow;err=decoder.Decode(&row);if err==io.EOF{break};if err!=nil{_ = f.Close();t.Fatal(err)};reference=append(reference,row)}
    if err=f.Close();err!=nil{t.Fatal(err)};if len(reference)!=116{t.Fatal("reference requires all116 rows")}
    cases:=make([]int,19)
    for i,send:=range sends{
        r:=reference[i]
        if send.ExecutionSeq!=i||r.ExecutionSeq!=i||r.RPCSeq!=send.RPCSeq||r.CaseID!=send.CaseID||r.CaseTitle!=send.CaseTitle||
            r.Phase!=send.Phase||r.Ordinal!=send.Ordinal||r.TransactionHash!=send.TransactionHash||r.Input!=send.Input||r.Result!=send.Result||
            !reflect.DeepEqual(r.Expected,send.Expected)||!reflect.DeepEqual(r.Ganache,send.Observed)||len(r.Errors)!=0||r.OriginalReplay==nil{
            t.Fatal("full original reference/input join differs")
        }
        for _,s:=range []gasFixtureSpec{send.Input,send.Result}{if prior,ok:=protected[s.Path];!ok||prior!=s{t.Fatal("unbound original input/result")}}
        cases[send.CaseID]++
    }
    if !reflect.DeepEqual(cases,summary.CaseInputCounts){t.Fatal("full case cohort differs")}
    if !filepath.IsAbs(manifest.OutputDirectory){t.Fatal("output must be absolute")}
    out:=filepath.Clean(manifest.OutputDirectory);parent,err:=gasFixturePath(filepath.Dir(out));if err!=nil{t.Fatal(err)}
    out=filepath.Join(parent,filepath.Base(out));if err=os.Mkdir(out,0700);err!=nil{t.Fatal("fresh output required: ",err)}
    rowsPath:=filepath.Join(out,"all-inputs.jsonl");rows,err:=os.OpenFile(rowsPath,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600);if err!=nil{t.Fatal(err)}
    encoder:=json.NewEncoder(rows);allErrors:=make([]string,0)
    counts:=map[string]uint64{"all_inputs":116,"original_expected_success":105,"original_expected_rejection":11}
    baselineTrialPhases:=make(map[string]uint64);baselineReasons:=make(map[string]uint64)
    for i,send:=range sends{
        row:=gasControlOne(send,reference[i]);counts["attempted_inputs"]++
        if row.StockCertificateExactReference{counts["stock_certificate_exact_reference_inputs"]++}
        counts["stock_estimator_trials"]+=uint64(len(row.StockAndCertificate.Stock.Trials))
        counts["certificate_estimator_trials"]+=uint64(len(row.StockAndCertificate.CertificateEstimate.Trials))
        if row.BaselineAttempted{counts["baseline_attempted_inputs"]++}
        if row.BaselineReport.Complete{counts["baseline_complete_inputs"]++}
        counts["baseline_trials"]+=uint64(len(row.BaselineReport.Trials));counts["baseline_trace_events"]+=row.BaselineReport.TraceEvents
        counts["baseline_system_events"]+=row.BaselineReport.SystemEvents;counts["baseline_call_stack_copies"]+=row.BaselineReport.CallStackCopies
        if row.BaselineReport.ReducerRan{counts["baseline_reducer_inputs"]++}
        for _,trial:=range row.BaselineReport.Trials{baselineTrialPhases[trial.Phase]++}
        if row.BaselineReturned{counts["baseline_returned_inputs"]++;if row.Baseline.Error!=""{counts["baseline_error_inputs"]++;baselineReasons[row.Baseline.Error]++}else{counts["baseline_accepted_inputs"]++}}
        if row.BaselineReturnedSufficient{counts["baseline_sufficient_inputs"]++}
        if row.BaselineStateRootEqualToOriginal{counts["baseline_state_root_equal_inputs"]++}
        if row.BaselineEffectsEqualToOriginal{counts["baseline_effects_equal_inputs"]++}
        if row.BaselineReplayEquivalentToOriginal{counts["baseline_replay_equivalent_inputs"]++}
        if len(row.Errors)!=0{counts["inputs_with_errors"]++}
        for _,why:=range row.Errors{allErrors=append(allErrors,fmt.Sprintf("execution_seq=%d: %s",send.ExecutionSeq,why))}
        if e:=encoder.Encode(row);e!=nil{allErrors=append(allErrors,"full row output: "+e.Error());break}
    }
    if e:=rows.Sync();e!=nil{allErrors=append(allErrors,"row sync: "+e.Error())};if e:=rows.Close();e!=nil{allErrors=append(allErrors,"row close: "+e.Error())}
    preserved:=true;keys:=make([]string,0,len(protected));for k:=range protected{keys=append(keys,k)};sort.Strings(keys)
    for _,k:=range keys{if e:=gasFixtureCheck(protected[k]);e!=nil{preserved=false;allErrors=append(allErrors,"input after: "+e.Error())}}
    protectedPath:=filepath.Join(out,"protected-inputs.json");if e:=gasFixtureWriteJSON(protectedPath,protected);e!=nil{allErrors=append(allErrors,"protected inventory: "+e.Error())}
    identify:=func(path string)gasFixtureSpec{s,e:=gasFixtureFileIdentity(path);if e!=nil{allErrors=append(allErrors,"output identity: "+e.Error())};return s}
    rowsID,protectedID:=identify(rowsPath),identify(protectedPath)
    if counts["attempted_inputs"]!=116||counts["stock_certificate_exact_reference_inputs"]!=116||counts["baseline_attempted_inputs"]!=116||
        counts["baseline_complete_inputs"]!=116||counts["baseline_accepted_inputs"]!=105||counts["baseline_sufficient_inputs"]!=105||counts["baseline_error_inputs"]!=11{
        allErrors=append(allErrors,"incomplete fixed full-cohort three-method qualification")
    }
    report:=map[string]any{
        "schema":"round1743-three-method-functional-summary-v1","test_binary":*manifest.Binary,"complete":len(allErrors)==0,"errors":allErrors,"inputs_preserved":preserved,
        "attempted_inputs":counts["attempted_inputs"],"all_inputs":116,"case_count":19,"case_input_counts":cases,
        "stock_certificate_exact_reference_inputs":counts["stock_certificate_exact_reference_inputs"],"baseline_attempted_inputs":counts["baseline_attempted_inputs"],
        "baseline_complete_inputs":counts["baseline_complete_inputs"],"baseline_accepted_inputs":counts["baseline_accepted_inputs"],"baseline_sufficient_inputs":counts["baseline_sufficient_inputs"],
        "counts":counts,"baseline_trial_phase_counts":baselineTrialPhases,"baseline_error_counts":baselineReasons,
        "full_rows":rowsID,"protected_inputs":protectedID,"manifest":manifestID,
        "functional_summary":manifest.FunctionalSummary,"functional_reference_rows":summary.FullRows,
        "fixed_original_case_names":materialized.ExpectedCases,"functional_only":true,"performance_measured":false,
        "baseline_contract":"own faithful policy on all116; every accepted returned budget independently succeeds; complete effects equality recorded separately, never forced by another budget/search",
        "stock_certificate_contract":"unchanged full gasFixtureOne + complete exact whole1734 row comparison, including trials/replays/report/state/effects",
    }
    if err=gasFixtureWriteJSON(filepath.Join(out,"summary.json"),report);err!=nil{t.Fatal(err)}
    if len(allErrors)!=0{t.Errorf("whole three-method functional cohort recorded with %d errors; see summary.json",len(allErrors))}
}
