# Gazelle extension for rules_rs

This extension generates Bazel rules from one Cargo workspace.
It uses the native Gazelle v2 API and public rules_rs interfaces.
The repository is private and has no public release.
The [compatibility report](COMPATIBILITY.md) records the adoption requirements and test coverage.

Cargo owns targets, dependencies, features, editions, and lint settings.
The extension does not require a second target list.
It reads locked Cargo metadata and per-platform Cargo unit graphs.
Configured consumers use unit graphs even when packages declare no features.
A pinned nightly Cargo supplies the unstable unit-graph interface.
The consumer selects the Rust compiler through its registered Bazel toolchain.

| Input                | Output                                                                |
| -------------------- | --------------------------------------------------------------------- |
| Libraries            | `rust_library`, `rust_proc_macro`, and requested library output types |
| Binaries             | `rust_binary`                                                         |
| Build scripts        | `cargo_build_script`                                                  |
| Tests                | Unit tests, integration tests, and integration test suites            |
| Examples             | Binaries or requested library output types                            |
| Benchmarks           | Manual `rust_test` targets with `--bench`                             |
| Doctests             | `rust_doc_test` from the public rules_rust API                        |
| Cargo unit graphs    | Context-specific local dependencies and features                      |
| Public crate helpers | External dependencies, aliases, crate names, editions, and lints      |
| Metadata handlers    | Additional rules, resource inputs, and environment values             |

## Consumer setup

Use the [consumer module](tests/consumer/MODULE.bazel) and [consumer BUILD file](tests/consumer/BUILD.bazel) as a complete example.
Replace its local module override with a pinned Git commit from this repository.
Private consumers must configure GitHub access before Bazel fetches the repository.

```starlark
bazel_dep(name = "gazelle_rules_rs", version = "0.0.0")
git_override(
    module_name = "gazelle_rules_rs",
    remote = "git@github.com:scalar0/gazelle_rules_rs.git",
    commit = "<reviewed-commit>",
)
```

Register a compatible Rust toolchain in the consumer module.
Use `@gazelle_rules_rs//:rules_rs` in a v2 `gazelle_binary.languages` list.
Add the public rules_rust repository when the workspace has doctests or `dylib` outputs.
The consumer fixture shows that repository import.

Define the platform list once in the consumer module.
Pass that variable to both `crate.from_cargo` and `backend_config`.
This removes the need to parse consumer MODULE syntax.

```starlark
backend_config = use_repo_rule("@gazelle_rules_rs//:config.bzl", "backend_config")
backend_config(
    name = "gazelle_backend",
    manifest = "project/Cargo.toml",
    platform_triples = PLATFORM_TRIPLES,
    crates_repository = "crates",
    rules_repository = "rules_rs",
)
```

Add `@gazelle_backend//:config.json` to the Gazelle rule's `data`.
Set its argument to `-rules_rs_config=$(execpath @gazelle_backend//:config.json)`.
The [configuration schema](configuration.schema.json) defines the public settings.
Repository names use their apparent Bzlmod names without `@`.
The rules_rust repository currently uses the apparent name `rules_rust`.

The selected Cargo workspace can be at the repository root or in a subdirectory.
Workspace members can use different directory depths inside the repository.
Standard Gazelle update, fix, and diff modes apply.
Run recursively over all selected workspace members so that dependency labels have corresponding generated rules.

The extension also reads excluded packages from workspace member and exclusion paths.
Each excluded package must have its own Cargo lock.
It uses literal package metadata and local dependency labels instead of workspace lookup functions.
Direct dependencies must use paths inside the repository.
Standalone registry dependencies need a separate crate repository and currently cause an explicit error.

The legacy `-rules_rs_crate_scope` flag remains available for existing consumers.
It restricts generation to immediate children of comma-separated directories.
Without a backend configuration, legacy feature resolution reads literal platform settings from MODULE.bazel.
New consumers should use the backend configuration.

## Rule ownership

Each generated rule has a marker comment.
The extension updates or removes rules with that marker.
It rejects name conflicts with handwritten rules.
Gazelle preserves `# keep` attributes and rules.
A kind change fails when a keep comment prevents replacement.

The extension validates workspace metadata, target generation, handler contributions, and package conflicts before Gazelle writes files.
A failure in a later Cargo package leaves earlier BUILD files unchanged.
Gazelle owns file formatting, merging, load updates, and diff output.
The extension uses the public Fix hook to update default backend load labels after repository renames.

Targets inherit package visibility.
Library labels use the package directory name, or the Cargo package name for a repository-root package.
Integration test labels use `<base>-integration-tests/<target>`.
Example labels use `<base>-examples/<target>`.
Explicit benchmark labels use `<base>-benches/<target>`.
Implicit library and binary benchmarks use `<target>-bench-test`.
Additional library outputs use `<target>-<crate-type>`.

