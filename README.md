# Same-path gas certificates

This repository accompanies our paper 

> **Same-Path Gas Certificates for Efficient Gas Estimation**.

This prototype adds a streaming certificate checker to Geth's gas estimator. During the estimator's first successful high-budget execution, the checker computes a sufficient gas budget for the observed path. An admitted certificate lets the estimator return that budget immediately. Inputs outside the checker's supported conditions continue through Geth's ordinary search.

The standalone `gas-certificate` command accepts your request, block context, account code, balances, and storage as JSON. It returns the estimated gas, the certificate report, and the gas limits actually tried. The input state stays fixed across trials, and the command runs locally using the supplied JSON context.

The checker supports Istanbul, Shanghai, and Osaka rule combinations. The standalone JSON interface currently accepts Osaka contexts and legacy-style gas prices, with an optional access list. A certificate applies to the supplied fixed context under the supported execution conditions. [Input details and examples](examples/README.md) describe the supported fields.

## Build

Use Git and **Go 1.25.3**. The exact Geth base is **v1.17.8**, commit `a579077007b98217c3e253a66e4b452ca0c32b96`. This repository contains the modified source files and a patch. The build below fetches the upstream Geth checkout and Go dependencies.

The following shell commands start from this repository's root and create a separate upstream checkout beside it:

```sh
ARTIFACT_DIR="$(pwd)"
git clone --no-checkout https://github.com/ethereum/go-ethereum.git ../geth-gas-certificate
cd ../geth-gas-certificate
git config core.autocrlf false
git config core.eol lf
git checkout a579077007b98217c3e253a66e4b452ca0c32b96
git apply --check "$ARTIFACT_DIR/patches/prototype.patch"
git apply "$ARTIFACT_DIR/patches/prototype.patch"
go build -mod=readonly -o gas-certificate ./cmd/gas-certificate
```

