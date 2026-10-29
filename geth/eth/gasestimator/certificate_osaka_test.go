// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: LGPL-3.0-or-later

package gasestimator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// These functional correctness fixtures use the mainnet schedule in a fixed
// post-Osaka, pre-Amsterdam context. Native transaction helpers execute the
// transactions and compare their state and effects outside performance measurement.
var (
	certificateOsakaSender = common.HexToAddress("0x10001")
	certificateOsakaRoot = common.HexToAddress("0x10002")
	certificateOsakaDelegate = common.HexToAddress("0x10003")
	certificateOsakaImplementation = common.HexToAddress("0x10004")
)

type certificateOsakaAccount struct {
	address common.Address
	code []byte
	balance uint64
	storage map[common.Hash]common.Hash
}

func certificateOsakaSetup(t *testing.T, accounts ...certificateOsakaAccount) (*gasFixtureChain, *state.StateDB) {
	t.Helper()
	z := uint64(0)
	h := &types.Header{Number:big.NewInt(24000000), Time:1792627200, Difficulty:new(big.Int),
		GasLimit:30000000, BaseFee:big.NewInt(1), Coinbase:common.HexToAddress("0xc0ffee"),
		MixDigest:common.HexToHash("0x1234"), BlobGasUsed:&z, ExcessBlobGas:&z}
	c := &gasFixtureChain{config:params.MainnetChainConfig, current:h,
		numbers:make(map[uint64]*types.Header), hashes:make(map[common.Hash]*types.Header)}
	bc := core.NewEVMBlockContext(h,c,nil)
	r := c.config.Rules(h.Number,bc.Random!=nil,h.Time)
	if f,e:=certificateFork(r); f!="osaka" || e!="" || !r.IsPrague || !r.IsCancun || r.IsAmsterdam || r.IsBogota {
		t.Fatalf("actual mainnet context is not supported Osaka: %+v %s %s",r,f,e)
	}
	db := state.NewDatabaseForTesting()
	t.Cleanup(func(){db.TrieDB().Close()})
	s,e := state.New(types.EmptyRootHash,db); if e!=nil {t.Fatal(e)}
	s.CreateAccount(certificateOsakaSender)
	s.SetBalance(certificateOsakaSender,uint256.NewInt(1000000000000000000),tracing.BalanceChangeUnspecified)
	for _,a := range accounts {
		s.CreateAccount(a.address)
		s.SetBalance(a.address,uint256.NewInt(a.balance),tracing.BalanceChangeUnspecified)
		s.SetCode(a.address,a.code,tracing.CodeChangeUnspecified)
		for k,v := range a.storage {s.SetState(a.address,k,v)}
	}
	// Commit and reopen storage so refund checks use the pre-transaction value
	// of each slot as its original value.
	root,e:=s.Commit(params.Rules{},0);if e!=nil{t.Fatal(e)}
	if e=db.TrieDB().Commit(root,false);e!=nil{t.Fatal(e)}
	s,e=state.New(root,db);if e!=nil{t.Fatal(e)}
	return c,s
}

func certificateOsakaMessage(to common.Address, data []byte) *core.Message {
	return &core.Message{From:certificateOsakaSender,To:&to,Nonce:0,GasLimit:200000,
		GasPrice:uint256.NewInt(3),GasFeeCap:uint256.NewInt(3),GasTipCap:uint256.NewInt(2),
		Value:new(uint256.Int),Data:data}
}

// Inspect the fee-settled state alongside the native replay comparison.
// Each attempt uses a fresh state copy and EVM with the same fees and fork rules.
func certificateOsakaApply(c *gasFixtureChain, base *state.StateDB, original *core.Message, limit uint64) (*core.ExecutionResult,*state.StateDB,error) {
	s:=base.Copy();m:=*original;m.GasLimit=limit
	s.SetTxContext(common.HexToHash("0x1755"),0,0)
	bc:=core.NewEVMBlockContext(c.current,c,nil)
	evm:=vm.NewEVM(bc,s,c.config,vm.Config{NoBaseFee:true});defer evm.Release()
	r,e:=core.ApplyMessage(evm,&m,nil);if e!=nil{return r,s,e}
	if e=s.Error();e!=nil{return r,s,e};return r,s,nil
}

func certificateOsakaEstimate(c *gasFixtureChain, base *state.StateDB, original *core.Message, report *CertificateReport) (uint64,[]byte,error,[]TrialReport) {
	trials:=make([]TrialReport,0)
	opts:=&Options{Config:c.config,Chain:c,Header:c.current,State:base.Copy(),ErrorRatio:.015,
		Certificate:report,OnTrial:func(r TrialReport){trials=append(trials,r)}}
	m:=*original
	g,data,e:=Estimate(context.Background(),&m,opts,0)
	return g,data,e,trials
}

