// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: LGPL-3.0-or-later

package gasestimator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

const gasFixtureSchema = "round1714-uniswap-materialization-v1"

type gasFixtureSpec struct { Path string `json:"path"`; Bytes int64 `json:"bytes"`; SHA256 string `json:"sha256"` }
type gasFixtureManifest struct {
	Binary *gasFixtureSpec `json:"test_binary"`
	Schema string `json:"schema"`
	MaterializationReceipt *gasFixtureSpec `json:"materialization_receipt"`
	OutputDirectory string `json:"output_directory"`
}
type gasFixtureExpected struct { Status int `json:"status"`; RevertReason *string `json:"revert_reason"` }
type gasFixtureObserved struct {
	Status int `json:"status"`
	RevertReason *string `json:"revert_reason"`
	Exception *string `json:"exception"`
	ReturnData string `json:"return_data"`
	GasUsed string `json:"gas_used"`
	Logs []gasFixtureLog `json:"logs"`
	CreatedAddress *string `json:"created_address"`
}
type gasFixtureSend struct {
	RPCSeq int `json:"rpc_seq"`; ExecutionSeq int `json:"execution_seq"`
	CaseID int `json:"case_id"`; CaseTitle string `json:"case_title"`
	Phase string `json:"phase"`; Ordinal int `json:"phase_send_ordinal"`
	RawTransaction string `json:"raw_transaction"`; TransactionHash string `json:"transaction_hash"`
	Expected gasFixtureExpected `json:"expected"`; Observed gasFixtureObserved `json:"observed"`
	CallbackObserved bool `json:"callback_observed"`; ExpectedMatches bool `json:"expected_matches"`
	Input gasFixtureSpec `json:"input"`; Result gasFixtureSpec `json:"result"`
}
type gasFixtureCase struct {
	ID int `json:"id"`; Title string `json:"title"`; AuthorState string `json:"author_state"`
	FixtureSends int `json:"fixture_sends"`; BodySends int `json:"body_sends"`
}
type gasFixtureReceipt struct {
	Schema string `json:"schema"`; Complete bool `json:"complete"`; Errors []json.RawMessage `json:"errors"`
	SourcePin string `json:"source_pin"`; Fork string `json:"fork"`
	OriginalAssertionsPassed bool `json:"original_assertions_passed"`; ProviderClosed bool `json:"provider_closed"`
	Cases []gasFixtureCase `json:"cases"`; ExpectedCases []string `json:"expected_cases"`
	RawSends []gasFixtureSend `json:"raw_sends"`; Files []gasFixtureSpec `json:"files"`; Inputs []gasFixtureSpec `json:"inputs"`
}
type gasFixtureAccount struct {
	Nonce string `json:"nonce"`; Balance string `json:"balance"`; Code string `json:"code"`
	CodeHash string `json:"code_hash"`; Storage map[string]string `json:"storage"`
}
type gasFixtureInput struct {
	Schema string `json:"schema"`; ExecutionSeq int `json:"execution_seq"`; RPCSeq int `json:"rpc_seq"`
	CaseID int `json:"case_id"`; CaseTitle string `json:"case_title"`; Phase string `json:"phase"`; Ordinal int `json:"phase_send_ordinal"`
	Expected gasFixtureExpected `json:"expected"`; RawTransaction string `json:"raw_transaction"`; TransactionHash string `json:"transaction_hash"`
	Message struct {
		From string `json:"from"`; To *string `json:"to"`; Nonce string `json:"nonce"`; Gas string `json:"gas"`
		GasPrice string `json:"gas_price"`; Value string `json:"value"`; Data string `json:"data"`; ChainID string `json:"chain_id"`
	} `json:"message"`
	Block struct {
		Header map[string]string `json:"header"`; RawHeader []string `json:"raw_header"`
		Hardfork string `json:"hardfork"`; ChainID string `json:"chain_id"`; NetworkID string `json:"network_id"`
		BaseFee *string `json:"base_fee_per_gas"`; PrevRandao *string `json:"prev_randao"`
	} `json:"block"`
	BlockHashWindow []struct { Number string `json:"number"`; Hash string `json:"hash"`; RawHeader []string `json:"raw_header"` } `json:"block_hash_window"`
	Prestate struct {
		Complete bool `json:"complete"`; AccountCount int `json:"account_count"`
		Accounts map[string]gasFixtureAccount `json:"accounts"`
		AccountRebuilt bool `json:"account_complete_by_rebuilt_root"`; StorageRebuilt bool `json:"storage_complete_by_rebuilt_root"`
		CacheDirty bool `json:"cache_dirty"`; GanacheRoot string `json:"ganache_state_root"`
	} `json:"prestate"`
}
type gasFixtureResult struct {
	Schema string `json:"schema"`; ExecutionSeq int `json:"execution_seq"`; RPCSeq int `json:"rpc_seq"`
	Expected gasFixtureExpected `json:"expected"`; Observed gasFixtureObserved `json:"observed"`; ExpectedMatches bool `json:"expected_matches"`
}
type gasFixtureEstimate struct {
	Estimate uint64 `json:"estimate"`; ReturnData string `json:"return_data"`; Error string `json:"error"`
	Reverted bool `json:"reverted"`
	Trials []TrialReport `json:"trials"`
}
type gasFixtureRow struct {
	ExecutionSeq int `json:"execution_seq"`; RPCSeq int `json:"rpc_seq"`; CaseID int `json:"case_id"`; CaseTitle string `json:"case_title"`
	Phase string `json:"phase"`; Ordinal int `json:"phase_send_ordinal"`; TransactionHash string `json:"transaction_hash"`
	Input gasFixtureSpec `json:"input"`; Result gasFixtureSpec `json:"result"`; Expected gasFixtureExpected `json:"expected"`
	Ganache gasFixtureObserved `json:"ganache"`; GethPrestateRoot common.Hash `json:"geth_prestate_root"`
	GanachePrestateRawRoot string `json:"ganache_prestate_raw_root"`
	OriginalNonce uint64 `json:"original_nonce"`; OriginalGasLimit uint64 `json:"original_gas_limit"`; OriginalGasPrice string `json:"original_gas_price"`
	HeaderHash common.Hash `json:"header_hash"`; OriginalDifficulty string `json:"original_difficulty"`; ContextRandomPresent bool `json:"context_random_present"`; Rules params.Rules `json:"rules"`
	OriginalReplay *gasFixtureReplay `json:"original_replay,omitempty"`
	Stock gasFixtureEstimate `json:"stock"`; CertificateEstimate gasFixtureEstimate `json:"certificate_estimate"`; Certificate CertificateReport `json:"certificate"`
	StockReturnedReplay *gasFixtureReplay `json:"stock_returned_replay,omitempty"`
	CertificateHighReplay *gasFixtureReplay `json:"certificate_high_replay,omitempty"`; CertificateCandidateReplay *gasFixtureReplay `json:"certificate_candidate_replay,omitempty"`
	CertificateEquivalent bool `json:"certificate_equivalent"`; Errors []string `json:"errors"`
	StockReturnedEquivalentToOriginal bool `json:"stock_returned_equivalent_to_original"`
}

