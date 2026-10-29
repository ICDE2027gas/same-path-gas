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
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/vm"
)

const gasThreeExpectedRequests = 33756
var gasThreeOrders = [6][3]string{
	{"stock","certificate","baseline"},{"certificate","baseline","stock"},{"baseline","stock","certificate"},
	{"stock","baseline","certificate"},{"baseline","certificate","stock"},{"certificate","stock","baseline"},
}
type gasThreeSummary struct {
	Binary gasFixtureSpec `json:"test_binary"`
	Schema string `json:"schema"`; Complete bool `json:"complete"`; Errors []string `json:"errors"`
	Counts map[string]uint64 `json:"counts"`; InputsPreserved bool `json:"inputs_preserved"`
	FullRows gasFixtureSpec `json:"full_rows"`; ProtectedInputs gasFixtureSpec `json:"protected_inputs"`
	FunctionalSummary *gasFixtureSpec `json:"functional_summary"`
	FunctionalReferenceRows gasFixtureSpec `json:"functional_reference_rows"`
	CaseInputCounts []int `json:"case_input_counts"`
	AttemptedInputs int `json:"attempted_inputs"`; AllInputs int `json:"all_inputs"`; CaseCount int `json:"case_count"`
	BaselineCompleteInputs int `json:"baseline_complete_inputs"`; BaselineAcceptedInputs int `json:"baseline_accepted_inputs"`
	BaselineSufficientInputs int `json:"baseline_sufficient_inputs"`; ExactReferenceInputs int `json:"stock_certificate_exact_reference_inputs"`
}
type gasThreePrepared struct { original *gasPaidPrepared; control gasControlRow }
type gasThreeRequest struct {
	RequestIndex int `json:"request_index"`; Phase string `json:"phase"`; TripleID int `json:"triple_id"`
	BlockID int `json:"block_id"`; Pass int `json:"pass"`; Position int `json:"position"`; Mode string `json:"mode"`
	ExecutionSeq int `json:"execution_seq"`; RPCSeq int `json:"rpc_seq"`; CaseID int `json:"case_id"`; CaseTitle string `json:"case_title"`
	OriginalPhase string `json:"original_phase"`; Ordinal int `json:"phase_send_ordinal"`; TransactionHash string `json:"transaction_hash"`
	Input gasFixtureSpec `json:"input"`; ExpectedStatus int `json:"expected_status"`
	FunctionallyCertified bool `json:"functionally_certified"`; AlreadyOneStockTrial bool `json:"already_one_stock_trial"`
	SavedTrial bool `json:"saved_trial"`; Unsupported bool `json:"unsupported"`; Rejected bool `json:"rejected"`
	ElapsedNS int64 `json:"elapsed_ns"`; Estimate gasFixtureEstimate `json:"estimate"`
	Certificate *CertificateReport `json:"certificate"`; BaselineReport GanachePolicyReport `json:"baseline_report_disabled_zero_value"`
	Panic string `json:"panic"`; Errors []string `json:"errors"`
}
type gasThreeBlock struct {
	Phase string `json:"phase"`; TripleID int `json:"triple_id"`; BlockID int `json:"block_id"`; Mode string `json:"mode"`
	CohortPasses int `json:"cohort_passes"`; RequestFirst int `json:"request_first"`; RequestLast int `json:"request_last"`
	gasPaidAggregate
	BlockWallNS int64 `json:"block_wall_ns_including_outside_timer_validation_io"`
}

