# List the standalone repository commands.
default:
    just --list

# Run the extension tests without parent workspace configuration or remote services.
test:
    ASPECT_WRAPPER_SKIP=1 BAZELISK_SKIP_WRAPPER=1 bazel --ignore_all_rc_files test //:rules_rs_test //:rules_rs_integration_test --remote_cache= --remote_executor= --bes_backend= --test_output=errors

# Check generation and build Rust targets through an external module dependency.
[working-directory("tests/consumer")]
consumer-test:
    ASPECT_WRAPPER_SKIP=1 BAZELISK_SKIP_WRAPPER=1 bazel --ignore_all_rc_files run //:gazelle --remote_cache= --remote_executor= --bes_backend= -- -r=true -mode=diff crates
    ASPECT_WRAPPER_SKIP=1 BAZELISK_SKIP_WRAPPER=1 bazel --ignore_all_rc_files test //crates/... //crates/probe:probe-benches/smoke //crates/probe:probe-bench-test //crates/dynamic:dynamic-bench-test --remote_cache= --remote_executor= --bes_backend= --test_output=errors