Libraries and binaries under `src/` use a compact `src/**/*.rs` glob that excludes `src/main.rs`.
A target whose root falls outside that glob also lists its root explicitly.
Other targets list their crate root without a directory-wide glob.
For shared modules or custom source layouts, set the complete `srcs` attribute with Gazelle's `# keep` comment.
The [consumer fixture](tests/consumer/crates/probe/BUILD.bazel) shows a test override for shared modules.
Bazel excludes nested Bazel packages from globs.
Declare non-Rust inputs in Cargo metadata.
Library inputs also include the Cargo README when present.

Targets with one public Cargo configuration compile under their normal labels.
Equivalent configurations use the standard backend dependency helpers.
Compatible unit tests reuse their crate through the `crate` attribute.
Additional configurations use content-hashed labels without absolute paths.
Public aliases select between configurations only when supported platforms need different targets.
All local edges use the same compiled target to preserve Rust type identity.
Explicit context edges also handle dependencies on the workspace-root package.
Required-feature conditions follow the selected Cargo unit graphs.
External dependency resolution remains the responsibility of rules_rs.

## Resource metadata

Use `[workspace.metadata.rules_rs]` for defaults.
Use `[package.metadata.rules_rs]` for package values.
An explicit package field replaces its workspace default, including an empty list or map.
The [metadata schema](metadata.schema.json) defines fields and validation rules.
Unknown fields in this namespace cause an error.

The `dep_overrides` array maps external dependency label prefixes to local labels.
Use these mappings when a workspace member depends on an excluded local package.
This keeps one compiled Rust type identity for each local package.
The existing `dep_override` field remains available for a single mapping.

```toml
[package.metadata.rules_rs]
library_compile_data = ["resources/*"]
unit_compile_data = ["tests/unit-fixture.json"]
test_data = ["test_data/**"]

[package.metadata.rules_rs.build_inputs.MODEL_SCHEMA]
bazel_label = "//schemas:model.json"
cargo_path = "vendor/model.json"
```

Input lists accept labels, package-relative paths, and glob patterns.
Build inputs pair a repository-relative Cargo path with a Bazel file label.
Generated build scripts receive those labels as data and environment values through `execpath`.
The public `@gazelle_rules_rs//:vendor-config` command maintains matching Cargo environment defaults.
It preserves configuration outside its marked block.
Build scripts own resource parsing and validation.

## Compiled metadata handlers

A consumer can compile additional handlers into its Gazelle binary.
The [handler API](handlers.go) defines `MetadataHandler`, `HandlerSpec`, `MetadataContext`, and `Contribution`.
The [consumer handler](tests/consumer/metadata/handler.go) supplies an independent example.

A handler declares one namespace, a JSON Schema, and its additional rule kinds with load information.
Its constructor calls `rulesrs.New(handler)` from the language package's `NewV2` function.
The extension validates registered workspace and package metadata before it calls the handler.
Unregistered metadata namespaces remain untouched.
A missing optional namespace does not call its handler.

Handlers receive separate copies of workspace and package metadata.
Each handler defines its own inheritance policy.
`TargetInfo.BazelTarget` identifies the generated build target for input contributions.
Handlers can add rules, compiler inputs, runtime inputs, and environment values.
They cannot replace Cargo target identity, dependencies, or features through this interface.
Duplicate namespaces, duplicate kinds, unknown input targets, conflicting environment values, and rule-name collisions cause errors.

Remove a handler's metadata and regenerate before you remove its compiled registration.
This lets Gazelle remove the handler's stale rules with the registered merge policy.

## Consumer tools

The public `@gazelle_rules_rs//:cargo` target supplies the pinned generation Cargo binary.
The public `@gazelle_rules_rs//:rustc` target supplies a runfiles path manifest for the consumer's registered Rust compiler.
Consumers can use these targets as declared test inputs.
The public `@gazelle_rules_rs//:metadata.schema.json` target supplies the Cargo metadata schema.

## Development and provenance

The [Justfile](Justfile) defines development commands.
The [CI workflow](.github/workflows/ci.yml) uses Aspect for standalone tests, consumer generation checks, and consumer tests on four hosts.
The [Aspect version file](.aspect/version.axl) pins the CLI for local and CI use.
The extension owns the copied format, lint, maintenance, and Git hook configuration.
CI also checks source formats, Bazel and shell policies, and Git hook behavior.
The consumer shares the Aspect configuration through a relative symbolic link.
The consumer uses renamed backend repositories, a registry dependency, resource metadata, and multiple Cargo target kinds.

The code originated in `scalar0/kernel-toolchain` at commit `20971ec52ecdca2534c53171944ab4425cc09fc4`.
The [Apache-2.0 license](LICENSE) continues to apply.
The extraction retains the original source notices.
