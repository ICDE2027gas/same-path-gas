// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: LGPL-3.0-or-later

package gasestimator

// This test-only port runs the Ganache v7.9.2 policy on the same Geth EVM
// as the historical controls, with certificate checking disabled.
// Policy source: 547c900a50d19b094ef636a9aeccf4f7f2356430, gas-estimator.ts.
import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"
)

type GanachePolicyTrial struct {
	Index int `json:"index"`
	Phase string `json:"phase"`
	Gas string `json:"gas"`
	Success bool `json:"success"`
	BlockLimitRejected bool `json:"block_limit_rejected"`
	Error string `json:"error"`
	ExecutionError string `json:"execution_error"`
	UsedGas uint64 `json:"used_gas"`
	RawRefund uint64 `json:"raw_refund"`
	TraceEvents uint64 `json:"trace_events"`
}
type GanachePolicyReport struct {
	Complete bool `json:"complete"`
	Trials []GanachePolicyTrial `json:"trials"`
	TraceEvents uint64 `json:"trace_events"`
	SystemEvents uint64 `json:"system_events"`
	CallStackCopies uint64 `json:"call_stack_copies"`
	DroppedAttempts uint64 `json:"dropped_attempts"`
	ReducerRan bool `json:"reducer_ran"`
	RawRefund uint64 `json:"raw_refund"`
	ExtraGas string `json:"extra_gas"`
	SignedReducerExtra string `json:"signed_reducer_extra"`
	StartingGas string `json:"starting_gas"`
	NormalizationError string `json:"normalization_error"`
}

type ganacheOpcode struct { name string; fee int64; peek int; dynamic bool }
var ganacheIstanbulOps = func() [256]ganacheOpcode {
	var out [256]ganacheOpcode
	for i := range out { out[i] = ganacheOpcode{name:"INVALID"} }
	set := func(op byte,name string,fee int64,peek int,dynamic bool) { out[op]=ganacheOpcode{name,fee,peek,dynamic} }
	for _, x := range []struct{ op byte; name string; fee int64 }{
		{0x00,"STOP",0},{0x01,"ADD",3},{0x02,"MUL",5},{0x03,"SUB",3},{0x04,"DIV",5},{0x05,"SDIV",5},
		{0x06,"MOD",5},{0x07,"SMOD",5},{0x08,"ADDMOD",8},{0x09,"MULMOD",8},{0x0b,"SIGNEXTEND",5},
		{0x10,"LT",3},{0x11,"GT",3},{0x12,"SLT",3},{0x13,"SGT",3},{0x14,"EQ",3},{0x15,"ISZERO",3},
		{0x16,"AND",3},{0x17,"OR",3},{0x18,"XOR",3},{0x19,"NOT",3},{0x1a,"BYTE",3},{0x1b,"SHL",3},{0x1c,"SHR",3},{0x1d,"SAR",3},
		{0x30,"ADDRESS",2},{0x32,"ORIGIN",2},{0x33,"CALLER",2},{0x34,"CALLVALUE",2},{0x35,"CALLDATALOAD",3},{0x36,"CALLDATASIZE",2},
		{0x38,"CODESIZE",2},{0x3a,"GASPRICE",2},{0x3d,"RETURNDATASIZE",2},{0x40,"BLOCKHASH",20},{0x41,"COINBASE",2},
		{0x42,"TIMESTAMP",2},{0x43,"NUMBER",2},{0x44,"DIFFICULTY",2},{0x45,"GASLIMIT",2},{0x46,"CHAINID",2},{0x47,"SELFBALANCE",5},
		{0x50,"POP",2},{0x56,"JUMP",8},{0x57,"JUMPI",10},{0x58,"PC",2},{0x59,"MSIZE",2},{0x5a,"GAS",2},{0x5b,"JUMPDEST",1},
	} { set(x.op,x.name,x.fee,0,false) }
	for i:=0x60;i<=0x7f;i++ { set(byte(i),fmt.Sprintf("PUSH%d",i-0x5f),3,0,false) }
	for i:=0x80;i<=0x8f;i++ { set(byte(i),fmt.Sprintf("DUP%d",i-0x7f),3,0,false) }
	for i:=0x90;i<=0x9f;i++ { set(byte(i),fmt.Sprintf("SWAP%d",i-0x8f),3,0,false) }
	for _,x:=range []struct{op byte; name string; fee int64; peek int}{
		{0x0a,"EXP",10,2},{0x20,"SHA3",30,2},{0x31,"BALANCE",700,0},{0x37,"CALLDATACOPY",3,3},
		{0x39,"CODECOPY",3,3},{0x3b,"EXTCODESIZE",700,0},{0x3c,"EXTCODECOPY",700,4},{0x3e,"RETURNDATACOPY",3,3},
		{0x3f,"EXTCODEHASH",700,0},{0x51,"MLOAD",3,1},{0x52,"MSTORE",3,1},{0x53,"MSTORE8",3,1},{0x54,"SLOAD",800,1},
		{0x55,"SSTORE",0,2},{0xf0,"CREATE",32000,3},{0xf1,"CALL",700,7},{0xf2,"CALLCODE",700,7},
		{0xf3,"RETURN",0,2},{0xf4,"DELEGATECALL",700,6},{0xf5,"CREATE2",32000,4},{0xfa,"STATICCALL",700,6},
		{0xfd,"REVERT",0,2},{0xff,"SELFDESTRUCT",5000,1},
	} { set(x.op,x.name,x.fee,x.peek,true) }
	for i:=0xa0;i<=0xa4;i++ { set(byte(i),fmt.Sprintf("LOG%d",i-0xa0),375,2,true) }
	return out
}()

