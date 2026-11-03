# Gazelle extension for rules_rs

The native Go extension generates direct `rules_rs` declarations from Cargo metadata.
The caller selects immediate crate packages with the `rules_rs_crate_scope` flag.
The runner loads this extension and Gazelle's built-in package defaults.
The [module](MODULE.bazel) pins Gazelle.
The runner uses the native Gazelle v2 interface.

Cargo owns package metadata, targets, dependency resolution, features, and lint settings.
The extension reads `cargo metadata --locked` through the public Cargo interface.
For local features, it also reads Cargo build and test unit graphs for each configured platform.
The runner supplies a pinned nightly Cargo binary because the public unit-graph interface remains unstable.
The Rust compiler stays on the existing stable toolchain.

The generated rules call the public `@crates` helpers for dependencies, aliases, crate names, editions, and lint settings.
The existing `rules_rs` extension still resolves dependency kinds and platform conditions.
The consumer root module owns platform configuration.
Cargo dependency kinds filter local aliases for each target.
This prevents development aliases from creating production dependency cycles through test-support crates.
Context rules replace canonical local aliases with their selected context labels.
Public library targets alias their selected Cargo units.
This prevents duplicate Rust type identities when callers combine public targets.
Targets with a different Cargo edition receive that edition as an explicit attribute.
Their unit tests use the same edition.

The extension uses Gazelle's public expression and merge interfaces to emit helper calls and loads.
It does not parse generated `rules_rs` data files.
It does not resolve dependencies or rewrite output after Gazelle.
The workspace uses direct `rules_rs` targets and has no crate-wrapper dependency.

## Ownership

| Input                                | Generated output                                                                        |
| ------------------------------------ | --------------------------------------------------------------------------------------- |
| Cargo target metadata                | Libraries, binaries, build scripts, unit tests, and integration tests                   |
| Cargo target `harness` setting       | Test harness selection                                                                  |
| Conventional Rust source directories | Library and binary globs, and individual integration-test sources                       |
| Cargo unit graphs                    | Context-specific features, local dependencies, aliases, and required-feature conditions |
| Cargo package metadata               | Version, package name, and package description                                          |
| Public `@crates` helpers             | Dependencies, aliases, crate name, edition, and lint configuration                      |
| Manifest resource metadata           | Custom resource inputs, build-script inputs, and environment mappings                   |

Each generated rule has a marker comment.
Gazelle removes stale rules with that marker.
Generation fails if a Cargo target name conflicts with a handwritten rule.
Generated rules can change kind, unless a `# keep` comment prevents the change.

Handwritten resource, schema, grammar, metadata, and corpus declarations remain in their existing BUILD files.
Integration test labels use `<base>-integration-tests/<Cargo-target-name>`.
The suite retains the `<base>-integration-tests` label.
Fixture BUILD files stay outside the generation scope.
Generated targets inherit the repository or package default visibility.
New BUILD files do not repeat the repository defaults.
Existing package settings remain unchanged.

Library sources use `glob(["src/**/*.rs"], exclude = ["src/main.rs"])`.
Bazel updates this source set when files change and excludes nested Bazel packages.
Library source additions and removals do not require regeneration.
The generator adds custom crate roots outside the glob as explicit sources.
Each integration test declares its Cargo root as its only Rust source.
Shared helpers belong to test-support crates referenced through Cargo dev-dependencies.
Compiler inputs include the native Cargo `package.readme` file when present.

Feature selections use the literal `platform_triples` list from the root `crates` module-extension call.
The generator does not keep a second platform list.
Targets with `required-features` become incompatible on platforms without those features.

Generated Rust rules retain `CARGO_PKG_NAME` because the Rust backend otherwise uses the Bazel target name.
Integration tests receive binary runfiles and `CARGO_BIN_EXE_*` compiler environment values.
With local features, Cargo's test unit graph selects the binary context.
The environment values use Bazel root paths relative to the workspace runfiles directory.
Tests that change their working directory must resolve these paths before the change.
The generator reads `harness = false` from Cargo target tables because Cargo metadata omits this setting.
It emits `use_libtest_harness = False` for those tests.

Local libraries, procedural macros, and build scripts receive direct declarations for their Cargo build contexts.
Each context has fixed features and local dependency labels.
Native aliases preserve build-script and procedural-macro labels and select a context before the execution transition.
The [contexts implementation](contexts.go) owns this generation mechanism.
Gazelle removes obsolete context declarations.

The required `-rules_rs_crate_scope` flag accepts comma-separated workspace-relative directories that contain crate packages.
The runner rejects generation arguments outside those directories and their immediate crate directories.
The extension has no default project scope.
The caller owns project scopes and generation flags.
Callers supply these values to the Bazel runner.
The [options implementation](options.go) owns this generation mechanism.

## Manifest resource metadata

Declare extra inputs in `[package.metadata.rules_rs]`.
Use `[workspace.metadata.rules_rs]` for shared defaults.
The [metadata schema](metadata.schema.json) defines all fields and path rules.
Consumers can reference the metadata schema from their Cargo editor schema.
The shared Go reader validates these declarations before either generator writes files.
Unknown fields and legacy `rules_rs` BUILD directives cause an error.

