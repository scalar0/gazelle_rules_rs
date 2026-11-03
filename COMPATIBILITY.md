# Compatibility and release requirements

The extension is ready for private adoption tests.
A public release requires successful CI results for all supported hosts and review of the remaining limits.

| Component | Pinned version |
| --- | --- |
| Bazel | 9.3.0 |
| Aspect CLI | 2026.44.6 |
| Gazelle | 2.0.0-beta.2 |
| rules_rs | 0.2.1 |
| rules_go | 0.65.0 |
| LLVM module | 0.8.26 |
| Test Rust compiler | 1.99.1 |
| Generation Cargo | Nightly 2026-11-01 |
| Cargo metadata format | 1 |
| Cargo unit graph | 1 |

The [module](MODULE.bazel) and [Cargo archive declarations](cargo.MODULE.bazel) own dependency versions and integrity hashes.
This table records the tested combination.
It does not promise compatibility with untested versions.

| Host | CI runner | Local evidence |
| --- | --- | --- |
| Linux x86_64 | ubuntu-24.04 | Standalone tests and consumer builds pass |
| Linux AArch64 | ubuntu-24.04-arm | CI required |
| macOS AArch64 | macos-15 | CI required |
| macOS x86_64 | macos-15-intel | CI required |

## Gazelle alignment

The implementation follows the [upstream extension guide](https://github.com/bazel-contrib/bazel-gazelle/blob/master/extend.md) and [v2 API](https://github.com/bazel-contrib/bazel-gazelle/blob/master/v2.md).
The pinned API source defines the exact interfaces used by this release candidate.

| Requirement | Implementation and evidence |
| --- | --- |
| Native v2 API | NewV2, Kinds, Generate, Configure, Fix, OnStart, and OnResolve |
| Context cancellation | Cargo commands use CommandContext |
| Critical diagnostics | Preflight errors stop generation before writes |
| Explicit loads | Kinds declares public rule load labels |
| Standard merge behavior | Generated expressions use the public merge interface |
| User ownership | Handwritten conflicts fail and keep comments remain effective |
| No unnecessary resolver | Cargo and public backend helpers supply dependency information |
| Independent reuse | A separate Bzlmod consumer compiles the public language library |
| Deterministic output | Repeated generation and diff checks |
| Negative tests | Invalid schemas, conflicts, stale rules, and unchanged files after errors |
| Workspace layout | Root packages, dependencies on root packages, and nested workspaces without scope flags |

## Supported behavior and limits

The tests cover libraries, binaries, procedural macros, build scripts, unit tests, integration tests, examples, benchmarks, and doctests.
Library outputs include rlib, dylib, cdylib, and staticlib.
A canonical rlib supports Bazel dependency edges even when Cargo requests only another library output.
The requested output types receive separate targets.

Bazel profiles do not reproduce every Cargo profile option.
Manual benchmark targets use the selected Bazel compilation mode.
Built-in unstable Rust benchmarks require a compatible nightly Rust compiler from the consumer.
A nightly Cargo binary alone does not enable unstable compiler features.
The consumer smoke benchmark uses a stable custom harness.

The extension processes one Cargo workspace per invocation.
Workspace members and target sources must remain inside the Bazel repository.
Rust source references must remain inside their Cargo package.
Source discovery follows literal module paths and include paths, including paths outside the target directory.
Source discovery rejects references across nested Bazel package boundaries.
Macro expansion and computed source paths require explicit `srcs` with a Gazelle `# keep` comment.
Conditional branches share one source list across supported platforms.

The backend owns external feature resolution and platform-specific dependency helpers.
The generator rejects unknown unit-graph versions and invalid graph indices, roots, or cycles.
Cargo unit graphs remain unstable, so toolchain upgrades require the full compatibility suite.
The pinned nightly Cargo is a generation tool, not an MSRV promise for consumers.

## Release procedure

1. Review the standalone and consumer test results on all four hosts.
2. Confirm that repeated consumer generation produces no diff.
3. Review dependency updates and generated output changes.
4. Update this report for the tested dependency combination.
5. Review Apache-2.0 notices and archive contents.
6. Set the module version for the release.
7. Create a signed release tag after release approval.
8. Publish an archive with its integrity hash after publication approval.

The private repository has no registry publication or release automation that changes visibility.
Consumers should pin a reviewed commit until a release exists.