func gasThreeEstimate(p *gasThreePrepared,mode string)(row gasThreeRequest) {
	// The service timer includes the outer state copy and message/Options preparation.
	start:=time.Now()
	defer func(){if value:=recover();value!=nil{row.ElapsedNS=time.Since(start).Nanoseconds();row.Panic=fmt.Sprint(value)}}()
	message:=*p.original.message
	if mode=="certificate" {row.Certificate=new(CertificateReport)}
	opts:=&Options{Config:p.original.chain.config,Chain:p.original.chain,Header:p.original.chain.current,
		State:p.original.base.Copy(),ErrorRatio:0.015,Certificate:row.Certificate,OnTrial:nil}
	var limit uint64;var data []byte;var err error
	if mode=="baseline" {limit,data,err,row.BaselineReport=ganachePolicyEstimateDiagnostics(context.Background(),&message,opts,0,false)} else {
		limit,data,err=Estimate(context.Background(),&message,opts,0)
	}
	row.ElapsedNS=time.Since(start).Nanoseconds()
	// Formatting, result/reference comparison and serialization are outside.
	row.Estimate=gasFixtureEstimate{Estimate:limit,ReturnData:gasFixtureHexOut(data),Error:gasFixtureError(err),Reverted:errors.Is(err,vm.ErrExecutionReverted)}
	return
}
func gasThreeCheck(row *gasThreeRequest,p *gasThreePrepared) {
	row.Errors=make([]string,0)
	if row.Panic!="" {row.Errors=append(row.Errors,"estimator panic: "+row.Panic)}
	if row.ElapsedNS<=0 {row.Errors=append(row.Errors,"nonpositive monotonic service interval")}
	want:=p.original.expected.Stock
	if row.Mode=="certificate" {want=p.original.expected.CertificateEstimate}
	if row.Mode=="baseline" {want=p.control.Baseline}
	if row.Estimate.Estimate!=want.Estimate||row.Estimate.ReturnData!=want.ReturnData||row.Estimate.Error!=want.Error||row.Estimate.Reverted!=want.Reverted {
		row.Errors=append(row.Errors,"estimate/error/revert/return differs from complete functional reference")
	}
	if len(row.Estimate.Trials)!=0 {row.Errors=append(row.Errors,"trial callback unexpectedly populated")}
	if row.Mode=="certificate" {
		if row.Certificate==nil||!reflect.DeepEqual(*row.Certificate,p.original.expected.Certificate){row.Errors=append(row.Errors,"certificate/report differs from complete functional reference")}
	} else if row.Certificate!=nil {row.Errors=append(row.Errors,"unexpected certificate enabled")}
	if !reflect.DeepEqual(row.BaselineReport,GanachePolicyReport{}) {row.Errors=append(row.Errors,"baseline report diagnostics not disabled")}
}

