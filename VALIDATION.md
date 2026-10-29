# Validation

The package was exercised on x86-64 Ubuntu 22.04 with Go 1.25.3 and CPython 3.10.12. The [runtime requirements](benchmarks/README.md#runtime-requirements) list the Linux interfaces and additional tools used by the experiment scripts.

Completed checks cover:

- **Standalone prototype:** build, all 4 command tests and 10 focused checker/transaction/replay test entrypoints, the three JSON examples, and stock/replay controls.
- **RPC service:** builds of pristine, accepted, and current variants. Full qualification against pristine Geth and complete four-client runs for ERC20 with accepted-service C and Pair with current-service V. An ERC20 serial C run reused its completed qualification and a fresh state copy.
- **Controlled contracts:** compilation with the specified Solidity toolchain, qualification of all 27 configurations under S/C/V, and one complete C process with the 64-block schedule including warmup.
- **Historical comparison:** runtime build, both complete 116-input qualification stages, and the full 39-block measurement schedule including warmup.

Subsequent comment edits were checked to preserve executable code. All four source patches were applied to the specified upstream files and their resulting contents verified. Input identities, data tables, and the analysis definitions remain associated with the original experiments reported in the paper.

The historical import adapter also verified all 233 distributed files and all 116 transaction associations after event-path metadata was made portable. Its input inventory reflects the distributed event stream. All 232 transaction and result files retain their original bytes.

These checks exercise the packaged interfaces at the scope listed above. The [benchmark guide](benchmarks/README.md) gives the complete process counts, method orders, measurement boundaries, and analysis procedures for reproducing the paper's experiments.
