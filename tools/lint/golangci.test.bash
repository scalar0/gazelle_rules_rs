#!/usr/bin/env bash
set -euo pipefail

launcher="$(rlocation "$1")"
fixture="$TEST_TMPDIR/workspace"
mkdir -p "$fixture/src" "$TEST_TMPDIR/bin"
cat >"$fixture/go.mod" <<'EOF'
module launcher.test

go 1.26.0
EOF
cat >"$fixture/src/main.go" <<'EOF'
package main

func main() {}
EOF
cat >"$TEST_TMPDIR/bin/go" <<'EOF'
#!/usr/bin/env bash
echo >&2 "ERROR: the launcher used the ambient Go executable."
exit 88
EOF
chmod +x "$TEST_TMPDIR/bin/go"
export BUILD_WORKSPACE_DIRECTORY="$fixture"
export PATH="$TEST_TMPDIR/bin:$PATH"
export GOROOT="$TEST_TMPDIR/missing-sdk"
export GOCACHE="$TEST_TMPDIR/go-cache"
export GOLANGCI_LINT_CACHE="$TEST_TMPDIR/lint-cache"

# A clean module must pass with the selected SDK and the declared configuration.
"$launcher"

# Source tests must also affect the launcher exit status.
cat >"$fixture/src/main_test.go" <<'EOF'
package main

var invalid int = "type error"
EOF
if "$launcher" >"$TEST_TMPDIR/findings" 2>&1; then
	echo >&2 "ERROR: the launcher accepted an invalid source test."
	exit 1
fi
if ! grep -q 'main_test.go' "$TEST_TMPDIR/findings"; then
	cat >&2 "$TEST_TMPDIR/findings"
	echo >&2 "ERROR: the launcher did not report the invalid source test."
	exit 1
fi
