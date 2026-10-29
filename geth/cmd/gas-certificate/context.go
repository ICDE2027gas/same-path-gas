// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

type contextInput struct {
	Schema    string                          `json:"schema"`
	Fork      string                          `json:"fork"`
	ChainID   *hexutil.Big                    `json:"chain_id"`
	Block     blockInput                      `json:"block"`
	Request   requestInput                    `json:"request"`
	Alloc     map[common.Address]accountInput `json:"alloc"`
	Ancestors []*types.Header                 `json:"ancestors,omitempty"`
}

type blockInput struct {
	Number       *hexutil.Uint64 `json:"number"`
	Timestamp    *hexutil.Uint64 `json:"timestamp"`
	GasLimit     *hexutil.Uint64 `json:"gas_limit"`
	BaseFee      *hexutil.Big    `json:"base_fee"`
	FeeRecipient *common.Address `json:"fee_recipient"`
	PrevRandao   *common.Hash    `json:"prev_randao"`
	ParentHash   *common.Hash    `json:"parent_hash"`
}

// GasPrice models a legacy transaction: fee cap and tip cap both equal the
// effective price. The nonce is checked against the supplied state.
type requestInput struct {
	From       *common.Address  `json:"from"`
	To         *common.Address  `json:"to"`
	Nonce      *hexutil.Uint64  `json:"nonce"`
	Gas        *hexutil.Uint64  `json:"gas"`
	GasPrice   *hexutil.Big     `json:"gas_price"`
	Value      *hexutil.Big     `json:"value"`
	Data       hexutil.Bytes    `json:"data"`
	AccessList types.AccessList `json:"access_list,omitempty"`
}

type accountInput struct {
	Nonce   hexutil.Uint64              `json:"nonce"`
	Balance *hexutil.Big                `json:"balance"`
	Code    hexutil.Bytes               `json:"code"`
	Storage map[common.Hash]common.Hash `json:"storage,omitempty"`
}

func decodeContext(raw []byte) (*contextInput, error) {
	var in contextInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return nil, fmt.Errorf("context JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("context JSON must contain exactly one object")
	}
	return &in, nil
}

// The standalone EVM context uses the engine to read the declared fee recipient.
// Consensus block import, validation, and finalization are outside this context.
type contextEngine struct{ consensus.Engine }

func (contextEngine) Author(h *types.Header) (common.Address, error) {
	return h.Coinbase, nil
}

type contextChain struct {
	config  *params.ChainConfig
	current *types.Header
	headers map[common.Hash]*types.Header
	missing error
}

var _ core.ChainContext = (*contextChain)(nil)

func (c *contextChain) Config() *params.ChainConfig  { return c.config }
func (c *contextChain) CurrentHeader() *types.Header { return c.current }
func (c *contextChain) Engine() consensus.Engine     { return contextEngine{} }
func (c *contextChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	header := c.GetHeaderByHash(hash)
	if header != nil && header.Number.Uint64() == number {
		return header
	}
	if c.missing == nil {
		c.missing = fmt.Errorf("BLOCKHASH needs ancestor %d (%s); supply connected full headers in ancestors", number, hash)
	}
	return nil
}

func (c *contextChain) GetHeaderByNumber(number uint64) *types.Header {
	if c.current.Number.Uint64() == number {
		return c.current
	}
	for _, header := range c.headers {
		if header.Number.Uint64() == number {
			return header
		}
	}
	return nil
}

func (c *contextChain) GetHeaderByHash(hash common.Hash) *types.Header {
	if c.current.Hash() == hash {
		return c.current
	}
	return c.headers[hash]
}

type preparedContext struct {
	chain   *contextChain
	state   *state.StateDB
	message *core.Message
	root    common.Hash
	close   func()
}

func asUint256(value *hexutil.Big, field string) (*uint256.Int, error) {
	if value == nil {
		return nil, fmt.Errorf("%s is required", field)
	}
	number := (*big.Int)(value)
	if number.Sign() < 0 || number.BitLen() > 256 {
		return nil, fmt.Errorf("%s must fit an unsigned 256-bit integer", field)
	}
	return uint256.MustFromBig(number), nil
}

