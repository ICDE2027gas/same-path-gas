// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: LGPL-3.0-or-later

package gasestimator

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/vm"
)

type certificateReplayObservation struct {
	gas uint64
	revert []byte
	err error
	report *CertificateReport
	trials []TrialReport
}

// Functional replay tests use immutable fixture state and the native execute
// implementation for every Estimate call. The paid RPC path keeps OnTrial nil.
func certificateReplayEstimate(t *testing.T, c *gasFixtureChain, base *state.StateDB, original *core.Message, certificate, replay bool) certificateReplayObservation {
	t.Helper()
	out := certificateReplayObservation{trials: make([]TrialReport, 0)}
	if certificate { out.report = new(CertificateReport) }
	opts := &Options{Config:c.config, Chain:c, Header:c.current, State:base.Copy(), ErrorRatio:.015,
		Certificate:out.report, CertificateReplay:replay,
		OnTrial:func(trial TrialReport) { out.trials = append(out.trials, trial) }}
	m := *original
	out.gas, out.revert, out.err = Estimate(context.Background(), &m, opts, 0)
	if m.GasLimit != original.GasLimit { t.Fatal("native execute did not restore message gas") }
	return out
}

func certificateReplaySameOutput(t *testing.T, c, v certificateReplayObservation) {
	t.Helper()
	if c.gas != v.gas || !bytes.Equal(c.revert, v.revert) || (c.err == nil) != (v.err == nil) {
		t.Fatalf("C/V outputs differ: C=%+v V=%+v", c, v)
	}
	if c.err != nil && c.err.Error() != v.err.Error() { t.Fatalf("C/V errors differ: %v / %v", c.err, v.err) }
	if !reflect.DeepEqual(c.report, v.report) { t.Fatalf("same checker produced different report: C=%+v V=%+v", c.report, v.report) }
}