func TestGasThreeMethodPaidCostCohort(t *testing.T) {
	path:=os.Getenv("GETH_THREE_METHOD_PAID_COST_MANIFEST")
	if path=="" {t.Skip("requires complete three-method reference manifest; skip is not measurement")}
	path,err:=gasFixturePath(path);if err!=nil{t.Fatal(err)}
	manifestID,err:=gasFixtureFileIdentity(path);if err!=nil{t.Fatal(err)}
	var manifest gasControlManifest;if err=gasFixtureRead(manifestID,&manifest);err!=nil{t.Fatal(err)}
	if manifest.Schema!="round1749-three-method-paid-input-v1"||manifest.FunctionalSummary==nil||manifest.Binary==nil {t.Fatal("unbound complete three-method qualification summary/binary")}
	if runtime.GOMAXPROCS(0)!=1||os.Getenv("GOMAXPROCS")!="1"||os.Getenv("GOGC")!=""||os.Getenv("GOMEMLIMIT")!=""||os.Getenv("GODEBUG")!="" {t.Fatal("requires GOMAXPROCS1 and normal unchanged GC")}
	var qualified gasThreeSummary;if err=gasFixtureRead(*manifest.FunctionalSummary,&qualified);err!=nil{t.Fatal(err)}
	if qualified.Schema!="round1743-three-method-functional-summary-v1"||qualified.Binary!=*manifest.Binary||
		!qualified.Complete||len(qualified.Errors)!=0||!qualified.InputsPreserved||qualified.AllInputs!=116||qualified.AttemptedInputs!=116||
		qualified.CaseCount!=19||qualified.ExactReferenceInputs!=116||qualified.BaselineCompleteInputs!=116||qualified.BaselineAcceptedInputs!=105||
		qualified.BaselineSufficientInputs!=105||qualified.Counts["inputs_with_errors"]!=0||qualified.Counts["baseline_error_inputs"]!=11||qualified.FunctionalSummary==nil {
		t.Fatal("whole three-method functional reference not qualified")
	}
	var prior gasPaidFunctionalSummary;if err=gasFixtureRead(*qualified.FunctionalSummary,&prior);err!=nil{t.Fatal(err)}
	if prior.Schema!="round1719-gas-functional-summary-v1"||prior.Binary!=*manifest.Binary||!prior.Complete||len(prior.Errors)!=0||!prior.InputsPreserved||
		prior.MaterializationReceipt==nil||prior.FullRows!=qualified.FunctionalReferenceRows||prior.Counts["attempted_inputs"]!=116||prior.Counts["inputs_with_errors"]!=0 {t.Fatal("original stock/certificate reference not qualified")}
	var materialized gasFixtureReceipt;if err=gasFixtureRead(*prior.MaterializationReceipt,&materialized);err!=nil{t.Fatal(err)}
	if err=gasFixtureCohort(&materialized);err!=nil{t.Fatal(err)}
	protected:=make(map[string]gasFixtureSpec)
	protect:=func(s gasFixtureSpec)error{if old,ok:=protected[s.Path];ok&&old!=s{return fmt.Errorf("conflicting FileSpecs")};protected[s.Path]=s;return gasFixtureCheck(s)}
	for _,s:=range []gasFixtureSpec{manifestID,*manifest.FunctionalSummary,*manifest.Binary,
		qualified.FullRows,qualified.ProtectedInputs,*qualified.FunctionalSummary,qualified.FunctionalReferenceRows,*prior.MaterializationReceipt}{if err=protect(s);err!=nil{t.Fatal(err)}}
	var originalProtected map[string]gasFixtureSpec;if err=gasFixtureRead(qualified.ProtectedInputs,&originalProtected);err!=nil{t.Fatal(err)}
	for key,s:=range originalProtected{if key!=s.Path{t.Fatal("original protected key differs")};if err=protect(s);err!=nil{t.Fatal(err)}}
	for key,s:=range prior.InputFiles{if key!=s.Path{t.Fatal("prior input key differs")};if err=protect(s);err!=nil{t.Fatal(err)}}
	for _,s:=range append(append([]gasFixtureSpec(nil),materialized.Files...),materialized.Inputs...){if old,ok:=protected[s.Path];!ok||old!=s{t.Fatal("full original inventory not bound")}}
	sends:=append([]gasFixtureSend(nil),materialized.RawSends...);sort.Slice(sends,func(i,j int)bool{return sends[i].ExecutionSeq<sends[j].ExecutionSeq})
	f,err:=os.Open(qualified.FullRows.Path);if err!=nil{t.Fatal(err)}
	decoder:=json.NewDecoder(f);references:=make([]gasControlRow,0,116)
	for{var row gasControlRow;err=decoder.Decode(&row);if err==io.EOF{break};if err!=nil{_ = f.Close();t.Fatal(err)};references=append(references,row)}
	if err=f.Close();err!=nil{t.Fatal(err)};if len(references)!=116||len(sends)!=116{t.Fatal("requires complete116 reference and sends")}
	prepared:=make([]*gasThreePrepared,0,116);closed:=false
	defer func(){if !closed{for _,p:=range prepared{if e:=p.original.base.Database().TrieDB().Close();e!=nil{t.Errorf("base close: %v",e)}}}}()
	cases:=make([]int,19)
	for i,send:=range sends {
		r:=references[i]
		if send.ExecutionSeq!=i||r.ExecutionSeq!=i||r.RPCSeq!=send.RPCSeq||r.CaseID!=send.CaseID||r.CaseTitle!=send.CaseTitle||
			r.Phase!=send.Phase||r.Ordinal!=send.Ordinal||r.TransactionHash!=send.TransactionHash||r.Input!=send.Input||r.Result!=send.Result||
			!reflect.DeepEqual(r.Expected,send.Expected)||len(r.Errors)!=0||!r.StockCertificateExactReference||!r.BaselineAttempted||!r.BaselineReturned||
			!r.BaselineReport.Complete||r.BaselineReport.NormalizationError!="" {t.Fatal("complete baseline/source input reference join differs")}
		if send.Expected.Status==1 {if r.Baseline.Error!=""||!r.BaselineReturnedSufficient{t.Fatal("missing accepted baseline sufficient result")}} else {
			if r.Baseline.Error==""||!r.Baseline.Reverted||r.Baseline.ReturnData!=send.Observed.ReturnData{t.Fatal("baseline original rejection differs")}
		}
		for _,s:=range []gasFixtureSpec{send.Input,send.Result}{if old,ok:=protected[s.Path];!ok||old!=s{t.Fatal("unbound input/result")}}
		p,e:=gasPaidPrepare(send,r.StockAndCertificate);if e!=nil{t.Fatalf("prepare input%d: %v",i,e)}
		prepared=append(prepared,&gasThreePrepared{original:p,control:r});cases[send.CaseID]++
	}
	if !reflect.DeepEqual(cases,qualified.CaseInputCounts)||!reflect.DeepEqual(cases,prior.CaseInputCounts){t.Fatal("whole case counts differ")}
	if !filepath.IsAbs(manifest.OutputDirectory){t.Fatal("absolute fresh output required")}
	out:=filepath.Clean(manifest.OutputDirectory);parent,err:=gasFixturePath(filepath.Dir(out));if err!=nil{t.Fatal(err)}
	out=filepath.Join(parent,filepath.Base(out));if err=os.Mkdir(out,0700);err!=nil{t.Fatal(err)}
	requestsPath:=filepath.Join(out,"all-requests.jsonl");blocksPath:=filepath.Join(out,"blocks.jsonl")
	requests,err:=os.OpenFile(requestsPath,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600);if err!=nil{t.Fatal(err)}
	blocksFile,err:=os.OpenFile(blocksPath,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600);if err!=nil{_ = requests.Close();t.Fatal(err)}
	requestEncoder,blockEncoder:=json.NewEncoder(requests),json.NewEncoder(blocksFile)
	allErrors:=make([]string,0);blocks:=make([]gasThreeBlock,0,39);groups:=make(map[string]*gasPaidGroup)
	labels:=[]string{"all","certified","noncertified","already_one_stock_trial","saved_trial","unsupported","rejected","creation"}
	for _,p:=range prepared{labels=append(labels,fmt.Sprintf("input:%03d",p.original.send.ExecutionSeq))}
	for i,name:=range materialized.ExpectedCases{labels=append(labels,fmt.Sprintf("case:%02d:%s",i,name))}
	for _,phase:=range []string{"warmup","measured"}{for _,mode:=range []string{"stock","certificate","baseline"}{for _,label:=range labels{
		key:=phase+"/"+mode+"/"+label;groups[key]=&gasPaidGroup{Phase:phase,Mode:mode,Label:label}
	}}}
	attempted:=0;ioFailed:=false
	addGroup:=func(phase,mode,label string,row *gasThreeRequest){
		key:=phase+"/"+mode+"/"+label;g:=groups[key]
		if g==nil{g=&gasPaidGroup{Phase:phase,Mode:mode,Label:label};groups[key]=g}
		g.Requests++;g.PaidNS+=row.ElapsedNS;if len(row.Errors)!=0{g.Errors++}
	}
	runBlock:=func(phase string,triple int,mode string,passes int){
		block:=gasThreeBlock{Phase:phase,TripleID:triple,BlockID:len(blocks),Mode:mode,CohortPasses:passes,RequestFirst:attempted,RequestLast:attempted-1}
		buffer:=make([]gasThreeRequest,0,116*passes);wall:=time.Now()
		for pass:=0;pass<passes;pass++ {for position,p:=range prepared {
			row:=gasThreeEstimate(p,mode);row.RequestIndex=attempted;row.Phase=phase;row.TripleID=triple;row.BlockID=block.BlockID;row.Pass=pass;row.Position=position;row.Mode=mode
			send:=p.original.send;reference:=p.original.expected
			row.ExecutionSeq=send.ExecutionSeq;row.RPCSeq=send.RPCSeq;row.CaseID=send.CaseID;row.CaseTitle=send.CaseTitle;row.OriginalPhase=send.Phase
			row.Ordinal=send.Ordinal;row.TransactionHash=send.TransactionHash;row.Input=send.Input;row.ExpectedStatus=send.Expected.Status
			row.FunctionallyCertified=reference.Certificate.Certified;row.AlreadyOneStockTrial=len(reference.Stock.Trials)==1
			row.SavedTrial=len(reference.CertificateEstimate.Trials)<len(reference.Stock.Trials);row.Unsupported=reference.Certificate.Status=="unsupported";row.Rejected=send.Expected.Status==0
			gasThreeCheck(&row,p);attempted++;block.Requests++;block.PaidNS+=row.ElapsedNS;block.RequestLast=row.RequestIndex
			if len(row.Errors)!=0{block.Errors++;for _,why:=range row.Errors{allErrors=append(allErrors,fmt.Sprintf("request%d: %s",row.RequestIndex,why))}}
			addGroup(phase,mode,"all",&row);addGroup(phase,mode,fmt.Sprintf("input:%03d",send.ExecutionSeq),&row)
			addGroup(phase,mode,fmt.Sprintf("case:%02d:%s",send.CaseID,send.CaseTitle),&row)
			stratum:="noncertified";if row.FunctionallyCertified{stratum="certified"};addGroup(phase,mode,stratum,&row)
			if row.AlreadyOneStockTrial{addGroup(phase,mode,"already_one_stock_trial",&row)};if row.SavedTrial{addGroup(phase,mode,"saved_trial",&row)}
			if row.Unsupported{addGroup(phase,mode,"unsupported",&row)};if row.Rejected{addGroup(phase,mode,"rejected",&row)};if p.original.message.To==nil{addGroup(phase,mode,"creation",&row)}
			buffer=append(buffer,row)
		}}
		// Run the full method block before serializing its request rows.
		// Policy-event callbacks exclude JSON output, printing, and clock reads.
		for i:=range buffer{if e:=requestEncoder.Encode(&buffer[i]);e!=nil{allErrors=append(allErrors,"request output: "+e.Error());ioFailed=true;break}}
		block.BlockWallNS=time.Since(wall).Nanoseconds();blocks=append(blocks,block)
		if e:=blockEncoder.Encode(block);e!=nil{allErrors=append(allErrors,"block output: "+e.Error());ioFailed=true}
	}
	for _,mode:=range []string{"stock","certificate","baseline"}{if !ioFailed{runBlock("warmup",-1,mode,1)}}
	for triple:=0;triple<12&&!ioFailed;triple++{for _,mode:=range gasThreeOrders[triple%6]{runBlock("measured",triple,mode,8);if ioFailed{break}}}
	for _,f:=range []*os.File{requests,blocksFile}{if e:=f.Sync();e!=nil{allErrors=append(allErrors,"output sync: "+e.Error())};if e:=f.Close();e!=nil{allErrors=append(allErrors,"output close: "+e.Error())}}
	preserved:=true
	for _,p:=range prepared{if p.original.base.IntermediateRoot(p.original.expected.Rules)!=p.original.expected.GethPrestateRoot||p.original.base.Error()!=nil{preserved=false;allErrors=append(allErrors,fmt.Sprintf("immutable base changed input%d",p.original.send.ExecutionSeq))}}
	for _,p:=range prepared{if e:=p.original.base.Database().TrieDB().Close();e!=nil{allErrors=append(allErrors,"base close: "+e.Error())}};closed=true
	paths:=make([]string,0,len(protected));for p:=range protected{paths=append(paths,p)};sort.Strings(paths)
	for _,p:=range paths{if e:=gasFixtureCheck(protected[p]);e!=nil{preserved=false;allErrors=append(allErrors,"input after: "+e.Error())}}
	protectedPath:=filepath.Join(out,"protected-inputs.json");if e:=gasFixtureWriteJSON(protectedPath,protected);e!=nil{allErrors=append(allErrors,"protected output: "+e.Error())}
	identify:=func(path string)gasFixtureSpec{s,e:=gasFixtureFileIdentity(path);if e!=nil{allErrors=append(allErrors,"output identity: "+e.Error())};return s}
	requestID,blockID,protectedID:=identify(requestsPath),identify(blocksPath),identify(protectedPath)
	grouped:=make([]gasPaidGroup,0,len(groups));keys:=make([]string,0,len(groups));for key:=range groups{keys=append(keys,key)};sort.Strings(keys)
	for _,key:=range keys{grouped=append(grouped,*groups[key])}
	totals:=map[string]gasPaidAggregate{"stock":{},"certificate":{},"baseline":{}};measuredCounts:=map[string]int{"stock":0,"certificate":0,"baseline":0}
	warm,measured:=0,0
	for _,b:=range blocks{
		passes:=8;if b.Phase=="warmup"{warm++;passes=1}else{measured++;g:=totals[b.Mode];g.Requests+=b.Requests;g.PaidNS+=b.PaidNS;g.Errors+=b.Errors;totals[b.Mode]=g;measuredCounts[b.Mode]+=b.Requests}
		if b.Requests!=116*passes||b.CohortPasses!=passes{allErrors=append(allErrors,"incomplete whole mode block")}
	}
	triples:=make([]map[string]any,0,12);fasterStock,fasterBaseline,completeTriples:=0,0,0
	for triple:=0;triple<12;triple++{
		byMode:=make(map[string]*gasThreeBlock)
		for i:=range blocks{b:=&blocks[i];if b.Phase=="measured"&&b.TripleID==triple{byMode[b.Mode]=b}}
		row:=map[string]any{"triple_id":triple,"order":gasThreeOrders[triple%6],"blocks":byMode,"certificate_over_stock_paid_ratio":nil,"certificate_over_baseline_paid_ratio":nil}
		c,s,b:=byMode["certificate"],byMode["stock"],byMode["baseline"]
		if c!=nil&&s!=nil&&b!=nil&&c.PaidNS>0&&s.PaidNS>0&&b.PaidNS>0{
			completeTriples++
			row["certificate_over_stock_paid_ratio"]=float64(c.PaidNS)/float64(s.PaidNS);row["certificate_over_baseline_paid_ratio"]=float64(c.PaidNS)/float64(b.PaidNS)
			if c.PaidNS<s.PaidNS{fasterStock++};if c.PaidNS<b.PaidNS{fasterBaseline++}
		}else{allErrors=append(allErrors,"incomplete/nonpositive matched triple")}
		triples=append(triples,row)
	}
	if attempted!=gasThreeExpectedRequests||len(blocks)!=39||warm!=3||measured!=36{allErrors=append(allErrors,"incomplete fixed33756/39block population")}
	for _,mode:=range []string{"stock","certificate","baseline"}{if measuredCounts[mode]!=11136{allErrors=append(allErrors,"incomplete measured method population")}}
	c,s,b:=totals["certificate"].PaidNS,totals["stock"].PaidNS,totals["baseline"].PaidNS
	var stockRatio,baselineRatio any
	if c>0&&s>0&&b>0{stockRatio=float64(c)/float64(s);baselineRatio=float64(c)/float64(b)}
	gate:=len(allErrors)==0&&c>0&&s>0&&b>0&&float64(c)<=0.8*float64(s)&&float64(c)<=0.8*float64(b)&&fasterStock>=10&&fasterBaseline>=10
	report:=map[string]any{
		"schema":"round1749-three-method-paid-summary-v1","test_binary":*manifest.Binary,"complete":len(allErrors)==0,"errors":allErrors,"inputs_preserved":preserved,
		"attempted_requests":attempted,"expected_requests":gasThreeExpectedRequests,"total_blocks":len(blocks),"warmup_blocks":warm,"measured_blocks":measured,
		"matched_triples":12,"complete_matched_triples":completeTriples,"measured_cohort_passes_per_block":8,"warmup_cohort_passes_per_block":1,"all_inputs":116,"case_count":19,
		"expected_measured_requests_per_mode":11136,"measured_requests_per_mode":measuredCounts,"full_requests":requestID,"full_blocks":blockID,"protected_inputs":protectedID,
		"aggregates":grouped,"triples":triples,"primary_totals":totals,"certificate_over_stock_paid_ratio":stockRatio,"certificate_over_baseline_paid_ratio":baselineRatio,
		"faster_certificate_triples_vs_stock":fasterStock,"faster_certificate_triples_vs_baseline":fasterBaseline,"fixed_gate_passed":gate,
		"manifest":manifestID,"functional_summary":manifest.FunctionalSummary,"functional_full_rows":qualified.FullRows,
		"fixed_original_case_names":materialized.ExpectedCases,"case_input_counts":cases,"trial_callbacks_installed":false,"baseline_diagnostics_enabled":false,
		"certificate_report_paid":true,"normal_gc":true,"gomaxprocs":1,"gas_cap":0,"error_ratio":0.015,
		"timing_boundary":"immediately before message/outer base.Copy/Options preparation through complete real estimator return; all checks/formatting/JSON outside",
		"block_wall":"includes outside-timer comparisons/grouping and buffered JSON; not estimator paid time",
		"output_policy":"one preallocated whole mode block; JSON only after all1 or8 full passes; all inputs/zeros/errors retained",
		"strata":"fixed functional labels: certified/noncertified partition; other labels overlap; already_one_stock_trial means len(stock.Trials)==1, saved_trial means len(certificate.Trials)<len(stock.Trials), unsupported means Certificate.Status==unsupported",
		"observation_unit":"one fixed application cohort; repeated requests and12 triples are not independent applications",
		"fixed_gate":"whole measured certificate<=0.8 each actual control and strict faster in>=10/12 triples against each; mismatch invalidates qualification",
	}
	if err=gasFixtureWriteJSON(filepath.Join(out,"summary.json"),report);err!=nil{t.Fatal(err)}
	if len(allErrors)!=0{t.Errorf("whole three-method paid run recorded with %d errors",len(allErrors))}
}