An omitted package field uses the workspace default.
An explicit field replaces that default, including empty lists and maps.
Unit tests inherit library compiler inputs and add their own extra inputs.
Libraries, binaries, unit tests, and integration tests include the native Cargo README.
Other Markdown files require explicit declarations.

```toml
[package.metadata.rules_rs]
library_compile_data = ["resources/*"]
unit_compile_data = ["tests/unit-fixture.json"]
test_data = ["test_data/**"]

[package.metadata.rules_rs.build_inputs.MODEL_SCHEMA]
bazel_label = "//schemas:ModelInterchange.json"
cargo_path = "vendor/model/ModelInterchange.json"
```

Resource lists accept Bazel labels, package-relative file paths, and package-relative glob patterns.
Build inputs pair a repository-relative Cargo path with a Bazel label for one file.
The generator adds each label to build-script runtime inputs and sets its environment variable with `execpath`.
Feature-specific build scripts receive the same input mappings.
Identical workspace-wide environment mappings share one Cargo default.
Conflicting mappings cause an error that identifies both manifests.
Generation does not require vendor files to exist.

The Rust generation workflow updates BUILD files and the marked vendor block in Cargo configuration.
Check mode reports stale outputs without changes.
The Cargo generator preserves configuration outside that block and rejects conflicting manual environment entries.
Generated Cargo defaults retain environment overrides.

The dependency override preserves the standalone language-layer fixture without generating its BUILD file.
It replaces matching dependency and alias labels after the public helper selects Cargo dependencies.

## Build-script resources

Declare build-script runtime inputs in `package.metadata.rules_rs.build_data`.
Include package and workspace manifests when a build library reads them.
Use filegroups for shared resource directories so Bazel tracks file additions and removals.
Gazelle copies these declarations into `cargo_build_script.data`, including feature-specific build scripts.
Build scripts own resource interpretation and validation.
Gazelle does not inspect other Cargo metadata namespaces or run project-specific generators.

## Build-script lint compatibility

The [lint adapter](lints.go) owns compatibility with the pinned build-script macro.

## Limits

Generation requires the declared Rust compiler and a current Cargo lockfile.
The runner supplies Cargo archives pinned by date and SHA-256 for Linux and Darwin on x86_64 and AArch64.
The extension supports conventional libraries, binaries, build scripts, and integration tests.
Other Cargo target kinds cause an error.
Examples need more generator support.

The unit-graph reader accepts version 1 and rejects invalid dependency indices, roots, and cycles.
Context dependencies on workspace packages outside the configured scope cause an error.
External dependency features and platform conditions remain under the existing `rules_rs` resolver.

The Go module lists external Go packages for Bzlmod.
The root Bazel module supplies Gazelle itself.
The [Justfile](Justfile) owns development commands and checks.

## Regression fixtures

Unit tests exercise expression merges, generated-rule deletion, kind changes, name conflicts, package visibility, and lint inheritance.
The integration test uses pinned Cargo and the actual Gazelle runner.
Its checked-in workspace covers normal, development, and build dependencies, renamed dependencies, default features, test-support crates, and build-script environment values.
The test-support crate depends on its consumer to check that development aliases do not create Bazel dependency cycles.
It also covers platform features, target edition overrides, required features, separate host features, and procedural-macro features.
Both a standard integration test and a custom test harness start a generated binary through `CARGO_BIN_EXE_probe`.

The integration test also checks stale detection, repeated generation, test removal, and unchanged files after errors.
It then moves the crates to `project/components` and generates new BUILD files with that configured scope.
The fixture uses only local Cargo dependencies and runs Cargo offline.

The fixture includes Bazel module and BUILD templates for a separate `rules_rs` build check.
The templates do not create additional packages in the production workspace.

## Development status

This repository contains the extension extracted from `scalar0/kernel-toolchain` commit `20971ec52ecdca2534c53171944ab4425cc09fc4`.
The [Apache-2.0 license](LICENSE) continues to apply.
The repository is private and has no public release.

The extension has a standalone Bzlmod module and a public `//:rules_rs` language library.
A custom `gazelle_binary` can include that library in its `languages` list.
Consumers must register the Rust toolchain that generation will use.
Bazel supplies Cargo and Rust compiler runfile paths during the build.
The optional `//:vendor-config` executable remains separate from Gazelle.

The current generation contract still requires an explicit immediate-child crate scope and a root Cargo workspace.
Platform discovery still requires a literal `crates.from_cargo` declaration in the consumer module.
Full Cargo target support and external metadata handlers remain required before public adoption.

## Adoption gate

The standalone unit and integration tests pass on Linux x86_64.
The separate [consumer fixture](tests/consumer/MODULE.bazel) generates, builds, and tests a Rust library through the external language library.
These checks do not establish full Cargo compatibility or support for other hosts.

The pinned rules_rs dependency does not expose a public machine-readable platform configuration.
Its generated dependency helpers retain the platform list in a private symbol.
A public backend configuration interface must supply that list before the extension can remove MODULE syntax restrictions without duplicate configuration.
Public adoption remains blocked on that interface and the remaining target and handler work.
