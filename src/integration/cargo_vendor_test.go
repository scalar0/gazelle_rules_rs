package rulesrs_test

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

func TestCargoVendorOverride(t *testing.T) {
	cargo, err := runfiles.Rlocation(*cargoPath)
	if err != nil {
		t.Fatal(err)
	}
	vendorConfig, err := runfiles.Rlocation(*vendorConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	rustcManifest, err := runfiles.Rlocation(*rustcPath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(rustcManifest)
	if err != nil {
		t.Fatal(err)
	}
	rustc, err := runfiles.Rlocation(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	sysrootManifest, err := runfiles.Rlocation(*sysrootPath)
	if err != nil {
		t.Fatal(err)
	}
	sysrootData, err := os.ReadFile(sysrootManifest)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := runfiles.Rlocation(strings.TrimSpace(string(sysrootData)))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for file, text := range map[string]string{
		"Cargo.toml": `[package]
name = "vendor-override"
version = "0.1.0"
edition = "2024"

[package.metadata.rules_rs.build_inputs.PROBE_VENDOR_INPUT]
cargo_path = "input.txt"
bazel_label = ":input.txt"
`,
		".cargo/config.toml": "[env]\n# BEGIN GENERATED rules_rs vendor inputs\n# END GENERATED rules_rs vendor inputs\n",
		"src/lib.rs":         `pub const CONTENT: &str = include_str!(env!("PROBE_VENDOR_INPUT"));`,
		"input.txt":          "default",
	} {
		path := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(success bool, override, tool string, args ...string) (stdout, stderr string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), tool, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CARGO_NET_OFFLINE=true", "RUSTC="+rustc, "CARGO_HOME="+filepath.Join(root, ".cargo"), "PATH="+t.TempDir(), "CARGO_ENCODED_RUSTFLAGS=--sysroot\x1f"+filepath.Dir(anchor))
		if override != "" {
			cmd.Env = append(cmd.Env, "PROBE_VENDOR_INPUT="+override)
		}
		var diagnostics strings.Builder
		cmd.Stderr = &diagnostics
		output, err := cmd.Output()
		if (err == nil) != success {
			t.Fatalf("Unexpected Cargo fixture result: %v\n%s%s", err, output, diagnostics.String())
		}
		return string(output), diagnostics.String()
	}
	run(true, "", cargo, "generate-lockfile", "--offline")
	exported, _ := run(true, "", vendorConfig, "--root", root, "--print-build-inputs")
	var bindings map[string]map[string]string
	if err := json.Unmarshal([]byte(exported), &bindings); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"cargo_path": "input.txt", "bazel_label": "//:input.txt"}
	if len(bindings) != 1 || !maps.Equal(bindings["PROBE_VENDOR_INPUT"], want) {
		t.Fatalf("Unexpected resolved input bindings: %s", exported)
	}
	run(false, "", vendorConfig, "--root", root, "--print-build-inputs", "--write")
	run(false, "", vendorConfig, "--root", root, "--check")
	run(true, "", vendorConfig, "--root", root, "--write")
	run(true, "", cargo, "check", "--locked", "--offline")
	alternate := filepath.Join(root, "alternate.txt")
	if err := os.WriteFile(alternate, []byte{0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, diagnostic := run(false, alternate, cargo, "check", "--locked", "--offline")
	if !strings.Contains(diagnostic, "utf-8") {
		t.Fatalf("Cargo did not select the explicit environment override: %s", diagnostic)
	}
	run(true, "", cargo, "check", "--locked", "--offline")
}