func prepare(in *contextInput) (*preparedContext, error) {
	if in.Schema != "gas-certificate-context-v1" || in.Fork != "osaka" {
		return nil, fmt.Errorf("require schema gas-certificate-context-v1 and fork osaka")
	}
	id, err := asUint256(in.ChainID, "chain_id")
	if err != nil || id.IsZero() {
		return nil, fmt.Errorf("chain_id must be a positive unsigned 256-bit integer")
	}
	b := &in.Block
	if b.Number == nil || b.Timestamp == nil || b.GasLimit == nil || b.BaseFee == nil || b.FeeRecipient == nil || b.PrevRandao == nil || b.ParentHash == nil {
		return nil, fmt.Errorf("block requires number, timestamp, gas_limit, base_fee, fee_recipient, prev_randao, and parent_hash")
	}
	baseFee, err := asUint256(b.BaseFee, "block.base_fee")
	if err != nil {
		return nil, err
	}
	if *b.GasLimit < hexutil.Uint64(params.TxGas) {
		return nil, fmt.Errorf("block.gas_limit must be at least %d", params.TxGas)
	}
	// Copy the pinned schedule so user contexts can select CHAINID without
	// changing the process-global mainnet configuration or fork activation.
	rawConfig, err := json.Marshal(params.MainnetChainConfig)
	if err != nil {
		return nil, err
	}
	var config params.ChainConfig
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return nil, err
	}
	config.ChainID = id.ToBig()
	zero := uint64(0)
	header := &types.Header{
		Number: new(big.Int).SetUint64(uint64(*b.Number)), Time: uint64(*b.Timestamp),
		GasLimit: uint64(*b.GasLimit), BaseFee: baseFee.ToBig(),
		Coinbase: *b.FeeRecipient, MixDigest: *b.PrevRandao, ParentHash: *b.ParentHash,
		Difficulty: new(big.Int), BlobGasUsed: &zero, ExcessBlobGas: &zero,
	}
	rules := config.Rules(header.Number, true, header.Time)
	if !rules.IsOsaka || !rules.IsPrague || !rules.IsCancun || !rules.IsShanghai || !rules.IsMerge || !rules.IsLondon || !rules.IsBerlin || rules.IsAmsterdam || rules.IsBogota || rules.IsUBT || rules.IsEIP4762 {
		return nil, fmt.Errorf("block number/time must select Osaka before later forks in the pinned mainnet schedule")
	}
	chain := &contextChain{config: &config, current: header, headers: make(map[common.Hash]*types.Header)}
	if len(in.Ancestors) > 256 || uint64(len(in.Ancestors)) > header.Number.Uint64() {
		return nil, fmt.Errorf("ancestors must contain at most the preceding 256 full headers")
	}
	expectedHash := header.ParentHash
	for i, ancestor := range in.Ancestors {
		expectedNumber := header.Number.Uint64() - 1 - uint64(i)
		if ancestor == nil || ancestor.Number == nil || !ancestor.Number.IsUint64() || ancestor.Number.Uint64() != expectedNumber || ancestor.Hash() != expectedHash {
			return nil, fmt.Errorf("ancestors[%d] does not connect to block.parent_hash in descending number order", i)
		}
		chain.headers[expectedHash] = ancestor
		expectedHash = ancestor.ParentHash
	}
	r := &in.Request
	if r.From == nil || r.Nonce == nil || r.Gas == nil || *r.Gas < hexutil.Uint64(params.TxGas) {
		return nil, fmt.Errorf("request requires from, nonce, and gas >= %d", params.TxGas)
	}
	price, err := asUint256(r.GasPrice, "request.gas_price")
	if err != nil {
		return nil, err
	}
	value, err := asUint256(r.Value, "request.value")
	if err != nil {
		return nil, err
	}
	if _, ok := in.Alloc[*r.From]; !ok {
		return nil, fmt.Errorf("alloc must include the sender")
	}
	message := &core.Message{
		From: *r.From, To: r.To, Nonce: uint64(*r.Nonce), GasLimit: uint64(*r.Gas),
		GasPrice: price, GasFeeCap: price.Clone(), GasTipCap: price.Clone(),
		Value: value, Data: r.Data, AccessList: r.AccessList,
	}
	db := state.NewDatabaseForTesting()
	closeDB := func() { db.TrieDB().Close() }
	s, err := state.New(types.EmptyRootHash, db)
	if err != nil {
		closeDB()
		return nil, err
	}
	addresses := make([]common.Address, 0, len(in.Alloc))
	for address := range in.Alloc {
		addresses = append(addresses, address)
	}
	sort.Slice(addresses, func(i, j int) bool { return bytes.Compare(addresses[i][:], addresses[j][:]) < 0 })
	for _, address := range addresses {
		account := in.Alloc[address]
		balance, err := asUint256(account.Balance, "alloc balance")
		if err != nil {
			closeDB()
			return nil, fmt.Errorf("%s: %w", address, err)
		}
		s.CreateAccount(address)
		s.SetBalance(address, balance, tracing.BalanceChangeUnspecified)
		s.SetNonce(address, uint64(account.Nonce), tracing.NonceChangeUnspecified)
		s.SetCode(address, account.Code, tracing.CodeChangeUnspecified)
		for key, value := range account.Storage {
			s.SetState(address, key, value)
		}
	}
	// Commit and reopen imported slots as the original state for SSTORE and
	// refund accounting in the transaction being estimated.
	root, err := s.Commit(params.Rules{}, 0)
	if err == nil {
		err = db.TrieDB().Commit(root, false)
	}
	if err == nil {
		s, err = state.New(root, db)
	}
	if err != nil {
		closeDB()
		return nil, err
	}
	s.SetTxContext(common.Hash{}, 0, 0)
	return &preparedContext{chain: chain, state: s, message: message, root: root, close: closeDB}, nil
}
