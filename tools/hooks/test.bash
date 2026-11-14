#!/usr/bin/env bash
# Test the public recipes and adapters in isolated Git repositories.
set -euo pipefail
root=$(cd -- "${BASH_SOURCE[0]%/*}/../.." && pwd -P)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/gazelle_rules_rs-git-test-XXXXXXXX")
scratch=$(cd -- "$scratch" && pwd -P)
trap 'rm -rf -- "$scratch"' EXIT
export JUST_TEMPDIR="$scratch"
export GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL=/dev/null
# Hooks can export repository locations. Do not pass them to the test repositories.
while IFS= read -r name; do unset "$name"; done < <(git rev-parse --local-env-vars)
unset GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT
export GIT_AUTHOR_NAME='Hook Test' GIT_COMMITTER_NAME='Hook Test'
export GIT_AUTHOR_EMAIL='hook@example.invalid' GIT_COMMITTER_EMAIL='hook@example.invalid'
export GAZELLE_RULES_RS_HOOK_LOG="$scratch/calls.jsonl"
export GAZELLE_RULES_RS_HOOK_INPUT="$scratch/input"
out="$scratch/stdout"
err="$scratch/stderr"
checks=0
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	cat "$out" "$err" >&2
	exit 1
}
run() {
	local expected=$1 actual=0
	shift
	"$@" >"$out" 2>"$err" || actual=$?
	[[ "$actual" == "$expected" ]] || fail "Expected exit $expected. Got $actual."
	checks=$((checks + 1))
}
assert_calls() {
	jq -es "$1" "$GAZELLE_RULES_RS_HOOK_LOG" >/dev/null || fail "$1"
}
mkdir -p "$scratch/bin" "$scratch/repo with spaces/tools/hooks"
cat >"$scratch/bin/probe" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
jq -cn --arg directory "$PWD" --arg editor "${GIT_EDITOR:-}" --args \
    '{directory:$directory,editor:$editor,argv:$ARGS.positional}' -- "$@" >> "$GAZELLE_RULES_RS_HOOK_LOG"
if [[ ${1:-} == input ]]; then cat > "$GAZELLE_RULES_RS_HOOK_INPUT"; fi
if [[ ${1:-} == lint-go ]]; then exit "${GAZELLE_RULES_RS_GO_LINT_EXIT:-0}"; fi
exit "${GAZELLE_RULES_RS_HOOK_EXIT:-0}"
SH
chmod +x "$scratch/bin/probe"
ln -s probe "$scratch/bin/aspect"
export PATH="$scratch/bin:$PATH"
repo="$scratch/repo with spaces"
cp "$root"/tools/hooks/{setup-hooks.bash,hook-recipes.jq,hook.sh.in,commit-msg.pl} "$repo/tools/hooks/"
cat >"$repo/Justfile" <<'JUST'
set default-list := true
mod child

fmt-check:
    probe quick

check-push:
    probe input
JUST
# Use the real public recipes while replacing only the expensive validation work.
for recipe in git-setup git-commit-msg git-patch commit-msg post-commit _commit-lint pre-commit pre-merge-commit pre-push; do
	just --justfile "$root/Justfile" --show "$recipe" >>"$repo/Justfile"
	printf '\n' >>"$repo/Justfile"
done
mkdir "$repo/child"
cat >"$repo/child/Justfile" <<'JUST'
[group('hooks')]
post-checkout:
    false
JUST
cd "$repo"
git -c core.fsmonitor=false init -q
git config core.fsmonitor false
mkdir -p .git/hooks
hooks_dir="$repo/.git/hooks"
# Setup must remove the generated hook whose recipe no longer exists.
sed 's/@HOOK@/post-applypatch/g' tools/hooks/hook.sh.in >"$hooks_dir/post-applypatch"
chmod +x "$hooks_dir/post-applypatch"
run 0 just git-setup
[[ -z $(git config --local --get core.hooksPath || true) ]] || fail 'Wrong hooks path.'
[[ ! -e "$hooks_dir"/post-applypatch && ! -e "$hooks_dir"/post-checkout && ! -e "$hooks_dir"/check-quick ]] || fail 'Generated an undeclared hook.'
[[ $(rg --files --hidden --no-ignore -g '!*.sample' "$hooks_dir" | wc -l) -eq 5 ]] || fail 'Wrong adapter count.'
for hook in commit-msg post-commit pre-commit pre-merge-commit pre-push; do
	[[ -x "$hooks_dir"/$hook ]] || fail "Adapter is not executable: $hook."
done
cmp tools/hooks/hook.sh.in "$root/tools/hooks/hook.sh.in" || fail 'Setup changed the adapter template.'
[[ ! -e tools/hooks/pre-commit ]] || fail 'Setup generated an adapter outside Git metadata.'
cp "$hooks_dir"/pre-commit "$scratch/first-adapter"
run 0 just git-setup
cmp "$hooks_dir"/pre-commit "$scratch/first-adapter" || fail 'Setup changed an unchanged adapter.'

: >"$GAZELLE_RULES_RS_HOOK_LOG"
run 0 git hook run pre-commit
run 0 git hook run pre-merge-commit
assert_calls 'map(.argv) == [["quick"],["lint-all","--strategy=soft","//..."],["lint-go"],["quick"],["lint-all","--strategy=soft","//..."],["lint-go"]]'
# Go lint failures must remain advisory. The existing failure check stops at formatting.
GAZELLE_RULES_RS_GO_LINT_EXIT=37 run 0 git hook run pre-commit
rg -q 'Go lint failed with exit code 37' "$err" || fail 'The hook hid a Go lint failure.'
GAZELLE_RULES_RS_HOOK_EXIT=37 run 37 git hook run pre-commit
printf 'refs/heads/main abc refs/heads/main def\n' >"$scratch/refs"
: >"$GAZELLE_RULES_RS_HOOK_LOG"
run 0 git hook run --to-stdin="$scratch/refs" pre-push -- 'remote name' 'ssh://host/path with spaces'
cmp "$scratch/refs" "$GAZELLE_RULES_RS_HOOK_INPUT" || fail 'The adapter changed the stdin.'
assert_calls 'length == 1 and .[0].argv == ["input"]'
run 0 just pre-push <"$scratch/refs"

