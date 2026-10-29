// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: LGPL-3.0-or-later

package gasestimator

import (
 "encoding/hex"
 "math/big"
 "reflect"
 "strings"

 "github.com/ethereum/go-ethereum/common"
 "github.com/ethereum/go-ethereum/consensus"
 "github.com/ethereum/go-ethereum/core"
 "github.com/ethereum/go-ethereum/core/state"
 "github.com/ethereum/go-ethereum/core/types"
 "github.com/ethereum/go-ethereum/core/vm"
 "github.com/ethereum/go-ethereum/crypto"
 "github.com/ethereum/go-ethereum/params"
)

// Native transaction helpers execute against caller-provided in-memory state.

type gasFixtureLog struct { Address string `json:"address"`; Topics []string `json:"topics"`; Data string `json:"data"` }
type gasFixtureReplay struct {
	GasLimit uint64 `json:"gas_limit"`; Status int `json:"status"`; ExecutionError string `json:"execution_error"`
	ReturnData string `json:"return_data"`; UsedGas uint64 `json:"used_gas"`; MaxUsedGas uint64 `json:"max_used_gas"`
	RefundCounter uint64 `json:"refund_counter"`; FeeSettledStateRoot common.Hash `json:"fee_settled_state_root"`
	Logs []gasFixtureLog `json:"logs"`; FullLogs []*types.Log `json:"full_logs"`; CreatedAddress *string `json:"created_address"`
}

func gasFixtureHexOut(b []byte) string {return "0x"+hex.EncodeToString(b)}
func gasFixtureError(e error) string {if e==nil{return ""};return e.Error()}

type gasFixtureEngine struct { consensus.Engine }
func (gasFixtureEngine) Author(h *types.Header) (common.Address,error) {return h.Coinbase,nil}
type gasFixtureChain struct { config *params.ChainConfig; current *types.Header; numbers map[uint64]*types.Header; hashes map[common.Hash]*types.Header }
func (c *gasFixtureChain) Config() *params.ChainConfig {return c.config}
func (c *gasFixtureChain) CurrentHeader() *types.Header {return c.current}
func (c *gasFixtureChain) GetHeader(h common.Hash,n uint64) *types.Header {v:=c.hashes[h];if v!=nil&&v.Number.Uint64()==n{return v};return nil}
func (c *gasFixtureChain) GetHeaderByNumber(n uint64) *types.Header {return c.numbers[n]}
func (c *gasFixtureChain) GetHeaderByHash(h common.Hash) *types.Header {return c.hashes[h]}
func (c *gasFixtureChain) Engine() consensus.Engine {return gasFixtureEngine{}}

func gasFixtureExecute(c *gasFixtureChain,base *state.StateDB,original *core.Message,txHash common.Hash,limit uint64) (*gasFixtureReplay,error) {
	s:=base.Copy();m:=*original;m.GasLimit=limit
	s.SetTxContext(txHash,0,0)
	bc:=core.NewEVMBlockContext(c.current,c,nil);if m.GasPrice.IsZero(){bc.BaseFee=new(big.Int)}
	evm:=vm.NewEVM(bc,s,c.config,vm.Config{NoBaseFee:true});defer evm.Release()
	r,e:=core.ApplyMessage(evm,&m,nil);if e!=nil{return nil,e};if e=s.Error();e!=nil{return nil,e}
	rules:=c.config.Rules(bc.BlockNumber,bc.Random!=nil,bc.Time)
	row:=&gasFixtureReplay{GasLimit:limit,Status:1,ExecutionError:gasFixtureError(r.Err),ReturnData:gasFixtureHexOut(r.ReturnData),UsedGas:r.UsedGas,MaxUsedGas:r.MaxUsedGas,RefundCounter:s.GetRefund()}
	if r.Failed(){row.Status=0}
	row.FullLogs=s.GetLogs(txHash,c.current.Number.Uint64(),c.current.Hash(),c.current.Time)
	row.Logs=make([]gasFixtureLog,0,len(row.FullLogs));for _,l:=range row.FullLogs{topics:=make([]string,len(l.Topics));for i,t:=range l.Topics{topics[i]=strings.ToLower(t.Hex())};row.Logs=append(row.Logs,gasFixtureLog{Address:strings.ToLower(l.Address.Hex()),Topics:topics,Data:gasFixtureHexOut(l.Data)})}
	if m.To==nil&&!r.Failed(){a:=strings.ToLower(crypto.CreateAddress(m.From,m.Nonce).Hex());row.CreatedAddress=&a}
	row.FeeSettledStateRoot=s.IntermediateRoot(rules);if e=s.Error();e!=nil{return nil,e};return row,nil
}

func gasFixtureEqual(a,b *gasFixtureReplay) bool {
	if a==nil||b==nil{return false}
	// GasLimit differs across executions. Every result, effect, and accounting
	// field below must match, including Geth's secure-key state root.
	return a.Status==b.Status&&a.ExecutionError==b.ExecutionError&&a.ReturnData==b.ReturnData&&a.UsedGas==b.UsedGas&&a.MaxUsedGas==b.MaxUsedGas&&a.RefundCounter==b.RefundCounter&&a.FeeSettledStateRoot==b.FeeSettledStateRoot&&reflect.DeepEqual(a.Logs,b.Logs)&&reflect.DeepEqual(a.FullLogs,b.FullLogs)&&reflect.DeepEqual(a.CreatedAddress,b.CreatedAddress)
}
