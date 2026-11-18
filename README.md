---
title: Gazelle extension for rules_rs
---

# Gazelle extension for rules_rs

This extension generates Bazel rules from one Cargo workspace.
It uses the native Gazelle v2 API and public rules_rs interfaces.
The repository is private and has no public release.

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
| HTML documentation   | `rust_doc` targets named `docs/lib` and `docs/bin/<name>`             |
| Cargo unit graphs    | Context-specific local dependencies and features                      |
| Public crate helpers | External dependencies, aliases, crate names, editions, and lints      |
| Metadata handlers    | Additional rules, resource inputs, and environment values             |

## Consumer setup

Use the [consumer module](tests/consumer/MODULE.bazel.in) and [consumer BUILD file](tests/consumer/BUILD.bazel.in) as configuration examples.
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
The [configuration schema](src/configuration.schema.json) defines the public settings.
Every generation run requires this backend configuration.
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
This flag also requires the backend configuration.
The extension does not parse consumer MODULE syntax.

## Rule ownership

Each generated rule has the marker `#CODEGEN:gazelle_rules_rs`.
The extension updates or removes rules with that marker.
It rejects name conflicts with handwritten rules.
Gazelle preserves `# keep` attributes and rules.
A kind change fails when a keep comment prevents replacement.
The generator preserves a `# keep` comment on a file item and excludes that file from generated globs.
For conditional kept file lists with generated globs, keep the complete attribute.

The extension validates workspace metadata, target generation, handler contributions, source boundaries, and package conflicts before Gazelle writes files.
A failure in a later Cargo package leaves earlier BUILD files unchanged.
Gazelle owns file formatting, merging, load updates, and diff output.
The extension uses the public Fix hook to update default backend load labels after repository renames.

Public targets inherit package visibility.
The target plan assigns one name to each Cargo target and derived test.
In the following table, `<name>` means the Cargo target name.

| Target                      | Label                         |
| --------------------------- | ----------------------------- |
| Library or procedural macro | `:lib`                        |
| Additional library output   | `:lib/<type>`                 |
| Build script                | `:build-script`               |
| Binary                      | `:bin/<name>`                 |
| Example                     | `:examples/<name>`            |
| Additional example output   | `:examples/<name>/<type>`     |
| Library unit tests          | `:tests/unit/lib`             |
| Binary unit tests           | `:tests/unit/bin/<name>`      |
| Example unit tests          | `:tests/unit/examples/<name>` |
| Documentation tests         | `:tests/doc`                  |
| Integration test            | `:tests/integration/<name>`   |
| Integration test suite      | `:tests/integration`          |
| Explicit benchmark          | `:benches/<name>`             |
| Library benchmark harness   | `:tests/bench/lib`            |
| Binary benchmark harness    | `:tests/bench/bin/<name>`     |

These names are public Bazel labels.
Every Cargo binary uses the same naming pattern, including a binary with the crate root `src/main.rs`.
For a Cargo binary named `cli`, the binary label is `:bin/cli` and its unit test label is `:tests/unit/bin/cli`.
Executable targets occupy leaf paths so a file cannot conflict with another target's output directory.
Executable output paths follow the rule names directly.
Cargo environment values use the executable location.

The package-name alias points to `:lib` because rules_rs uses that label for local dependencies.
Repository-root packages use the Cargo package name for this alias.
The extension rejects collisions between this alias and another generated target.

Libraries and binaries under `src/` use a compact `src/**/*.rs` glob that excludes `src/main.rs`.
A target whose root falls outside that glob also lists its root explicitly.
Other targets list their crate root without a directory-wide glob.
For shared modules or custom source layouts, set the complete `srcs` attribute with Gazelle's `# keep` comment.
Bazel excludes nested Bazel packages from globs.
The extension rejects explicit package-relative Rust sources below another Bazel package.
Declare non-Rust inputs in Cargo metadata.
Library inputs also include the Cargo README when present.
Manifest access requires explicit compiler or runtime inputs and a `CARGO_MANIFEST_PATH` value.

Cargo targets with one configuration do not need context-specific variants.
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
The [metadata schema](src/metadata.schema.json) defines fields and validation rules.
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
Use `--print-build-inputs` to print the resolved workspace bindings as JSON.
Each environment variable maps to its repository-relative Cargo path and resolved Bazel label.
This mode only reads workspace metadata.
Build scripts own resource parsing and validation.

## Manifest access

The generator omits `CARGO_MANIFEST_PATH` and manifest inputs by default.
It preserves explicit metadata, handler contributions, and Gazelle keep attributes.
Existing consumers that read their manifest must opt in before regeneration.
Cargo package environment values and other compiler inputs retain their existing behavior.

For Rust compilation, declare the manifest through resource metadata.

```toml
[package.metadata.rules_rs]
library_compile_data = ["Cargo.toml"]
```

Keep the `rustc_env` attribute on the generated library.
Retain other values from that attribute when the code uses them.

```starlark
rust_library(
    name = "lib",
    rustc_env = {
        "CARGO_MANIFEST_PATH": "$(execpath :Cargo.toml)",
        "CARGO_PKG_DESCRIPTION": "Read the package manifest.",
        "CARGO_PKG_NAME": "manifest-reader",
    },  # keep
)
```

