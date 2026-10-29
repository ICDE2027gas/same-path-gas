// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

const (
	testSender = "0x0000000000000000000000000000000000010001"
	testRoot   = "0x0000000000000000000000000000000000010002"
	testChild  = "0x0000000000000000000000000000000000010003"
)

func testContext(t *testing.T, code, extraAlloc, accessList string) *contextInput {
	t.Helper()
	raw := fmt.Sprintf(`{
		"schema":"gas-certificate-context-v1", "fork":"osaka", "chain_id":"0x1",
		"block":{"number":"0x16e3600", "timestamp":"0x6ad95200", "gas_limit":"0x1c9c380",
			"base_fee":"0x1", "fee_recipient":"0x0000000000000000000000000000000000c0ffee",
			"prev_randao":"%s", "parent_hash":"%s"},
		"request":{"from":"%s", "to":"%s", "nonce":"0x0", "gas":"0x30d40",
			"gas_price":"0x3", "value":"0x0", "data":"0x"%s},
		"alloc":{"%s":{"nonce":"0x0", "balance":"0xde0b6b3a7640000", "code":"0x"},
			"%s":{"nonce":"0x0", "balance":"0x2710", "code":"%s"}%s}
	}`, common.HexToHash("0x1234"), common.Hash{}, testSender, testRoot, accessList, testSender, testRoot, code, extraAlloc)
	in, err := decodeContext([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestPortableCertificateSstore(t *testing.T) {
	// Set slot 0 to 1, emit one 32-byte log, and return the word 42.
	in := testContext(t, "0x6001600055602a60005260206000a060206000f3", "", "")
	out, err := estimate(context.Background(), in, "certificate")
	if err != nil {
		t.Fatal(err)
	}
	if out.Error != "" || out.Certificate == nil || !out.Certificate.Certified || out.Certificate.Sstores != 1 || out.EstimatedGas != 43761 || len(out.Trials) != 1 {
		t.Fatalf("SSTORE input/output: %+v certificate=%+v", out, out.Certificate)
	}
	// The replay control adds one candidate execution after the high-limit check.
	replay, err := estimate(context.Background(), in, "replay")
	if err != nil || replay.Error != "" || replay.EstimatedGas != out.EstimatedGas || !reflect.DeepEqual(replay.Certificate, out.Certificate) || len(replay.Trials) != 2 || replay.Trials[1].GasLimit != out.EstimatedGas {
		t.Fatalf("replay control: %+v error=%v", replay, err)
	}
}

func TestPortableCertificateNestedStipend(t *testing.T) {
	parent := "0x6020600060006000600173" + strings.TrimPrefix(testChild, "0x") + "5af15060206000f3"
	extra := fmt.Sprintf(`,"%s":{"nonce":"0x0","balance":"0x0","code":"0x6000600055602a60005260206000f3"}`, testChild)
	access := fmt.Sprintf(`,"access_list":[{"address":"%s","storageKeys":["%s"]}]`, testChild, common.Hash{})
	in := testContext(t, parent, extra, access)
	out, err := estimate(context.Background(), in, "certificate")
	if err != nil {
		t.Fatal(err)
	}
	r := out.Certificate
	if out.Error != "" || r == nil || !r.Certified || r.CompletedCalls != 1 || r.CompletedFrames != 2 || r.GasCapCalls != 1 || r.Sstores != 1 || r.Minimum <= r.Depletion || len(out.Trials) != 1 {
		t.Fatalf("nested stipend/sentry input/output: %+v certificate=%+v", out, r)
	}
}

func TestPortableCertificateGasFallback(t *testing.T) {
	// Observe/discard GAS, then stop. This ordinary program is outside the
	// admitted GAS-immediately-consumed-by-CALL pattern.
	in := testContext(t, "0x5a5000", "", "")
	out, err := estimate(context.Background(), in, "certificate")
	if err != nil {
		t.Fatal(err)
	}
	stock, err := estimate(context.Background(), in, "stock")
	if err != nil {
		t.Fatal(err)
	}
	r := out.Certificate
	if out.Error != "" || stock.Error != "" || r == nil || r.Certified || r.Status != "unsupported" || r.ReasonCounts["gas_not_immediate_call"] != 1 || out.EstimatedGas != stock.EstimatedGas || len(out.Trials) < 2 || !reflect.DeepEqual(out.Trials, stock.Trials) {
		t.Fatalf("ordinary fallback differs: certificate=%+v stock=%+v report=%+v", out, stock, r)
	}
}

func TestPortableContextValidation(t *testing.T) {
	for _, raw := range []string{`{"unknown":true}`, `{} {}`, `{"schema":42}`} {
		if _, err := decodeContext([]byte(raw)); err == nil {
			t.Fatalf("invalid JSON interface accepted: %s", raw)
		}
	}
	in := testContext(t, "0x00", "", "")
	*in.Block.Timestamp = 1
	if _, err := prepare(in); err == nil {
		t.Fatal("non-Osaka timestamp accepted")
	}
	in = testContext(t, "0x00", "", "")
	delete(in.Alloc, common.HexToAddress(testSender))
	if _, err := prepare(in); err == nil {
		t.Fatal("missing sender accepted")
	}
	// A BLOCKHASH reaching beyond the declared parent needs actual ancestor
	// headers. An incomplete context must not produce a successful estimate.
	in = testContext(t, "0x63016e35fe405000", "", "") // BLOCKHASH(23999998), POP, STOP.
	if _, err := estimate(context.Background(), in, "certificate"); err == nil || !strings.Contains(err.Error(), "BLOCKHASH needs ancestor") {
		t.Fatalf("missing BLOCKHASH context was silently accepted: %v", err)
	}
}