func gasFixturePath(path string) (string,error) {
	clean:=filepath.Clean(path)
	if !filepath.IsAbs(clean)||clean!=path {return "",fmt.Errorf("canonical absolute input path required")}
	p,err:=filepath.EvalSymlinks(clean);if err!=nil{return "",err}
	if p!=clean{return "",fmt.Errorf("symlink input path is not canonical")};return p,nil
}
func gasFixtureCheck(s gasFixtureSpec) error {
	p,err:=gasFixturePath(s.Path); if err!=nil { return err }
	f,err:=os.Open(p); if err!=nil { return err }; defer f.Close()
	h:=sha256.New(); n,err:=io.Copy(h,f); if err!=nil { return err }
	if n!=s.Bytes || hex.EncodeToString(h.Sum(nil))!=s.SHA256 { return fmt.Errorf("FileSpec mismatch: %s",s.Path) }; return nil
}
func gasFixtureRead(s gasFixtureSpec,v any) error { if err:=gasFixtureCheck(s);err!=nil{return err}; b,e:=os.ReadFile(s.Path);if e!=nil{return e};return json.Unmarshal(b,v) }
func gasFixtureHex(s string,n int) ([]byte,error) {
	if !strings.HasPrefix(s,"0x") {return nil,fmt.Errorf("missing hex prefix")}; b,e:=hex.DecodeString(s[2:]); if e!=nil{return nil,e}
	if n>=0 && len(b)!=n{return nil,fmt.Errorf("hex length %d != %d",len(b),n)};return b,nil
}
func gasFixtureBig(s string) (*big.Int,error) { if !strings.HasPrefix(s,"0x"){return nil,fmt.Errorf("missing quantity prefix")};v,ok:=new(big.Int).SetString(s[2:],16);if !ok||v.Sign()<0{return nil,fmt.Errorf("invalid quantity")};return v,nil }
func gasFixtureU64(s string) (uint64,error) {v,e:=gasFixtureBig(s);if e!=nil{return 0,e};if !v.IsUint64(){return 0,fmt.Errorf("quantity exceeds uint64")};return v.Uint64(),nil}
func gasFixtureAddress(s string) (common.Address,error) {b,e:=gasFixtureHex(s,20);return common.BytesToAddress(b),e}
func gasFixtureHash(s string) (common.Hash,error) {b,e:=gasFixtureHex(s,32);return common.BytesToHash(b),e}
func gasFixtureHeader(raw []string) (*types.Header,error) {
	if len(raw)!=15{return nil,fmt.Errorf("not a 15-field legacy header")}
	x:=make([][]byte,15);for i,s:=range raw{b,e:=gasFixtureHex(s,-1);if e!=nil{return nil,e};x[i]=b}
	b,e:=rlp.EncodeToBytes(x);if e!=nil{return nil,e};var h types.Header
	if e=rlp.DecodeBytes(b,&h);e!=nil{return nil,e};back,e:=rlp.EncodeToBytes(&h)
	if e!=nil||!bytes.Equal(b,back){return nil,fmt.Errorf("header RLP did not round trip")};return &h,nil
}

