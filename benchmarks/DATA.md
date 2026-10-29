# Data packages

The [v0.1.0 release](https://github.com/ICDE2027gas/same-path-gas/releases/tag/v0.1.0) provides seven archives, totaling **182,610,261 bytes (about 183 MB)** compressed. Download individual packages using the links below. [releases.json](releases.json) records their exact byte sizes, SHA256 hashes, payload counts, and extraction paths.

Archive downloads are optional for building and using the standalone prototype. For evaluation, the repository includes **17 CSV tables**, totaling 2,524,853 bytes and 23,746 data rows, in [results/](results/). Their identities and source associations are recorded in [results/manifest.json](results/manifest.json).

| Package | Compressed bytes | Payload files | Extracted top-level path and purpose |
| --- | ---: | ---: | --- |
| [service-concurrent-raw.tar.gz](https://github.com/ICDE2027gas/same-path-gas/releases/download/v0.1.0/service-concurrent-raw.tar.gz) | 78,439,352 | 288 | `service-concurrent-raw/`: main four-client P/S/C HTTP response streams |
| [replay-ablation-raw.tar.gz](https://github.com/ICDE2027gas/same-path-gas/releases/download/v0.1.0/replay-ablation-raw.tar.gz) | 78,358,534 | 288 | `replay-ablation-raw/`: separate four-client P/V/C HTTP response streams |
| [service-serial-raw.tar.gz](https://github.com/ICDE2027gas/same-path-gas/releases/download/v0.1.0/service-serial-raw.tar.gz) | 18,956,433 | 36 | `service-serial-raw/`: serial P/S/C HTTP response streams |
| [workload-sensitivity-raw.tar.gz](https://github.com/ICDE2027gas/same-path-gas/releases/download/v0.1.0/workload-sensitivity-raw.tar.gz) | 1,421,997 | 1,152 | `workload-sensitivity-raw/`: every warmup and measured native block in the controlled suite |
| [ganache-comparison-raw.tar.gz](https://github.com/ICDE2027gas/same-path-gas/releases/download/v0.1.0/ganache-comparison-raw.tar.gz) | 3,660,028 | 2 | `ganache-comparison-raw/`: historical same-engine `all-requests.jsonl` and `blocks.jsonl` |
| [workflow-state.tar.gz](https://github.com/ICDE2027gas/same-path-gas/releases/download/v0.1.0/workflow-state.tar.gz) | 430,159 | 80 | `workflow-state/`: closed ERC20 and Pair state databases under `E/node/` and `A/node/` |
| [historical116-inputs.tar.gz](https://github.com/ICDE2027gas/same-path-gas/releases/download/v0.1.0/historical116-inputs.tar.gz) | 1,343,758 | 233 | `historical116-inputs/`: 116 input JSONs, 116 result JSONs, and `events.jsonl` |

The first four packages are the main service, replay, serial, and controlled raw measurement groups. The fifth preserves the historical policy comparison. The last two supply inputs for exact recorded-context reproduction. Each archive additionally contains `<top-level>/MANIFEST.json` with every payload file's relative path, byte size, and SHA256. The file counts above exclude that one embedded manifest. `raw_bytes` in `releases.json` is the sum of payload bytes, excluding the manifest and tar metadata.

The historical request records use transaction-relative input paths. The historical event stream uses relative source and toolchain labels in provenance and error stacks. These path-only changes preserve every transaction, result, timing and measurement value. `releases.json` records the original and distributed file hashes for both changed records.

## Choose the data you need

The CSV tables retain complete process, configuration, position, and method values used for aggregate CPU, throughput, latency summaries, allocation, memory, and uncertainty calculations:

| Values | Repository files |
| --- | --- |
| Every service server's lifetime/interval CPU, allocation, GC, heap snapshots, and peak RSS, plus all concurrent clients' CPU | `service-processes.csv` (180 servers), `service-clients.csv` (576 clients) |
| Every controlled-suite process's CPU and memory, plus every measured configuration and mixture block | `sensitivity-processes.csv` (18 processes), `sensitivity-configurations.csv`, `sensitivity-mixtures.csv` |
| HTTP summaries for each process, position, and class, including slower positions | `http-populations-and-processes.csv`, `http-process-positions.csv`, `http-process-classes.csv`, `http-positions.csv` |
| Historical policy method blocks/rounds and full position/stratum totals | `ganache-rounds.csv`, `ganache-strata-and-positions.csv` |

Use the raw HTTP package for the matching experiment to independently recompute per-request latency distributions, including aggregate p50/p95/p99, or reconcile individual responses. Exact reconstruction requires the individual samples. The controlled raw package contains all 1,152 blocks, including warmup, and its measured block values are also in the CSVs. The historical raw package contains per-request estimator timings and all method blocks. Statistical definitions and measurement boundaries are in the [benchmark guide](README.md).

For exact service reproduction, extract `workflow-state.tar.gz` and pass the resulting `workflow-state` directory to the service runner alongside the recorded context/population JSONs already in the repository. The state archive excludes `nodekey`, node-discovery data, the blob pool, and non-database lock files. The included database files are enumerated in [STATES.expected.json](workloads/uniswap/STATES.expected.json), and each server run uses a fresh writable copy.

For the Istanbul historical comparison, extract `historical116-inputs.tar.gz` and pass `historical116-inputs` as `--inputs` to the [historical runner](workloads/historical116/README.md). The historical payload consists entirely of transactions generated by the public tests, including their signatures and test addresses.

## Verify an archive locally

The following standard-library Python command checks one local archive against `releases.json` before extraction. Replace the archive path with any selected package:

```sh
python3 - benchmarks/releases.json /path/to/workflow-state.tar.gz <<'PY'
import hashlib, json, pathlib, sys
manifest = json.loads(pathlib.Path(sys.argv[1]).read_text())
path = pathlib.Path(sys.argv[2])
entry = next(a for a in manifest['archives'] if a['name'] == path.name)
assert path.stat().st_size == entry['bytes'], 'archive byte size differs'
digest = hashlib.sha256()
with path.open('rb') as stream:
    for chunk in iter(lambda: stream.read(1024 * 1024), b''):
        digest.update(chunk)
assert digest.hexdigest() == entry['sha256'], 'archive SHA256 differs'
print(path.name, 'size and SHA256 match')
PY
```

Run this from the source repository root, or provide an absolute path to `releases.json`. The hashes describe the completed packaged bytes, so a successful local comparison verifies the copy you have obtained.
