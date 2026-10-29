# Controlled fixture

This additive Geth test uses the prototype's native helper definitions. Copy the overlay into the same pinned Geth checkout after applying the prototype patch, then build the test executable:

```sh
cp -R "$ARTIFACT/benchmarks/workloads/coverage/geth/." "$GETH/"
cd "$GETH"
go test -c -mod=readonly -o "$BENCH/benign-coverage.test" ./eth/gasestimator
```

`ARTIFACT`, `GETH` and `BENCH` are absolute paths to this repository, the patched Geth checkout and an existing writable benchmark directory. Use the specified Go toolchain documented at the repository entry point. The overlay uses the fixture helpers from the prototype patch.

Provide Node `v12.22.12` and a local copy of the exact compiler identified by `../controlled/COMPILER_PIN.json`. The materializer checks the compiler SHA256, compiler version, Node version and original ERC20 artifact identity. It uses the recorded Solidity source bytes, optimizer 999999 and Istanbul target.

```sh
python3 "$ARTIFACT/benchmarks/workloads/controlled/materialize.py" \
  --node "$NODE12" --soljson "$SOLJSON" --out "$BENCH/compiled"
python3 "$ARTIFACT/benchmarks/workloads/coverage/run.py" \
  --binary "$BENCH/benign-coverage.test" --artifacts "$BENCH/compiled/artifacts.json" \
  --phase qualify --cpu "$CPU" --out "$BENCH/qualification"
python3 "$ARTIFACT/benchmarks/workloads/coverage/run.py" \
  --binary "$BENCH/benign-coverage.test" --artifacts "$BENCH/compiled/artifacts.json" \
  --phase paid --method C --cpu "$CPU" \
  --qualification "$BENCH/qualification/native/qualification.json" --out "$BENCH/paid-C"
```

`NODE12` is the Node executable, `SOLJSON` the compiler JS file, and `CPU` one CPU available to the caller. Output directories must be fresh. The runner pins one direct child to that CPU, sets `GOMAXPROCS=1`, uses default GC/debug settings, enforces a 300-second default timeout, reaps the child with `wait4`, and writes process usage and complete logs. A failed run retains its output and exits nonzero. The runner constructs `GETH_BENIGN_COVERAGE_MANIFEST` and `GAS_CERTIFICATE_CPU`, which are also required when invoking the test executable directly.

Qualification deploys the token, batch and three forwarders and checks all 27 configurations under S/C/V, including settled state, logs, returned budgets and replay equivalence. One measurement invocation runs the fixed 64-block sequence: 27 configurations with 4 warmup/64 measured repeats and five 72-input mixtures with 1 warmup/4 measured repeats. It records 468 warmup and 3,168 measured estimates. Diagnostic callbacks stay outside the timed loop, which uses default GC. This example runs one process, while the paper uses six independently ordered process triples.

`native/block-*.json` records each block's inputs, CPU/wall boundaries, allocation/GC snapshots and response observations. `native/paid.json` indexes all blocks, `native/qualification.json` contains all qualification rows, and `native/receipt.json` records completion/input preservation. `process.json` contains whole-process child usage including startup, fixture setup and exit. Native block measurements and whole-process usage have separate boundaries.

The adapter accepts canonical absolute input paths and an explicit CPU selection while preserving fixture construction, the recorded schedule, qualification checks, estimation calls, block loops and accounting. See the [validation summary](../../../VALIDATION.md) for the checked configurations.