// This transaction-only header reader obtains Author from the captured legacy
// coinbase. Consensus import, validation, and finalization are outside the
// harness. The embedded Engine interface makes unexpected consensus calls fail.
func gasFixtureContext(in *gasFixtureInput) (*gasFixtureChain,error) {
	if in.Block.Hardfork!="istanbul"||in.Block.BaseFee!=nil||in.Block.PrevRandao!=nil{return nil,fmt.Errorf("original Istanbul context differs")}
	id,ok:=new(big.Int).SetString(in.Block.ChainID,10);if !ok||id.Sign()<0{return nil,fmt.Errorf("invalid chain ID")}
	h,e:=gasFixtureHeader(in.Block.RawHeader);if e!=nil{return nil,e}
	names:=[]string{"parentHash","uncleHash","coinbase","stateRoot","transactionsTrie","receiptTrie","bloom","difficulty","number","gasLimit","gasUsed","timestamp","extraData","mixHash","nonce"}
	if len(in.Block.Header)!=15{return nil,fmt.Errorf("header field map differs")}
	for i,n:=range names{a,e:=gasFixtureHex(in.Block.Header[n],-1);if e!=nil{return nil,e};b,_:=gasFixtureHex(in.Block.RawHeader[i],-1);if !bytes.Equal(a,b){return nil,fmt.Errorf("header map/raw mismatch")}}
	z:=func()*big.Int{return new(big.Int)}
	cfg:=&params.ChainConfig{ChainID:id,HomesteadBlock:z(),EIP150Block:z(),EIP155Block:z(),EIP158Block:z(),ByzantiumBlock:z(),ConstantinopleBlock:z(),PetersburgBlock:z(),IstanbulBlock:z()}
	c:=&gasFixtureChain{config:cfg,current:h,numbers:make(map[uint64]*types.Header),hashes:make(map[common.Hash]*types.Header)}
	if h.Number==nil||!h.Number.IsUint64()||h.Number.Sign()<=0{return nil,fmt.Errorf("invalid block number")}
	n:=h.Number.Uint64();start:=uint64(0);if n>256{start=n-256}
	if uint64(len(in.BlockHashWindow))!=n-start{return nil,fmt.Errorf("incomplete BLOCKHASH window")}
	for i,w:=range in.BlockHashWindow{nr,e:=gasFixtureU64(w.Number);if e!=nil||nr!=start+uint64(i){return nil,fmt.Errorf("BLOCKHASH window order")};a,e:=gasFixtureHeader(w.RawHeader);if e!=nil{return nil,e};hash,e:=gasFixtureHash(w.Hash);if e!=nil||a.Hash()!=hash||a.Number.Uint64()!=nr{return nil,fmt.Errorf("historical header identity mismatch")};c.numbers[nr]=a;c.hashes[hash]=a;if i>0&&a.ParentHash!=c.numbers[nr-1].Hash(){return nil,fmt.Errorf("historical parent mismatch")}}
	if c.numbers[n-1].Hash()!=h.ParentHash{return nil,fmt.Errorf("current parent mismatch")}
	bc:=core.NewEVMBlockContext(h,c,nil)
	r:=cfg.Rules(h.Number,bc.Random!=nil,h.Time)
	if fork,reason:=certificateFork(r);fork!="istanbul"||reason!=""{return nil,fmt.Errorf("actual EVM rules are not Istanbul")}
	return c,nil
}