This example shows the attributes to add to the generated rule.
Gazelle continues to maintain its other attributes.
Rust source can then use `include_str!(env!("CARGO_MANIFEST_PATH"))`.
Use `unit_compile_data`, `test_compile_data`, or `build_compile_data` when only that compilation reads the manifest.
Set the matching target's `rustc_env` attribute with `# keep`.
Crate-based unit tests inherit their library's inputs and environment.
Separate Cargo test contexts need their own explicit environment value.
Additional library outputs and independent target variants also need their own kept environment values.
A compiled metadata handler can supply both inputs and environment values through `TargetInputs`.

For build-script execution, use an existing build input.
For a workspace-root package, set `cargo_path` to `Cargo.toml`.
For a nested package, include its repository-relative directory.

```toml
[package.metadata.rules_rs.build_inputs.CARGO_MANIFEST_PATH]
cargo_path = "crates/manifest-reader/Cargo.toml"
bazel_label = ":Cargo.toml"
```

The generator adds the manifest to build-script `data` and sets `build_script_env.CARGO_MANIFEST_PATH` through `execpath`.
This declaration supports a build script that calls `std::env::var("CARGO_MANIFEST_PATH")` during execution.
Each build-input variable needs one mapping across the workspace.
If several packages need different manifests, use package-local `build_data` and a kept `build_script_env` attribute instead.

```toml
[package.metadata.rules_rs]
build_data = ["Cargo.toml"]
```

```starlark
cargo_build_script(
    name = "build-script",
    build_script_env = {
        "CARGO_MANIFEST_PATH": "$(execpath :Cargo.toml)",
    },  # keep
)
```

Retain other build-script environment values when adding this keep attribute.
Build-script compilation needs a separate `build_compile_data` input and a kept `rustc_env` value if its source uses `env!`.
Whole-attribute keep comments also support explicit `compile_data`, `data`, and `build_script_env` declarations.
Keep attributes take ownership of the complete value, including future resource and environment changes.

## Compiled metadata handlers

A consumer can compile additional handlers into its Gazelle binary.
The [handler API](src/handlers.go) defines `MetadataHandler`, `HandlerSpec`, `MetadataContext`, and `Contribution`.
Import `github.com/scalar0/gazelle_rules_rs/src` to use this API.
The [consumer handler](src/integration/metadata/handler.go) shows target selection through these descriptions.

A handler declares one namespace, a JSON Schema, and its additional rule kinds with load information.
Its constructor calls `rulesrs.New(handler)` from the language package's `NewV2` function.
The extension validates registered workspace and package metadata before it calls the handler.
Unregistered metadata namespaces remain untouched.
A missing optional namespace does not call its handler.

Handlers receive separate copies of workspace and package metadata.
Each handler defines its own inheritance policy.
`TargetInfo.Role` identifies a target's purpose, including derived tests.
`TargetInfo.OutputType` identifies its Cargo output type.
Select the required description and use its `BazelTarget` for input contributions.
Handlers do not need to construct generated target names.
Descriptions retain their Cargo role when a public label uses a context alias.
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

## Compatibility and limits

The [module](MODULE.bazel) and [Cargo archive declarations](cargo.MODULE.bazel) define dependency versions and integrity hashes.
The pinned nightly Cargo does not define the minimum Rust compiler version for consumers.
Cargo unit graphs use an unstable interface.
Run the full test suite after a toolchain update.
The generator rejects unknown unit-graph versions, invalid indices, invalid roots, and cycles.

Workspace members and target sources must stay inside the Bazel repository.
Target source roots must stay inside their Cargo package and Bazel package.
A canonical `rlib` supports Bazel dependency edges when Cargo requests only another library output.
Each requested library output type gets a separate target.

Bazel profiles do not reproduce every Cargo profile option.
Manual benchmark targets use the selected Bazel compilation mode.
Built-in unstable Rust benchmarks require a compatible nightly Rust compiler from the consumer.
A nightly Cargo binary alone does not enable unstable compiler features.

## Development and provenance

The [Justfile](Justfile) defines development commands.
The [CI workflow](.github/workflows/ci.yml) runs full validation on Linux and macOS for x86-64 and ARM64.
The [Aspect version file](.aspect/version.axl) pins the CLI for local and CI use.
Run `aspect auth login` to use the shared cache and build reports during local development.
The [workspace Bazel configuration](.bazelrc) enables Aspect Cloud for direct Bazel builds and tests.
Set `GAZELLE_ASPECT_REMOTE=none` to use local execution for Aspect tasks.
Main branch CI uses the repository's `ASPECT_API_TOKEN` secret.
Pull request jobs and staged consumer workspaces run without Aspect Cloud.
Go code uses normal packages under `src/` and standard test filenames.
The official Gazelle Go extension maintains Go BUILD rules.
An internal Cargo adapter owns declared tool resolution and subprocess execution.
The module uses the registry release of `aspect_rules_lint` for Bazel and shell checks.
The formatter npm workspace belongs to `tools/` and does not supply consumer dependencies.

Go integration tests and Rust consumer tests share the same fixture inputs.
Bazel fetches the upstream rules_rs manifest-path fixture without vendoring its source files.
A small Cargo workspace patch makes its nested build script part of our generation input.
That fixture opts in through resource metadata and a kept environment attribute.
The project fixture supplies metadata handlers, generated resources, platform contexts, and additional target types.
Consumer tests stage these inputs before generation and compilation.
The project fixture also uses renamed backend repositories and a registry dependency.

The code originated in `scalar0/kernel-toolchain` at commit `20971ec52ecdca2534c53171944ab4425cc09fc4`.
The [Apache-2.0 license](LICENSE) continues to apply.
The extraction retains the original source notices.
