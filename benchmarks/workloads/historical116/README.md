# Historical Pair corpus and policy

`corpus.json` preserves the 116 recorded raw signed sends in execution order, their case/phase markers, expected status, observed outcome and exact input/result file identities. `FILES.expected.json` indexes the complete captured payload: 116 input JSON files, 116 result JSON files and one event stream, totaling 11,691,660 bytes. Extract `historical116-inputs.tar.gz` to a directory of your choice and set `HISTORICAL_INPUTS` to its extracted root containing `transactions/` and `events.jsonl`. Pass that directory through `--inputs` as shown below. Each input JSON contains the complete enumerated account/storage prestate, signed message and original Istanbul block/BLOCKHASH context. Its matching result JSON contains the original VM outcome and logs.

The upstream 19 Pair tests use the published Uniswap V2 source revision recorded in `../README.md`. Ganache Core `2.10.2`, Waffle `2.4.1`, ethers `4.0.46` and Node `v12.22.12` produced this capture with fixture reuse. It contains four fixture sends and 112 body sends. Eleven body sends must revert with `UniswapV2: K`, while the other 105 sends succeed. Fixture reuse produces a different population from the service workload's 188 fresh-workflow estimates.

The policy comparison ports Ganache `v7.9.2` commit `547c900a50d19b094ef636a9aeccf4f7f2356430` onto the same Geth EVM used for S and C. Its upstream source is `packages/ethereum/ethereum/src/helpers/gas-estimator.ts`. `geth/eth/gasestimator/ganache_control_test.go` contains the exact policy port and its three source/boundary tests. It can be added to the prototype's Geth test package and checked with:

```sh
go test -mod=readonly ./eth/gasestimator \
  -run '^TestGanacheControl(BNAndSearch|SourceOpaqueReducer|SourcePreHookBoundaries)$' -count=1
```

The recorded historical timings use an earlier runtime than the current prototype. `historical-runtime.patch` contains the recorded checker, estimator and two production hook source files. `SOURCE_VARIANT.json` identifies all four historical source hashes and the original reference/result files. Build this stage with Go 1.25.3 in a fresh checkout at the Geth commit below, separate from the prototype/service builds.

```sh
git clone https://github.com/ethereum/go-ethereum.git geth-historical116
cd geth-historical116
git checkout --detach a579077007b98217c3e253a66e4b452ca0c32b96
git -c core.autocrlf=false apply --check "$ARTIFACT/benchmarks/workloads/historical116/historical-runtime.patch"
git -c core.autocrlf=false apply "$ARTIFACT/benchmarks/workloads/historical116/historical-runtime.patch"
cp -R "$ARTIFACT/benchmarks/workloads/historical116/geth/." .
cp "$ARTIFACT/benchmarks/workloads/historical116/runtime-tests/"*.go eth/gasestimator/
go test -c -mod=readonly -o "$BENCH/historical116.test" ./eth/gasestimator
```

`runtime-tests/` supplies the original focused checker tests and the shared native helper extraction for this independent checkout. The importer omits those helper declarations, so each symbol has one definition. The policy implementation preserves the recorded historical policy's execution code. The corpus importer, context reconstruction, signed-message interpretation, complete state import, estimator calls and fresh-state replay checks remain the original functions. The portability adapter changes input paths and reference-manifest controls.

After extracting the optional input attachment to `HISTORICAL_INPUTS` so that it contains `transactions/` and `events.jsonl`, run both complete qualification stages and the original measurement schedule:

```sh
python3 "$ARTIFACT/benchmarks/workloads/historical116/run.py" \
  --binary "$BENCH/historical116.test" --inputs "$HISTORICAL_INPUTS" \
  --phase all --cpu "$CPU" --out "$BENCH/historical116-run"
```

All variables above are absolute paths except `CPU`, which is one available CPU number. The output directory must be fresh. The runner verifies all 233 captured file identities, writes an absolute-path materialization manifest, pins each native child to that CPU and sets `GOMAXPROCS=1` with default GC/debug settings. It uses a 600-second default timeout per process, records full stdout/stderr, and reaps each direct child with `wait4`. Errors retain complete generated output and return nonzero.

The first stage, `stock-certificate/`, imports every original prestate and reconstructs the original legacy block/BLOCKHASH context. It checks signed messages, original Ganache outcomes, S/C estimates and fresh-state returned-budget/high/candidate executions for all 116 sends. Its full reference rows are generated from these inputs. The second stage, `three-method/`, re-executes the complete stock/certificate helper and requires equality with every first-stage row, then runs the Ganache policy port and checks all accepted budgets for sufficiency and every expected rejection. Complete summaries, full rows and all input hashes form a self-contained reference chain bound to the same test-binary identity. This chain replaces the author-specific release, retirement and process-receipt dependencies.

`--phase qualify` runs the two functional qualification stages. The timed measurement phase uses the command label `--phase paid` and can reuse the unchanged content-bound reference chain:

```sh
python3 "$ARTIFACT/benchmarks/workloads/historical116/run.py" \
  --binary "$BENCH/historical116.test" --phase paid --cpu "$CPU" \
  --qualification "$BENCH/historical116-run/three-method/summary.json" \
  --out "$BENCH/historical116-paid"
```

`paid/` preserves the original 39-block schedule: three warmup method blocks with one whole-cohort pass each, followed by 12 triples with eight passes per method block and all six method orders repeated twice. It records 33,756 total requests, including 11,136 measured requests per method. The per-request timer includes message, outer state-copy and Options preparation through the estimator return. Functional comparisons, grouping and JSON serialization occur outside that timer, with whole method blocks buffering output before serialization. All inputs, expected rejections, fallback rows and adverse observations remain. The original `fixed_gate_passed` field is a historical diagnostic. Successful completion requires correctness and complete data, irrespective of measured speedup.

`run.json` indexes successful stage summaries. Each stage has a manifest, full logs and a `*.process.json` with exit status, binary/input preservation and whole-process resource usage. Native `summary.json` indexes full qualification rows or measured `all-requests.jsonl` and `blocks.jsonl`. The reference chain is generated under new path and binary identities, while recorded paper timings remain associated with the original run in `SOURCE_VARIANT.json`.

The recorded production-source hashes and measured-run identities are listed in `SOURCE_VARIANT.json`. The [validation summary](../../../VALIDATION.md) describes the checks completed for this package.
