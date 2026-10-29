# Geth source overlay

`geth/` contains the changed and new files for Geth v1.17.8, commit `a579077007b98217c3e253a66e4b452ca0c32b96`. Use the [main guide](../README.md) for building this version, running examples, locating the implementation, and testing its properties. Apply `../patches/prototype.patch` once to a fresh upstream checkout to install these files.

The checker (`eth/gasestimator/certificate.go`) and all 7 modified production files retain the evaluated implementation's executable code, with comments edited for readability. The portable command in `cmd/gas-certificate/` provides a JSON interface to the same `gasestimator.Estimate` API. The historical HTTP service launcher and its measurement instrumentation are supplied with the benchmarks.

The prototype retains the 3 focused original test files: `certificate_test.go`, `certificate_osaka_test.go`, and `certificate_replay_test.go`. Their transaction helpers are in `certificate_native_helpers_test.go`, so these tests construct their inputs locally. Tests that use recorded workload inputs are provided with the corresponding benchmark.

The remaining integration files are `eth/ethconfig/config.go` (configuration), `eth/backend.go` and `eth/api_backend.go` (backend options), and `internal/ethapi/api.go` (ordinary RPC invocation). They set certificate/replay options when the backend is constructed. The standalone command selects its invocation options directly.

Library files retain LGPL-3.0-or-later notices. The new standalone command declares GPL-3.0-or-later. See the [license scope](../LICENSE).
