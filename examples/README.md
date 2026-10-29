# Small executable contexts

Build `gas-certificate` in the patched Geth checkout. In the commands below, replace `<artifact>` with this repository's absolute path. The command accepts any input path.

```sh
./gas-certificate -input <artifact>/examples/sstore.json
./gas-certificate -input <artifact>/examples/nested-stipend.json
./gas-certificate -input <artifact>/examples/gas-fallback.json
```

These outputs were observed with Go 1.25.3 on the Geth version specified in the [build guide](../README.md#build):

| Input | Estimated gas | Trials | Program and observed check |
| --- | ---: | ---: | --- |
| `sstore.json` | 43,761 | 1 | Writes 1 to storage slot 0, emits a 32-byte log, and returns 42. The report is certified with 1 SSTORE. |
| `nested-stipend.json` | 34,430 | 1 | Parent sends value 1 using `GAS` immediately followed by `CALL`, supplying the ordinary 2300-gas stipend. The warm child performs a no-op SSTORE and returns 42. The certified report has 1 completed call, 2 completed frames, required entry gas 9,130, and gas consumed 6,955. |
| `gas-fallback.json` | 21,169 | 6 | Executes `GAS`, `POP`, then `STOP`. The report has `certified: false`, `status: "unsupported"`, and `reason_counts.gas_not_immediate_call: 1`. Geth's ordinary search returns the estimate. |

The output contains `estimated_gas`, the complete `certificate` report, `trials`, and a `prestate_root` for the reconstructed state. `candidate` in the report is meaningful only when `certified` is true. The default mode returns an admitted candidate directly and preserves the input state.

For an explicit functional comparison, run the same input through the stock estimator or the replay control:

```sh
./gas-certificate -mode stock -input <artifact>/examples/gas-fallback.json
./gas-certificate -mode replay -input <artifact>/examples/sstore.json
```

`stock` leaves the checker disabled and omits the report. `replay` runs the same checker and then executes an admitted candidate once using the ordinary executor. The additional trial appears in `trials`. The ordinary fallback must agree with `stock` on both the estimate and the trial sequence. These commands collect trial reports for inspecting behavior. Use the [benchmark harness](../benchmarks/README.md) for service timing.

The observed stock run of `gas-fallback.json` returned 21,169 gas with the identical 6-trial sequence. The replay run of `sstore.json` returned 43,761 gas with trials at 200,000 and 43,761 gas.

Ordinary search uses `ErrorRatio=0.015` in every mode. The returned estimate provides a sufficient gas budget under the supplied context.

## Supplying your own context

Copy one JSON file and replace the request, block fields, and allocation. All quantities use canonical `0x` hex strings, addresses use 20 bytes, and hashes/storage keys/storage values use 32 bytes. Bytecode and calldata are even-length `0x` byte strings. Unknown fields and multiple JSON objects are rejected. Input is limited to 64 MiB. JSON may also be piped on stdin with `-input -`.

- Set `schema` to `gas-certificate-context-v1` and `fork` to `osaka`.
- `chain_id` is a positive unsigned 256-bit integer. The fork activation schedule is Geth's mainnet schedule at the specified commit. The block number/time must fall within Osaka under that schedule.
- `block` supplies `number`, `timestamp`, `gas_limit`, `base_fee`, `fee_recipient`, `prev_randao`, and `parent_hash` as the EVM execution context.
- `request` supplies `from`, `to`, `nonce`, `gas`, `gas_price`, `value`, `data`, and an optional standard Ethereum `access_list`. `gas` is the requested high allowance, subject to Geth's ordinary balance and fork caps. `to: null` denotes contract creation, which uses ordinary fallback. The nonce is checked against the supplied state.
- `gas_price` supplies the effective legacy-style price, fee cap, and tip cap. The command supports positive prices and zero price. Blob transactions, authorization lists, and dynamic-fee request fields are outside this adapter's input format.
- `alloc` maps addresses to `nonce`, `balance`, `code`, and optional `storage`. Include the sender and a `balance` for each listed account. Omitted nonce/code/storage mean 0/empty/empty. Every unlisted account or storage slot is absent/zero in this declared state. The adapter commits and reopens imported storage before estimating so SSTORE uses the correct original values.
- If execution uses `BLOCKHASH`, `block.parent_hash` supplies the immediate predecessor hash. For earlier hashes, supply `ancestors` as full Geth JSON headers in descending order, starting at the predecessor and connected to `parent_hash`. A requested missing ancestor causes an input error. At most 256 ancestor headers are accepted. The adapter checks their hashes and linkage for EVM use. Consensus validity is the input provider's responsibility.

Estimation errors appear in the output's `error` and optional `revert_data`, with process exit code 1. Invalid/incomplete context or other adapter errors go to stderr with exit code 2. Successful estimates exit 0. Use `-timeout 30s` to change the default 10-second EVM timeout.