func certificateOsakaCheck(t *testing.T,c *gasFixtureChain,base *state.StateDB,m *core.Message,wantFloor uint64) (CertificateReport,*core.ExecutionResult,*state.StateDB) {
	t.Helper()
	high,e:=gasFixtureExecute(c,base,m,common.HexToHash("0x1755"),m.GasLimit)
	if e!=nil || high.Status!=1 {t.Fatalf("high execution: %+v %v",high,e)}
	var report CertificateReport
	g,data,e,trials:=certificateOsakaEstimate(c,base,m,&report)
	if e!=nil || len(data)!=0 || !report.Certified || report.Fork!="osaka" || report.Floor!=wantFloor || report.Candidate!=g {
		t.Fatalf("Osaka certificate: gas=%d data=%x err=%v report=%+v",g,data,e,report)
	}
	if len(trials)!=1 {t.Fatalf("certificate did not replace only the post-high search: %+v",trials)}
	bc:=core.NewEVMBlockContext(c.current,c,nil)
	rules:=c.config.Rules(bc.BlockNumber,bc.Random!=nil,bc.Time)
	i,e:=core.IntrinsicGas(m.Data,m.AccessList,m.SetCodeAuthorizations,m.From,m.To,m.Value,rules)
	if e!=nil || report.Intrinsic!=i || report.RootEntry!=m.GasLimit-i {t.Fatalf("intrinsic/root entry: %d %v %+v",i,e,report)}
	f,e:=core.FloorDataGas(rules,m.From,m.To,m.Value,m.Data,m.AccessList)
	if e!=nil || f!=wantFloor {t.Fatalf("real floor: %d want %d err %v",f,wantFloor,e)}
	b,ok:=addCertificate(i,report.Minimum)
	if !ok || g!=max(b,f) || g>m.GasLimit {t.Fatalf("candidate is not checked max(I+M,F): %+v",report)}
	// All legal endpoints of the claimed interval (and an interior point) run
	// through actual ApplyMessage, including final fees, logs and state root.
	for _,limit:=range []uint64{g,g+(m.GasLimit-g)/2,m.GasLimit} {
		got,e:=gasFixtureExecute(c,base,m,common.HexToHash("0x1755"),limit)
		if e!=nil || !gasFixtureEqual(high,got) {t.Fatalf("full high/candidate replay differs at %d: high=%+v got=%+v err=%v",limit,high,got,e)}
	}
	r,s,e:=certificateOsakaApply(c,base,m,g)
	if e!=nil || r.Failed() {t.Fatalf("candidate native outcome %v %+v",e,r)}
	before:=base.GetBalance(m.From).ToBig()
	fee:=new(big.Int).Mul(new(big.Int).SetUint64(r.UsedGas),m.GasPrice.ToBig())
	want:=new(big.Int).Sub(before,fee);want.Sub(want,m.Value.ToBig())
	if m.GasPrice.IsZero() || s.GetBalance(m.From).ToBig().Cmp(want)!=0 {t.Fatal("original positive fee settlement was not retained")}
	preRefund,ok:=addCertificate(i,report.Depletion);if !ok{t.Fatal("depletion overflow")}
	refund:=min(preRefund/5,s.GetRefund())
	if r.UsedGas!=max(preRefund-refund,f) {t.Fatalf("native refund/floor settlement differs: used=%d before=%d refund=%d floor=%d",r.UsedGas,preRefund,refund,f)}
	if f>i {
		_,_,e:=certificateOsakaApply(c,base,m,f-1)
		if !errors.Is(e,core.ErrFloorDataGas) {t.Fatalf("F-1 did not use actual floor admission: %v",e)}
	}
	return report,r,s
}

func certificateOsakaReturnLog() []byte {
	// Store 42, emit a LOG0 of that word, return the identical 32 bytes.
	return []byte{byte(vm.PUSH1),42,byte(vm.PUSH1),0,byte(vm.MSTORE),
		byte(vm.PUSH1),32,byte(vm.PUSH1),0,byte(vm.LOG0),
		byte(vm.PUSH1),32,byte(vm.PUSH1),0,byte(vm.RETURN)}
}