func gasFixtureMessage(in *gasFixtureInput,c *gasFixtureChain) (*core.Message,error) {
	b,e:=gasFixtureHex(in.RawTransaction,-1);if e!=nil{return nil,e};var tx types.Transaction
	if e=tx.UnmarshalBinary(b);e!=nil{return nil,e};back,e:=tx.MarshalBinary();if e!=nil||!bytes.Equal(b,back){return nil,fmt.Errorf("transaction RLP mismatch")}
	h,e:=gasFixtureHash(in.TransactionHash);if e!=nil||tx.Hash()!=h||tx.Type()!=types.LegacyTxType{return nil,fmt.Errorf("transaction identity/type mismatch")}
	m,e:=core.TransactionToMessage(&tx,types.NewEIP155Signer(tx.ChainId()),nil);if e!=nil{return nil,e}
	from,e:=gasFixtureAddress(in.Message.From);if e!=nil||m.From!=from{return nil,fmt.Errorf("sender mismatch")}
	if (m.To==nil)!=(in.Message.To==nil){return nil,fmt.Errorf("destination shape mismatch")};if m.To!=nil{a,e:=gasFixtureAddress(*in.Message.To);if e!=nil||a!=*m.To{return nil,fmt.Errorf("destination mismatch")}}
	for _,q:=range []struct{s string;v *big.Int}{{in.Message.Nonce,new(big.Int).SetUint64(m.Nonce)},{in.Message.Gas,new(big.Int).SetUint64(m.GasLimit)},{in.Message.GasPrice,m.GasPrice.ToBig()},{in.Message.Value,m.Value.ToBig()}}{v,e:=gasFixtureBig(q.s);if e!=nil||v.Cmp(q.v)!=0{return nil,fmt.Errorf("message numeric mismatch")}}
	data,e:=gasFixtureHex(in.Message.Data,-1);if e!=nil||!bytes.Equal(data,m.Data){return nil,fmt.Errorf("message data mismatch")}
	id,ok:=new(big.Int).SetString(in.Message.ChainID,10);if !ok||id.Cmp(tx.ChainId())!=0{return nil,fmt.Errorf("signed transaction chain ID mismatch")}
	return m,nil
}

func gasFixtureImport(in *gasFixtureInput) (*state.StateDB,common.Hash,error) {
	if !in.Prestate.Complete||!in.Prestate.AccountRebuilt||!in.Prestate.StorageRebuilt||in.Prestate.CacheDirty||len(in.Prestate.Accounts)!=in.Prestate.AccountCount{return nil,common.Hash{},fmt.Errorf("prestate incomplete")}
	db:=state.NewDatabaseForTesting();s,e:=state.New(types.EmptyRootHash,db);if e!=nil{return nil,common.Hash{},e}
	addresses:=make([]string,0,len(in.Prestate.Accounts));for a:=range in.Prestate.Accounts{addresses=append(addresses,a)};sort.Strings(addresses)
	for _,key:=range addresses{
		a,e:=gasFixtureAddress(key);if e!=nil{return nil,common.Hash{},e};v:=in.Prestate.Accounts[key]
		n,e:=gasFixtureU64(v.Nonce);if e!=nil{return nil,common.Hash{},e};balance,e:=gasFixtureBig(v.Balance);if e!=nil{return nil,common.Hash{},e};u,overflow:=uint256.FromBig(balance);if overflow{return nil,common.Hash{},fmt.Errorf("balance overflow")}
		code,e:=gasFixtureHex(v.Code,-1);if e!=nil{return nil,common.Hash{},e};ch,e:=gasFixtureHash(v.CodeHash);if e!=nil||crypto.Keccak256Hash(code)!=ch{return nil,common.Hash{},fmt.Errorf("code hash mismatch")}
		s.CreateAccount(a);s.SetBalance(a,u,tracing.BalanceChangeUnspecified);s.SetNonce(a,n,tracing.NonceChangeUnspecified);s.SetCode(a,code,tracing.CodeChangeUnspecified)
		for k,x:=range v.Storage{slot,e:=gasFixtureHash(k);if e!=nil{return nil,common.Hash{},e};value,e:=gasFixtureHash(x);if e!=nil||value==(common.Hash{}){return nil,common.Hash{},fmt.Errorf("invalid nonzero storage")};s.SetState(a,slot,value)}
	}
	// State import reconstructs the captured representation and preserves every
	// source account, including empty accounts, when committing the imported trie.
	root,e:=s.Commit(params.Rules{},0);if e!=nil{return nil,common.Hash{},e};if e=db.TrieDB().Commit(root,false);e!=nil{return nil,common.Hash{},e}
	s,e=state.New(root,db);if e!=nil{return nil,common.Hash{},e}
	for _,key:=range addresses{a,_:=gasFixtureAddress(key);v:=in.Prestate.Accounts[key];n,_:=gasFixtureU64(v.Nonce);b,_:=gasFixtureBig(v.Balance);code,_:=gasFixtureHex(v.Code,-1)
		if !s.Exist(a)||s.GetNonce(a)!=n||s.GetBalance(a).ToBig().Cmp(b)!=0||!bytes.Equal(s.GetCode(a),code){return nil,common.Hash{},fmt.Errorf("imported account differs")}
		for k,x:=range v.Storage{slot,_:=gasFixtureHash(k);value,_:=gasFixtureHash(x);if s.GetState(a,slot)!=value{return nil,common.Hash{},fmt.Errorf("imported storage differs")}}
	}
	if e=s.Error();e!=nil{return nil,common.Hash{},e};return s,root,nil
}