func ganacheCall(name string) bool { return name=="CALL"||name=="CALLCODE"||name=="DELEGATECALL"||name=="STATICCALL" }
func ganacheCreate(name string) bool { return name=="CREATE"||name=="CREATE2" }
func ganacheTerm(name string) bool { return name=="STOP"||name=="RETURN"||name=="REVERT"||name=="INVALID"||name=="SELFDESTRUCT" }
func ganacheBig(v uint64) *big.Int { return new(big.Int).SetUint64(v) }
func ganacheAbs(v *big.Int) *big.Int { return new(big.Int).Abs(v) }
// The first magnitude conversion occurs BEFORE UsedGas/refund addition.
func ganacheSourceCandidate(used,refund uint64,signedExtra *big.Int)*big.Int {
	out:=new(big.Int).Add(ganacheBig(used),ganacheAbs(signedExtra))
	return out.Add(out,ganacheBig(refund))
}

// BN 4.12.0 muln = clone().imuln(Number(64/63)) uses binary64 arithmetic
// and JavaScript bitwise operations on each limb, including their rounding.
func ganacheMuln(v *big.Int) *big.Int {
	const mask uint32 = 0x3ffffff
	n := ganacheAbs(v)
	words := make([]uint32,0,(n.BitLen()+25)/26+1)
	for n.Sign()!=0 { words=append(words,uint32(new(big.Int).And(n,big.NewInt(int64(mask))).Uint64())); n.Rsh(n,26) }
	if len(words)==0 { words=append(words,0) }
	var carry int32
	factor := float64(64)/float64(63)
	for i,word:=range words {
		w := float64(int32(word))*factor
		lo := (int32(w)&int32(mask))+(carry&int32(mask))
		carry >>= 26
		carry += int32(w/float64(0x4000000))
		carry += int32(uint32(lo)>>26)
		words[i] = uint32(lo)&mask
	}
	if carry!=0 { words=append(words,uint32(carry)) }
	out:=new(big.Int)
	for i:=len(words)-1;i>=0;i-- { out.Lsh(out,26); out.Add(out,new(big.Int).SetUint64(uint64(words[i]))) }
	if v.Sign()<0 { out.Neg(out) }
	return out
}

