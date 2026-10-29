// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: LGPL-3.0-or-later
package gasestimator

import (
 "bytes"
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "math/big"
 "os"
 "path/filepath"
 "reflect"
 "runtime"
 "strings"
 "syscall"
 "testing"
 "time"

 "github.com/ethereum/go-ethereum/accounts/abi"
 "github.com/ethereum/go-ethereum/common"
 "github.com/ethereum/go-ethereum/core"
 "github.com/ethereum/go-ethereum/core/state"
 "github.com/ethereum/go-ethereum/core/tracing"
 "github.com/ethereum/go-ethereum/core/vm"
 "github.com/ethereum/go-ethereum/crypto"
 "github.com/holiman/uint256"
)

const benignHigh uint64 = 8000000
const benignCompilerSHA = "52ad76de0634e875ac3d50e09a0057532ff53c6d851ef61204af1b0d620797b6"
type benignSpec struct { Path string `json:"path"`; Bytes int `json:"bytes"`; SHA256 string `json:"sha256"` }
type benignContract struct { ABI json.RawMessage `json:"abi"`; Bytecode string `json:"bytecode"`; Runtime string `json:"runtime"`; Metadata string `json:"metadata"` }
type benignArtifacts struct {
 Schema string `json:"schema"`; Complete bool `json:"complete"`
 Compiler benignSpec `json:"compiler"`; Node benignSpec `json:"node"`; Sources map[string]benignSpec `json:"sources"`
 Contracts map[string]benignContract `json:"contracts"`; ERC20 benignContract `json:"erc20"`
}
type benignInstance struct { ID string `json:"id"`; N int `json:"n"`; D int `json:"d"`; Kind int `json:"kind"` }
type benignMixture struct { Q string `json:"q"`; Indices []int `json:"indices"` }
type benignSchedule struct { Schema string `json:"schema"`; Instances []benignInstance `json:"instances"`; Mixtures []benignMixture `json:"mixtures"` }
type benignManifest struct {
 Schema string `json:"schema"`; Released bool `json:"released"`; Phase string `json:"phase"`; Method string `json:"method"`
 Artifacts benignSpec `json:"artifacts"`; Schedule benignSpec `json:"schedule"`; Qualification *benignSpec `json:"qualification"`; Output string `json:"output_directory"`
}
type benignFixture struct {
 chain *gasFixtureChain; base *state.StateDB; root common.Hash
 token, batch, owner common.Address; targets map[int]common.Address
 tokenABI, batchABI abi.ABI; nonce uint64; deadline *big.Int; v uint8; r,s [32]byte
 setup []map[string]interface{}
}
type benignObservation struct {
 Gas uint64 `json:"gas"`; Revert []byte `json:"revert"`; Error string `json:"error"`; Report *CertificateReport `json:"report"`; Trials []TrialReport `json:"trials,omitempty"`
}
type benignRow struct {
 Instance benignInstance `json:"instance"`; Target common.Address `json:"target"`; Data string `json:"data"`; Nonce uint64 `json:"nonce"`
 Outputs map[string]benignObservation `json:"outputs"`; High *gasFixtureReplay `json:"high"`; Replays map[string]*gasFixtureReplay `json:"replays"`
 Diagnostic map[string]interface{} `json:"diagnostic"`; Complete bool `json:"complete"`; Errors []string `json:"errors"`
}
type benignQualification struct {
 Schema string `json:"schema"`; Complete bool `json:"complete"`; Artifacts benignSpec `json:"artifacts"`; Schedule benignSpec `json:"schedule"`
 Prestate common.Hash `json:"prestate_root"`; Nonce uint64 `json:"sender_nonce"`; Rows []benignRow `json:"rows"`; Setup []map[string]interface{} `json:"setup"`; Errors []string `json:"errors"`
}
func benignPath(path string) (string,error) {
 clean:=filepath.Clean(path)
 if !filepath.IsAbs(clean) {return "",fmt.Errorf("absolute path required")}
 actual,e:=filepath.EvalSymlinks(clean);if e!=nil || actual!=clean{return "",fmt.Errorf("canonical existing path required: %s",path)};return clean,nil
}
func benignRead(spec benignSpec) ([]byte,error) {
 path,e:=benignPath(spec.Path);if e!=nil{return nil,e};info,e:=os.Stat(path);if e!=nil{return nil,e}
 if !info.Mode().IsRegular() || info.Size()>128<<20{return nil,fmt.Errorf("bounded regular input required")}
 raw,e:=os.ReadFile(path);if e!=nil{return nil,e};sum:=sha256.Sum256(raw)
 if len(raw)!=spec.Bytes || hex.EncodeToString(sum[:])!=spec.SHA256{return nil,fmt.Errorf("input identity differs: %s",path)};return raw,nil
}
func benignSave(path string,value interface{}) (benignSpec,error) {
 raw,e:=json.MarshalIndent(value,"","  ");if e!=nil{return benignSpec{},e};raw=append(raw,'\n')
 f,e:=os.OpenFile(path,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600);if e!=nil{return benignSpec{},e}
 _,e=f.Write(raw);ce:=f.Close();if e==nil{e=ce};sum:=sha256.Sum256(raw);return benignSpec{path,len(raw),hex.EncodeToString(sum[:])},e
}
func benignDecode(s string) ([]byte,error) {return hex.DecodeString(strings.TrimPrefix(s,"0x"))}
func benignABI(raw json.RawMessage) (abi.ABI,error) {return abi.JSON(bytes.NewReader(raw))}
func benignCommit(c *gasFixtureChain,s *state.StateDB) (*state.StateDB,common.Hash,error) {
 bc:=core.NewEVMBlockContext(c.current,c,nil);rules:=c.config.Rules(bc.BlockNumber,bc.Random!=nil,bc.Time)
 root,e:=s.Commit(rules,0);if e!=nil{return nil,root,e};if e=s.Database().TrieDB().Commit(root,false);e!=nil{return nil,root,e}
 next,e:=state.New(root,s.Database());return next,root,e
}
func benignMessage(base *state.StateDB,to *common.Address,data []byte) *core.Message {
 return &core.Message{From:certificateOsakaSender,To:to,Nonce:base.GetNonce(certificateOsakaSender),GasLimit:benignHigh,GasPrice:uint256.NewInt(3),GasFeeCap:uint256.NewInt(3),GasTipCap:uint256.NewInt(2),Value:new(uint256.Int),Data:data}
}
func benignApply(c *gasFixtureChain,base *state.StateDB,m *core.Message,limit uint64) (*core.ExecutionResult,*state.StateDB,error) {
 s:=base.Copy();copy:=*m;copy.GasLimit=limit;s.SetTxContext(common.HexToHash("0x1832"),0,0)
 evm:=vm.NewEVM(core.NewEVMBlockContext(c.current,c,nil),s,c.config,vm.Config{NoBaseFee:true});defer evm.Release()
 result,e:=core.ApplyMessage(evm,&copy,nil);if e==nil{e=s.Error()};return result,s,e
}
func benignSetupCall(f *benignFixture,to *common.Address,data []byte,label string) (common.Address,error) {
 m:=benignMessage(f.base,to,data);created:=common.Address{};if to==nil{created=crypto.CreateAddress(m.From,m.Nonce)}
 result,next,e:=benignApply(f.chain,f.base,m,benignHigh);if e!=nil{return created,e};if result.Failed(){return created,fmt.Errorf("setup %s: %v",label,result.Err)}
 f.base,f.root,e=benignCommit(f.chain,next);if e!=nil{return created,e}
 f.setup=append(f.setup,map[string]interface{}{"label":label,"nonce":m.Nonce,"created_address":created,"used_gas":result.UsedGas,"prestate_after":f.root})
 return created,nil
}
func benignView(f *benignFixture,s *state.StateDB,to common.Address,a abi.ABI,name string,args ...interface{}) ([]interface{},error) {
 data,e:=a.Pack(name,args...);if e!=nil{return nil,e};result,_,e:=benignApply(f.chain,s,benignMessage(s,&to,data),benignHigh)
 if e!=nil{return nil,e};if result.Failed(){return nil,fmt.Errorf("view %s: %v",name,result.Err)};return a.Unpack(name,result.ReturnData)
}
func benignBuild(t *testing.T,art benignArtifacts) (*benignFixture,error) {
 key,e:=crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001");if e!=nil{return nil,e}
 owner:=crypto.PubkeyToAddress(key.PublicKey)
 chain,base:=certificateOsakaSetup(t,certificateOsakaAccount{address:owner,balance:1000000000000000000})
 f:=&benignFixture{chain:chain,base:base,owner:owner,targets:make(map[int]common.Address),deadline:big.NewInt(2000000000)}
 f.tokenABI,e=benignABI(art.ERC20.ABI);if e!=nil{return nil,e};f.batchABI,e=benignABI(art.Contracts["BenignBatch"].ABI);if e!=nil{return nil,e}
 tokenCode,e:=benignDecode(art.ERC20.Bytecode);if e!=nil{return nil,e}
 supply:=new(big.Int).Mul(big.NewInt(10000),new(big.Int).Exp(big.NewInt(10),big.NewInt(18),nil))
 constructor,e:=f.tokenABI.Pack("",supply);if e!=nil{return nil,e}
 f.token,e=benignSetupCall(f,nil,append(tokenCode,constructor...),"deploy-token");if e!=nil{return nil,e}
 code,e:=benignDecode(art.Contracts["BenignBatch"].Bytecode);if e!=nil{return nil,e};constructor,e=f.batchABI.Pack("",f.token,owner);if e!=nil{return nil,e}
 f.batch,e=benignSetupCall(f,nil,append(code,constructor...),"deploy-batch");if e!=nil{return nil,e};f.targets[0]=f.batch
 forwardABI,e:=benignABI(art.Contracts["BenignForwarder"].ABI);if e!=nil{return nil,e};forwardCode,e:=benignDecode(art.Contracts["BenignForwarder"].Bytecode);if e!=nil{return nil,e}
 destination:=f.batch
 for i:=1;i<=3;i++ {constructor,e=forwardABI.Pack("",destination);if e!=nil{return nil,e};destination,e=benignSetupCall(f,nil,append(append([]byte{},forwardCode...),constructor...),fmt.Sprintf("deploy-forward-%d",i));if e!=nil{return nil,e};f.targets[i]=destination}
 transfer,e:=f.tokenABI.Pack("transfer",owner,big.NewInt(1000000));if e!=nil{return nil,e};if _,e=benignSetupCall(f,&f.token,transfer,"fund-token-owner");e!=nil{return nil,e}
 // Verify constructor output, storage layout and funding through native state and ordinary getters.
 for i:=0;i<64;i++ {if f.base.GetState(f.batch,common.BigToHash(big.NewInt(int64(2+i))))!=common.BigToHash(big.NewInt(1)){return nil,fmt.Errorf("initial ledger %d",i)}}
 for _,i:=range []int64{0,63} {v,e:=benignView(f,f.base,f.batch,f.batchABI,"entries",big.NewInt(i));if e!=nil{return nil,e};if v[0].(*big.Int).Uint64()!=1{return nil,fmt.Errorf("getter/layout differs")}}
 for address,contract:=range map[common.Address]benignContract{f.token:art.ERC20,f.batch:art.Contracts["BenignBatch"],f.targets[1]:art.Contracts["BenignForwarder"],f.targets[2]:art.Contracts["BenignForwarder"],f.targets[3]:art.Contracts["BenignForwarder"]} {
  expected,e:=benignDecode(contract.Runtime);if e!=nil{return nil,e};if !bytes.Equal(f.base.GetCode(address),expected){return nil,fmt.Errorf("deployed runtime differs: %s",address)}
 }
 nonce,e:=benignView(f,f.base,f.token,f.tokenABI,"nonces",owner);if e!=nil{return nil,e};if nonce[0].(*big.Int).Sign()!=0{return nil,fmt.Errorf("permit nonce is not zero")}
 balance,e:=benignView(f,f.base,f.token,f.tokenABI,"balanceOf",owner);if e!=nil{return nil,e};if balance[0].(*big.Int).Cmp(big.NewInt(1000000))!=0{return nil,fmt.Errorf("token owner not funded")}
 domain,e:=benignView(f,f.base,f.token,f.tokenABI,"DOMAIN_SEPARATOR");if e!=nil{return nil,e};domainWord,ok:=domain[0].([32]byte);if !ok{return nil,fmt.Errorf("domain word type")}
 word:=func(value *big.Int) []byte{return common.LeftPadBytes(value.Bytes(),32)}
 payload:=append([]byte{},crypto.Keccak256([]byte("Permit(address owner,address spender,uint256 value,uint256 nonce,uint256 deadline)"))...)
 payload=append(payload,common.LeftPadBytes(owner.Bytes(),32)...);payload=append(payload,common.LeftPadBytes(f.batch.Bytes(),32)...)
 payload=append(payload,word(big.NewInt(1))...);payload=append(payload,word(new(big.Int))...);payload=append(payload,word(f.deadline)...)
 digest:=crypto.Keccak256Hash([]byte{0x19,0x01},domainWord[:],crypto.Keccak256(payload));signature,e:=crypto.Sign(digest.Bytes(),key);if e!=nil{return nil,e}
 copy(f.r[:],signature[:32]);copy(f.s[:],signature[32:64]);f.v=signature[64]+27;f.nonce=f.base.GetNonce(certificateOsakaSender)
 if f.nonce!=6{return nil,fmt.Errorf("actual post-setup nonce %d expected six setup transactions",f.nonce)}
 return f,nil
}
func benignCall(f *benignFixture,row benignInstance) (*core.Message,error) {
 data,e:=f.batchABI.Pack("run",big.NewInt(int64(row.N)),uint8(row.Kind),f.deadline,f.v,f.r,f.s);if e!=nil{return nil,e}
 target,ok:=f.targets[row.D];if !ok{return nil,fmt.Errorf("unknown forwarding level")};m:=benignMessage(f.base,&target,data)
 if m.Nonce!=f.nonce{return nil,fmt.Errorf("post-setup nonce changed")};return m,nil
}
func benignEstimate(f *benignFixture,original *core.Message,method string,diagnostic bool) benignObservation {
 out:=benignObservation{};m:=*original
 if method!="S" {out.Report=new(CertificateReport)}
 opts:=&Options{Config:f.chain.config,Chain:f.chain,Header:f.chain.current,State:f.base.Copy(),ErrorRatio:.015,Certificate:out.Report,CertificateReplay:method=="V"}
 if diagnostic {out.Trials=make([]TrialReport,0);opts.OnTrial=func(row TrialReport){out.Trials=append(out.Trials,row)}}
 gas,revert,e:=Estimate(context.Background(),&m,opts,0);out.Gas,out.Revert=gas,revert;if e!=nil{out.Error=e.Error()}
 if m.GasLimit!=original.GasLimit{out.Error+=" message gas not restored"};return out
}
func benignChecksum(n int) common.Hash {
 var sum common.Hash
 for i:=0;i<n;i++ {raw:=append([]byte{},sum.Bytes()...);raw=append(raw,common.LeftPadBytes(big.NewInt(int64(i)).Bytes(),32)...);raw=append(raw,common.LeftPadBytes(big.NewInt(2).Bytes(),32)...);sum=crypto.Keccak256Hash(raw)}
 return sum
}
// This wrapper is used only for the one declared untimed high diagnostic.
// Production hooks, their first-reason semantics and paid execution are unchanged.
func benignDiagnostic(f *benignFixture,original *core.Message) (map[string]interface{},error) {
 s:=f.base.Copy();m:=*original;s.SetTxContext(common.HexToHash("0x1832"),0,0)
 evm:=vm.NewEVM(core.NewEVMBlockContext(f.chain.current,f.chain,nil),s,f.chain.config,vm.Config{NoBaseFee:true});defer evm.Release()
 observer:=newCertificateObserver();underlying:=observer.bind(&m,evm)
 frames:=make([]map[string]interface{},0);active:=make(map[int]map[string]interface{})
 counts:=make(map[string]uint64);prefix:=make(map[string]uint64);var total uint64;maxDepth:=-1
 var first map[string]interface{}
 capture:=func(kind string,depth int,pc *uint64,op *byte,frame map[string]interface{},execution *common.Address) {
  if first!=nil || observer.reason=="" {return}
  first=map[string]interface{}{"reason":observer.reason,"event_kind":kind,"hook_depth":depth,"pc":pc,"opcode":op,"raw_opcode_events_at_reject":total,"observed_max_call_depth_at_reject":maxDepth,"code_address":nil,"execution_address":nil}
  if frame!=nil {first["code_address"]=frame["code_address"];first["execution_address"]=frame["execution_address"]}
  if execution!=nil {first["execution_address"]=*execution}
 }
 capture("bind",-1,nil,nil,nil,nil)
 hooks:=&tracing.Hooks{}
 hooks.OnEnterV2=func(depth int,kind byte,from,to common.Address,input []byte,gas tracing.Gas,value *big.Int) {
  execution:=to
  if kind==byte(vm.DELEGATECALL)||kind==byte(vm.CALLCODE) {if parent:=active[depth-1];parent!=nil {execution=parent["execution_address"].(common.Address)} else {execution=from}}
  frame:=map[string]interface{}{"ordinal":len(frames),"call_depth":depth,"kind":vm.OpCode(kind).String(),"kind_byte":kind,"from":from,"code_address":to,"execution_address":execution,"input_bytes":len(input),"entry_execution_gas":gas.Execution,"entry_state_gas":gas.State,"value":nil,"opcode_events":uint64(0),"exited":false}
  if value!=nil {frame["value"]=value.String()};frames=append(frames,frame);active[depth]=frame;if depth>maxDepth {maxDepth=depth}
  if underlying!=nil&&underlying.OnEnterV2!=nil {underlying.OnEnterV2(depth,kind,from,to,input,gas,value)}
  capture("enter",depth,nil,nil,frame,nil)
 }
 hooks.OnOpcodeV2=func(pc uint64,op byte,gas,cost tracing.Gas,scope tracing.OpContext,rData []byte,depth int,err error) {
  total++;name:=vm.OpCode(op).String();counts[name]++
  if observer.reason=="" {prefix[name]++}
  frame:=active[depth-1];if frame!=nil {frame["opcode_events"]=frame["opcode_events"].(uint64)+1}
  var execution *common.Address;if scope!=nil {address:=scope.Address();execution=&address}
  if underlying!=nil&&underlying.OnOpcodeV2!=nil {underlying.OnOpcodeV2(pc,op,gas,cost,scope,rData,depth,err)}
  capture("opcode",depth,&pc,&op,frame,execution)
 }
 hooks.OnFaultV2=func(pc uint64,op byte,gas,cost tracing.Gas,scope tracing.OpContext,depth int,err error) {
  var execution *common.Address;if scope!=nil {address:=scope.Address();execution=&address}
  if underlying!=nil&&underlying.OnFaultV2!=nil {underlying.OnFaultV2(pc,op,gas,cost,scope,depth,err)}
  capture("fault",depth,&pc,&op,active[depth-1],execution)
 }
 hooks.OnExitV2=func(depth int,output []byte,left tracing.Gas,err error,reverted bool) {
  frame:=active[depth];if frame!=nil {frame["exited"]=true;frame["remaining_execution_gas"]=left.Execution;frame["remaining_state_gas"]=left.State;frame["reverted"]=reverted;frame["error"]="";frame["output_bytes"]=len(output);if err!=nil {frame["error"]=err.Error()}}
  if underlying!=nil&&underlying.OnExitV2!=nil {underlying.OnExitV2(depth,output,left,err,reverted)}
  // A precompiled contract or frame without opcode callbacks can trigger
  // fallback here, with the opcode PC left unavailable.
  capture("exit",depth,nil,nil,frame,nil);delete(active,depth)
 }
 evm.Config.Tracer=hooks;result,e:=core.ApplyMessage(evm,&m,nil);if e==nil {e=s.Error()}
 report:=observer.finish(result,e,result!=nil&&result.Failed());capture("finish",-1,nil,nil,nil,nil)
 out:=map[string]interface{}{"schema":"round1832-untimed-diagnostic-v1","complete":e==nil&&result!=nil&&!result.Failed()&&len(active)==0,"report":report,"first_reject_event":first,"raw_opcode_events":total,"raw_opcode_histogram":counts,"prefix_opcode_histogram_including_rejecting_event":prefix,"maximum_observed_call_depth":maxDepth,"frame_count":len(frames),"frames":frames,"observer_prefix_max_frame_depth":report.MaxFrameDepth,"used_gas":nil,"max_used_gas":nil,"error":"","paid":false}
 if result!=nil {out["used_gas"]=result.UsedGas;out["max_used_gas"]=result.MaxUsedGas};if e!=nil {out["error"]=e.Error()}
 return out,e
}
func benignEffects(f *benignFixture,row benignInstance,m *core.Message) []string {
 errors:=make([]string,0);fail:=func(ok bool,what string){if !ok {errors=append(errors,what)}}
 result,post,e:=benignApply(f.chain,f.base,m,benignHigh);if e!=nil {return []string{"expected-effects execution: "+e.Error()}}
 if result==nil||result.Failed(){return []string{"expected-effects high did not succeed"}}
 checksum:=benignChecksum(row.N);fail(bytes.Equal(result.ReturnData,checksum.Bytes()),"checksum return differs")
 for i:=0;i<64;i++ {expected:=int64(1);if i<row.N {expected=2};fail(post.GetState(f.batch,common.BigToHash(big.NewInt(int64(i+2))))==common.BigToHash(big.NewInt(expected)),fmt.Sprintf("ledger slot %d",i))}
 permit:=int64(0);if row.Kind!=0 {permit=1}
 nonce,e:=benignView(f,post,f.token,f.tokenABI,"nonces",f.owner);fail(e==nil&&len(nonce)==1,"permit nonce view")
 if e==nil&&len(nonce)==1 {fail(nonce[0].(*big.Int).Int64()==permit,"permit nonce effect")}
 allowance,e:=benignView(f,post,f.token,f.tokenABI,"allowance",f.owner,f.batch);fail(e==nil&&len(allowance)==1,"allowance view")
 if e==nil&&len(allowance)==1 {fail(allowance[0].(*big.Int).Int64()==permit,"allowance effect")}
 fail(post.GetNonce(m.From)==f.nonce+1,"transaction sender nonce effect")
 senderExpected:=new(uint256.Int).Sub(f.base.GetBalance(m.From),new(uint256.Int).Mul(uint256.NewInt(result.UsedGas),uint256.NewInt(3)))
 coinbase:=f.chain.current.Coinbase;coinbaseExpected:=new(uint256.Int).Add(f.base.GetBalance(coinbase),new(uint256.Int).Mul(uint256.NewInt(result.UsedGas),uint256.NewInt(2)))
 fail(post.GetBalance(m.From).Cmp(senderExpected)==0,"settled sender fee balance");fail(post.GetBalance(coinbase).Cmp(coinbaseExpected)==0,"settled coinbase tip balance")
 fail(post.GetBalance(f.owner).Cmp(f.base.GetBalance(f.owner))==0,"permit owner native balance changed")
 logs:=post.GetLogs(common.HexToHash("0x1832"),f.chain.current.Number.Uint64(),f.chain.current.Hash(),f.chain.current.Time)
 expectedLogs:=1;if permit!=0 {expectedLogs=2};fail(len(logs)==expectedLogs,"log count")
 batchIndex:=0;if row.Kind==1 {batchIndex=1}
 if len(logs)==expectedLogs {
  batch:=logs[batchIndex];fail(batch.Address==f.batch&&reflect.DeepEqual(batch.Topics,[]common.Hash{f.batchABI.Events["BatchResult"].ID,common.BigToHash(big.NewInt(int64(row.N)))})&&bytes.Equal(batch.Data,checksum.Bytes()),"batch log")
  if permit!=0 {index:=1-batchIndex;approval:=logs[index];fail(approval.Address==f.token&&reflect.DeepEqual(approval.Topics,[]common.Hash{f.tokenABI.Events["Approval"].ID,common.BytesToHash(f.owner.Bytes()),common.BytesToHash(f.batch.Bytes())})&&bytes.Equal(approval.Data,common.LeftPadBytes(big.NewInt(1).Bytes(),32)),"permit approval log/order")}
 }
 return errors
}
func benignQualifyRow(f *benignFixture,instance benignInstance) benignRow {
 row:=benignRow{Instance:instance,Outputs:make(map[string]benignObservation),Replays:make(map[string]*gasFixtureReplay),Errors:make([]string,0)}
 fail:=func(ok bool,what string){if !ok {row.Errors=append(row.Errors,what)}}
 m,e:=benignCall(f,instance);if e!=nil {row.Errors=append(row.Errors,e.Error());return row};row.Target=*m.To;row.Data="0x"+hex.EncodeToString(m.Data);row.Nonce=m.Nonce
 for _,method:=range []string{"S","C","V"} {out:=benignEstimate(f,m,method,true);row.Outputs[method]=out;fail(out.Error==""&&out.Gas>0&&out.Gas<=benignHigh&&len(out.Revert)==0,method+" estimation did not succeed")}
 stock,c,v:=row.Outputs["S"],row.Outputs["C"],row.Outputs["V"]
 fail(c.Gas==v.Gas&&c.Error==v.Error&&bytes.Equal(c.Revert,v.Revert)&&reflect.DeepEqual(c.Report,v.Report),"C/V output or certificate report differs")
 if c.Report!=nil&&c.Report.Certified {
  fail(c.Gas==c.Report.Candidate,"C output is not actual certificate candidate")
  fail(reflect.DeepEqual(c.Trials,[]TrialReport{{GasLimit:benignHigh,Failed:false,Outcome:"success"}}),"admitted C trial sequence")
  fail(reflect.DeepEqual(v.Trials,[]TrialReport{{GasLimit:benignHigh,Failed:false,Outcome:"success"},{GasLimit:c.Gas,Failed:false,Outcome:"success"}}),"admitted V must add exactly one candidate replay")
 } else {
  fail(stock.Gas==c.Gas&&stock.Error==c.Error&&bytes.Equal(stock.Revert,c.Revert)&&reflect.DeepEqual(stock.Trials,c.Trials)&&reflect.DeepEqual(stock.Trials,v.Trials),"fallback S/C/V ordered trials or outputs differ")
 }
 row.High,e=gasFixtureExecute(f.chain,f.base,m,common.HexToHash("0x1832"),benignHigh);fail(e==nil&&row.High!=nil&&row.High.Status==1,"high full-effects replay")
 row.Errors=append(row.Errors,benignEffects(f,instance,m)...)
 for _,method:=range []string{"S","C","V"} {
  gas:=row.Outputs[method].Gas;if gas==0||gas>benignHigh {continue}
  replay,err:=gasFixtureExecute(f.chain,f.base,m,common.HexToHash("0x1832"),gas);row.Replays[method+"_returned"]=replay;fail(err==nil&&gasFixtureEqual(row.High,replay),method+" returned-budget full settled effects differ")
 }
 if c.Gas>0&&c.Gas<=benignHigh {mid:=c.Gas+(benignHigh-c.Gas)/2;replay,err:=gasFixtureExecute(f.chain,f.base,m,common.HexToHash("0x1832"),mid);row.Replays["C_midpoint"]=replay;fail(err==nil&&gasFixtureEqual(row.High,replay),"midpoint full settled effects differ")}
 row.Diagnostic,e=benignDiagnostic(f,m);fail(e==nil&&row.Diagnostic["complete"]==true,"untimed high diagnostic failed")
 if row.Diagnostic!=nil {report,ok:=row.Diagnostic["report"].(CertificateReport);fail(ok&&c.Report!=nil&&reflect.DeepEqual(report,*c.Report),"untimed high report differs from actual C first run")}
 row.Complete=len(row.Errors)==0;return row
}
func benignExpectedSchedule() benignSchedule {
 schedule:=benignSchedule{Schema:"round1832-benign-schedule-v1",Instances:make([]benignInstance,0),Mixtures:make([]benignMixture,0)}
 for _,n:=range []int{1,8,64} {for _,d:=range []int{0,1,3} {for kind:=0;kind<3;kind++ {schedule.Instances=append(schedule.Instances,benignInstance{ID:fmt.Sprintf("N%d-D%d-K%d",n,d,kind),N:n,D:d,Kind:kind})}}}
 multiplicities:=[][3]int{{0,4,4},{2,3,3},{4,2,2},{6,1,1},{8,0,0}}
 for q,counts:=range multiplicities {
  subcycle:=make([]int,0,8);remaining:=counts
  for len(subcycle)<8 {for kind:=0;kind<3;kind++ {if remaining[kind]>0 {subcycle=append(subcycle,kind);remaining[kind]--}}}
  indices:=make([]int,0,72);for position:=0;position<8;position++ {for pair:=0;pair<9;pair++ {indices=append(indices,3*pair+subcycle[position])}}
  schedule.Mixtures=append(schedule.Mixtures,benignMixture{Q:[]string{"0","1/4","1/2","3/4","1"}[q],Indices:indices})
 }
 return schedule
}
type benignMemory struct {
 Alloc uint64 `json:"alloc_bytes"`;TotalAlloc uint64 `json:"total_alloc_bytes"`;Sys uint64 `json:"sys_bytes"`
 HeapAlloc uint64 `json:"heap_alloc_bytes"`;HeapInuse uint64 `json:"heap_inuse_bytes"`;HeapIdle uint64 `json:"heap_idle_bytes"`;HeapReleased uint64 `json:"heap_released_bytes"`;HeapObjects uint64 `json:"heap_objects"`
 Mallocs uint64 `json:"mallocs"`;Frees uint64 `json:"frees"`;NumGC uint32 `json:"num_gc"`;PauseTotal uint64 `json:"pause_total_ns"`;GCCPUFraction float64 `json:"gc_cpu_fraction"`;LastGC uint64 `json:"last_gc_unix_ns"`
}
func benignMem() benignMemory {
 var m runtime.MemStats;runtime.ReadMemStats(&m)
 return benignMemory{m.Alloc,m.TotalAlloc,m.Sys,m.HeapAlloc,m.HeapInuse,m.HeapIdle,m.HeapReleased,m.HeapObjects,m.Mallocs,m.Frees,m.NumGC,m.PauseTotalNs,m.GCCPUFraction,m.LastGC}
}
type benignUsage struct { UserNS int64 `json:"user_ns"`;SystemNS int64 `json:"system_ns"`;MaxRSSKiB int64 `json:"maximum_rss_kib"`;MinorFaults int64 `json:"minor_faults"`;MajorFaults int64 `json:"major_faults"`;VoluntarySwitches int64 `json:"voluntary_context_switches"`;InvoluntarySwitches int64 `json:"involuntary_context_switches"` }
func benignUsageNow() (benignUsage,error) {
 var r syscall.Rusage;e:=syscall.Getrusage(syscall.RUSAGE_SELF,&r)
 return benignUsage{r.Utime.Sec*1000000000+r.Utime.Usec*1000,r.Stime.Sec*1000000000+r.Stime.Usec*1000,r.Maxrss,r.Minflt,r.Majflt,r.Nvcsw,r.Nivcsw},e
}
type benignBlock struct {
 Ordinal int `json:"ordinal"`;Class string `json:"class"`;Phase string `json:"phase"`;InstanceIndex *int `json:"instance_index"`;Q string `json:"q,omitempty"`;Repetitions int `json:"repetitions"`;InputIndices []int `json:"input_indices"`;Count int `json:"count"`
 StartedUnixNS int64 `json:"started_unix_ns"`;FinishedUnixNS int64 `json:"finished_unix_ns"`;WallNS int64 `json:"wall_ns"`
 UsageBefore benignUsage `json:"usage_before"`;UsageAfter benignUsage `json:"usage_after"`;CPUUserNS int64 `json:"cpu_user_ns"`;CPUSystemNS int64 `json:"cpu_system_ns"`;CPUNS int64 `json:"cpu_ns"`;CPUResolutionLimited bool `json:"cpu_resolution_limited"`
 MemoryBefore benignMemory `json:"memory_before"`;MemoryAfter benignMemory `json:"memory_after"`;TotalAllocatedBytes uint64 `json:"total_allocated_bytes"`;GCCycles uint32 `json:"gc_cycles"`;GCPauseNS uint64 `json:"gc_pause_ns"`
 Observations []benignObservation `json:"observations"`;Complete bool `json:"complete"`;Errors []string `json:"errors"`
}
func benignJSONEqual(a,b interface{}) bool {left,e:=json.Marshal(a);if e!=nil {return false};right,e:=json.Marshal(b);return e==nil&&bytes.Equal(left,right)}
func benignSamePaid(actual,expected benignObservation) bool {return actual.Gas==expected.Gas&&actual.Error==expected.Error&&bytes.Equal(actual.Revert,expected.Revert)&&reflect.DeepEqual(actual.Report,expected.Report)&&len(actual.Trials)==0}
func benignMeasure(f *benignFixture,messages []*core.Message,q benignQualification,method,class,phase string,ordinal int,index *int,mix string,sequence []int,repetitions int) benignBlock {
 block:=benignBlock{Ordinal:ordinal,Class:class,Phase:phase,InstanceIndex:index,Q:mix,Repetitions:repetitions,InputIndices:append([]int{},sequence...),Count:len(sequence)*repetitions,Errors:make([]string,0)}
 block.Observations=make([]benignObservation,block.Count)
 // Output buffers are allocated before timing. The method-block CPU and wall
 // timers include message/state/report/options copies and all Estimate work.
 block.MemoryBefore=benignMem();var e error;block.UsageBefore,e=benignUsageNow();if e!=nil {block.Errors=append(block.Errors,e.Error());return block}
 start:=time.Now();block.StartedUnixNS=start.UnixNano();position:=0
 for repeat:=0;repeat<repetitions;repeat++ {for _,input:=range sequence {block.Observations[position]=benignEstimate(f,messages[input],method,false);position++}}
 end:=time.Now();block.FinishedUnixNS=end.UnixNano();block.WallNS=end.Sub(start).Nanoseconds()
 block.UsageAfter,e=benignUsageNow();if e!=nil {block.Errors=append(block.Errors,e.Error())}
 block.MemoryAfter=benignMem();block.CPUUserNS=block.UsageAfter.UserNS-block.UsageBefore.UserNS;block.CPUSystemNS=block.UsageAfter.SystemNS-block.UsageBefore.SystemNS;block.CPUNS=block.CPUUserNS+block.CPUSystemNS
 block.CPUResolutionLimited=block.CPUNS<=1000;block.TotalAllocatedBytes=block.MemoryAfter.TotalAlloc-block.MemoryBefore.TotalAlloc;block.GCCycles=block.MemoryAfter.NumGC-block.MemoryBefore.NumGC;block.GCPauseNS=block.MemoryAfter.PauseTotal-block.MemoryBefore.PauseTotal
 for i,out:=range block.Observations {input:=sequence[i%len(sequence)];if !benignSamePaid(out,q.Rows[input].Outputs[method]) {block.Errors=append(block.Errors,fmt.Sprintf("request %d input %d differs from complete qualification",i,input))}}
 if block.CPUUserNS<0||block.CPUSystemNS<0||block.WallNS<=0 {block.Errors=append(block.Errors,"invalid complete-block process clock interval")}
 block.Complete=len(block.Errors)==0;return block
}
func benignPaid(f *benignFixture,schedule benignSchedule,q benignQualification,method,out string) (map[string]interface{},error) {
 result:=map[string]interface{}{"schema":"round1832-benign-paid-v1","complete":false,"method":method,"blocks":make([]benignSpec,0),"endpoint_blocks":54,"mixture_blocks":10,"warmup_estimates":0,"measured_estimates":0,"total_estimates":0,"errors":make([]string,0),"paid_on_trial":false,"forced_gc_invocations":0,"high_only_paid_arm":false,"timer_source":"CLOCK_MONOTONIC via Go time.Time.Sub; getrusage(RUSAGE_SELF) user+system complete-block process CPU","cpu_clock_representation_resolution_ns":1000,"clock_resolution_policy":"retain every block; cpu<=1000ns marked resolution-limited; no calibration or extra repetitions","memory_before":benignMem()}
 startUsage,e:=benignUsageNow();if e!=nil {return result,e};result["usage_before"]=startUsage
 messages:=make([]*core.Message,len(schedule.Instances));for i,instance:=range schedule.Instances {messages[i],e=benignCall(f,instance);if e!=nil {return result,e}}
 blocks:=make([]benignSpec,0,64);errors:=make([]string,0);ordinal,warm,measured:=0,0,0
 record:=func(class,phase string,index *int,mix string,sequence []int,repetitions int) error {
  block:=benignMeasure(f,messages,q,method,class,phase,ordinal,index,mix,sequence,repetitions)
  spec,e:=benignSave(filepath.Join(out,fmt.Sprintf("block-%02d.json",ordinal)),block);if e!=nil {return e};blocks=append(blocks,spec);errors=append(errors,block.Errors...)
  if phase=="warmup" {warm+=block.Count}else {measured+=block.Count};ordinal++;return nil
 }
 for i:=range schedule.Instances {index:=i;if e=record("endpoint","warmup",&index,"",[]int{i},4);e!=nil {return result,e};if e=record("endpoint","measured",&index,"",[]int{i},64);e!=nil {return result,e}}
 for _,mix:=range schedule.Mixtures {if e=record("mixture","warmup",nil,mix.Q,mix.Indices,1);e!=nil {return result,e};if e=record("mixture","measured",nil,mix.Q,mix.Indices,4);e!=nil {return result,e}}
 endUsage,e:=benignUsageNow();if e!=nil {return result,e};result["usage_after"]=endUsage;result["memory_after"]=benignMem();result["blocks"]=blocks;result["warmup_estimates"]=warm;result["measured_estimates"]=measured;result["total_estimates"]=warm+measured
 if ordinal!=64||warm!=468||measured!=3168||warm+measured!=3636 {errors=append(errors,"fixed process counts differ")}
 result["errors"]=errors;result["complete"]=len(errors)==0;return result,nil
}
func benignPrestateUnchanged(f *benignFixture) bool {
 copy:=f.base.Copy();bc:=core.NewEVMBlockContext(f.chain.current,f.chain,nil);rules:=f.chain.config.Rules(bc.BlockNumber,bc.Random!=nil,bc.Time)
 root:=copy.IntermediateRoot(rules);return copy.Error()==nil&&root==f.root&&f.base.GetNonce(certificateOsakaSender)==f.nonce
}
func benignAffinity() (map[string]interface{},error) {
 status,e:=os.ReadFile("/proc/self/status");if e!=nil {return nil,e};allowed:=""
 for _,line:=range strings.Split(string(status),"\n") {if strings.HasPrefix(line,"Cpus_allowed_list:") {allowed=strings.TrimSpace(strings.TrimPrefix(line,"Cpus_allowed_list:"))}}
 expected:=os.Getenv("GAS_CERTIFICATE_CPU");if expected=="" {expected="0"}
 if allowed!=expected {return nil,fmt.Errorf("observed process CPU affinity %q, expected %s",allowed,expected)}
 return map[string]interface{}{"pid":os.Getpid(),"observed_cpus_allowed_list":allowed,"required_gomaxprocs":1,"observed_gomaxprocs":runtime.GOMAXPROCS(0),"GOGC":os.Getenv("GOGC"),"GOMEMLIMIT":os.Getenv("GOMEMLIMIT"),"GODEBUG":os.Getenv("GODEBUG"),"go_version":runtime.Version(),"goos":runtime.GOOS,"goarch":runtime.GOARCH},nil
}
func benignRun(t *testing.T,path string) (runError error) {
 manifestPath,e:=benignPath(path);if e!=nil {return e};raw,e:=os.ReadFile(manifestPath);if e!=nil {return e};if len(raw)>65536 {return fmt.Errorf("manifest is oversized")}
 var manifest benignManifest;if e=json.Unmarshal(raw,&manifest);e!=nil {return e}
 if manifest.Schema!="round1832-benign-input-v1" {return fmt.Errorf("native manifest schema differs")}
 if manifest.Phase!="qualify"&&manifest.Phase!="paid" {return fmt.Errorf("unknown phase")}
 if manifest.Phase=="qualify"&&(manifest.Method!=""||manifest.Qualification!=nil) {return fmt.Errorf("qualification has no paid method or prior qualification")}
 if manifest.Phase=="paid"&&(manifest.Method!="S"&&manifest.Method!="C"&&manifest.Method!="V"||manifest.Qualification==nil) {return fmt.Errorf("paid requires one S/C/V method and complete qualification")}
 out:=filepath.Clean(manifest.Output);if !filepath.IsAbs(out)||out!=manifest.Output {return fmt.Errorf("canonical fresh output required")}
 if _,e=benignPath(filepath.Dir(out));e!=nil {return e};if _,e=os.Lstat(out);!os.IsNotExist(e) {return fmt.Errorf("output already exists or cannot be examined")};if e=os.Mkdir(out,0700);e!=nil {return e}
 sum:=sha256.Sum256(raw);inputs:=map[string]benignSpec{"manifest":{manifestPath,len(raw),hex.EncodeToString(sum[:])},"artifacts":manifest.Artifacts,"schedule":manifest.Schedule}
 receipt:=map[string]interface{}{"schema":"round1832-benign-receipt-v1","complete":false,"phase":manifest.Phase,"method":manifest.Method,"started_unix_ns":time.Now().UnixNano(),"errors":make([]string,0),"inputs":inputs,"output_directory":out,"original_high":benignHigh,"actual_released":true,"automatic_retries":0,"production_certificate_changes":0,"process_final_wait4_owner":"outer benchmark runner"}
 defer func(){
  preserved:=true;for _,spec:=range inputs {if _,err:=benignRead(spec);err!=nil {preserved=false;if runError==nil {runError=err}}}
  receipt["inputs_preserved"]=preserved;receipt["finished_unix_ns"]=time.Now().UnixNano();receipt["memory_at_native_return"]=benignMem();usage,err:=benignUsageNow();receipt["usage_at_native_return"]=usage
  if err!=nil&&runError==nil {runError=err};if runError!=nil {receipt["errors"]=[]string{runError.Error()};receipt["complete"]=false}
  if _,err=benignSave(filepath.Join(out,"receipt.json"),receipt);err!=nil&&runError==nil {runError=err}
 }()
 if runtime.GOOS!="linux"||runtime.GOMAXPROCS(0)!=1||os.Getenv("GOGC")!=""||os.Getenv("GOMEMLIMIT")!=""||os.Getenv("GODEBUG")!="" {return fmt.Errorf("Linux GOMAXPROCS1 and default GC/debug environment required")}
 affinity,e:=benignAffinity();if e!=nil {return e};receipt["environment"]=affinity
 usage,e:=benignUsageNow();if e!=nil {return e};receipt["usage_at_native_entry"]=usage;receipt["memory_at_native_entry"]=benignMem()
 artifactRaw,e:=benignRead(manifest.Artifacts);if e!=nil {return e};var artifacts benignArtifacts;if e=json.Unmarshal(artifactRaw,&artifacts);e!=nil {return e}
 if artifacts.Schema!="round1832-compiled-contracts-v1"||!artifacts.Complete||artifacts.Compiler.SHA256!=benignCompilerSHA||len(artifacts.Contracts)!=2 {return fmt.Errorf("complete exact compiled artifacts required")}
 inputs["compiler"]=artifacts.Compiler;inputs["node"]=artifacts.Node
 for name,spec:=range artifacts.Sources {inputs["artifact-source/"+name]=spec}
 token,ok:=artifacts.Sources["erc20_artifact"];if !ok||token.Bytes!=69315||token.SHA256!="7917faf747fc7f3023bfe102bde224ecce2f261c5993b0a5f2308f95789ff530" {return fmt.Errorf("unchanged original ERC20 artifact required")}
 sourceSchedule,ok:=artifacts.Sources["schedule"];if !ok||sourceSchedule.Bytes!=manifest.Schedule.Bytes||sourceSchedule.SHA256!=manifest.Schedule.SHA256 {return fmt.Errorf("schedule differs from compiled materialization")}
 for _,spec:=range inputs {if _,e=benignRead(spec);e!=nil {return e}}
 scheduleRaw,e:=benignRead(manifest.Schedule);if e!=nil {return e};var schedule benignSchedule;if e=json.Unmarshal(scheduleRaw,&schedule);e!=nil {return e}
 if !reflect.DeepEqual(schedule,benignExpectedSchedule()) {return fmt.Errorf("frozen 27 instances/five exact 72-input mixtures differ")}
 fixture,e:=benignBuild(t,artifacts);if e!=nil {return e};receipt["prestate_root"]=fixture.root;receipt["sender_nonce"]=fixture.nonce;receipt["setup"]=fixture.setup;receipt["header"]=fixture.chain.current;receipt["token"]=fixture.token;receipt["batch"]=fixture.batch;receipt["permit_owner"]=fixture.owner;receipt["targets"]=fixture.targets
 if manifest.Phase=="qualify" {
  q:=benignQualification{Schema:"round1832-benign-qualification-v1",Artifacts:manifest.Artifacts,Schedule:manifest.Schedule,Prestate:fixture.root,Nonce:fixture.nonce,Setup:fixture.setup,Rows:make([]benignRow,0,27),Errors:make([]string,0)}
  for _,instance:=range schedule.Instances {row:=benignQualifyRow(fixture,instance);q.Rows=append(q.Rows,row);for _,what:=range row.Errors {q.Errors=append(q.Errors,instance.ID+": "+what)}}
  unchanged:=benignPrestateUnchanged(fixture);if !unchanged {q.Errors=append(q.Errors,"original committed prestate changed")};q.Complete=len(q.Rows)==27&&len(q.Errors)==0
  spec,err:=benignSave(filepath.Join(out,"qualification.json"),q);if err!=nil {return err};receipt["qualification"]=spec;receipt["prestate_preserved"]=unchanged;receipt["instance_count"]=len(q.Rows);receipt["complete"]=q.Complete
  if !q.Complete {return fmt.Errorf("qualification incomplete: retained all27 rows and errors")}
 } else {
  inputs["qualification"]=*manifest.Qualification;qraw,err:=benignRead(*manifest.Qualification);if err!=nil {return err};var q benignQualification;if err=json.Unmarshal(qraw,&q);err!=nil {return err}
  if q.Schema!="round1832-benign-qualification-v1"||!q.Complete||len(q.Errors)!=0||len(q.Rows)!=27||q.Artifacts!=manifest.Artifacts||q.Schedule!=manifest.Schedule||q.Prestate!=fixture.root||q.Nonce!=fixture.nonce||!benignJSONEqual(q.Setup,fixture.setup) {return fmt.Errorf("matching whole successful qualification required")}
  for i,row:=range q.Rows {
   m,err:=benignCall(fixture,schedule.Instances[i]);if err!=nil {return err}
   if !row.Complete||len(row.Errors)!=0||row.Instance!=schedule.Instances[i]||row.Target!=*m.To||row.Nonce!=m.Nonce||row.Data!="0x"+hex.EncodeToString(m.Data)||len(row.Outputs)!=3 {return fmt.Errorf("qualification row %d identity differs",i)}
   for _,method:=range []string{"S","C","V"} {expected,ok:=row.Outputs[method];if !ok||expected.Error!=""||expected.Gas==0||expected.Gas>benignHigh||len(expected.Revert)!=0 {return fmt.Errorf("qualification method %s row %d is not successful",method,i)}}
  }
  paid,err:=benignPaid(fixture,schedule,q,manifest.Method,out);if err!=nil {return err};unchanged:=benignPrestateUnchanged(fixture);paid["prestate_preserved"]=unchanged;if !unchanged {paid["complete"]=false}
  spec,err:=benignSave(filepath.Join(out,"paid.json"),paid);if err!=nil {return err};receipt["paid"]=spec;receipt["prestate_preserved"]=unchanged;receipt["complete"]=paid["complete"]==true
  if paid["complete"]!=true {return fmt.Errorf("paid result incomplete; all completed blocks retained")}
 }
 return nil
}
func TestBenignCoverage(t *testing.T) {
 manifest:=os.Getenv("GETH_BENIGN_COVERAGE_MANIFEST");if manifest=="" {t.Skip("benign coverage manifest is absent")}
 if e:=benignRun(t,manifest);e!=nil {t.Fatal(e)}
}