The build commands and examples below target Linux with a POSIX shell. The standalone command takes JSON input and uses the Go implementation directly. The experiment scripts additionally require Python and Linux process interfaces, listed in the [runtime requirements](benchmarks/README.md#runtime-requirements). Preserve LF line endings when applying the patch. You can read the updated source files in this repository's `geth/` directory. The patch installs those files in the upstream checkout.

## Run a small example

From the patched Geth checkout, retaining `ARTIFACT_DIR` from the build commands:

```sh
./gas-certificate -input "$ARTIFACT_DIR/examples/sstore.json"
./gas-certificate -input "$ARTIFACT_DIR/examples/nested-stipend.json"
./gas-certificate -input "$ARTIFACT_DIR/examples/gas-fallback.json"
```

The observed default-mode results are:

| Example | Estimated gas | Trials | Observed report |
| --- | ---: | ---: | --- |
| `sstore.json` | 43,761 | 1 | Certified. Writes a storage slot, emits a log, and returns 42 |
| `nested-stipend.json` | 34,430 | 1 | Certified. 1 value call, 2 frames, and required entry gas greater than gas consumed |
| `gas-fallback.json` | 21,169 | 6 | `gas_not_immediate_call`. Ordinary search completes for a program that executes `GAS` followed by `POP` |

The JSON result includes `estimated_gas`, `certificate`, `trials`, and `prestate_root`. Read `certificate.candidate` as a certified budget only when `certificate.certified` is true. On fallback, the report explains why checking stopped and the ordinary estimator supplies the estimate. Estimation failures include `error` and, for a revert, `revert_data`.

To use your own context, copy an example and replace its request and allocation. Supply the sender and every relevant account, code byte string, and storage slot. Unlisted accounts and slots are absent or zero in the declared state. The [input reference](examples/README.md#supplying-your-own-context) covers fork selection, fees, nonces, ancestor headers, stdin, timeout, and exit codes.

Two explicit controls help inspect behavior on the same input:

```sh
./gas-certificate -mode stock -input "$ARTIFACT_DIR/examples/gas-fallback.json"
./gas-certificate -mode replay -input "$ARTIFACT_DIR/examples/sstore.json"
```

`stock` disables the checker. `replay` adds one ordinary execution of an admitted candidate. The default `certificate` mode returns an admitted candidate directly. Every command above collects diagnostic trial reports. Use the benchmark harness for service-cost measurements.

The observed stock fallback also returned 21,169 gas with the same 6 trials. The SSTORE replay returned 43,761 gas with 2 trials.

## Read and test the implementation

| Source in the overlay | Responsibility |
| --- | --- |
| [`certificate.go`](geth/eth/gasestimator/certificate.go) | Streaming checks, call-frame summaries, candidate, rejection reasons |
| [`gasestimator.go`](geth/eth/gasestimator/gasestimator.go) | First-run integration, certified return, fallback, replay control |
| [`hooks.go`](geth/core/tracing/hooks.go), [`interpreter.go`](geth/core/vm/interpreter.go) | VM event interface |
| [`cmd/gas-certificate/`](geth/cmd/gas-certificate/) | JSON adapter, in-memory state import, command tests |
| [`geth/`](geth/README.md) | Overlay provenance and production RPC integration files |

### Paper algorithm and source

Algorithm 1 is implemented through EVM event callbacks in `certificate.go`. The following map connects its procedures and budget calculation to those callbacks:

| Paper operation | Implementation |
| --- | --- |
| `EnterFrame` | [`certificateObserver.enter`](geth/eth/gasestimator/certificate.go#L209) initializes and pushes the frame record. |
| `Accumulate` | [`opcode`](geth/eth/gasestimator/certificate.go#L296) composes an ordinary instruction, and [`exit`](geth/eth/gasestimator/certificate.go#L349) composes a completed call into its parent. |
| `CallEntry` | [`opcode`](geth/eth/gasestimator/certificate.go#L273) saves call parameters. The child-entry branch of [`enter`](geth/eth/gasestimator/certificate.go#L221) computes the local charge and checks gas forwarding. |
| `SuccessfulExit` | [`certificateObserver.exit`](geth/eth/gasestimator/certificate.go#L317) checks the completed frame and combines its summary with the parent. |
| Inverse forwarding bound `phi(k)` | [`phiCertificate`](geth/eth/gasestimator/certificate.go#L200) computes the bound with checked arithmetic. |
| Root budget `G* = max(I + M, F)` | [`certificateObserver.finish`](geth/eth/gasestimator/certificate.go#L359) forms the candidate and checks it against the high limit after the initial execution completes. |

The paper's frame variables `e`, `q`, and `m` correspond to `entry`, `used`, and `need` in `certificateFrame`. The checked forwarding flag `gasForward` corresponds to `certificateCall.capPattern`.

### Property tests

Run tests in the patched Geth checkout. Select any entrypoint below with `-run '^NAME$'`. These focused tests construct their own inputs and state.

```sh
go test -mod=readonly ./cmd/gas-certificate -count=1
go test -mod=readonly ./eth/gasestimator -run '^TestCertificatePhiBoundaries$' -count=1
```

| Property | Entrypoint in `./eth/gasestimator` |
| --- | --- |
| Inverse EIP-150 rounding and overflow | `TestCertificatePhiBoundaries` |
| SSTORE entry-gas requirement and final gas consumption | `TestCertificateSstoreAndFinalDepletion` |
| Calls, stipends, returned gas, GAS/CALL pattern | `TestCertificateCallTransfers` |
| Unsupported event shapes | `TestCertificateRejectsUnqualifiedEvents` |
| Sender and positive/zero-price conditions | `TestCertificateFeeRelationEligibility` |
| Supported rule combinations | `TestCertificateForkEligibility` |
| Osaka intrinsic/floor accounting and final fees | `TestCertificateOsakaFloorAndFeeSettlement` |
| Nested and delegated native calls | `TestCertificateOsakaNestedAndDelegatedCalls` |
| Ordinary unsupported-input fallback | `TestCertificateOsakaUnsupportedFallback` |
| Candidate replay, fallback, and initial failure | `TestCertificateReplayAblation` |

The command's tests exercise SSTORE, nested stipend calls, fallback equality, input validation, and replay accounting. For an individual command test, select `TestPortableCertificateSstore`, `TestPortableCertificateNestedStipend`, `TestPortableCertificateGasFallback`, or `TestPortableContextValidation` in `./cmd/gas-certificate`.

See [Validation](VALIDATION.md) for the tested platform and the build, test, and benchmark coverage of this package.

## Reproduce the evaluation

The [benchmark guide](benchmarks/README.md) describes the paper's service, replay, sensitivity, and same-engine policy experiments, including source variants, inputs, state requirements, measurement boundaries, schedules, result tables, and statistics. The small examples above are self-contained. Reproducing the service experiments uses the corresponding fixed-state databases and recorded contexts. State databases and full response streams are distributed in separate data archives.

File-level copyright and license notices apply. See [LICENSE](LICENSE), [NOTICE](NOTICE), and the preserved [GPLv3](licenses/COPYING) and [LGPLv3](licenses/COPYING.LESSER) texts.