// This read-only projection checks source pre-hook conditions before host
// gas and refund updates. Host dynamic gas functions remain outside the projection.
func ganacheEventAllowed(op byte,gas uint64,scope tracing.OpContext,rdata []byte,readOnly bool,s *state.StateDB) bool {
	meta:=ganacheIstanbulOps[op]
	if !meta.dynamic { return true }
	if readOnly && (op==0x55||(op>=0xa0&&op<=0xa4)||op==0xf0||op==0xf5||op==0xff) { return false }
	stack:=scope.StackData()
	if len(stack)<meta.peek { return false }
	word:=func(i int)*big.Int { return stack[len(stack)-1-i].ToBig() }
	if op==0x3e { return new(big.Int).Add(word(1),word(2)).Cmp(ganacheBig(uint64(len(rdata))))<=0 }
	if op==0x55 {
		key:=common.Hash(stack[len(stack)-1].Bytes32())
		value:=common.Hash(stack[len(stack)-2].Bytes32())
		current,original:=s.GetStateAndCommittedState(scope.Address(),key)
		if gas<=2300 { return false }
		if current==value||original==current { return true }
		// Source subtracts this refund before any later reset refund addition.
		return !(original!=(common.Hash{})&&current==(common.Hash{})&&s.GetRefund()<15000)
	}
	if op!=0xf1&&op!=0xf2&&op!=0xf4 { return true }
	if op==0xf1&&readOnly&&word(2).Sign()!=0 { return false }
	// Source pre-hook memory arithmetic uses BigInt for ranges extending beyond
	// uint64. Both ranges share the increasing logical memory word count.
	words:=ganacheBig(uint64(len(scope.MemoryData())/32))
	cost:=big.NewInt(meta.fee)
	memory:=func(offset,length *big.Int) {
		if length.Sign()==0 { return }
		end:=new(big.Int).Add(offset,length)
		end.Add(end,big.NewInt(31));end.Quo(end,big.NewInt(32))
		if end.Cmp(words)<=0 { return }
		memCost:=func(w *big.Int)*big.Int {
			square:=new(big.Int).Mul(w,w);square.Quo(square,big.NewInt(512))
			return square.Add(square,new(big.Int).Mul(w,big.NewInt(3)))
		}
		cost.Add(cost,new(big.Int).Sub(memCost(end),memCost(words)));words.Set(end)
	}
	if op==0xf4 { memory(word(2),word(3));memory(word(4),word(5)) } else {
		memory(word(3),word(4));memory(word(5),word(6))
		value:=word(2)
		if value.Sign()!=0 { cost.Add(cost,big.NewInt(9000)) }
		if op==0xf1 {
			address:=common.Address(stack[len(stack)-2].Bytes20())
			empty:=s.Empty(address)
			if empty&&value.Sign()!=0 { cost.Add(cost,big.NewInt(25000)) }
		}
	}
	available:=new(big.Int).Sub(ganacheBig(gas),cost)
	allowed:=new(big.Int).Sub(available,new(big.Int).Quo(new(big.Int).Set(available),big.NewInt(64)))
	limit:=word(0);if limit.Cmp(allowed)>0 { limit.Set(allowed) }
	if limit.Cmp(available)>0 { return false }
	return op!=0xf1||cost.Cmp(ganacheBig(gas))<=0
}

type ganacheOp struct { gas uint64; depth int; name string; fee int64; stack []uint256.Int }
type ganacheTrace struct {
	ops []ganacheOp
	sys []int
	precompile map[int]bool
	pending bool
	pendingDepth int
	copies,dropped uint64
	diagnostics bool
	err error
}
func (g *ganacheTrace) collect(pc uint64,op byte,gas uint64,scope tracing.OpContext,rdata []byte,depth int,readOnly bool,s *state.StateDB) {
	if g.err!=nil { return }
	if pc>=uint64(len(scope.ContractCode())) { if g.diagnostics {g.dropped++};return }
	include:=ganacheEventAllowed(op,gas,scope,rdata,readOnly,s)
	if err:=s.Error();err!=nil { g.err=err;return }
	if !include { if g.diagnostics {g.dropped++};return }
	meta:=ganacheIstanbulOps[op]
	row:=ganacheOp{gas:gas,depth:depth-1,name:meta.name,fee:meta.fee}
	if g.pending {
		if row.depth==g.pendingDepth { g.precompile[len(g.ops)-1]=true }
		g.pending=false
	}
	if ganacheCall(row.name) {
		row.stack=append([]uint256.Int(nil),scope.StackData()...);if g.diagnostics {g.copies++}
		g.pending=true;g.pendingDepth=row.depth;g.sys=append(g.sys,len(g.ops))
	} else if ganacheCreate(row.name)||ganacheTerm(row.name) { g.sys=append(g.sys,len(g.ops)) }
	g.ops=append(g.ops,row)
}
func (g *ganacheTrace) done() bool { return len(g.ops)==0||len(g.sys)<2||!ganacheTerm(g.ops[len(g.ops)-1].name) }