func TestCertificateOsakaFloorAndFeeSettlement(t *testing.T) {
	t.Run("legacy_report_omits_floor",func(t *testing.T){
		for _,fork:=range []string{"istanbul","shanghai"} {
			r:=initialCertificateReport();r.Fork=fork
			b,e:=json.Marshal(r);if e!=nil{t.Fatal(e)}
			if bytes.Contains(b,[]byte(`"floor":`)){t.Fatal("zero Osaka field changed legacy JSON")}
		}
	})
	one:=common.HexToHash("0x01")
	access:=types.AccessList{{Address:common.HexToAddress("0x20001"),StorageKeys:[]common.Hash{{},{1}}}}
	cases:=[]struct{
		name string;data,code []byte;access types.AccessList;original common.Hash;floor uint64;execution,refund bool
	}{
		{"empty",nil,[]byte{byte(vm.STOP)},nil,common.Hash{},21000,false,false},
		{"zero_data",bytes.Repeat([]byte{0},100),[]byte{byte(vm.STOP)},nil,common.Hash{},22000,false,false},
		{"nonzero_data",bytes.Repeat([]byte{255},100),certificateOsakaReturnLog(),nil,common.Hash{},25000,false,false},
		{"mixed_data",append(bytes.Repeat([]byte{0},50),bytes.Repeat([]byte{255},50)...),certificateOsakaReturnLog(),nil,common.Hash{},23500,false,false},
		{"access_list_intrinsic",bytes.Repeat([]byte{255},10),[]byte{byte(vm.STOP)},access,common.Hash{},21400,true,false},
		{"execution_dominant",nil,append([]byte{byte(vm.PUSH1),1,byte(vm.PUSH1),0,byte(vm.SSTORE)},certificateOsakaReturnLog()...),nil,common.Hash{},21000,true,false},
		{"refund_floor",bytes.Repeat([]byte{255},100),append([]byte{byte(vm.PUSH1),0,byte(vm.PUSH1),0,byte(vm.SSTORE)},certificateOsakaReturnLog()...),nil,one,25000,true,true},
	}
	for _,tc:=range cases {t.Run(tc.name,func(t *testing.T){
		c,base:=certificateOsakaSetup(t,certificateOsakaAccount{address:certificateOsakaRoot,code:tc.code,storage:map[common.Hash]common.Hash{{}:tc.original}})
		m:=certificateOsakaMessage(certificateOsakaRoot,tc.data);m.AccessList=tc.access
		r,out,s:=certificateOsakaCheck(t,c,base,m,tc.floor)
		if tc.execution {if r.Candidate<=r.Floor {t.Fatal("execution/intrinsic-dominant boundary was not exercised")}} else if r.Candidate!=r.Floor {t.Fatal("floor-dominant boundary was not exercised")}
		if tc.refund && (s.GetRefund()==0 || out.UsedGas!=tc.floor) {t.Fatalf("refund floor not exercised: refund=%d used=%d",s.GetRefund(),out.UsedGas)}
		if tc.name=="execution_dominant" && s.GetState(certificateOsakaRoot,common.Hash{})!=one {t.Fatal("actual state change missing")}
		if tc.name=="refund_floor" && s.GetState(certificateOsakaRoot,common.Hash{})!=(common.Hash{}) {t.Fatal("actual original slot clear missing")}
		if tc.name=="nonzero_data" || tc.name=="mixed_data" || tc.name=="execution_dominant" || tc.name=="refund_floor" {
			if len(out.ReturnData)!=32 || out.ReturnData[31]!=42 || len(s.GetLogs(common.HexToHash("0x1755"),c.current.Number.Uint64(),c.current.Hash(),c.current.Time))!=1 {t.Fatal("return/log path not exercised")}
		}
	})}
}

