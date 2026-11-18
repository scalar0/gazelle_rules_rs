export DO_NOT_TRACK := "1"

# List the standalone repository commands.
default:
    just --list

# Run the Go unit and integration tests.
test:
    aspect test //src/... --test_output=errors

# Build the standalone targets.
build:
    aspect build //...

# Write .aspect/generated/coverage.lcov after the local Go tests pass.
# Include the fixture runner so it can write coverage metadata.
coverage:
    #!/usr/bin/env bash
    set -euo pipefail
    report_dir=.aspect/generated
    mkdir -p "$report_dir"
    scratch=$(mktemp -d "$report_dir/coverage.XXXXXX")
    trap 'rm -rf -- "$scratch"' EXIT
    aspect test --remote=none --coverage=true \
        --coverage-report="$scratch/report.lcov" \
        --instrumentation_filter='^//src[/:],^//:integration_runner$' //src/...
    mv -f -- "$scratch/report.lcov" "$report_dir/coverage.lcov"
    printf 'Wrote %s\n' "$report_dir/coverage.lcov"

# Stage shared fixture inputs so both suites test the same Cargo source files.
# Generate rules before checking their stability and compiling every consumer target.
consumer-test:
    #!/usr/bin/env bash
    set -euo pipefail
    fixture_root="$(mktemp -d "${TMPDIR:-/tmp}/gazelle-rust-consumer.XXXXXX")"
    trap 'rm -rf -- "$fixture_root"' EXIT
    aspect run //:consumer-test \
        -- --destination="$fixture_root" --extension-root="{{ justfile_directory() }}"
    for fixture in owned upstream; do
        (
            cd "$fixture_root/$fixture"
            # Consumers do not inherit development dependencies from the extension.
            bazel --ignore_all_rc_files query '@gazelle_rules_rs//:all'
            aspect --ignore_all_rc_files run --remote=none //:gazelle -- -mode=fix .
            aspect --ignore_all_rc_files run --remote=none //:gazelle -- -mode=diff .
            aspect --ignore_all_rc_files test --remote=none //... //:benchmarks --test_output=errors
        )
    done

# Check Go BUILD rules with the official Go extension after source or dependency changes.
go-check:
    aspect run //:gazelle_go

# Check the Bzlmod lockfile without changing it.
bazel-mod-lock-check:
    aspect mod -- deps

# Update the lockfile after a Bazel dependency changes.
bazel-mod-lock-update:
    aspect mod -- --lockfile_mode=update deps

# Validate Git source files with the same Taplo tool as the formatter.
toml-check:
    #!/usr/bin/env bash
    set -euo pipefail
    git ls-files --cached --others --exclude-standard -z -- '*.toml' \
    | xargs -0 aspect run --run-in=bazel-root //tools:taplo -- lint --config=.taplo.toml

# Update the formatter lockfile with the pinned pnpm tool.
# Apply the repository's YAML format after pnpm writes the lockfile.
[env("BAZEL_BINDIR", ".")]
npm-lock-update:
    aspect run --run-in=bazel-root @pnpm//:pnpm -- \
    --dir tools install --lockfile-only --ignore-scripts
    aspect run --run-in=bazel-root //tools:prettier -- --write tools/pnpm-lock.yaml

# Format source files through the same Aspect task used by Git hooks.
fmt:
    aspect format-sources --scope=all
    just --fmt --unstable

# Check all source files and the Justfile without changes.
fmt-check:
    aspect format-check
    just --fmt --check --unstable

# Check Bazel files across the checkout, then lint Bazel and shell targets.
lint:
    aspect run //tools/lint:buildifier_check
    aspect lint-all //...

# Check source formats, the Bzlmod lockfile, TOML, and Go BUILD files.
check: fmt-check bazel-mod-lock-check toml-check go-check

# Run all development checks in the same order as CI.
check-full: check lint build test consumer-test git-hooks-test

# Run full validation before a push.
check-push: check-full

# Generate adapters from root recipes tagged with group('hooks'). Recipe names become Git hook names.
# Git stores adapters in its shared hooks directory, normally .git/hooks.
# Run again after adding, removing, or renaming hook declarations.
# Setup requires Bash, Git, just, and jq. Adapters require Git and just on PATH.
# Linked worktrees share adapters. Each adapter selects the active worktree Justfile.
# Setup uses the default hooks path without changing Git configuration.
# Unknown adapter files stop setup.
# Subtree hook recipes run only when a root hook explicitly calls them.
[doc("Install or refresh adapters in Git's hooks directory.")]
[group('git')]
git-setup:
    bash tools/hooks/setup-hooks.bash

# Test hook setup, Git inputs, message validation, and linked worktrees in temporary repositories.
# Tests require Bash, Git, just, jq, Perl, and ripgrep.
[group('git')]
git-hooks-test:
    bash tools/hooks/test.bash

alias git-p := git-patch

# Save a patch in the current worktree's Git metadata directory.
[arg('args', help='Git diff arguments, including revisions and path selection')]
[group('git')]
[positional-arguments]
git-patch *args:
    git diff -p "$@" > "$(git rev-parse --git-path patch.diff)"

# Validate and clean a commit message. The default file is .gitcommit in the repository root.
[arg('file', help='Path to the commit message file')]
[group('git')]
[positional-arguments]
git-commit-msg file=(justfile_directory() / '.gitcommit'):
    perl tools/hooks/commit-msg.pl "$1"

# Validate the message file supplied by Git.
[group('hooks')]
[no-exit-message]
commit-msg file: (git-commit-msg file)

# Delete the root commit message file after a commit.
[group('hooks')]
[no-exit-message]
post-commit:
    rm -f -- "{{ justfile_directory() / '.gitcommit' }}"

# Reject format failures before a commit. Report lint failures without blocking the commit.
# Check the full current checkout, including unstaged changes.
[group('hooks')]
[no-exit-message]
pre-commit: fmt-check _commit-lint

# Apply the same format and lint policy to merge commits.
[group('hooks')]
[no-exit-message]
pre-merge-commit: pre-commit

# Keep lint execution failures visible without blocking a commit.
[private]
_commit-lint:
    aspect lint-all --strategy=soft //... \
    || { status=$?; printf 'Warning: Lint checks failed with exit code %s. The commit can continue.\n' "$status" >&2; }

# Check generation, format, lint, extension tests, consumer tests, and hook tests before a push.
# Git supplies the remote name, location, and reference data on `stdin`.
# Empty argument defaults permit manual invocation. Git supplies these arguments to the hook.
[doc('Check generation, format, lint, extension tests, consumer tests, and hook tests before a push.')]
[group('hooks')]
[no-exit-message]
pre-push remote='' location='': check-push