type ganacheContext struct {
	g *ganacheTrace
	start,stop,index int
	base,composite bool
	op,next ganacheOp
	cost,sixty,callingFee,intermediate *big.Int
}
func (g *ganacheTrace) at(i int)(ganacheOp,error) { if i<0||i>=len(g.ops) {return ganacheOp{},fmt.Errorf("Ganache source op index %d outside %d",i,len(g.ops))};return g.ops[i],nil }
func (g *ganacheTrace) context(index int,fee *big.Int)(*ganacheContext,error) {
	op,err:=g.at(index);if err!=nil{return nil,err};next,err:=g.at(index+1);if err!=nil{return nil,err}
	return &ganacheContext{g:g,start:index,index:index,base:index==0,op:op,next:next,cost:new(big.Int),sixty:new(big.Int),callingFee:new(big.Int).Set(fee),intermediate:new(big.Int).Sub(ganacheBig(op.gas),ganacheBig(next.gas))},nil
}
func (c *ganacheContext) addGas(v *big.Int) {
	if c.sixty.Sign()>0 { if v.Cmp(c.sixty)>=0 {c.sixty=new(big.Int)} else {c.sixty.Sub(c.sixty,v)} }
	c.cost.Add(c.cost,v)
}
func (c *ganacheContext) setStart(i int) { c.start=i;c.composite=true }
func (c *ganacheContext) gas(i int)(*big.Int,error) { row,err:=c.g.at(i);if err!=nil{return nil,err};return ganacheBig(row.gas),nil }
func (c *ganacheContext) addRange(fee *big.Int) error {
	i:=c.start;if !c.base&&!c.composite {i++}
	a,err:=c.gas(i);if err!=nil{return err};b,err:=c.gas(c.stop);if err!=nil{return err}
	c.addGas(new(big.Int).Add(new(big.Int).Sub(a,b),fee));return nil
}
func (c *ganacheContext) finalizeRange() error {
	start,err:=c.gas(c.start);if err!=nil{return err};stop,err:=c.gas(c.stop);if err!=nil{return err}
	var value *big.Int
	if c.composite&&c.stop!=len(c.g.ops)-1 {
		before,err:=c.gas(c.stop-1);if err!=nil{return err}
		c.addGas(new(big.Int).Sub(start,before))
		value=new(big.Int).Add(new(big.Int).Sub(before,stop),c.intermediate)
	} else { value=new(big.Int).Sub(start,stop) }
	c.addGas(value.Sub(value,c.callingFee))
	if (c.op.name=="CALL"||c.op.name=="CALLCODE")&&len(c.op.stack)>=3&&!c.op.stack[len(c.op.stack)-3].IsZero() {
		c.cost.Add(c.cost,c.sixty)
		before,err:=c.gas(c.stop-1);if err!=nil{return err}
		inner:=new(big.Int).Sub(ganacheBig(c.next.gas),before)
		if inner.Cmp(big.NewInt(2300))>0 { c.sixty=new(big.Int).Quo(new(big.Int).Set(c.cost),big.NewInt(63)) } else { c.sixty=new(big.Int).Sub(big.NewInt(2300),inner) }
	} else if c.stop!=len(c.g.ops)-1 { c.cost.Add(c.cost,c.sixty);c.sixty=new(big.Int).Quo(new(big.Int).Set(c.cost),big.NewInt(63)) }
	return nil
}
func (g *ganacheTrace) total()(*big.Int,error) {
	context,err:=g.context(0,new(big.Int));if err!=nil{return nil,err}
	stack:=make([]*ganacheContext,0)
	for _,currentIndex:=range g.sys {
		current,err:=g.at(currentIndex);if err!=nil{return nil,err}
		if ganacheCall(current.name)||ganacheCreate(current.name) {
			if g.precompile[currentIndex] {
				context.stop=currentIndex+1;if err=context.addRange(new(big.Int));err!=nil{return nil,err}
				context.setStart(currentIndex+1);context.sixty.Add(context.sixty,big.NewInt(2300))
			} else {
				context.stop=currentIndex;fee:=big.NewInt(current.fee)
				if err=context.addRange(fee);err!=nil{return nil,err};stack=append(stack,context)
				context,err=g.context(currentIndex,fee);if err!=nil{return nil,err}
			}
		} else if ganacheTerm(current.name) {
			context.stop=currentIndex;if currentIndex+1<len(g.ops) {context.stop++}
			if err=context.finalizeRange();err!=nil{return nil,err}
			if len(stack)==0 {break}
			parent:=stack[len(stack)-1];stack=stack[:len(stack)-1]
			parent.addGas(context.cost);parent.sixty.Add(parent.sixty,context.sixty)
			context=parent;context.setStart(currentIndex+1)
		} else {return nil,errors.New("Ganache source INVALID OPCODE")}
	}
	return new(big.Int).Add(context.cost,context.sixty),nil
}