// Run Estimate on fresh message and state copies and record its result and
// trial sequence for functional qualification outside paid service measurement.
// gasFixtureOne performs the independent replays and fee-settled-state comparisons.
func gasFixtureEstimateRun(c *gasFixtureChain,base *state.StateDB,m *core.Message,certificate *CertificateReport) gasFixtureEstimate {
	row:=gasFixtureEstimate{Trials:make([]TrialReport,0)}
	opts:=&Options{Config:c.config,Chain:c,Header:c.current,State:base.Copy(),ErrorRatio:0.015,Certificate:certificate,OnTrial:func(t TrialReport){row.Trials=append(row.Trials,t)}}
	message:=*m
	limit,data,e:=Estimate(context.Background(),&message,opts,0)
	row.Estimate,row.ReturnData,row.Error=limit,gasFixtureHexOut(data),gasFixtureError(e)
	row.Reverted=errors.Is(e,vm.ErrExecutionReverted)
	return row
}

func gasFixtureOne(send gasFixtureSend) (row gasFixtureRow) {
	row=gasFixtureRow{ExecutionSeq:send.ExecutionSeq,RPCSeq:send.RPCSeq,CaseID:send.CaseID,CaseTitle:send.CaseTitle,Phase:send.Phase,Ordinal:send.Ordinal,TransactionHash:send.TransactionHash,Input:send.Input,Result:send.Result,Expected:send.Expected,Ganache:send.Observed,Errors:make([]string,0)}
	defer func(){if p:=recover();p!=nil{row.Errors=append(row.Errors,fmt.Sprintf("unexpected harness panic: %v",p))}}()
	fail:=func(stage string,e error){row.Errors=append(row.Errors,stage+": "+gasFixtureError(e))}
	var in gasFixtureInput;var result gasFixtureResult
	if e:=gasFixtureRead(send.Input,&in);e!=nil{fail("input",e);return}
	if e:=gasFixtureRead(send.Result,&result);e!=nil{fail("original result",e);return}
	if in.Schema!=gasFixtureSchema||result.Schema!=gasFixtureSchema||in.ExecutionSeq!=send.ExecutionSeq||in.RPCSeq!=send.RPCSeq||result.ExecutionSeq!=send.ExecutionSeq||result.RPCSeq!=send.RPCSeq||in.CaseID!=send.CaseID||in.CaseTitle!=send.CaseTitle||in.Phase!=send.Phase||in.Ordinal!=send.Ordinal||in.RawTransaction!=send.RawTransaction||in.TransactionHash!=send.TransactionHash||!reflect.DeepEqual(in.Expected,send.Expected)||!reflect.DeepEqual(result.Expected,send.Expected)||!result.ExpectedMatches||!reflect.DeepEqual(result.Observed,send.Observed){fail("join",fmt.Errorf("input/result/receipt identity differs"));return}
	c,e:=gasFixtureContext(&in);if e!=nil{fail("context",e);return}
	bc:=core.NewEVMBlockContext(c.current,c,nil)
	row.HeaderHash,row.OriginalDifficulty,row.ContextRandomPresent=c.current.Hash(),c.current.Difficulty.String(),bc.Random!=nil
	row.Rules=c.config.Rules(bc.BlockNumber,bc.Random!=nil,bc.Time)
	m,e:=gasFixtureMessage(&in,c);if e!=nil{fail("raw message",e);return}
	row.OriginalNonce,row.OriginalGasLimit,row.OriginalGasPrice=m.Nonce,m.GasLimit,m.GasPrice.ToBig().String()
	row.GanachePrestateRawRoot=in.Prestate.GanacheRoot
	base,root,e:=gasFixtureImport(&in);if e!=nil{fail("state import",e);return};row.GethPrestateRoot=root
	defer func(){if e:=base.Database().TrieDB().Close();e!=nil{fail("memory trie close",e)}}()
	if base.GetNonce(m.From)!=m.Nonce{fail("original nonce",fmt.Errorf("prestate nonce differs from signed transaction"));return}
	hash,_:=gasFixtureHash(in.TransactionHash)
	original,e:=gasFixtureExecute(c,base,m,hash,m.GasLimit);if e!=nil{fail("original replay",e);return};row.OriginalReplay=original
	gas,ok:=new(big.Int).SetString(send.Observed.GasUsed,10)
	if !ok||!gas.IsUint64()||gas.Uint64()!=original.UsedGas||original.Status!=send.Observed.Status||original.Status!=send.Expected.Status||!strings.EqualFold(original.ReturnData,send.Observed.ReturnData)||!reflect.DeepEqual(original.Logs,send.Observed.Logs)||!reflect.DeepEqual(original.CreatedAddress,send.Observed.CreatedAddress){fail("Ganache interpretation",fmt.Errorf("status/return/logs/charged gas/created address differs"))}
	row.Stock=gasFixtureEstimateRun(c,base,m,nil)
	row.CertificateEstimate=gasFixtureEstimateRun(c,base,m,&row.Certificate)
	if row.Stock.Error=="" {
		v,e:=gasFixtureExecute(c,base,m,hash,row.Stock.Estimate);if e!=nil{fail("stock returned budget",e)}else{row.StockReturnedReplay=v;row.StockReturnedEquivalentToOriginal=gasFixtureEqual(original,v);if v.Status!=1{fail("stock returned budget",fmt.Errorf("did not succeed"))}}
		if send.Expected.Status!=1{fail("stock result",fmt.Errorf("unexpected success on original K rejection"))}
	} else if send.Expected.Status!=0||!row.Stock.Reverted||!strings.EqualFold(row.Stock.ReturnData,send.Observed.ReturnData){fail("stock rejection",fmt.Errorf("does not reproduce original K rejection"))}
	if !row.Certificate.Certified {
		// Both paths must reuse identical original high/search decisions when
		// unsupported, including plain transfer and original application failure.
		if !reflect.DeepEqual(row.Stock,row.CertificateEstimate){fail("fallback",fmt.Errorf("stock and unsupported certificate estimator differ"))}
		return
	}
	if row.CertificateEstimate.Error!=""||row.CertificateEstimate.Estimate!=row.Certificate.Candidate||!row.Certificate.FirstRunExecuted||row.Certificate.Fork!="istanbul"||len(row.CertificateEstimate.Trials)!=1||row.CertificateEstimate.Trials[0].GasLimit!=row.Certificate.High||row.Certificate.High>m.GasLimit||row.Certificate.Candidate>row.Certificate.High{fail("certificate adapter",fmt.Errorf("trial/candidate/fork contract differs"));return}
	high,e:=gasFixtureExecute(c,base,m,hash,row.Certificate.High);if e!=nil{fail("independent high",e)}else{row.CertificateHighReplay=high}
	candidate,e:=gasFixtureExecute(c,base,m,hash,row.Certificate.Candidate);if e!=nil{fail("independent candidate",e)}else{row.CertificateCandidateReplay=candidate}
	if high!=nil&&candidate!=nil{
		row.CertificateEquivalent=gasFixtureEqual(high,candidate)
		if !row.CertificateEquivalent||high.Status!=1||candidate.Status!=1||row.Certificate.UsedGas!=high.UsedGas||row.Certificate.MaxUsedGas!=high.MaxUsedGas{fail("certificate equivalence",fmt.Errorf("complete state/return/logs/status/refund/gas differs"))}
	}
	return
}