func TestCertificateReplayAblation(t *testing.T) {
	t.Run("certified_state_effect_and_return", func(t *testing.T) {
		code := append([]byte{byte(vm.PUSH1),1,byte(vm.PUSH1),0,byte(vm.SSTORE)}, certificateOsakaReturnLog()...)
		chain, base := certificateOsakaSetup(t, certificateOsakaAccount{address:certificateOsakaRoot, code:code})
		m := certificateOsakaMessage(certificateOsakaRoot, nil)
		beforeBalance := base.GetBalance(m.From).Clone()
		beforeNonce := base.GetNonce(m.From)
		c := certificateReplayEstimate(t, chain, base, m, true, false)
		v := certificateReplayEstimate(t, chain, base, m, true, true)
		certificateReplaySameOutput(t, c, v)
		if c.err != nil || !c.report.Certified || c.report.Status != "certified" || c.gas != c.report.Candidate || c.gas >= m.GasLimit {
			t.Fatalf("fixture did not admit a lower candidate: %+v report=%+v", c, c.report)
		}
		wantC := []TrialReport{{GasLimit:m.GasLimit, Outcome:"success"}}
		wantV := append(append([]TrialReport{}, wantC...), TrialReport{GasLimit:c.gas, Outcome:"success"})
		if !reflect.DeepEqual(c.trials, wantC) || !reflect.DeepEqual(v.trials, wantV) {
			t.Fatalf("expected exactly [H] versus [H,G*]: C=%+v V=%+v", c.trials, v.trials)
		}
		// Replay has actual effects/return bytes on its private state, while the
		// fixture state supplied to each estimate remains unchanged.
		high, err := gasFixtureExecute(chain, base, m, common.HexToHash("0x1827"), m.GasLimit)
		if err != nil { t.Fatal(err) }
		candidate, err := gasFixtureExecute(chain, base, m, common.HexToHash("0x1827"), c.gas)
		if err != nil || !gasFixtureEqual(high, candidate) { t.Fatalf("candidate full effects differ: %v high=%+v candidate=%+v", err, high, candidate) }
		if base.GetBalance(m.From).Cmp(beforeBalance) != 0 || base.GetNonce(m.From) != beforeNonce || base.GetState(certificateOsakaRoot, common.Hash{}) != (common.Hash{}) || !bytes.Equal(base.GetCode(certificateOsakaRoot), code) {
			t.Fatal("estimation mutated the supplied base state")
		}
	})
	t.Run("certified_candidate_equals_high_still_replays", func(t *testing.T) {
		chain, base := certificateOsakaSetup(t, certificateOsakaAccount{address:certificateOsakaRoot, code:[]byte{byte(vm.STOP)}})
		m := certificateOsakaMessage(certificateOsakaRoot, []byte{1})
		m.GasLimit = 21040 // Osaka floor for one nonzero calldata byte.
		c := certificateReplayEstimate(t, chain, base, m, true, false)
		v := certificateReplayEstimate(t, chain, base, m, true, true)
		certificateReplaySameOutput(t, c, v)
		if c.err != nil || !c.report.Certified || c.gas != m.GasLimit { t.Fatalf("equal-high fixture not admitted: %+v %+v", c, c.report) }
		one := TrialReport{GasLimit:m.GasLimit, Outcome:"success"}
		if !reflect.DeepEqual(c.trials, []TrialReport{one}) || !reflect.DeepEqual(v.trials, []TrialReport{one, one}) { t.Fatalf("equal-high candidate must execute again: C=%+v V=%+v", c.trials, v.trials) }
	})
	t.Run("unsupported_GAS_fallback_unchanged", func(t *testing.T) {
		// This benign functional fixture observes and discards remaining gas.
		// It exercises the unsupported-program fallback outside performance measurement.
		chain, base := certificateOsakaSetup(t, certificateOsakaAccount{address:certificateOsakaRoot, code:[]byte{byte(vm.GAS),byte(vm.POP),byte(vm.STOP)}})
		m := certificateOsakaMessage(certificateOsakaRoot, nil)
		stock := certificateReplayEstimate(t, chain, base, m, false, false)
		c := certificateReplayEstimate(t, chain, base, m, true, false)
		v := certificateReplayEstimate(t, chain, base, m, true, true)
		certificateReplaySameOutput(t, c, v)
		if c.err != nil || c.report.Certified || c.report.Status != "unsupported" || c.report.ReasonCounts["gas_not_immediate_call"] != 1 || len(c.trials) <= 1 || c.gas != stock.gas || !reflect.DeepEqual(c.trials, stock.trials) || !reflect.DeepEqual(c.trials, v.trials) { t.Fatalf("unsupported fallback changed: stock=%+v C=%+v V=%+v report=%+v", stock, c, v, c.report) }
	})
	t.Run("initial_high_revert_unchanged", func(t *testing.T) {
		code := []byte{byte(vm.PUSH1),42,byte(vm.PUSH1),0,byte(vm.MSTORE),byte(vm.PUSH1),32,byte(vm.PUSH1),0,byte(vm.REVERT)}
		chain, base := certificateOsakaSetup(t, certificateOsakaAccount{address:certificateOsakaRoot, code:code})
		m := certificateOsakaMessage(certificateOsakaRoot, nil)
		c := certificateReplayEstimate(t, chain, base, m, true, false)
		v := certificateReplayEstimate(t, chain, base, m, true, true)
		certificateReplaySameOutput(t, c, v)
		want := []TrialReport{{GasLimit:m.GasLimit, Failed:true, Outcome:"execution_failed"}}
		if !errors.Is(c.err, vm.ErrExecutionReverted) || c.gas != 0 || len(c.revert) != 32 || c.revert[31] != 42 || c.report.Status != "first_run_failed" || !reflect.DeepEqual(c.trials, want) || !reflect.DeepEqual(v.trials, want) { t.Fatalf("high failure changed: C=%+v V=%+v report=%+v", c, v, c.report) }
	})
	t.Run("transfer_shortcut_unchanged", func(t *testing.T) {
		chain, base := certificateOsakaSetup(t, certificateOsakaAccount{address:certificateOsakaRoot})
		m := certificateOsakaMessage(certificateOsakaRoot, nil)
		c := certificateReplayEstimate(t, chain, base, m, true, false)
		v := certificateReplayEstimate(t, chain, base, m, true, true)
		certificateReplaySameOutput(t, c, v)
		want := []TrialReport{{GasLimit:21000, Outcome:"success"}}
		if c.err != nil || c.report.Status != "stock_transfer_fastpath" || c.report.Certified || c.gas != 21000 || !reflect.DeepEqual(c.trials, want) || !reflect.DeepEqual(v.trials, want) { t.Fatalf("transfer shortcut changed: C=%+v V=%+v report=%+v", c, v, c.report) }
	})
	t.Run("nil_certificate_replay_option_ignored", func(t *testing.T) {
		chain, base := certificateOsakaSetup(t, certificateOsakaAccount{address:certificateOsakaRoot, code:certificateOsakaReturnLog()})
		m := certificateOsakaMessage(certificateOsakaRoot, nil)
		c := certificateReplayEstimate(t, chain, base, m, false, false)
		v := certificateReplayEstimate(t, chain, base, m, false, true)
		certificateReplaySameOutput(t, c, v)
		if c.err != nil || c.report != nil || len(c.trials) <= 1 || !reflect.DeepEqual(c.trials, v.trials) { t.Fatalf("nil-certificate stock path changed: C=%+v V=%+v", c, v) }
	})
	t.Run("before_first_execution_allowance_rejection_unchanged", func(t *testing.T) {
		chain, base := certificateOsakaSetup(t, certificateOsakaAccount{address:certificateOsakaRoot, code:[]byte{byte(vm.STOP)}})
		m := certificateOsakaMessage(certificateOsakaRoot, nil)
		m.Value = base.GetBalance(m.From).Clone()
		c := certificateReplayEstimate(t, chain, base, m, true, false)
		v := certificateReplayEstimate(t, chain, base, m, true, true)
		certificateReplaySameOutput(t, c, v)
		if !errors.Is(c.err, core.ErrInsufficientFundsForTransfer) || c.gas != 0 || c.report.Status != "not_reached" || len(c.trials) != 0 || len(v.trials) != 0 { t.Fatalf("pre-execution allowance rejection changed: C=%+v V=%+v report=%+v", c, v, c.report) }
	})
}
