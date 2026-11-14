#!/usr/bin/env bash

# Initialize the maintained rules_shell runfiles library.
set -uo pipefail
set +e
f=bazel_tools/tools/bash/runfiles/runfiles.bash
# shellcheck disable=SC1090
source "${RUNFILES_DIR:-/dev/null}/$f" 2>/dev/null || \
  source "$(grep -sm1 "^$f " "${RUNFILES_MANIFEST_FILE:-/dev/null}" | cut -f2- -d' ')" 2>/dev/null || \
  source "$0.runfiles/$f" 2>/dev/null || \
  source "$(grep -sm1 "^$f " "$0.runfiles_manifest" | cut -f2- -d' ')" 2>/dev/null || \
  source "$(grep -sm1 "^$f " "$0.exe.runfiles_manifest" | cut -f2- -d' ')" 2>/dev/null || \
  { echo >&2 "ERROR: cannot find $f"; exit 1; }
set -e

if [[ -z "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then
  echo >&2 "ERROR: run this target with bazel run or aspect lint-go."
  exit 1
fi

binary="$(rlocation '@@BINARY@@')"
config="$(rlocation '@@CONFIG@@')"
sdk_root="$(rlocation '@@SDK_ROOT@@')"
export GOROOT="${sdk_root%/ROOT}"
export PATH="$GOROOT/bin:$PATH"
export GOTOOLCHAIN=local
export GOENV=off
export GOWORK=off
cd "$BUILD_WORKSPACE_DIRECTORY"
exec "$binary" run --config "$config" "$@" ./src/...
