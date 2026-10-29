// Copyright 2026 Research prototype contributors
// SPDX-License-Identifier: GPL-3.0-or-later

// gas-certificate estimates gas for an explicit, self-contained EVM context.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/eth/gasestimator"
)

const sourcePin = "a579077007b98217c3e253a66e4b452ca0c32b96"

type estimateOutput struct {
	Schema       string                          `json:"schema"`
	SourcePin    string                          `json:"source_pin"`
	Mode         string                          `json:"mode"`
	Fork         string                          `json:"fork"`
	PrestateRoot common.Hash                     `json:"prestate_root"`
	EstimatedGas uint64                          `json:"estimated_gas"`
	Certificate  *gasestimator.CertificateReport `json:"certificate,omitempty"`
	Trials       []gasestimator.TrialReport      `json:"trials"`
	RevertData   hexutil.Bytes                   `json:"revert_data,omitempty"`
	Error        string                          `json:"error,omitempty"`
}

func estimate(ctx context.Context, in *contextInput, mode string) (*estimateOutput, error) {
	if mode != "certificate" && mode != "stock" && mode != "replay" {
		return nil, fmt.Errorf("mode must be certificate, stock, or replay")
	}
	prepared, err := prepare(in)
	if err != nil {
		return nil, err
	}
	defer prepared.close()
	out := &estimateOutput{
		Schema: "gas-certificate-result-v1", SourcePin: sourcePin,
		Mode: mode, Fork: in.Fork, PrestateRoot: prepared.root,
		Trials: make([]gasestimator.TrialReport, 0),
	}
	if mode != "stock" {
		out.Certificate = new(gasestimator.CertificateReport)
	}
	opts := &gasestimator.Options{
		Config: prepared.chain.config, Chain: prepared.chain,
		Header: prepared.chain.current, State: prepared.state,
		ErrorRatio: 0.015, Certificate: out.Certificate,
		CertificateReplay: mode == "replay",
		OnTrial: func(trial gasestimator.TrialReport) {
			out.Trials = append(out.Trials, trial)
		},
	}
	out.EstimatedGas, out.RevertData, err = gasestimator.Estimate(ctx, prepared.message, opts, 0)
	if prepared.chain.missing != nil {
		return nil, prepared.chain.missing
	}
	if err != nil {
		out.Error = err.Error()
	}
	return out, nil
}

func decodeInput(r io.Reader) (*contextInput, error) {
	const maxInput = 64 << 20
	raw, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxInput {
		return nil, fmt.Errorf("input exceeds 64 MiB")
	}
	return decodeContext(raw)
}

func run() int {
	inputPath := flag.String("input", "-", "context JSON file, or - for stdin")
	mode := flag.String("mode", "certificate", "certificate, stock, or replay")
	timeout := flag.Duration("timeout", 10*time.Second, "estimation timeout")
	flag.Parse()
	if flag.NArg() != 0 || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "usage: gas-certificate -input CONTEXT.json [-mode certificate|stock|replay] [-timeout 10s]")
		return 2
	}
	var reader io.Reader = os.Stdin
	if *inputPath != "-" {
		file, err := os.Open(*inputPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		defer file.Close()
		reader = file
	}
	in, err := decodeInput(reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	out, err := estimate(ctx, in, *mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if out.Error != "" {
		return 1
	}
	return 0
}

func main() {
	os.Exit(run())
}