func gasFixtureNames() ([]string,[]int) {
	n:=[]string{"mint"};b:=[]int{3}
	for i:=0;i<7;i++{n=append(n,fmt.Sprintf("getInputPrice:%d",i));b=append(b,6)}
	for i:=0;i<4;i++{n=append(n,fmt.Sprintf("optimistic:%d",i));b=append(b,6)}
	n=append(n,"swap:token0","swap:token1","swap:gas","burn","price{0,1}CumulativeLast","feeTo:off","feeTo:on")
	b=append(b,5,5,6,5,7,7,8);return n,b
}
func gasFixtureCohort(r *gasFixtureReceipt) error {
	names,body:=gasFixtureNames()
	if r.Schema!=gasFixtureSchema||!r.Complete||len(r.Errors)!=0||!r.ProviderClosed||!r.OriginalAssertionsPassed||r.SourcePin!="4dd59067c76dea4a0e8e4bfdda41877a6b16dedc"||r.Fork!="istanbul"||len(r.RawSends)!=116||len(r.Cases)!=19||!reflect.DeepEqual(names,r.ExpectedCases){return fmt.Errorf("materialization is not the complete fixed original cohort")}
	counts:=make([][2]int,19);seenExecution:=make(map[int]bool);ordinals:=make(map[string]bool);rejects:=0
	for i,c:=range r.Cases{fixture:=0;if i==0{fixture=4};if c.ID!=i||c.Title!=names[i]||c.AuthorState!="passed"||c.BodySends!=body[i]||c.FixtureSends!=fixture{return fmt.Errorf("original case/order/count differs")}}
	lastCase:=-1
	for i,s:=range r.RawSends{
		if s.RPCSeq!=i||s.ExecutionSeq<0||s.ExecutionSeq>=116||seenExecution[s.ExecutionSeq]||s.CaseID<0||s.CaseID>=19||s.CaseTitle!=names[s.CaseID]||s.CaseID<lastCase||!s.CallbackObserved||!s.ExpectedMatches{return fmt.Errorf("raw invocation identity/order differs")};lastCase=s.CaseID;seenExecution[s.ExecutionSeq]=true
		phase:=0;if s.Phase=="body"{phase=1}else if s.Phase!="fixture"{return fmt.Errorf("unknown phase")}
		counts[s.CaseID][phase]++
		if s.Ordinal!=counts[s.CaseID][phase]{return fmt.Errorf("original phase ordinal differs")}
		key:=fmt.Sprintf("%d/%s/%d",s.CaseID,s.Phase,s.Ordinal);if ordinals[key]{return fmt.Errorf("duplicate phase ordinal")};ordinals[key]=true
		want:=1;var reason *string
		if phase==1&&s.CaseID>=1&&s.CaseID<=11&&s.Ordinal==5{want=0;k:="UniswapV2: K";reason=&k;rejects++}
		if s.Expected.Status!=want||!reflect.DeepEqual(s.Expected.RevertReason,reason)||s.Observed.Status!=want||!reflect.DeepEqual(s.Observed.RevertReason,reason){return fmt.Errorf("source-fixed success/rejection differs")}
	}
	for i,c:=range counts{f:=0;if i==0{f=4};if c[0]!=f||c[1]!=body[i]{return fmt.Errorf("complete phase count differs")}}
	if rejects!=11{return fmt.Errorf("original rejection count differs")};return nil
}