// Search follows the source candidate/expand/bisect/MAX order, including
// repeated verification at MAX. Context cancellation returns an incomplete run
// through an error, preserving the source's uncapped retry behavior.
func ganacheSearch(ctx context.Context,starting,max *big.Int,enough func(*big.Int,string)(bool,error))(*big.Int,error) {
	lo,hi:=new(big.Int).Set(starting),new(big.Int).Set(starting)
	ok,err:=enough(hi,"candidate");if err!=nil{return nil,err}
	if !ok {
		for { if err=ctx.Err();err!=nil{return nil,err};hi=ganacheMuln(hi);ok,err=enough(hi,"expand");if err!=nil{return nil,err};if ok{break} }
		for new(big.Int).Add(lo,big.NewInt(1)).Cmp(hi)<0 {
			mid:=new(big.Int).Quo(new(big.Int).Add(lo,hi),big.NewInt(2))
			ok,err=enough(mid,"bisect");if err!=nil{return nil,err};if ok {hi=mid} else {lo=mid}
		}
		if hi.Cmp(max)>=0 {ok,err=enough(hi,"max_verify");if err!=nil{return nil,err};if !ok{return nil,errors.New("gas required exceeds allowance or always failing transaction")}}
	}
	return ganacheAbs(hi),nil
}

