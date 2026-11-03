package rulesrs_test

import (
	"bytes"
	"flag"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

var (
	runnerPath       = flag.String("gazelle", "", "Gazelle runner runfile")
	fixturePath      = flag.String("fixture", "", "Cargo fixture manifest runfile")
	cargoPath        = flag.String("cargo", "", "Pinned Cargo runfile")
	rustcPath        = flag.String("rustc", "", "Declared Rust compiler manifest")
	sysrootPath      = flag.String("sysroot", "", "Declared Rust standard library manifest")
	vendorConfigPath = flag.String("vendor-config", "", "Cargo vendor configuration command")
)

func TestCargoGeneration(t *testing.T) {
	cargo, err := runfiles.Rlocation(*cargoPath)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := runfiles.Rlocation(*runnerPath)
	if err != nil {
		t.Fatal(err)
	}
	vendorConfig, err := runfiles.Rlocation(*vendorConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := runfiles.Rlocation(*fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	rustcManifest, err := runfiles.Rlocation(*rustcPath)
	if err != nil {
		t.Fatal(err)
	}
	rustcData, err := os.ReadFile(rustcManifest)
	if err != nil {
		t.Fatal(err)
	}
	rustc, err := runfiles.Rlocation(strings.TrimSpace(string(rustcData)))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Dir(manifest)
	if err := filepath.WalkDir(source, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(source, file)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		dest := filepath.Join(root, strings.TrimSuffix(rel, ".in"))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	run := func(want int, tool string, args ...string) string {
		t.Helper()
		cmd := exec.Command(tool, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "BUILD_WORKSPACE_DIRECTORY="+root, "BUILD_WORKING_DIRECTORY="+root, "CARGO_NET_OFFLINE=true", "RUSTC="+rustc, "PATH="+t.TempDir(), "HOME="+root, "CARGO_HOME="+filepath.Join(root, ".cargo"))
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != want {
			t.Fatalf("%s %v exited %d, want %d:\n%s", tool, args, code, want, out)
		}
		if bytes.Contains(out, []byte("could not merge")) {
			t.Fatalf("Gazelle reported a merge error:\n%s", out)
		}
		return string(out)
	}
	scope := "crates"
	gazelle := func(want int, mode string) string {
		t.Helper()
		args := []string{"-repo_root=" + root, "-index=false", "-r=true", "-rules_rs_crate_scope=" + scope, "-mode=" + mode}
		return run(want, runner, append(args, strings.Split(scope, ",")...)...)
	}
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	write := func(rel, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	builds := func() map[string]string {
		t.Helper()
		result := map[string]string{}
		for _, crate := range []string{"build-support", "dev", "macros", "normal", "probe"} {
			file := "crates/" + crate + "/BUILD.bazel"
			result[file] = read(file)
		}
		return result
	}
	unchanged := func(before map[string]string) {
		t.Helper()
		for file, text := range before {
			if read(file) != text {
				t.Fatalf("Unexpected edit to %s", file)
			}
		}
	}

	run(0, cargo, "generate-lockfile", "--offline")
	run(2, vendorConfig, "--check", "--write")
	run(1, vendorConfig, "--root", root, "--check")
	run(0, vendorConfig, "--root", root, "--write")
	configBefore := read(".cargo/config.toml")
	run(0, vendorConfig, "--root", root)
	run(0, vendorConfig, "--root", root, "--write")
	if read(".cargo/config.toml") != configBefore {
		t.Fatal("Repeated Cargo configuration generation changed the output.")
	}
	gazelle(0, "fix")
	gazelle(0, "diff")
	const buildFile = "crates/probe/BUILD.bazel"
	current := read(buildFile)
	legacy := strings.ReplaceAll(current, "probe-integration-tests/check", "probe-integration-tests_tests/check_test")
	write(buildFile, legacy)
	gazelle(1, "diff")
	if read(buildFile) != legacy {
		t.Fatal("Read-only migration changed the legacy labels.")
	}
	gazelle(0, "fix")
	if strings.Contains(read(buildFile), "probe-integration-tests_tests/check_test") {
		t.Fatal("The generator retained a legacy test label.")
	}
	gazelle(0, "diff")
	if !strings.Contains(read("crates/macros/BUILD.bazel"), "--extern=proc_macro") {
		t.Fatal("Procedural macro unit tests need the proc_macro import.")
	}
	if strings.Contains(current, "default_visibility") || strings.Contains(current, "workspace_internal") {
		t.Fatal("New BUILD files must inherit the repository defaults.")
	}
	for _, want := range []string{"all_crate_deps", "probe-bin-unit-tests", "probe-build-script", "enabled", "--forbid=unsafe_code", `edition = "2021"`, "use_libtest_harness = False", "CARGO_BIN_EXE_probe", `"CARGO_PKG_DESCRIPTION": "This fixture tests Cargo package metadata.\nIt includes a \"quoted\" word."`, "$(execpath //:vendor-input.txt)", "PROBE_INPUT", `compile_data = ["README.md"]`, `timeout = "short"`} {
		if !strings.Contains(read(buildFile), want) {
			t.Fatalf("Missing %q in generated BUILD file", want)
		}
	}
	for _, want := range []string{`"Cargo.toml"`, "//:Cargo.toml", "//schemas:probe.schema.json"} {
		if !strings.Contains(read(buildFile), want) {
			t.Fatalf("Missing declared build input %q", want)
		}
	}
	schemaBefore := read("schemas/probe.schema.json")
	beforeSchema := builds()
	write("schemas/probe.schema.json", `{"$id":"https://example.test/probe.schema.json","$ref":"https://missing.test/schema"}`)
	// Build scripts own schema validation. Generation only reads declared input labels.
	run(0, vendorConfig, "--root", root, "--write")
	gazelle(0, "fix")
	unchanged(beforeSchema)
	if read(".cargo/config.toml") != configBefore {
		t.Fatal("An invalid schema changed Cargo configuration.")
	}
	write("schemas/probe.schema.json", schemaBefore)
	for _, want := range []string{
		`"@rules_rs//rs/platforms/config:x86_64-unknown-linux-gnu":\s*\[\s*"linux-only",?\s*\]`,
		`"@rules_rs//rs/platforms/config:aarch64-apple-darwin":\s*\[\s*\]`,
	} {
		if !regexp.MustCompile(want).MatchString(read("crates/normal/BUILD.bazel")) {
			t.Fatalf("Missing platform feature selection: %s", want)
		}
	}
	const probeManifest = "crates/probe/Cargo.toml"
	manifestBefore := read(probeManifest)
	withoutInput := regexp.MustCompile(`(?m)\n\[package\.metadata\.rules_rs\.build_inputs\.PROBE_INPUT\]\n(?:[^\n]*\n)*`).ReplaceAllString(manifestBefore, "\n")
	if withoutInput == manifestBefore {
		t.Fatal("The fixture must declare a vendor input.")
	}
	write(probeManifest, withoutInput)
	run(1, vendorConfig, "--root", root, "--check")
	run(0, vendorConfig, "--root", root, "--write")
	gazelle(1, "diff")
	gazelle(0, "fix")
	if strings.Contains(read(buildFile), "PROBE_INPUT") || strings.Contains(read(buildFile), "vendor-input.txt") || strings.Contains(read(".cargo/config.toml"), "PROBE_INPUT") {
		t.Fatal("Removed vendor mappings remain in generated output.")
	}
	write(probeManifest, manifestBefore)
	run(0, vendorConfig, "--root", root, "--write")
	gazelle(0, "fix")
	write(probeManifest, manifestBefore+"\n[package.metadata.rules_rs]\nunknown = true\n")
	beforeInvalid := builds()
	configBefore = read(".cargo/config.toml")
	run(1, vendorConfig, "--root", root, "--write")
	gazelle(1, "fix")
	unchanged(beforeInvalid)
	if read(".cargo/config.toml") != configBefore {
		t.Fatal("Invalid metadata changed Cargo configuration.")
	}
	write(probeManifest, manifestBefore)
	write(probeManifest, strings.Replace(manifestBefore, "harness = false", "harness = true", 1))
	beforeHarness := builds()
	gazelle(1, "diff")
	unchanged(beforeHarness)
	gazelle(0, "fix")
	if strings.Contains(read(buildFile), "use_libtest_harness") {
		t.Fatal("The removed custom harness setting remains.")
	}
	write(probeManifest, manifestBefore)
	gazelle(0, "fix")
	write(probeManifest, manifestBefore+"\n"+`[build-dependencies.renamed]
package = "normal-support"
path = "../normal"
features = ["host-only"]
`)
	beforeContexts := builds()
	gazelle(1, "diff")
	unchanged(beforeContexts)
	gazelle(0, "fix")
	if !strings.Contains(read("crates/normal/BUILD.bazel"), `"host-only"`) ||
		!strings.Contains(read("crates/probe/BUILD.bazel"), "cargo_only = True") {
		t.Fatal("The generator did not preserve separate Cargo contexts.")
	}
	gazelle(0, "diff")
	write(probeManifest, manifestBefore)
	write(probeManifest, regexp.MustCompile(`features\s*=\s*\[\s*"linux-only",?\s*\]`).ReplaceAllString(manifestBefore, `features = []`))
	beforeFeatures := builds()
	gazelle(1, "diff")
	unchanged(beforeFeatures)
	gazelle(0, "fix")
	if strings.Contains(read("crates/normal/BUILD.bazel"), "select(") ||
		strings.Contains(read("crates/normal/BUILD.bazel"), `"host-only"`) ||
		strings.Contains(read("crates/normal/BUILD.bazel"), "normal-bin-feature-tool") {
		t.Fatalf("Removed Cargo features remain:\n%s", read("crates/normal/BUILD.bazel"))
	}
	gazelle(0, "diff")
	write(probeManifest, manifestBefore)
	gazelle(0, "fix")
	gazelle(0, "diff")
	write("crates/probe/tests/added.rs", "#[test]\nfn added() {}\n")
	before := builds()
	gazelle(1, "diff")
	unchanged(before)
	gazelle(0, "fix")
	if !strings.Contains(read(buildFile), "integration-tests/added") {
		t.Fatal("The new test target is missing.")
	}
	if err := os.Remove(filepath.Join(root, "crates/probe/tests/added.rs")); err != nil {
		t.Fatal(err)
	}
	gazelle(0, "fix")
	if strings.Contains(read(buildFile), "integration-tests/added") {
		t.Fatal("The removed test target remains.")
	}
	gazelle(0, "diff")

	original := read(buildFile)
	write(buildFile, strings.Replace(original, "# Generated by rules_rs Gazelle.\n", "", 1))
	before = builds()
	if out := gazelle(1, "fix"); !strings.Contains(out, "handwritten") {
		t.Fatalf("Missing conflict diagnostic: %s", out)
	}
	unchanged(before)
	write(buildFile, original+"\n# gazelle:rules_rs_unknown value\n")
	before = builds()
	gazelle(1, "fix")
	unchanged(before)
	write(buildFile, original)

	before = builds()
	run(1, runner, "-repo_root="+root, "-index=false", "-rules_rs_crate_scope="+scope, ".")
	unchanged(before)
	if read("fixtures/untouched/BUILD.bazel") != "# This fixture stays outside the generation scope.\n" {
		t.Fatal("Generation changed the outside-scope fixture.")
	}
	if read("crates/probe/fixtures/BUILD.bazel") != "load(\"@crates//:defs.bzl\", \"aliases\")\nfilegroup(name=\"untouched\",srcs=[])\n" {
		t.Fatal("Generation changed a nested BUILD file.")
	}
	gazelle(0, "diff")

	// Generation must follow the configured scope at a different directory depth.
	if err := os.MkdirAll(filepath.Join(root, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, scope), filepath.Join(root, "project/components")); err != nil {
		t.Fatal(err)
	}
	write("Cargo.toml", strings.ReplaceAll(read("Cargo.toml"), scope+"/*", "project/components/*"))
	scope = "project/components"
	files, err := filepath.Glob(filepath.Join(root, scope, "*", "BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
	}
	gazelle(0, "fix")
	gazelle(0, "diff")
	if !strings.Contains(read(scope+"/probe/BUILD.bazel"), "//project/components/normal:") {
		t.Fatal("The generator did not use the configured crate scope.")
	}
	// Generate a dependency in a second scope and preserve nested fixtures in both scopes.
	if err := os.MkdirAll(filepath.Join(root, "shared/crates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "project/components/normal"), filepath.Join(root, "shared/crates/normal")); err != nil {
		t.Fatal(err)
	}
	write("Cargo.toml", strings.ReplaceAll(read("Cargo.toml"), `"project/components/*"`, `"project/components/*", "shared/crates/*"`))
	manifests, err := filepath.Glob(filepath.Join(root, "project/components/*/Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, manifest := range manifests {
		rel, err := filepath.Rel(root, manifest)
		if err != nil {
			t.Fatal(err)
		}
		write(rel, strings.ReplaceAll(read(rel), `"../normal"`, `"../../../shared/crates/normal"`))
	}
	if err := os.MkdirAll(filepath.Join(root, "shared/crates/normal/fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("shared/crates/normal/fixtures/BUILD.bazel", "# Preserve this nested fixture.\n")
	scope = "project/components,shared/crates"
	gazelle(0, "fix")
	gazelle(0, "diff")
	if !strings.Contains(read("project/components/probe/BUILD.bazel"), "//shared/crates/normal:") {
		t.Fatal("The generator lost the cross-scope dependency.")
	}
	if read("shared/crates/normal/fixtures/BUILD.bazel") != "# Preserve this nested fixture.\n" {
		t.Fatal("Generation changed a nested fixture in the second scope.")
	}
}

// Each case uses the public runner without a project-specific scope flag.
func TestWorkspaceLayouts(t *testing.T) {
	runner, err := runfiles.Rlocation(*runnerPath)
	if err != nil {
		t.Fatal(err)
	}
	cargo, err := runfiles.Rlocation(*cargoPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := runfiles.Rlocation(*rustcPath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	rustc, err := runfiles.Rlocation(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, layout := range []string{"root", "nested"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			put := func(name, text string) {
				t.Helper()
				file := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			workspace := ""
			if layout == "nested" {
				workspace = "project/rust/"
			}
			put("MODULE.bazel", "module(name = \"layout\")\n")
			put(workspace+"Cargo.toml", "[package]\nname='layout'\nversion='0.1.0'\nedition='2024'\n[workspace]\nmembers=['deep/member']\n")
			put(workspace+"src/lib.rs", "pub fn value() -> u32 { 42 }\n")
			put(workspace+"deep/member/Cargo.toml", "[package]\nname='member'\nversion='0.1.0'\nedition='2024'\n")
			put(workspace+"deep/member/src/lib.rs", "pub fn value() -> u32 { 42 }\n")
			put("backend.json", `{"manifest":"`+workspace+`Cargo.toml","platform_triples":["x86_64-unknown-linux-gnu"]}`)
			run := func(want int, tool string, args ...string) {
				t.Helper()
				cmd := exec.Command(tool, args...)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "RUSTC="+rustc, "CARGO_NET_OFFLINE=true", "CARGO_HOME="+root+"/.cargo", "HOME="+root)
				output, err := cmd.CombinedOutput()
				code := 0
				if err != nil {
					if exit, ok := err.(*exec.ExitError); ok {
						code = exit.ExitCode()
					} else {
						t.Fatal(err)
					}
				}
				if code != want {
					t.Fatalf("%v exited %d, want %d:\n%s", args, code, want, output)
				}
			}
			run(0, cargo, "generate-lockfile", "--manifest-path", workspace+"Cargo.toml", "--offline")
			args := []string{"-repo_root=" + root, "-rules_rs_config=" + root + "/backend.json", "-index=false", "-r=true", "."}
			run(0, runner, args...)
			run(0, runner, append([]string{"fix"}, args...)...)
			run(0, runner, append([]string{"-mode=diff"}, args...)...)
			file := filepath.Join(root, workspace, "BUILD.bazel")
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			name := "rust"
			if layout == "root" {
				name = "layout"
			}
			if !bytes.Contains(before, []byte(`name = "`+name+`"`)) {
				t.Fatalf("Missing root target:\n%s", before)
			}
			// A late package conflict must leave the earlier package unchanged.
			put(workspace+"deep/member/BUILD.bazel", "filegroup(name='member', srcs=[])\n")
			put(workspace+"src/main.rs", "fn main() {}\n")
			run(1, runner, args...)
			after, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("A late conflict changed an earlier BUILD file.")
			}
		})
	}
}
