// This fixed-context benchmark command uses the RPC, estimator, and VM
// implementations selected by the shared or pristine build tree.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
)

var project=os.Getenv("GAS_CERTIFICATE_BENCH_ROOT")
const sourcePin="a579077007b98217c3e253a66e4b452ca0c32b96"
type spec struct { Path string `json:"path"`; Bytes int64 `json:"bytes"`; SHA256 string `json:"sha256"` }
type manifest struct {
	Schema string `json:"schema"`; Released bool `json:"released"`; Purpose string `json:"purpose"`; Cohort string `json:"cohort"`; Variant string `json:"variant"`
	Binary *spec `json:"server_binary"`; Context *spec `json:"context"`; Population *spec `json:"population"`; Qualification *spec `json:"qualification"`; CloneReceipt *spec `json:"clone_receipt"`
	SourceProofs []spec `json:"source_proofs"`; CloneDataDirectory string `json:"clone_data_directory"`; OutputDirectory string `json:"output_directory"`
	HTTPPort int `json:"http_port"`; AuthPort int `json:"auth_port"`
}
type fixedContext struct {
	Schema string `json:"schema"`; Cohort string `json:"cohort"`; SourcePin string `json:"source_pin"`; NodeName string `json:"node_name"`; OriginalNode string `json:"original_node_directory"`
	Config params.ChainConfig `json:"config"`; Headers []json.RawMessage `json:"headers"`; SourceReceipt *spec `json:"source_receipt"`; SourceRPCLog *spec `json:"source_rpc_log"`; SourceRetirement *spec `json:"source_retirement"`
	RPCGasCap uint64 `json:"rpc_gas_cap"`; RPCEVMTimeoutNS int64 `json:"rpc_evm_timeout_ns"`
}
type population struct { Schema string `json:"schema"`; Cohort string `json:"cohort"`; Context spec `json:"context"`; Count int `json:"count"`; ExpectedReverts int `json:"expected_reverts"`; Entries []json.RawMessage `json:"entries"` }
type qualification struct { Schema string `json:"schema"`; Complete bool `json:"complete"`; Variant string `json:"variant"`; Cohort string `json:"cohort"`; Context spec `json:"context"`; Population spec `json:"population"`; Binary spec `json:"server_binary"`; ServerClosed bool `json:"server_closed"`; ResponseCount int `json:"response_count"`; ExpectedReverts int `json:"expected_reverts"` }
func must(err error){if err!=nil{panic(err)}}
func require(ok bool,text string){if !ok{panic(text)}}
func contained(name string)string{name=filepath.Clean(name);require(filepath.IsAbs(name)&&strings.HasPrefix(name,project+"/"),"project-contained absolute path");actual,err:=filepath.EvalSymlinks(name);must(err);require(actual==name,"canonical non-symlink path");return name}
func identity(name string)spec{f,err:=os.Open(contained(name));must(err);defer f.Close();h:=sha256.New();n,err:=io.Copy(h,f);must(err);return spec{name,n,hex.EncodeToString(h.Sum(nil))}}
func verify(s spec){require(identity(s.Path)==s,"input identity changed: "+s.Path)}
func load(name string,target any){f,err:=os.Open(contained(name));must(err);defer f.Close();raw,err:=io.ReadAll(io.LimitReader(f,(64<<20)+1));must(err);require(len(raw)<=64<<20,"whole input cap");must(json.Unmarshal(raw,target))}
func encode(v any)json.RawMessage{raw,err:=json.Marshal(v);must(err);return raw}
func equal(a,b any)bool{return bytes.Equal(encode(a),encode(b))}
func emit(v any){raw:=encode(v);require(len(raw)<=8<<20,"control message cap");_,err:=os.Stdout.Write(append(raw,'\n'));must(err)}
func cpu()map[string]int64{var u syscall.Rusage;must(syscall.Getrusage(syscall.RUSAGE_SELF,&u));return map[string]int64{"user_ns":u.Utime.Sec*1000000000+u.Utime.Usec*1000,"system_ns":u.Stime.Sec*1000000000+u.Stime.Usec*1000,"native_time_resolution_ns":1000}}
func snapshot(label string,origin time.Time)map[string]any{
	before:=cpu();monoBefore:=time.Since(origin).Nanoseconds();var m runtime.MemStats;runtime.ReadMemStats(&m)
	samples:=[]metrics.Sample{{Name:"/gc/heap/live:bytes"}};metrics.Read(samples);require(samples[0].Value.Kind()==metrics.KindUint64,"exact toolchain last-marked-live metric")
	values:=map[string]any{"num_gc":m.NumGC,"pause_total_ns":m.PauseTotalNs,"total_alloc_bytes":m.TotalAlloc,"mallocs":m.Mallocs,"frees":m.Frees,"heap_alloc_bytes":m.HeapAlloc,"heap_objects":m.HeapObjects,"heap_inuse_bytes":m.HeapInuse,"heap_sys_bytes":m.HeapSys,"sys_bytes":m.Sys,"next_gc_bytes":m.NextGC,"last_marked_live_bytes":samples[0].Value.Uint64()}
	after:=cpu();return map[string]any{"label":label,"cpu_before":before,"cpu_after":after,"monotonic_before_ns":monoBefore,"monotonic_after_ns":time.Since(origin).Nanoseconds(),"normal_gc_heap":values}
}
func recordedHeader(raw json.RawMessage)*types.Header{var h types.Header;must(json.Unmarshal(raw,&h));var claim struct{Hash common.Hash `json:"hash"`};must(json.Unmarshal(raw,&claim));require(h.Number!=nil&&h.Number.IsUint64()&&h.Root!=(common.Hash{})&&claim.Hash==h.Hash(),"full recorded header/hash/root");return &h}
func contextHeaders(c *fixedContext)[]*types.Header{
	require(len(c.Headers)>0&&len(c.Headers)<=10001,"complete bounded0..head header list");out:=[]*types.Header{}
	for i,raw:=range c.Headers{h:=recordedHeader(raw);require(h.Number.Uint64()==uint64(i),"no missing canonical ancestor");if i>0{require(h.ParentHash==out[i-1].Hash(),"connected complete canonical ancestry")};out=append(out,h)}
	return out
}
func checkContext(b *eth.Ethereum,c *fixedContext,headers []*types.Header)map[string]any{
	chain:=b.BlockChain();require(equal(chain.Config(),&c.Config),"stored chain config unchanged");head:=chain.CurrentBlock();require(head!=nil&&head.Hash()==headers[len(headers)-1].Hash(),"retained final head; no advancement/rewind")
	actual:=[]*types.Header{};for i,want:=range headers{got:=chain.GetHeaderByNumber(uint64(i));require(got!=nil&&chain.GetCanonicalHash(uint64(i))==want.Hash()&&got.Hash()==want.Hash()&&equal(got,want),"exact canonical header/root/ancestor");actual=append(actual,got)}
	require(b.APIBackend.RPCGasCap()==c.RPCGasCap&&int64(b.APIBackend.RPCEVMTimeout())==c.RPCEVMTimeoutNS,"recorded RPC cap/timeout unchanged")
	return map[string]any{"complete":true,"config":chain.Config(),"head":head,"headers":actual,"canonical_headers_checked":len(actual)}
}
func historyProgress(b *eth.Ethereum)map[string]any{
	states,tries,err:=b.BlockChain().StateIndexProgress();var failure any;if err!=nil{failure=err.Error()}
	return map[string]any{"state_remaining":states,"trienode_remaining":tries,"error":failure}
}
func historyReady(b *eth.Ethereum,headers []*types.Header,report map[string]any){
	start:=time.Now();deadline:=start.Add(10*time.Minute);chain:=b.BlockChain()
	history:=map[string]any{"schema":"round1796-history-opening-v1","ready":false,"complete":false,"timeout_ns":int64(10*time.Minute),"poll_interval_ns":int64(time.Second),"attempted_poll_count":0,"recorded_header_count":len(headers),"actual_state_scheme":chain.TrieDB().Scheme(),"last_complete_failed_sample":nil,"rpc_or_EVM_invocations":0,"full_state_integrity_claimed":false}
	report["history_opening"]=history;require(chain.TrieDB().Scheme()==rawdb.PathScheme,"actual retained path-state scheme required; no conversion")
	for attempt:=1;;attempt++{
		before:=historyProgress(b);if attempt==1{history["initial_progress"]=before};history["attempted_poll_count"]=attempt
		rows:=[]map[string]any{};allOpen:=true
		for _,h:=range headers{
			row:=map[string]any{"number":h.Number.Uint64(),"hash":h.Hash(),"state_root":h.Root,"time":h.Time,"route":"unavailable","state_at_error":nil,"historic_state_error":nil,"native_historic_reader_diagnostic":nil,"opened":false}
			live,err:=chain.StateAt(h.Root,h.Number,h.Time)
			if err==nil&&live!=nil{row["route"]="live";row["opened"]=true}else{
				if err!=nil{row["state_at_error"]=err.Error()}else{row["state_at_error"]="StateAt returned nil state without error"}
				old,historicErr:=chain.HistoricState(h.Root,h.Number,h.Time)
				if historicErr==nil&&old!=nil{row["route"]="historic";row["opened"]=true}else{
					allOpen=false;if historicErr!=nil{row["historic_state_error"]=historicErr.Error()}else{row["historic_state_error"]="HistoricState returned nil state without error"}
					reader,nativeErr:=chain.TrieDB().HistoricStateReader(h.Root);var nativeFailure any;if nativeErr!=nil{nativeFailure=nativeErr.Error()}
					row["native_historic_reader_diagnostic"]=map[string]any{"reader_constructed":reader!=nil,"error":nativeFailure,"used_for_readiness":false}
				}
			};rows=append(rows,row)
		}
		after:=historyProgress(b);elapsed:=time.Since(start).Nanoseconds()
		zero:=before["error"]==nil&&after["error"]==nil&&before["state_remaining"]==uint64(0)&&before["trienode_remaining"]==uint64(0)&&after["state_remaining"]==uint64(0)&&after["trienode_remaining"]==uint64(0)
		sample:=map[string]any{"attempt":attempt,"elapsed_ns":elapsed,"progress_before":before,"progress_after":after,"all_roots_attempted":len(rows)==len(headers),"all_roots_open":allOpen,"roots":rows,"zero_progress":zero}
		history["last_sample"]=sample;history["final_progress"]=after;history["elapsed_ns"]=elapsed
		if allOpen&&zero&&time.Now().Before(deadline){history["ready"]=true;history["complete"]=true;return}
		history["last_complete_failed_sample"]=sample
		remaining:=time.Until(deadline);require(remaining>0,"normal archive history readiness timeout; full last sample retained")
		if remaining>time.Second{remaining=time.Second};time.Sleep(remaining)
	}
}
func run(name string)(code int){
	origin:=time.Now();var m manifest;load(name,&m);require(m.Schema=="round1789-server-input-v1","concrete server manifest schema")
	require(m.Purpose=="qualification"||m.Purpose=="paid","explicit server purpose");require(m.Cohort=="E"||m.Cohort=="A","declared complete cohort");require(m.HTTPPort>=1024&&m.HTTPPort<=65535&&m.AuthPort>=1024&&m.AuthPort<=65535&&m.HTTPPort!=m.AuthPort,"distinct loopback ports")
	require(m.Binary!=nil&&m.Context!=nil&&m.Population!=nil&&m.CloneReceipt!=nil&&len(m.SourceProofs)>0,"actual binary/context/population/clone/source identities")
	executable,err:=os.Executable();must(err);require(identity(executable)==*m.Binary,"exact compiled server artifact")
	protected:=append([]spec{identity(name),*m.Binary,*m.Context,*m.Population,*m.CloneReceipt},m.SourceProofs...)
	if m.Purpose=="paid"{require(m.Qualification!=nil,"separate completed fixed-context qualification required");protected=append(protected,*m.Qualification)}else{require(m.Qualification==nil,"qualification has no guessed future proof")}
	for _,s:=range protected{verify(s)}
	var c fixedContext;load(m.Context.Path,&c);require(c.Schema=="round1789-fixed-context-v1"&&c.Cohort==m.Cohort&&c.SourcePin==sourcePin&&c.Config.ChainID!=nil&&c.Config.ChainID.Cmp(big.NewInt(1))==0,"exact declared current chain/config")
	for _,s:=range protected{verify(s)} // Public source/state origins are supplied through SourceProofs.
	require(c.NodeName!=""&&filepath.Base(c.NodeName)==c.NodeName&&c.NodeName!="."&&c.NodeName!="..","explicit original node instance name");require(c.RPCGasCap==50000000&&c.RPCEVMTimeoutNS==int64(5*time.Second),"qualified original caps")
	headers:=contextHeaders(&c);var p population;load(m.Population.Path,&p);count,reverts:=9,2;if m.Cohort=="A"{count,reverts=188,11};require(p.Schema=="round1789-fixed-population-v1"&&p.Cohort==m.Cohort&&p.Context==*m.Context&&p.Count==count&&len(p.Entries)==count&&p.ExpectedReverts==reverts,"whole frozen population; no filtering")
	if m.Purpose=="paid"{var q qualification;load(m.Qualification.Path,&q);require(q.Schema=="round1789-qualification-v1"&&q.Complete&&q.ServerClosed&&q.Variant==m.Variant&&q.Cohort==m.Cohort&&q.Context==*m.Context&&q.Population==*m.Population&&q.Binary==*m.Binary&&q.ResponseCount==count&&q.ExpectedReverts==reverts,"exact same-context separately qualified variant; no authorchain substitution")}
	clone:=contained(m.CloneDataDirectory);original:=contained(c.OriginalNode);require(clone!=original&&!strings.HasPrefix(clone,original+"/"),"independent clone only; original DB never opened");contained(filepath.Join(clone,c.NodeName,"chaindata"))
	var proof struct{Schema string `json:"schema"`;Complete bool `json:"complete"`;Source string `json:"source_data_directory"`;Clone string `json:"clone_data_directory"`;Independent bool `json:"independent_closed_full_clone"`};load(m.CloneReceipt.Path,&proof);require(proof.Schema=="round1773-independent-clone-v1"&&proof.Complete&&proof.Independent&&proof.Source==original&&proof.Clone==clone,"actual independent whole closed clone proof")
	require(filepath.IsAbs(m.OutputDirectory)&&strings.HasPrefix(m.OutputDirectory,project+"/"),"fresh output under ROOT");contained(filepath.Dir(m.OutputDirectory));must(os.Mkdir(m.OutputDirectory,0700));out:=contained(m.OutputDirectory)
	report:=map[string]any{"schema":"round1789-server-receipt-v1","complete":false,"purpose":m.Purpose,"cohort":m.Cohort,"variant":m.Variant,"production_variant":productionVariant(m.Variant),"source_pin":sourcePin,"server_binary":*m.Binary,"context":*m.Context,"population":*m.Population,"errors":[]string{},"forced_gc_invocations":0,"requests_issued_by_server":0}
	var stack *node.Node;closed,stopped:=false,false
	defer func(){if v:=recover();v!=nil{report["errors"]=append(report["errors"].([]string),fmt.Sprint(v));code=2};if stack!=nil&&!closed{err:=stack.Close();closed=err==nil;if err!=nil{report["errors"]=append(report["errors"].([]string),err.Error())}};report["stack_closed"]=closed;for _,s:=range protected{if identity(s.Path)!=s{report["errors"]=append(report["errors"].([]string),"input changed: "+s.Path)}};report["protected_inputs"]=protected;report["inputs_preserved"]=len(report["errors"].([]string))==0;report["complete"]=stopped&&closed&&report["inputs_preserved"]==true;if !report["complete"].(bool){code=2};raw,err:=json.MarshalIndent(report,"","  ");must(err);require(len(raw)<=8<<20,"whole receipt cap");must(os.WriteFile(filepath.Join(out,"receipt.json"),append(raw,'\n'),0600))}()
	n:=node.DefaultConfig;n.Name=c.NodeName;n.DataDir=clone;n.KeyStoreDir=filepath.Join(out,"keystore");n.IPCPath="";n.WSHost="";n.HTTPHost="127.0.0.1";n.HTTPPort=m.HTTPPort;n.HTTPModules=[]string{"eth","net","web3"};n.HTTPVirtualHosts=[]string{"127.0.0.1","localhost"};n.AuthAddr="127.0.0.1";n.AuthPort=m.AuthPort;n.JWTSecret=filepath.Join(out,"jwtsecret");n.P2P=p2p.Config{MaxPeers:0,NoDiscovery:true,NoDial:true,ListenAddr:""}
	e:=ethconfig.Defaults;e.Genesis=nil;e.SyncMode=ethconfig.FullSync;e.TxPool.NoLocals=true;e.LogNoHistory=true;e.NetworkId=1;e.Miner.GasPrice=big.NewInt(1);e.RPCGasCap=c.RPCGasCap;e.NoPruning=true;configureVariant(&e,m.Variant)
	require(e.StateHistory==ethconfig.Defaults.StateHistory&&e.TrienodeHistory==ethconfig.Defaults.TrienodeHistory&&e.StateScheme==ethconfig.Defaults.StateScheme,"original retention and state-scheme selection preserved")
	report["storage_opening_policy"]=map[string]any{"no_pruning":e.NoPruning,"state_history":e.StateHistory,"trienode_history":e.TrienodeHistory,"configured_state_scheme":e.StateScheme,"retention_unchanged":true,"index_timing_override":false,"forced_gc_invocations":0}
	stack,err=node.New(&n);must(err);backend,err:=eth.New(stack,&e);must(err);report["context_before"]=checkContext(backend,&c,headers);must(stack.Start());assertVariant(backend,m.Variant)
	historyReady(backend,headers,report);report["context_after_readiness"]=checkContext(backend,&c,headers);require(equal(report["context_before"],report["context_after_readiness"]),"exact canonical context preserved through normal history opening")
	report["http_timeouts"]=n.HTTPTimeouts;report["rpc_gas_cap"]=c.RPCGasCap;report["rpc_evm_timeout_ns"]=c.RPCEVMTimeoutNS;report["node_name"]=c.NodeName;report["pid"]=os.Getpid();report["ready"]=snapshot("READY",origin)
	emit(map[string]any{"kind":"READY","pid":os.Getpid(),"variant":m.Variant,"purpose":m.Purpose,"cohort":m.Cohort,"server_binary":*m.Binary,"context":*m.Context,"population":*m.Population,"snapshot":report["ready"]})
	commands:=make(chan string);scanErrors:=make(chan error,1);go func(){scanner:=bufio.NewScanner(os.Stdin);scanner.Buffer(make([]byte,4096),1<<20);for scanner.Scan(){commands<-scanner.Text()};scanErrors<-scanner.Err()}()
	signals:=make(chan os.Signal,1);signal.Notify(signals,syscall.SIGTERM,syscall.SIGINT);defer signal.Stop(signals);begun,ended:=false,false
	for !stopped{select{case sig:=<-signals:panic("owned signal: "+sig.String());case err:=<-scanErrors:must(err);panic("control EOF before STOP");case raw:=<-commands:
		var command struct{Command string `json:"command"`};must(json.Unmarshal([]byte(raw),&command))
		switch command.Command{
		case "BEGIN":require(m.Purpose=="paid"&&!begun&&!ended,"one BEGIN after warmup");begun=true;report["begin"]=snapshot("BEGIN",origin);emit(map[string]any{"kind":"BEGIN","snapshot":report["begin"]})
		case "END":require(m.Purpose=="paid"&&begun&&!ended,"one END after complete measured body");ended=true;report["end"]=snapshot("END",origin);emit(map[string]any{"kind":"END","snapshot":report["end"]})
		case "STOP":require(m.Purpose=="qualification"||begun&&ended,"paid STOP follows END");report["context_after"]=checkContext(backend,&c,headers);require(equal(report["context_before"],report["context_after"]),"exact full context preserved");must(stack.Close());closed=true;report["closed"]=snapshot("CLOSED",origin);stopped=true;emit(map[string]any{"kind":"CLOSED","snapshot":report["closed"],"context_unchanged":true})
		default:panic("unsupported private control command")
		}
	}}
	return 0
}
func main(){require(filepath.IsAbs(project)&&filepath.Clean(project)==project,"set GAS_CERTIFICATE_BENCH_ROOT to a canonical absolute workspace");actual,err:=filepath.EvalSymlinks(project);must(err);require(actual==project,"canonical benchmark workspace");require(len(os.Args)==3&&os.Args[1]=="--manifest","usage: benchmark-rpc --manifest MANIFEST");os.Exit(run(contained(os.Args[2])))}
