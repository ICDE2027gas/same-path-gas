# Experiments and measurements

This guide describes the experiments for same-path gas certificates. For building and using the estimator, start with the [prototype guide](../README.md). The compact [result tables](results/) contain the measurements behind the reported CPU, throughput, latency, memory, ablation, sensitivity, and same-engine policy comparisons. Seven data archives are available in the [v0.1.0 release](https://github.com/ICDE2027gas/same-path-gas/releases/tag/v0.1.0). The [data guide](DATA.md) lists their exact names, sizes, hashes and contents.

## Choose an experiment

| Question | Experiment | Methods | Result tables |
| --- | --- | --- | --- |
| Does certification reduce the cost of serving complete workflows? | Loopback HTTP service, serial and four clients | P/S/C | `service-processes.csv`, `service-clients.csv` |
| What does avoiding candidate replay contribute? | Separate four-client HTTP service run | P/V/C | `service-processes.csv` with `experiment=replay` |
| Which requests get slower, and how do allocation and memory change? | All source positions and request classes from each service run | Each run's own methods | `http-positions.csv`, `http-populations-and-processes.csv`, `http-process-positions.csv` |
| How do request size, forwarding depth, fallback timing, and admission share affect cost? | Standalone Solidity suite | S/C/V | `sensitivity-*.csv` |
| How does the method compare with Ganache's policy in the same EVM? | Standalone estimator, Istanbul rules | S/C/Ganache | `ganache-rounds.csv`, `ganache-strata-and-positions.csv` |
| Which inputs certify, and are the returned budgets sufficient? | Untimed workflow and native validation | Corresponding methods | `workflow-qualification.csv`, `sensitivity-qualification.csv`, native tests in the prototype guide |

P is pristine Geth v1.17.8 with the common service launcher. S disables certificates in the research implementation. C enables them. V uses the same checker and candidate calculation as C, then performs one ordinary, untraced execution at the candidate, including when the candidate equals the high budget. Unsupported inputs follow the same search fallback in S/C/V. P and S therefore answer different comparisons. The historical table's `ganache` method is a port of Ganache v7.9.2's exactimation policy into the Geth EVM.

The main and serial P/S/C experiments use the preserved `accepted-service` source variant. The later replay experiment uses `current-service`, which adds the replay option. Sensitivity adds the standalone coverage fixture to that version. Match the source variant to the dataset when reproducing a historical result. [manifest.json](results/manifest.json) records experiment identities, upstream versions, summary hashes, table hashes and row counts. A P measurement in `main` and a P measurement in `replay` come from separate processes and must remain in their own experiment.

## Environment and fixed inputs

### Runtime requirements

The experiment scripts run on Linux and use the following tools and interfaces. The standalone Go command is described in the [prototype guide](../README.md).

| Component | Requirement |
| --- | --- |
| Operating system | Linux with readable `/proc` and CPU topology under `/sys/devices/system/cpu`. The tested system uses Ubuntu 22.04 on x86-64. |
| Python | CPython **3.10.12**, using the standard library. The runners require `os.wait4` and Linux CPU-affinity interfaces. |
| Process control | `taskset` from util-linux on `PATH`, permission to set affinity within the process's allowed CPU set, and local loopback sockets for RPC. |
| Builds | Git, Go **1.25.3**, and a POSIX shell for the commands below. |
| Controlled-contract compilation | Node **v12.22.12** and the Solidity **0.5.16** compiler module identified by `workloads/controlled/COMPILER_PIN.json`. |

Native fixtures use one available CPU. Serial RPC runs require two distinct physical cores. Four-client RPC runs require eight distinct physical cores on one socket and NUMA node, split between four server and four client CPUs. Use writable directories for output and state copies. The [validation summary](../VALIDATION.md) records the exercised configurations.

### Recorded environment and inputs

The measured system was a Dell PowerEdge R750xs with two Intel Xeon Gold 6342 processors, 48 physical cores, 251 GiB RAM, Ubuntu 22.04, and Go 1.25.3. HTTP clients connected over loopback. Servers had no peers and the chain head stayed fixed. Each server opened its own full copy of the same closed database for its workflow and performed normal historical indexing and readiness work. The main, serial, replay and sensitivity experiments use Osaka rules. The Ganache policy comparison uses Istanbul rules.

The Geth base is commit `a579077007b98217c3e253a66e4b452ca0c32b96` (v1.17.8). The workflow source is [Uniswap V2 core](https://github.com/Uniswap/v2-core/tree/4dd59067c76dea4a0e8e4bfdda41877a6b16dedc), package `@uniswap/v2-core@1.0.1`, commit `4dd59067c76dea4a0e8e4bfdda41877a6b16dedc`. Solidity is `0.5.16+commit.9c3226ce`, with 999,999 optimizer runs and an Istanbul compilation target. The service executes the resulting bytecode under Osaka rules.

The [workflow inputs](workloads/uniswap/) preserve the arguments and source order of every estimate request. E is the ERC20 workflow: 9 positions, including 2 estimator rejections. A is the Pair workflow: 188 positions from all 19 upstream Pair cases, including 57 creations, 120 successful noncreation calls and 11 estimator rejections. A uses a uniform automatic-gas wallet policy and fresh case workflows. A position is one source request entry. Repeating it many times produces measurement samples of that position.

Each recorded request selects its original canonical block by hash, giving all methods the same state and block context. Requests omit gas limits, have zero top-level value, and use recorded base fees and legacy gas prices of 2 gwei for calls and 9 gwei for creations. Search sets `ErrorRatio=0.015`, RPC gas cap 50,000,000 and EVM timeout 5 s. Osaka's transaction cap makes the initial limit 2²⁴ gas for these workflows. Reproduction requires both the context files, which provide block headers, and the state archive, which provides account storage and historical state.

Use a separate writable database copy for every fresh server process. Verify the workload and state archive hashes, validate the recorded canonical headers, complete historical-state readiness, and validate the entire request population before starting a measured schedule. Keep state construction, qualification and trial-list instrumentation outside performance measurements. The source-position identity is retained in [source-positions.csv](results/source-positions.csv).

## Harness interfaces

The runners provide separate functional qualification and timed measurement phases. Some commands and manifests call the measurement phase `paid`. Use that literal value where shown below.

### Run qualification or one measured unit

The [RPC launcher](workloads/rpc/run.py) verifies the unpacked state files against `STATES.expected.json`, copies data into a fresh workspace, materializes the context/population path bindings, creates manifests and starts the existing service controllers. It retains original request/header fields and uses a fresh writable database clone for every server. `--output` must name a new directory under an existing parent. The launcher retains all copies, manifests, output, raw responses and failure logs there.

After building the [RPC binaries](workloads/rpc/README.md) and unpacking the state archive, use a full, once-through functional qualification:

```sh
python3 benchmarks/workloads/rpc/run.py \
  --binary "$BIN/benchmark-rpc-current" --pristine-binary "$BIN/benchmark-rpc-pristine" \
  --state-dir "$DATA/workflow-state" --cohort E --variant C \
  --mode qualify --output "$RUNS/qualify-E-C"
```

`BIN`, `DATA` and `RUNS` are absolute directories. Create the parent `RUNS` directory beforehand. The state directory can be the unpacking root, `workflow-state`, or the cohort's `node` directory. For P, pass its pristine binary as `--binary` and omit `--pristine-binary`. For S/C/V, full qualification first starts a separate pristine process, then validates the requested variant against its returned bytes and errors. Each positive estimate is checked with `eth_call`. The complete qualification covers all 9 E or 188 A positions.

For a smaller packaging check, add `--smoke` in qualify mode. A pristine binary is optional for that subset because it checks the selected estimates/errors and call-result hashes against the recorded qualification tables:

```sh
python3 benchmarks/workloads/rpc/run.py \
  --binary "$BIN/benchmark-rpc-current" --state-dir "$DATA/workflow-state" \
  --cohort A --variant V --mode qualify --smoke \
  --output "$RUNS/smoke-A-V"
```

The smoke run checks E positions 0/2/8 or A positions 0/3/6/15, latest headers before and after the requests, every selected positive returned budget, normal server STOP, complete context/state readiness and actual process retirement. Its result is marked as a subset. Measured runs require full qualification of the entire population.

Each measured invocation performs separate full qualification first, then creates another fresh clone for one measured run using the unchanged serial/concurrent/replay controller. For example:

```sh
python3 benchmarks/workloads/rpc/run.py \
  --binary "$BIN/benchmark-rpc-accepted" --pristine-binary "$BIN/benchmark-rpc-pristine" \
  --state-dir "$DATA/workflow-state" --cohort A --variant C \
  --source-variant accepted-service --mode concurrent \
  --server-cpus 0,2,4,6 --client-cpus 8,10,12,14 \
  --output "$RUNS/concurrent-A-C"

python3 benchmarks/workloads/rpc/run.py \
  --binary "$BIN/benchmark-rpc-current" --pristine-binary "$BIN/benchmark-rpc-pristine" \
  --state-dir "$DATA/workflow-state" --cohort A --variant V \
  --source-variant current-service --mode replay \
  --server-cpus 0,2,4,6 --client-cpus 8,10,12,14 \
  --output "$RUNS/replay-A-V"
```

Select CPU IDs available on your machine. If CPU arguments are omitted, the launcher selects distinct available physical cores on one socket and NUMA node and records them. Qualify/serial use one server and one client core, while concurrent/replay use four of each. Serial mode accepts one integer for each CPU argument. The smoke run uses the controller for its requests and can use one available core. Explicit source-version arguments associate each measured population with its implementation: serial/concurrent allow P/S/C and require `accepted-service` for S/C, while replay allows P/V/C and requires `current-service` for C/V. P always uses the pristine build.

Each measured invocation produces one process observation. To repeat the balanced experiment, follow the method orders and independent-process counts below, using a fresh output directory for every invocation. Qualification and preparation occur outside each measured process boundary. `run.json` records stage completion and receipt identities, and `invocation.json` records the declared source variant, binary/input hashes and CPU selection. The launcher uses fresh output, opens writable state clones in Geth, preserves repetition constants and gives new runs their own identities alongside the recorded paper measurements.

Allow several minutes for each fresh server to report READY. With an old fixed chain head, this Geth revision's path-state history indexer can wait more than 5 minutes for its normal stalled-sync transition before indexing a small history backlog. Keep the default `--timeout 1800`. The server allows 10 minutes for state readiness. A non-pristine invocation starts pristine qualification, target qualification and, in a measured mode, the measurement server sequentially, so their startup waits accumulate. Keep the captured timestamps and normal indexing policy unchanged. Startup CPU stays within the documented whole-server boundary.

### Low-level interfaces

The RPC [server source](workloads/rpc/geth/cmd/benchmark-rpc/main.go) accepts a concrete manifest:

```sh
export GAS_CERTIFICATE_BENCH_ROOT=/absolute/benchmark-workspace
"$GAS_CERTIFICATE_BENCH_ROOT/bin/benchmark-rpc" \
  --manifest "$GAS_CERTIFICATE_BENCH_ROOT/inputs/server.json"
```

This is a server interface, requiring an owning controller with open stdin/stdout pipes. The manifest supplies `purpose` (`qualification` or `paid`), `cohort` (`E` or `A`), `variant`, the exact binary/context/population identities, clone receipt, source proofs, fresh output directory and distinct loopback HTTP/auth ports. Every file path must be canonical, absolute, inside the benchmark workspace and free of symlinks. Materialize the portable context's `original_node_directory` and the population's context reference for that workspace, updating the corresponding file identities. Preserve every header, request, argument and source position. A measurement manifest additionally identifies a complete, closed, same-binary/same-context qualification.

The server reports READY after checking all 15 E or 183 A canonical headers, fixed head, and historical-state availability. Qualification ends with the controller's `{"command":"STOP"}`. A measurement controller performs warmup, sends BEGIN, runs the complete measured work, sends END, completes deferred client validation/output, then sends STOP and reaps the child. Each server process uses one immutable variant and one workflow database.

The Python controllers expose `run_unit(...)` functions: [serial](workloads/rpc/serial/unit.py), [four-client main](workloads/rpc/concurrent/unit.py), and [four-client replay](workloads/rpc/replay/unit.py). Serial takes one server manifest and one client manifest, while concurrent/replay take one server manifest and four client manifests in client-ID order. They also take a fresh output directory and the [lifecycle module](workloads/rpc/lifecycle.py). These modules own and reap their children. Prepare database copies, manifests, binaries and the process order before starting measured runs. For concurrent CPU assignment, `GAS_CERTIFICATE_SERVER_CPUS` and `GAS_CERTIFICATE_CLIENT_CPUS` are comma-separated lists of four CPUs each. Select distinct physical cores on one socket and NUMA node. Serial uses `GAS_CERTIFICATE_SERIAL_SERVER_CPU` and `GAS_CERTIFICATE_SERIAL_CLIENT_CPU`.

The controlled input compiler has a direct CLI. Set `NODE12` and `SOLJSON` to the specified Node v12.22.12 executable and Solidity 0.5.16 compiler module. The compiler hash is checked against `COMPILER_PIN.json`. Output parents must already exist and output directories must be fresh:

```sh
python3 benchmarks/workloads/controlled/materialize.py \
  --node "$NODE12" --soljson "$SOLJSON" --out "$WORK/compiled"

python3 benchmarks/workloads/coverage/run.py \
  --binary "$WORK/bin/benign-coverage.test" \
  --artifacts "$WORK/compiled/artifacts.json" \
  --phase qualify --cpu "$CPU" --out "$WORK/coverage-qualification"

python3 benchmarks/workloads/coverage/run.py \
  --binary "$WORK/bin/benign-coverage.test" \
  --artifacts "$WORK/compiled/artifacts.json" \
  --phase paid --method C \
  --qualification "$WORK/coverage-qualification/native/qualification.json" \
  --cpu "$CPU" --out "$WORK/coverage-C"
```

`WORK` is an absolute workspace directory and `CPU` is an available physical CPU. Build `benign-coverage.test` with the [coverage fixture](workloads/coverage/geth/eth/gasestimator/certificate_benign_coverage_test.go) in the current-service source tree. A measurement invocation runs one complete fixed schedule for its method. Use separate invocations and fresh outputs for all S/C/V orders. The six process triples reported below are the original experiment.

## HTTP service schedule

For serial execution, pin the server to one physical core and use `GOMAXPROCS=1`, one client and one outstanding request. For concurrent execution, pin the server to four physical cores and use `GOMAXPROCS=4`. Pin four clients to four other physical cores on the same socket and NUMA node, with one persistent HTTP connection and one outstanding request per client. All eight selected hardware threads must belong to distinct physical cores.

| Per fresh server or client | E | A |
| --- | ---: | ---: |
| Serial warmup requests/server | 144 (16 passes) | 188 (1 pass) |
| Serial measured requests/server | 36,864 (4,096 passes) | 48,128 (256 passes) |
| Concurrent warmup passes/client | 4 | 1 |
| Concurrent measured passes/client | 1,024 | 64 |
| Concurrent measured requests/server, all four clients | 36,864 | 48,128 |

Run every request in source order in every pass, retaining rejected estimates and creations. Check each response against its expected success or exact error payload. Response validation and output serialization occur after the measured service interval. Keep default garbage collection enabled, without forced GC or startup subtraction.

The serial experiment has six matched P/S/C process triples per workflow, covering `PSC`, `PCS`, `SPC`, `SCP`, `CPS`, and `CSP` once. The main concurrent experiment repeats all six orders twice: twelve matched triples per workflow. The replay run similarly repeats all six P/V/C permutations twice. Every server is a fresh process with a fresh database copy. Each triple provides one independent paired process comparison for each contrast. Repeated requests within a process are within-process samples.

Serial contains 36 server processes across both workflows and 1,535,832 responses including warmup. Each concurrent experiment contains 72 servers, 288 client streams, 3,091,968 responses including warmup, and 3,059,712 measured responses. Report E and A separately.

### Timing and resource boundaries

* **Whole-server CPU:** user plus system CPU, obtained from the child process's final resource usage, from launch through actual exit. It includes initialization, historical indexing, warmup, serving, checking, normal GC and shutdown.
* **Measured-interval CPU:** user plus system CPU between the service interval boundaries after warmup. `interval_*` fields retain this boundary separately from whole-process CPU.
* **HTTP latency:** elapsed time for one request through reading the complete response body. The `http_total_ns` sum adds these individual durations, including concurrent requests. It is different from the controller's wall time.
* **Throughput:** measured response count divided by the controller wall interval from the common client release until all clients finish. Across processes, divide total measured count by total controller wall time.
* **Client CPU:** the entire client process, including preparation and deferred validation. Add the four clients for a server before comparing methods.
* **Allocation and GC:** differences between the measured interval's memory snapshots. End heap is the observed interval-end heap, and peak RSS comes from whole-process resource usage. Their boundaries and meanings differ.

For the replay ablation, the primary comparison is A's whole-server C/V CPU. Other replay contrasts and their pointwise intervals are descriptive. Compare C and V using this run's process pairing. The decomposition `(T_V - T_C) / (T_P - T_C)` uses whole-server CPU totals from this same replay run to describe the share of the observed C/P saving attributable to omitting replay. Both its numerator and denominator use this run's P/V/C observations.

## Solidity sensitivity schedule

The [controlled suite](workloads/controlled/) contains ledger batches with N = 1, 8 or 64 updates and h = 0, 1 or 3 extra typed forwarding levels. K0 executes only the ledger batch. K1 performs a valid ERC20 permit before the batch, while K2 performs it afterward. The calls use compiler-generated default gas forwarding. Each `(N,h,K)` is a single-kind configuration. All 27 configurations remain in the results. The 9 K0 configurations certify, while all 18 permit configurations fall back.

Use six fresh S/C/V process triples, in orders `SCV`, `SVC`, `CSV`, `CVS`, `VSC`, `VCS`, pinned to one core with `GOMAXPROCS=1` and default GC. Within each process, run configurations in increasing N, then h, then K. Each configuration has one block of 4 warmup estimates followed by one block of 64 measured estimates.

Next execute five mixtures with K0 request share α = 0, 1/4, 1/2, 3/4, 1. K1 and K2 split the remainder equally. One cycle has 72 requests. Within each of the nine `(N,h)` pairs, the respective K multiplicities are `(0,4,4)`, `(2,3,3)`, `(4,2,2)`, `(6,1,1)`, `(8,0,0)`. Repeatedly visit K0/K1/K2 while each has remaining requests, then interleave the nine `(N,h)` subcycles by position. The exact arrays are in `SCHEDULE.json`. Every mixture has one warmup cycle and one measured block containing four cycles.

A block's user plus system CPU includes message/state-copy, options/report preparation and estimation, using `getrusage(RUSAGE_SELF)`. Wall time uses Go's monotonic clock. Output validation follows the block. The CPU representation has 1,000 ns resolution. Keep blocks at or below that value with `cpu_resolution_limited=true`, using the fixed repetition counts for every input, including low-cost and slower inputs. Initial high budget is 8,000,000 gas in this native suite.

There are 18 processes, 64 blocks per process (32 warmup and 32 measured), 1,152 blocks overall, 8,424 warmup estimates and 57,024 measured estimates. `sensitivity-configurations.csv` contains all 486 measured single-kind blocks, and `sensitivity-mixtures.csv` contains all 90 measured mixture blocks. `sensitivity-processes.csv` records the complete-process boundary, which includes the entire single-kind and mixture schedule.

### Mixture prediction

For process triple i, method m and input j, let `t[i,m,j] = cpu_ns / measured_count` from its single-kind block. Let `n[α,j]` be the exact multiplicity in one 72-request cycle. Predict the four-cycle CPU as:

```
predicted[i,m,α] = 4 × Σ_j n[α,j] × t[i,m,j]
predicted_ratio = predicted[i,C,α] / predicted[i,S,α]
measured_ratio = measured_cpu[i,C,α] / measured_cpu[i,S,α]
normalized_residual = ((measured_C - measured_S)
                       - (predicted_C - predicted_S)) / measured_S
```

The figure uses the signed difference residual above. `signed_method_cpu_residual_ns` in the table is each method's measured minus predicted CPU. Subtract S's residual from C's before normalizing, preserving the sign.

Within the admitted and fallback classes the schedule gives each input equal conditional weight. Let g be mean single-kind per-request S−C saving over the 9 admitted inputs, and f be mean C−S overhead over the 18 fallback inputs. The additive predicted crossing is `α* = f / (g+f)` when `g>0` and `f>=0`. This interpolation assumes those conditional distributions stay fixed. The six predictions are about 9.0–23.0% admission. Measurements cover the five executed schedules, while crossings between those schedules come from the additive model.

Admission share counts requests. The corresponding baseline CPU work share is `Σ_admitted n[α,j] × t[i,S,j] / Σ_all n[α,j] × t[i,S,j]`. The table records its numerator and denominator. For these qualified inputs, α equals the actual admission fraction. Check that equality when changing workloads.

## Same-engine Ganache policy comparison

This experiment uses a port of Ganache v7.9.2's exactimation policy, Geth's stock estimator with certificates disabled, and C in the same standalone Geth EVM under Istanbul rules. The port follows instruction records and stack snapshots, integer call-tree arithmetic, candidate verification, refund handling and search. The measured quantity is estimator component time.

The [historical input guide](workloads/historical116/) describes the complete corpus importer, its two qualification stages, the measurement loop and the historical runtime patch required for this comparison. Its [runner](workloads/historical116/run.py) accepts `--binary`, `--inputs`, `--phase qualify|all`, `--cpu` and a fresh `--out`. Measurement-only mode (`--phase paid`) takes a completed `--qualification` as its input reference.

The 116 inputs cover all 19 upstream Pair cases: four fixture sends plus 112 case-body requests, with reuse of prepared fixture state. This differs from A's 188-position fresh workflow. The original input-generation stack was Waffle 2.4.1, ethers 4.0.46 and Ganache Core 2.10.2, while the evaluated policy is Ganache v7.9.2. Reproduce this comparison with the historical native fixture and its input population.

Run one process with `GOMAXPROCS=1` and normal GC. One warmup pass per method precedes 12 measured balanced rounds covering the six method orders twice. Each method block in a round performs 8 complete passes, or 928 estimates. This gives 11,136 measured estimates per method. Every input and returned error remains in the population.

The request timer starts immediately before message, outer state-copy and options preparation and ends at estimator return. Checks, grouping, formatting and JSON output follow that timer. `estimator_time_ns` is the sum of timed request durations. `block_wall_ns` also includes outside-timer validation and output. The 12 rounds share one process and provide 12 observations of each method within it. `ganache-strata-and-positions.csv` includes overlapping groups such as uncertified, already-one-stock-trial and rejected requests. Their totals require accounting for that overlap.

## Semantic checks before timing

Workflow qualification executes every positive estimate at its returned limit using `eth_call` and compares returned bytes with pristine Geth. Rejected estimates must reproduce the expected error payload. Returned gas limits can differ across methods. The 197 positions include 184 positive estimates: C certifies 107 (6 E, 101 A), while all other positions retain S's ordered trial list. Untimed trial and callback measurements are separate from CPU measurement.

The focused native tests cover gas forwarding and stipends, inverse-rounding minimality and overflow, warm no-op `SSTORE`, permitted and rejected `GAS` patterns, intrinsic/floor constraints, fee settlement, delegation and fallback. For admitted focused cases, execute the candidate, a midpoint and the high limit, comparing status, outputs, logs, gas, raw refunds and full account-state root after settlement. These tests contain 9 entrypoints and 30 named subtests, including 11 native Osaka transaction scenarios, 18 synthetic hook fixtures and 1 serialization case. Arithmetic assertions occur within those tests. Seven additional cases check V's replay and fallback.

For all 27 Solidity inputs, compare native executions at all S/C/V returned budgets and the C midpoint with the high-limit execution, including full post-settlement state. The Ganache fixture checks all 105 positive budgets and 11 rejection payloads under Istanbul. These checks validate sampled budgets in the implementation, while the paper's conditional theorem gives the interval/path guarantee.

## Recompute reported quantities

Tables are UTF-8 CSV with a header, integer nanoseconds and byte counts, and zero-based source positions. Blank fields mark unavailable or inapplicable values, and measured zeros are written explicitly. Pair rows using `triple` and `process` together with `experiment` and `workflow`, since process keys are reused across experiments/workflows. Sensitivity and historical native rounds use zero-based triple/round indices, while service triples are numbered 1 onward.

* **CPU totals and reductions:** sum the matching `whole_cpu_ns` rows for each method. C/control ratio is `sum(C)/sum(control)` and reduction is `1-ratio`. Measured-interval ratios use `interval_cpu_ns`. For sensitivity configurations, use `cpu_ns` and aggregate across six process pairs. Keep every pair, including ratios above 1.
* **Paired geometric mean and 95% CI:** for each of the 12 concurrent triples compute `r_i=CPU_C,i/CPU_control,i` and `l_i=ln(r_i)`. Let `L=mean(l_i)` and `s` be the sample standard deviation of the 12 log ratios (denominator 11). Geometric mean is `exp(L)`. The pointwise interval is `exp(L ± 2.20098516 × s/√12)`, with 11 degrees of freedom. It assumes independent process pairs and an adequate log-ratio approximation. Its replication unit is a process pair, and its coverage applies pointwise to that contrast. Compute the geometric mean and ratio of CPU totals separately.
* **Rate:** `10^9 × sum(measured_count)/sum(controller_wall_ns)`. Compute each paired rate using that pair's own count and controller wall time, which accounts for concurrent service of requests whose individual HTTP latencies may overlap.
* **Position/class elapsed-time comparisons:** sum `http_total_ns` over the same position/class and method within one experiment. Ratios compare equal populations. Count slower positions from these totals, including all creations and estimator rejections. Interpret a position's ratio as a descriptive comparison of elapsed time. Causal admission overhead requires an additional decomposition.
* **Quantiles:** sort every measured response's integer latency in the chosen population ascending. At percentile p select the sample at zero-based index `floor((count−1)×p/100)`. Compute an aggregate quantile from the pooled samples for that population. The compact tables contain count, sum, min, max, p50, p95 and p99, with exact independent recalculation requiring the Release response streams.
* **Memory and GC:** sum interval allocation for aggregate reduction, use the reported median of per-process peak RSS or end heap for median comparisons, and report the full range of GC cycles. MiB means 1,048,576 bytes, and memory table fields ending `_kib` use 1,024 bytes. End heap, allocation and lifetime peak RSS answer different questions.

The main service experiment finds about 24.8% and 25.4% lower concurrent C/P whole-server CPU for E and A, respectively. The separate replay run saves about 9.8% A whole-server CPU for C/V. In the main concurrent run, 27 A positions are slower than P. In replay, 31 are slower than P and 31 are slower than V. Seventeen of 18 sensitivity fallback configurations cost more C/S CPU in aggregate. At α=0 all six mixture pairs regress, and at α=1/4 one pair is about 0.1% slower despite an aggregate saving. Memory can increase even when allocation falls.

## Data files and paper correspondence

| Files in `results/` | Meaning | Paper use |
| --- | --- | --- |
| `service-processes.csv`, `service-clients.csv` | All 180 service servers and 576 concurrent clients, with user/system CPU, count, wall boundary and memory | End-to-end service cost, service table, CPU plots, replay, memory |
| `http-populations-and-processes.csv` | All service process HTTP summaries plus whole-method and whole-class distributions | Tails and class costs |
| `http-positions.csv`, `http-process-positions.csv`, `http-process-classes.csv` | Full source-position population, method totals and every process-position/class breakdown in all three service experiments | Position-cost plot and all negative position/class findings |
| `source-positions.csv`, `workflow-qualification.csv`, `workflow-returned-estimates.csv` | Source ordering, admission/fallback, attempts, block context, untimed callbacks, all P/S/C budgets/errors and returned-budget call-result hashes | Semantic coverage, budget ratios and attempt counts |
| `sensitivity-configurations.csv`, `sensitivity-mixtures.csv`, `sensitivity-processes.csv` | Every measured block and process in the controlled suite | All single-kind points, mixtures and whole-process cost |
| `sensitivity-mixture-multiplicities.csv`, `sensitivity-models.csv`, `sensitivity-qualification.csv` | Exact weighting, six predicted crossings and untimed admission checks | Mixture predictions, residuals and validation |
| `ganache-rounds.csv`, `ganache-strata-and-positions.csv` | All 12 within-process rounds and full overlapping groups/input totals | Same-engine comparison and negative groups |
| `manifest.json` | Versions, source-summary identities and table hashes | Integrity and provenance |

The compact tables contain process/configuration/position measurements and their components for recomputing the displayed aggregates and uncertainty formulas. Response streams record each request's phase, identity, response body and latency for exact tail recalculation and response reconciliation. The [data guide](DATA.md) maps each archive to its experiment and provides download links. Verify archive hashes before using the data. Archive downloads are optional for building and using the prototype.
