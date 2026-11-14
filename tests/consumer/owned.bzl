"""Benchmark targets for the project consumer fixture."""

BENCHMARK_TARGETS = [
    "//:tests/bench/lib",
    "//crates/build-support:tests/bench/lib",
    "//crates/dev:tests/bench/lib",
    "//crates/dynamic:tests/bench/lib",
    "//crates/macros:tests/bench/lib",
    "//crates/normal:tests/bench/lib",
    "//crates/normal:tests/bench/bin/feature-tool",
    "//crates/probe:tests/bench/lib",
    "//crates/probe:tests/bench/bin/probe",
    "//crates/probe:benches/smoke",
    "//crates/root-user:tests/bench/lib",
]