func ganachePolicyEstimate(ctx context.Context,call *core.Message,opts *Options,gasCap uint64)(uint64,[]byte,error,GanachePolicyReport) {
	return ganachePolicyEstimateDiagnostics(ctx,call,opts,gasCap,true)
}
// diagnostics=false suppresses report materialization while retaining event
// normalization, allOps/sysOps/stack copies, reduction, and execution trials.
func ganachePolicyEstimateDiagnostics(ctx context.Context,call *core.Message,opts *Options,gasCap uint64,diagnostics bool)(uint64,[]byte,error,GanachePolicyReport) {
	report:=GanachePolicyReport{}
	if diagnostics {report.Trials=make([]GanachePolicyTrial,0);report.ExtraGas="0";report.SignedReducerExtra="0"}
	fail:=func(err error)(uint64,[]byte,error,GanachePolicyReport){if diagnostics {report.NormalizationError=err.Error()};return 0,nil,err,report}
	if opts==nil||opts.Header==nil||opts.Config==nil||opts.State==nil||call==nil {return fail(errors.New("Ganache control requires complete input context"))}
	if gasCap!=0&&gasCap!=opts.Header.GasLimit {return fail(errors.New("Ganache source has no additional gasCap policy"))}
	if opts.Certificate!=nil {return fail(errors.New("Ganache control cannot enable certificate"))}
	bc:=core.NewEVMBlockContext(opts.Header,opts.Chain,nil)
	if fork,reason:=certificateFork(opts.Config.Rules(bc.BlockNumber,bc.Random!=nil,bc.Time));fork!="istanbul"||reason!="" {return fail(errors.New("Ganache event projection requires exact Istanbul rules"))}
	trace:=&ganacheTrace{precompile:make(map[int]bool),diagnostics:diagnostics}
	var highRawRefund uint64
	runTrial:=func(limit *big.Int,phase string,collect bool)(*core.ExecutionResult,error) {
		if err:=ctx.Err();err!=nil {return nil,err}
		// Fresh copy and VM are paid even for the source runTx block-limit guard.
		s:=opts.State.Copy();m:=*call
		block:=core.NewEVMBlockContext(opts.Header,opts.Chain,nil)
		if opts.BlobBaseFee!=nil {block.BlobBaseFee=new(big.Int).Set(opts.BlobBaseFee)}
		if m.GasPrice.Sign()==0 {block.BaseFee=new(big.Int)}
		if m.BlobGasFeeCap!=nil&&m.BlobGasFeeCap.BitLen()==0 {block.BlobBaseFee=new(big.Int)}
		config:=vm.Config{NoBaseFee:true}
		if collect {config.Tracer=&tracing.Hooks{OnOpcodeAttempt:func(pc uint64,op byte,gas uint64,scope tracing.OpContext,data []byte,depth int,readOnly bool){trace.collect(pc,op,gas,scope,data,depth,readOnly,s)}}}
		evm:=vm.NewEVM(block,s,opts.Config,config);defer evm.Release()
		var trial *GanachePolicyTrial
		if diagnostics {trial=&GanachePolicyTrial{Index:len(report.Trials),Phase:phase,Gas:ganacheAbs(limit).String()}}
		finish:=func(result *core.ExecutionResult,err error)(*core.ExecutionResult,error){
			// The raw refund required by the algorithm is independent of report.
			if collect {highRawRefund=s.GetRefund()}
			if diagnostics {
				if err!=nil {trial.Error=err.Error()};if result!=nil {trial.UsedGas=result.UsedGas;trial.ExecutionError=gasGanacheError(result.Err);trial.Success=err==nil&&!result.Failed()}
				trial.RawRefund=s.GetRefund();if collect {trial.TraceEvents=uint64(len(trace.ops))};report.Trials=append(report.Trials,*trial)
			}
			return result,err
		}
		magnitude:=ganacheAbs(limit)
		if magnitude.Cmp(ganacheBig(opts.Header.GasLimit))>0 {if diagnostics {trial.BlockLimitRejected=true};return finish(nil,errors.New("tx has a higher gas limit than the block"))}
		m.GasLimit=magnitude.Uint64()
		childCtx,cancel:=context.WithCancel(ctx)
		stopped:=make(chan struct{})
		go func(){defer close(stopped);<-childCtx.Done();evm.Cancel()}()
		defer func(){cancel();<-stopped}()
		result,err:=core.ApplyMessage(evm,&m,nil)
		if backendErr:=s.Error();backendErr!=nil {err=backendErr}
		return finish(result,err)
	}
	high,err:=runTrial(ganacheBig(call.GasLimit),"high_trace",true)
	if diagnostics {report.TraceEvents=uint64(len(trace.ops));report.SystemEvents=uint64(len(trace.sys));report.CallStackCopies=trace.copies;report.DroppedAttempts=trace.dropped}
	if ctx.Err()!=nil {return fail(ctx.Err())}
	if trace.err!=nil {return fail(trace.err)}
	if err!=nil {if diagnostics {report.Complete=true};return 0,nil,err,report}
	if high==nil {return fail(errors.New("missing actual high execution result"))}
	if high.Failed() {if diagnostics {report.Complete=true};return 0,high.ReturnData,high.Err,report}
	if diagnostics {report.RawRefund=highRawRefund}
	signedExtra:=new(big.Int)
	if !trace.done() {
		if diagnostics {report.ReducerRan=true}
		total,err:=trace.total();if err!=nil{return fail(err)}
		actual:=new(big.Int).Sub(ganacheBig(trace.ops[0].gas),ganacheBig(trace.ops[len(trace.ops)-1].gas))
		signedExtra.Sub(total,actual);if diagnostics {report.SignedReducerExtra=signedExtra.String()}
	}
	if diagnostics {report.ExtraGas=ganacheAbs(signedExtra).String()}
	starting:=ganacheSourceCandidate(high.UsedGas,highRawRefund,signedExtra);if diagnostics {report.StartingGas=starting.String()}
	result,err:=ganacheSearch(ctx,starting,ganacheBig(opts.Header.GasLimit),func(gas *big.Int,phase string)(bool,error){
		r,runErr:=runTrial(gas,phase,false)
		if ctx.Err()!=nil {return false,ctx.Err()}
		return runErr==nil&&r!=nil&&!r.Failed(),nil
	})
	if ctx.Err()!=nil {return fail(ctx.Err())}
	if diagnostics {report.Complete=true}
	if err!=nil {return 0,nil,err,report}
	if !result.IsUint64() {return fail(errors.New("successful source gas does not fit the host transaction field"))}
	return result.Uint64(),nil,nil,report
}
func gasGanacheError(err error) string {if err==nil{return ""};return err.Error()}