# A declared hook with parameters checks quoting and environment forwarding.
cp Justfile "$scratch/base-Justfile"
cat >>Justfile <<'JUST'

[group('hooks')]
[positional-arguments]
post-checkout *args:
    probe "$@"
JUST
run 0 just git-setup
: >"$GAZELLE_RULES_RS_HOOK_LOG"
GIT_EDITOR=: run 0 git hook run post-checkout -- 'a b' '' '--literal' "\$(false)"
# The command substitution text must remain literal in the jq query.
# shellcheck disable=SC2016
assert_calls 'length == 1 and .[0].argv == ["a b", "", "--literal", "$(false)"] and .[0].editor == ":"'
cp "$scratch/base-Justfile" Justfile
printf 'Keep this file.\n' >"$hooks_dir"/local-note
run 0 just git-setup
[[ ! -e "$hooks_dir"/post-checkout && -f "$hooks_dir"/local-note ]] || fail 'Wrong stale adapter cleanup.'

# A conflict must stop setup before it changes adapters.
cp "$hooks_dir"/pre-commit "$scratch/owned-pre-commit"
printf 'Unknown hook.\n' >"$hooks_dir"/pre-commit
run 1 just git-setup
[[ $(cat "$hooks_dir"/pre-commit) == 'Unknown hook.' ]] || fail 'Setup overwrote an unknown hook.'
cp "$scratch/owned-pre-commit" "$hooks_dir"/pre-commit
run 0 just git-setup

# Parse failures and invalid hook names must preserve existing adapters.
cp "$hooks_dir"/pre-commit "$scratch/before-error"
printf '\ninvalid syntax\n' >>Justfile
run 1 just git-setup
cmp "$hooks_dir"/pre-commit "$scratch/before-error" || fail 'Setup changed adapters after a parse error.'
cp "$scratch/base-Justfile" Justfile
printf '\n[group("hooks")]\ninvalid_hook:\n    true\n' >>Justfile
run 5 just git-setup
cmp "$hooks_dir"/pre-commit "$scratch/before-error" || fail 'Setup changed adapters after an invalid hook name.'
cp "$scratch/base-Justfile" Justfile

# Symbolic links at adapter paths must not redirect setup writes.
mv "$hooks_dir"/pre-commit "$scratch/link-target"
ln -s "$scratch/link-target" "$hooks_dir"/pre-commit
run 1 just git-setup
cmp "$scratch/link-target" "$scratch/before-error" || fail 'Setup wrote through a symbolic link.'
rm "$hooks_dir"/pre-commit
mv "$scratch/link-target" "$hooks_dir"/pre-commit

# Keep the existing message checks and cleanup through the Git adapter.
message="$scratch/message with spaces"
printf 'feat: add hook tests\n\n# Remove this comment.\n' >"$message"
run 0 git hook run commit-msg -- "$message"
! rg -q '^#' "$message" || fail 'The validator did not clean the message.'
printf 'Invalid message\n' >"$message"
run 1 git hook run commit-msg -- "$message"
for prefix in 'Merge ' 'Revert ' 'fixup! ' 'squash! '; do
	printf '%smessage\n' "$prefix" >"$message"
	run 0 git hook run commit-msg -- "$message"
done
printf 'fix: check default message\n' >.gitcommit
run 0 just git-commit-msg
run 1 git hook run commit-msg -- "$scratch/missing-message"

# Exercise real commit hooks and the metadata path in a linked worktree.
git add Justfile child tools
run 0 git commit -qm 'test: add hook fixture'
[[ ! -e .gitcommit ]] || fail 'The commit did not delete the root message file.'
printf 'Keep the original message.\n' >.gitcommit
run 0 git worktree add -qb hook-test "$scratch/linked worktree"
cd "$scratch/linked worktree"
[[ "$(git rev-parse --path-format=absolute --git-common-dir)/hooks" == "$hooks_dir" ]] || fail 'Worktrees must share adapters.'
run 0 just git-setup
mkdir -p nested
cd nested
: >"$GAZELLE_RULES_RS_HOOK_LOG"
run 0 git hook run pre-commit
assert_calls 'length == 3 and all(.[]; .directory | endswith("/linked worktree"))'
printf 'Keep the nested message.\n' >.gitcommit
printf 'Delete the worktree message.\n' >../.gitcommit
run 0 git hook run post-commit
[[ ! -e ../.gitcommit ]] || fail 'The hook did not delete the worktree message file.'
[[ -f .gitcommit && -f "$repo/.gitcommit" ]] || fail 'The hook deleted another message file.'
run 0 git hook run post-commit
cd ..
printf '\n# Worktree change.\n' >>Justfile
run 0 just git-patch
[[ -s $(git rev-parse --git-path patch.diff) ]] || fail 'The worktree patch is missing.'
# A missing runner must fail instead of silently skipping validation.
mkdir "$scratch/without-just"
ln -s "$(command -v git)" "$scratch/without-just/git"
PATH="$scratch/without-just" run 127 /bin/sh "$hooks_dir"/pre-commit

printf 'PASS: %d Git hook checks.\n' "$checks"
