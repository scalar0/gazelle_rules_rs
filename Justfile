export DO_NOT_TRACK := "1"

# List the standalone repository commands.
default:
    just --list

# Run the extension tests without parent workspace configuration or remote services.
test:
    aspect --ignore_all_rc_files test //:rules_rs_test //:rules_rs_integration_test --remote_cache= --remote_executor= --bes_backend= --test_output=errors

# Check generation and build Rust targets through an external module dependency.
[working-directory("tests/consumer")]
consumer-test:
    aspect --ignore_all_rc_files gazelle --remote_cache= --remote_executor= --bes_backend= .
    aspect --ignore_all_rc_files test //... //:root-probe-bench-test //crates/root-user:root-user-bench-test //crates/probe:probe-benches/smoke //crates/probe:probe-bench-test //crates/probe:probe-bin-bench-test //crates/macros:macros-bench-test //crates/dynamic:dynamic-bench-test --remote_cache= --remote_executor= --bes_backend= --test_output=errors

# Format source files through the same Aspect task used by Git hooks.
fmt:
    aspect format-sources --scope=all
    just --fmt --unstable

# Check all source files and the Justfile without changes.
fmt-check:
    aspect format-check
    just --fmt --check --unstable

# Check all declared Bazel and shell targets.
lint:
    aspect lint-all //...

# Check generation, format, lint, tests, and hook behavior before a push.
check-push: fmt-check lint test consumer-test git-hooks-test

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

# Reject format failures before a commit. Report Bazel lint failures without blocking the commit.
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
    aspect lint-all --strategy=soft //... || { status=$?; printf 'Warning: Lint checks failed with exit code %s. The commit can continue.\n' "$status" >&2; }

# Check generation, format, lint, extension tests, consumer tests, and hook tests before a push.
# Git supplies the remote name, location, and reference data on `stdin`.
# Empty argument defaults permit manual invocation. Git supplies these arguments to the hook.
[doc('Check generation, format, lint, extension tests, consumer tests, and hook tests before a push.')]
[group('hooks')]
[no-exit-message]
pre-push remote='' location='': check-push