func gasFixtureFileIdentity(path string) (gasFixtureSpec,error) {
	f,e:=os.Open(path);if e!=nil{return gasFixtureSpec{},e};defer f.Close();h:=sha256.New();n,e:=io.Copy(h,f)
	return gasFixtureSpec{Path:path,Bytes:n,SHA256:hex.EncodeToString(h.Sum(nil))},e
}
func gasFixtureWriteJSON(path string,v any) error {f,e:=os.OpenFile(path,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600);if e!=nil{return e};enc:=json.NewEncoder(f);enc.SetIndent("","  ");if e=enc.Encode(v);e==nil{e=f.Sync()};closeErr:=f.Close();if e!=nil{return e};return closeErr}

func TestGasCertificateFixtureCohort(t *testing.T) {
	manifestPath:=os.Getenv("GETH_CERTIFICATE_FIXTURE_MANIFEST")
	if manifestPath==""{t.Skip("requires a complete historical116 input manifest; skipped is not qualification")}
	manifestPath,e:=gasFixturePath(manifestPath);if e!=nil{t.Fatal(e)}
	manifestIdentity,e:=gasFixtureFileIdentity(manifestPath);if e!=nil{t.Fatal(e)}
	var manifest gasFixtureManifest;if e=gasFixtureRead(manifestIdentity,&manifest);e!=nil{t.Fatal(e)}
	if manifest.Schema!="round1719-gas-functional-input-v1"||manifest.MaterializationReceipt==nil||manifest.Binary==nil{t.Fatal("unbound complete materialization input/binary")}
	var receipt gasFixtureReceipt;if e=gasFixtureRead(*manifest.MaterializationReceipt,&receipt);e!=nil{t.Fatal(e)}
	if e=gasFixtureCohort(&receipt);e!=nil{t.Fatal(e)}
	protected:=make(map[string]gasFixtureSpec)
	protect:=func(s gasFixtureSpec)error{if prior,ok:=protected[s.Path];ok&&prior!=s{return fmt.Errorf("conflicting FileSpecs")};protected[s.Path]=s;return gasFixtureCheck(s)}
	for _,s:=range append(append([]gasFixtureSpec{manifestIdentity,*manifest.MaterializationReceipt,*manifest.Binary},receipt.Files...),receipt.Inputs...){if e=protect(s);e!=nil{t.Fatal(e)}}
	for _,s:=range receipt.RawSends{for _,f:=range []gasFixtureSpec{s.Input,s.Result}{if p,ok:=protected[f.Path];!ok||p!=f{t.Fatal("input/result absent from bound full materialization inventory")}}}
	if !filepath.IsAbs(manifest.OutputDirectory){t.Fatal("output directory must be absolute")}
	out:=filepath.Clean(manifest.OutputDirectory);parent,e:=gasFixturePath(filepath.Dir(out));if e!=nil{t.Fatal(e)};out=filepath.Join(parent,filepath.Base(out))
	if e=os.Mkdir(out,0700);e!=nil{t.Fatal("output must be fresh: ",e)}
	rowsPath:=filepath.Join(out,"all-inputs.jsonl");f,e:=os.OpenFile(rowsPath,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600);if e!=nil{t.Fatal(e)}
	sends:=append([]gasFixtureSend(nil),receipt.RawSends...);sort.Slice(sends,func(i,j int)bool{return sends[i].ExecutionSeq<sends[j].ExecutionSeq})
	counts:=map[string]uint64{"all_inputs":116,"original_expected_success":105,"original_expected_rejection":11}
	reasons:=make(map[string]uint64);allErrors:=make([]string,0);caseCounts:=make([]int,19);enc:=json.NewEncoder(f)
	for _,send:=range sends{
		row:=gasFixtureOne(send);caseCounts[send.CaseID]++
		if len(row.Errors)!=0{counts["inputs_with_errors"]++}
		if row.Certificate.Status==""||row.Certificate.Status=="not_reached"{counts["observer_not_reached"]++}
		counts["attempted_inputs"]++;counts["stock_estimator_trials"]+=uint64(len(row.Stock.Trials));counts["certificate_estimator_trials"]+=uint64(len(row.CertificateEstimate.Trials))
		if row.Certificate.FirstRunExecuted{counts["first_high_executions"]++;if row.Certificate.Status!="first_run_failed"{counts["first_high_successes"]++}}
		if row.Certificate.Status=="stock_transfer_fastpath"{counts["stock_transfer_shortcut"]++}
		if send.Expected.Status==1&&row.Certificate.FirstRunExecuted{counts["successful_nonshortcut_requests"]++}
		if row.Certificate.Certified{counts["certified_inputs"]++;if row.CertificateEquivalent{counts["certified_equivalent_inputs"]++}}
		for reason,n:=range row.Certificate.ReasonCounts{reasons[reason]+=n}
		for _,why:=range row.Errors{allErrors=append(allErrors,fmt.Sprintf("execution_seq=%d: %s",send.ExecutionSeq,why))}
		if e=enc.Encode(row);e!=nil{allErrors=append(allErrors,"write complete row: "+e.Error());break}
	}
	if e=f.Sync();e!=nil{allErrors=append(allErrors,"row sync: "+e.Error())};if e=f.Close();e!=nil{allErrors=append(allErrors,"row close: "+e.Error())}
	// All original FileSpecs, including unused receipt-listed normal outputs and
	// author/dependency inputs, are retained and rehashed after the whole cohort.
	paths:=make([]string,0,len(protected));for p:=range protected{paths=append(paths,p)};sort.Strings(paths)
	inputsPreserved:=true
	for _,p:=range paths{if e=gasFixtureCheck(protected[p]);e!=nil{inputsPreserved=false;allErrors=append(allErrors,"input after: "+e.Error())}}
	if counts["attempted_inputs"]!=116{allErrors=append(allErrors,"not all 116 planned inputs attempted")}
	rowsIdentity,e:=gasFixtureFileIdentity(rowsPath);if e!=nil{allErrors=append(allErrors,"row identity: "+e.Error())}
	summary:=map[string]any{
		"schema":"round1719-gas-functional-summary-v1","test_binary":*manifest.Binary,"complete":len(allErrors)==0,"errors":allErrors,"counts":counts,"first_reason_counts":reasons,"case_input_counts":caseCounts,
		"fixed_original_case_names":receipt.ExpectedCases,"manifest":manifestIdentity,"materialization_receipt":manifest.MaterializationReceipt,
		"full_rows":rowsIdentity,"input_files_before_after":protected,"inputs_preserved":inputsPreserved,
		"fork":"original Istanbul; difficulty/header retained; actual Rules checked per input","state_roots":"Geth secure-key import/poststate; never equated with Ganache raw-key trie root",
		"functional_only":true,"performance_measured":false,"trial_callbacks_installed":true,"ganache_estimator_control_implemented":false,
		"input_contract":"all payload and reference identities bound and rehashed; no author process-approval files","go_source_pin":"a579077007b98217c3e253a66e4b452ca0c32b96",
	}
	if e=gasFixtureWriteJSON(filepath.Join(out,"summary.json"),summary);e!=nil{t.Fatal(e)}
	if len(allErrors)!=0{t.Errorf("full fixed cohort recorded with %d qualification errors; see summary.json",len(allErrors))}
}
