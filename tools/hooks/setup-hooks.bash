#!/usr/bin/env bash
# Generate Git adapters from the root hook declarations.
set -euo pipefail

fail() {
	printf 'Hook setup failed: %s\n' "$*" >&2
	exit 1
}

for tool in git just jq; do
	command -v "$tool" >/dev/null || fail "Missing required tool: $tool."
done
root=$(git rev-parse --show-toplevel)
cd "$root"
hooks_dir="$(git rev-parse --path-format=absolute --git-common-dir)/hooks"
[[ ! -L "$hooks_dir" ]] || fail 'The hooks directory must not be a symbolic link.'
scratch=$(mktemp -d "${TMPDIR:-/tmp}/gazelle_rules_rs-hooks-XXXXXXXX")
staged=''
trap 'rm -rf -- "$scratch"; if [[ -n "$staged" ]]; then rm -f -- "$staged"; fi' EXIT
just --justfile "$root/Justfile" --dump --dump-format json >"$scratch/recipes.json"
jq -r -f tools/hooks/hook-recipes.jq "$scratch/recipes.json" >"$scratch/names"
hooks=()
while IFS= read -r hook; do hooks+=("$hook"); done <"$scratch/names"
marker=$(sed -n '2p' tools/hooks/hook.sh.in)
[[ -n "$marker" ]] || fail 'The adapter template has no ownership marker.'
owned() {
	[[ -f "$1" && ! -L "$1" ]] && [[ $(sed -n '2p' "$1") == "$marker" ]]
}

# Check every destination before changing any adapter.
for hook in "${hooks[@]}"; do
	path="$hooks_dir/$hook"
	if [[ -e "$path" || -L "$path" ]]; then
		owned "$path" || fail "Unknown file at $path."
	fi
	sed "s/@HOOK@/$hook/g" tools/hooks/hook.sh.in >"$scratch/$hook"
	chmod 755 "$scratch/$hook"
done

mkdir -p "$hooks_dir"
for hook in "${hooks[@]}"; do
	# Create the replacement on the same filesystem for an atomic rename.
	staged=$(mktemp "$hooks_dir/.adapter-XXXXXXXX")
	cp "$scratch/$hook" "$staged"
	chmod 755 "$staged"
	mv -f -- "$staged" "$hooks_dir/$hook"
done
for path in "$hooks_dir"/*; do
	owned "$path" || continue
	keep=false
	for hook in "${hooks[@]}"; do
		[[ "${path##*/}" != "$hook" ]] || keep=true
	done
	if [[ "$keep" == false ]]; then
		rm -- "$path"
	fi
done
printf 'Installed %d Git hook adapters.\n' "${#hooks[@]}"