func TestCertificateOsakaNestedAndDelegatedCalls(t *testing.T) {
	t.Run("top_delegated_positive_fee",func(t *testing.T){
		code:=append([]byte{byte(vm.PUSH1),1,byte(vm.PUSH1),0,byte(vm.SSTORE)},certificateOsakaReturnLog()...)
		c,base:=certificateOsakaSetup(t,
			certificateOsakaAccount{address:certificateOsakaDelegate,code:types.AddressToDelegation(certificateOsakaImplementation)},
			certificateOsakaAccount{address:certificateOsakaImplementation,code:code})
		m:=certificateOsakaMessage(certificateOsakaDelegate,nil)
		r,_,s:=certificateOsakaCheck(t,c,base,m,21000)
		if r.CompletedFrames!=1 || s.GetState(certificateOsakaDelegate,common.Hash{})!=common.HexToHash("0x01") || s.GetState(certificateOsakaImplementation,common.Hash{})!=(common.Hash{}) {t.Fatal("delegated storage context differs")}
		if !bytes.Equal(s.GetCode(certificateOsakaDelegate),base.GetCode(certificateOsakaDelegate)) {t.Fatal("existing delegation was changed")}
	})
	t.Run("nested_delegated_sentry",func(t *testing.T){
		// Warm no-op SSTORE costs 100 gas and requires gasleft > 2300 under
		// EIP-2200. The value-carrying CALL adds the 2300-gas stipend.
		child:=[]byte{byte(vm.PUSH1),0,byte(vm.PUSH1),0,byte(vm.SSTORE),byte(vm.PUSH1),42,byte(vm.PUSH1),0,byte(vm.MSTORE),byte(vm.PUSH1),32,byte(vm.PUSH1),0,byte(vm.RETURN)}
		parent:=[]byte{byte(vm.PUSH1),32,byte(vm.PUSH1),0,byte(vm.PUSH1),0,byte(vm.PUSH1),0,byte(vm.PUSH1),1,byte(vm.PUSH20)}
		parent=append(parent,certificateOsakaDelegate[:]...)
		parent=append(parent,byte(vm.GAS),byte(vm.CALL),byte(vm.POP),byte(vm.PUSH1),32,byte(vm.PUSH1),0,byte(vm.RETURN))
		c,base:=certificateOsakaSetup(t,
			certificateOsakaAccount{address:certificateOsakaRoot,code:parent,balance:10000},
			certificateOsakaAccount{address:certificateOsakaDelegate,code:types.AddressToDelegation(certificateOsakaImplementation)},
			certificateOsakaAccount{address:certificateOsakaImplementation,code:child})
		m:=certificateOsakaMessage(certificateOsakaRoot,nil)
		m.AccessList=types.AccessList{{Address:certificateOsakaDelegate,StorageKeys:[]common.Hash{{}}}}
		r,out,s:=certificateOsakaCheck(t,c,base,m,21000)
		if r.CompletedCalls!=1 || r.CompletedFrames!=2 || r.GasCapCalls!=1 || r.Sstores!=1 || r.Minimum<=r.Depletion {t.Fatalf("nested sentry/cap path not exercised: %+v",r)}
		if len(out.ReturnData)!=32 || out.ReturnData[31]!=42 || s.GetBalance(certificateOsakaDelegate).Uint64()!=1 || s.GetBalance(certificateOsakaRoot).Uint64()!=9999 {t.Fatal("actual delegated CALL return/value differs")}
	})
}

func TestCertificateOsakaUnsupportedFallback(t *testing.T) {
	for _,kind:=range []string{"blob_fields","authorization_list"} {t.Run(kind,func(t *testing.T){
		accounts:=[]certificateOsakaAccount{{address:certificateOsakaRoot,code:[]byte{byte(vm.STOP)}},{address:certificateOsakaImplementation,code:[]byte{byte(vm.STOP)}}}
		m:=certificateOsakaMessage(certificateOsakaRoot,nil)
		var authority common.Address
		if kind=="blob_fields" {
			m.BlobHashes=[]common.Hash{{1}} // One valid version-1 blob hash for transaction-field eligibility.
			m.BlobGasFeeCap=uint256.NewInt(2)
		} else {
			// A valid authorization exercises the unsupported transaction-field fallback.
			key,e:=crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001");if e!=nil{t.Fatal(e)}
			authority=crypto.PubkeyToAddress(key.PublicKey)
			auth,e:=types.SignSetCode(key,types.SetCodeAuthorization{ChainID:*uint256.NewInt(1),Address:certificateOsakaImplementation,Nonce:0});if e!=nil{t.Fatal(e)}
			if a,e:=auth.Authority();e!=nil || a!=authority {t.Fatalf("valid authorization identity: %s %v",a,e)}
			m.SetCodeAuthorizations=[]types.SetCodeAuthorization{auth}
			accounts=append(accounts,certificateOsakaAccount{address:authority,balance:1})
		}
		c,base:=certificateOsakaSetup(t,accounts...)
		high,e:=gasFixtureExecute(c,base,m,common.HexToHash("0x1755"),m.GasLimit);if e!=nil || high.Status!=1{t.Fatalf("ordinary unsupported input did not succeed: %+v %v",high,e)}
		stock,sd,se,st:=certificateOsakaEstimate(c,base,m,nil)
		var report CertificateReport
		got,gd,ge,gt:=certificateOsakaEstimate(c,base,m,&report)
		if report.Certified || report.ReasonCounts[kind]!=1 || report.Status!="unsupported" || se!=nil || ge!=nil || got!=stock || !bytes.Equal(gd,sd) || !reflect.DeepEqual(st,gt) {t.Fatalf("stock fallback differs: stock=%d %v %+v certificate=%d %v %+v report=%+v",stock,se,st,got,ge,gt,report)}
		row,e:=gasFixtureExecute(c,base,m,common.HexToHash("0x1755"),got);if e!=nil || !gasFixtureEqual(high,row){t.Fatalf("fallback full effects differ: %+v %+v %v",high,row,e)}
		if kind=="authorization_list" {
			_,s,e:=certificateOsakaApply(c,base,m,got)
			if e!=nil || s.GetNonce(authority)!=1 || !bytes.Equal(s.GetCode(authority),types.AddressToDelegation(certificateOsakaImplementation)) {t.Fatal("valid authorization application was not exercised")}
		}
	})}
}
