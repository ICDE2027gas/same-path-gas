# Fixed-context service modules

`geth/cmd/benchmark-rpc/main.go` is a portable adapter of the recorded HTTP benchmark service. Select exactly one `variant.go` from `variants/`: `current.go` supports S/C/V on the current patched source, `accepted.go` supports S/C on the earlier accepted source, and `pristine.go` supports P on the unmodified Geth revision specified below. The shared service opens a writable state copy, verifies the entire recorded canonical header chain and every historical root, exposes loopback JSON-RPC and accepts the original READY/BEGIN/END/STOP lifecycle protocol.

## Select and build the source variant

Use Go 1.25.3 and Geth commit `a579077007b98217c3e253a66e4b452ca0c32b96` (v1.17.8). Start every variant from its own fresh checkout of that exact commit. Each runtime patch contains the 7 modified production files and the certificate checker from its recorded implementation. The release revises source comments while preserving the recorded execution code. Apply the selected patch directly to bare upstream. Historical commands and fixture tests are provided separately.

| Recorded experiment / method | Source at the pinned base | RPC variant file |
| --- | --- | --- |
| Main concurrent or serial, P | Bare upstream | `variants/pristine.go` |
| Main concurrent or serial, S/C | `patches/accepted-runtime.patch` | `variants/accepted.go` |
| Replay experiment, P | Bare upstream | `variants/pristine.go` |
| Replay experiment, V/C | `patches/current-runtime.patch` | `variants/current.go` |
| Controlled native suite, S/C/V | Current runtime with the required native helpers in `patches/prototype.patch` | Native command in [the controlled fixture guide](../coverage/README.md) |

The full prototype patch has the same production execution code as `current-runtime.patch` and additionally includes the standalone command and focused/native helper tests. An existing prototype checkout is ready to build the replay RPC service with `variants/current.go`. Apply `current-runtime.patch` only when starting from bare upstream. For the main and serial results, both S and C use the accepted runtime.

The following commands select one fresh RPC build. Set `ARTIFACT`, `GETH`, and `BENCH` to absolute paths for this repository, a new checkout directory, and an existing writable output directory. Select `RUNTIME=pristine`, `accepted`, or `current` using the table above.

```sh
RUNTIME=current
git clone --no-checkout https://github.com/ethereum/go-ethereum.git "$GETH"
git -C "$GETH" config core.autocrlf false
git -C "$GETH" config core.eol lf
git -C "$GETH" checkout a579077007b98217c3e253a66e4b452ca0c32b96
case "$RUNTIME" in
  pristine) ;;
  accepted|current)
    git -C "$GETH" apply --check "$ARTIFACT/patches/$RUNTIME-runtime.patch"
    git -C "$GETH" apply "$ARTIFACT/patches/$RUNTIME-runtime.patch"
    ;;
  *) exit 2 ;;
esac
mkdir -p "$GETH/cmd/benchmark-rpc"
cp "$ARTIFACT/benchmarks/workloads/rpc/geth/cmd/benchmark-rpc/main.go" "$GETH/cmd/benchmark-rpc/"
cp "$ARTIFACT/benchmarks/workloads/rpc/variants/$RUNTIME.go" "$GETH/cmd/benchmark-rpc/variant.go"
cd "$GETH"
go build -mod=readonly -o "$BENCH/benchmark-rpc-$RUNTIME" ./cmd/benchmark-rpc
```

The patch identities are:

```text
729c943aee8ceda3649e8f31bfd0e1a61d825ac4e56243cbee7b59462588a4af  accepted-runtime.patch (32983 bytes)
c12aac5ae5885b927aa11e0f8c52f9472087d26a65811badd961d058bca7f974  current-runtime.patch (34759 bytes)
```

The [validation summary](../../../VALIDATION.md) describes the source checks and exercised RPC configurations.

## Run the service modules

Use [run.py](run.py) to verify the supplied state files, create independent writable clones and manifests, and control the complete server/client lifecycle. For example, qualify every E position under the current C implementation against a separately qualified pristine P baseline:

```sh
python3 "$ARTIFACT/benchmarks/workloads/rpc/run.py" \
  --binary "$BENCH/benchmark-rpc-current" \
  --pristine-binary "$BENCH/benchmark-rpc-pristine" \
  --state-dir "$WORKFLOW_STATE" --cohort E --variant C \
  --source-variant current-service --mode qualify --output "$BENCH/rpc-E-C"
```

`WORKFLOW_STATE` is the absolute path to the unpacked workflow-state attachment. The output workspace must be fresh. The runner selects available distinct physical cores on one socket/NUMA node, or uses the explicit selection in `--server-cpus` and `--client-cpus`. See the [benchmark guide](../../README.md) for measured serial/concurrent/replay modes, recorded source associations and output fields.

With a captured head older than six hours, Geth's default history indexing may wait about 5–6 minutes before READY. Keep the runner's default `--timeout 1800` so normal startup can finish. Readiness requires zero pending indexing work and every recorded state root to open. The runner preserves the recorded headers and storage settings throughout.

`serial/client.py` and `serial/unit.py` implement one server/one HTTP client, including full-population functional qualification, with V also supported through that qualification path. `concurrent/` preserves the four-client P/S/C load, and `replay/` preserves the four-client P/V/C load. Their `run_unit` functions own direct-child `wait4` accounting and retirement. Warmup/pass counts, raw-response buffering, post-END validation and fence accounting remain those in the recorded driver. The runner qualifies each requested variant before timed measurement.

The runner sets `GAS_CERTIFICATE_BENCH_ROOT` and CPU environment variables for these modules. Direct callers can select serial CPUs with `GAS_CERTIFICATE_SERIAL_SERVER_CPU` and `GAS_CERTIFICATE_SERIAL_CLIENT_CPU`, or four-client CPU lists with `GAS_CERTIFICATE_SERVER_CPUS` and `GAS_CERTIFICATE_CLIENT_CPUS`. The four-client controller checks distinct physical cores, affinity and absence of selected SMT siblings, using four server CPUs, four client CPUs and `GOMAXPROCS=4`.

The server manifests use schema `round1789-server-input-v1`. Input FileSpecs are canonical absolute paths with byte counts and SHA256. `source_proofs` should contain the public workload origin and expected-state manifests. The adapter uses these public provenance inputs in place of the original author process-receipt gate and performs the canonical-context and historical-state checks. It requires a fresh independent clone, fresh output directory and two distinct unprivileged loopback ports. The obsolete `released` field is ignored. Portability changes have new source/binary/qualification identities, while the paper measurements keep the recorded source associations described in the [benchmark guide](../../README.md).