// Unit checks exercise source-derived arithmetic and search functions in isolation.
func TestGanacheControlBNAndSearch(t *testing.T) {
	for _,x:=range []struct{in,want int64}{{0,0},{1,1},{63,64},{126,128},{1<<26,1<<26},{(1<<26)+63,(1<<26)+64},{-63,-64}} {
		input:=big.NewInt(x.in);got:=ganacheMuln(input)
		if got.Cmp(big.NewInt(x.want))!=0||input.Int64()!=x.in {t.Fatalf("locked BN word golden %d: got %s want %d; input=%s",x.in,got,x.want,input)}
	}
	// TS converts the signed excess through unsigned BN bytes before adding
	// UsedGas and the raw refund: 10000 + abs(-7) + 5 = 10012.
	if got:=ganacheSourceCandidate(10000,5,big.NewInt(-7));got.Cmp(big.NewInt(10012))!=0 {t.Fatalf("source excess serialization: got %s",got)}
	var calls []string
	got,err:=ganacheSearch(context.Background(),big.NewInt(63),big.NewInt(64),func(n *big.Int,p string)(bool,error){calls=append(calls,p+":"+n.String());return n.Cmp(big.NewInt(64))>=0,nil})
	if err!=nil||got.Int64()!=64||fmt.Sprint(calls)!="[candidate:63 expand:64 max_verify:64]" {t.Fatalf("literal MAX repeat: %v %v %v",got,err,calls)}
	calls=nil
	got,err=ganacheSearch(context.Background(),big.NewInt(126),big.NewInt(1000),func(n *big.Int,p string)(bool,error){calls=append(calls,p+":"+n.String());return n.Cmp(big.NewInt(128))>=0,nil})
	if err!=nil||got.Int64()!=128||fmt.Sprint(calls)!="[candidate:126 expand:128 bisect:127]" {t.Fatalf("literal bisection: %v %v %v",got,err,calls)}
}

func TestGanacheControlSourceOpaqueReducer(t *testing.T) {
	// The source classifies an empty-code zero-value CALL as opaque and adds
	// 2300. The hand calculation gives cost = 703 and sixty = 2297, totaling
	// 3000. The observed entry-gas difference is 703.
	g:=&ganacheTrace{ops:[]ganacheOp{{gas:1000,name:"CALL",fee:700,stack:make([]uint256.Int,8)},{gas:300,name:"ADD",fee:3},{gas:297,name:"STOP"}},sys:[]int{0,2},precompile:map[int]bool{0:true}}
	if g.done(){t.Fatal("source word unexpectedly bypasses reducer")}
	total,err:=g.total();if err!=nil||total.Cmp(big.NewInt(3000))!=0 {t.Fatalf("source opaque/stipend golden got %v %v",total,err)}
	// Natural code exhaustion bypasses the source reducer.
	g.ops=g.ops[:2];g.sys=[]int{0};if !g.done(){t.Fatal("natural EOF must bypass source reducer")}
}

type ganachePureScope struct { stack []uint256.Int; code []byte }
func (s ganachePureScope) MemoryData()[]byte{return nil}
func (s ganachePureScope) StackData()[]uint256.Int{return s.stack}
func (s ganachePureScope) Caller()common.Address{return common.Address{}}
func (s ganachePureScope) Address()common.Address{return common.Address{}}
func (s ganachePureScope) CallValue()*uint256.Int{return new(uint256.Int)}
func (s ganachePureScope) CallInput()[]byte{return nil}
func (s ganachePureScope) ContractCode()[]byte{return s.code}
func TestGanacheControlSourcePreHookBoundaries(t *testing.T) {
	// These unit cases check the source pre-hook producer's event conditions.
	if !ganacheEventAllowed(0x52,0,ganachePureScope{stack:make([]uint256.Int,1)},nil,false,nil){t.Fatal("MSTORE source peeks only offset before the step")}
	if !ganacheEventAllowed(0xa4,0,ganachePureScope{stack:make([]uint256.Int,2)},nil,false,nil){t.Fatal("LOG4 source pre-hook does not peek topics")}
	if ganacheEventAllowed(0xa4,1000,ganachePureScope{stack:make([]uint256.Int,6)},nil,true,nil){t.Fatal("static LOG rejects before source step")}
	if !ganacheEventAllowed(0x31,0,ganachePureScope{},nil,false,nil){t.Fatal("Istanbul BALANCE has no pre-hook stack peek")}
	stack:=make([]uint256.Int,3);stack[1].SetUint64(5)
	if ganacheEventAllowed(0x3e,1000,ganachePureScope{stack:stack},make([]byte,4),false,nil){t.Fatal("return offset past buffer rejects even when length is zero")}
	if !ganacheEventAllowed(0x5f,0,ganachePureScope{},nil,false,nil)||ganacheIstanbulOps[0x5f].name!="INVALID" {t.Fatal("inactive PUSH0 is source INVALID, not host pretty name")}
}
