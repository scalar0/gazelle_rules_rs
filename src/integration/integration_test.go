package rulesrs_test

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bazel-contrib/bazel-gazelle/v2/testtools"
	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/scalar0/gazelle_rules_rs/src/internal/testfixture"
)

var (
	runnerPath          = flag.String("gazelle", "", "Gazelle runner runfile")
	upstreamFixturePath = flag.String("upstream-fixture", "", "Upstream Cargo fixture manifest runfile")
	vendorFixturePath   = flag.String("vendor-fixture", "", "Offline registry crate manifest runfile")
	backendConfigPath   = flag.String("backend-config", "", "Shared backend configuration runfile")
	fixturePath         = flag.String("fixture", "", "Cargo fixture manifest runfile")
	cargoPath           = flag.String("cargo", "", "Pinned Cargo runfile")
	rustcPath           = flag.String("rustc", "", "Declared Rust compiler manifest")
	sysrootPath         = flag.String("sysroot", "", "Declared Rust standard library manifest")
	vendorConfigPath    = flag.String("vendor-config", "", "Cargo vendor configuration command")
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
	if err := testfixture.Stage(manifest, root); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.Vendor(*vendorFixturePath, root); err != nil {
		t.Fatal(err)
	}
	if err := configureFixture(root, "Cargo.toml"); err != nil {
		t.Fatal(err)
	}
	run := func(want int, tool string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), tool, args...)
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
		args := []string{"-repo_root=" + root, "-index=false", "-r=true", "-rules_rs_config=" + filepath.Join(root, "backend.json"), "-mode=" + mode}
		return run(want, runner, append(args, ".")...)
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
		result := map[string]string{"BUILD.bazel": read("BUILD.bazel")}
		for _, crate := range []string{"build-support", "dev", "dynamic", "macros", "normal", "probe", "root-user"} {
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
	// Default generation must omit manifest access while preserving explicit resource inputs.
	// The old fixture checked only automatically supplied manifest inputs.
	for file, output := range builds() {
		if strings.Contains(output, "CARGO_MANIFEST_PATH") {
			t.Fatalf("Default generation added manifest access to %s", file)
		}
		if file != buildFile && strings.Contains(output, `":Cargo.toml"`) {
			t.Fatalf("Default generation added a manifest input to %s", file)
		}
	}
	if !strings.Contains(current, "\"tests/shared/mod.rs\"") || !strings.Contains(current, "],  # keep") {
		t.Fatal("The generator lost the kept integration source list.")
	}
	// A kind migration must reject a kept source before any file changes.
	const keptBuildFile = "crates/build-support/BUILD.bazel"
	keptOriginal := read(keptBuildFile)
	if !strings.Contains(keptOriginal, `"src/lib.rs",  # keep`) {
		t.Fatal("The generator lost the kept source file.")
	}
	keptKind := strings.Replace(keptOriginal, "rust_library(\n    name = \"lib\",", "rust_binary(\n    name = \"lib\",", 1)
	if keptKind == keptOriginal {
		t.Fatal("The fixture must have a library target with kept sources.")
	}
	write(keptBuildFile, keptKind)
	beforeKeptKind := builds()
	gazelle(1, "fix")
	unchanged(beforeKeptKind)
	write(keptBuildFile, keptOriginal)
	legacy := strings.ReplaceAll(current, "tests/integration/custom", "probe-integration-tests_tests/custom_test")
	write(buildFile, legacy)
	gazelle(1, "diff")
	if read(buildFile) != legacy {
		t.Fatal("Read-only migration changed the legacy labels.")
	}
	gazelle(0, "fix")
	if strings.Contains(read(buildFile), "probe-integration-tests_tests/custom_test") {
		t.Fatal("The generator retained a legacy test label.")
	}
	gazelle(0, "diff")
	if output := read("crates/macros/BUILD.bazel"); !strings.Contains(output, "--extern=proc_macro") && !strings.Contains(output, `crate = ":lib"`) {
		t.Fatal("Procedural macro unit tests need the proc_macro import.")
	}
	if strings.Contains(current, "default_visibility") || strings.Contains(current, "workspace_internal") {
		t.Fatal("New BUILD files must inherit the repository defaults.")
	}
	for _, want := range []string{"manifest_resources", "all_crate_deps", "tests/unit/lib", "tests/bench/lib", "tests/unit/bin/probe", `name = "bin/probe"`, "build-script", "enabled", "--forbid=unsafe_code", `edition = "2021"`, "use_libtest_harness = False", "CARGO_BIN_EXE_probe", `"CARGO_PKG_DESCRIPTION": "This fixture tests Cargo package metadata.\nIt includes a \"quoted\" word."`, "$(execpath //:vendor-input.txt)", "PROBE_INPUT", `"README.md"`, `timeout = "short"`} {
		if !strings.Contains(read(buildFile), want) {
			t.Fatalf("Missing %q in generated BUILD file", want)
		}
	}
	for _, want := range []string{`":Cargo.toml"`, "//:Cargo.toml", "//schemas:probe.schema.json"} {
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
		`"@rust_rules//rs/platforms/config:x86_64-unknown-linux-gnu":\s*\[\s*"linux-only",?\s*\]`,
		`"@rust_rules//rs/platforms/config:aarch64-apple-darwin":\s*\[\s*\]`,
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
	customTest := regexp.MustCompile(`(?s)rust_test\(\s*name = "tests/integration/custom",.*?\n\)`).FindString(read(buildFile))
	if customTest == "" {
		t.Fatal("The custom integration test target is missing.")
	}
	if strings.Contains(customTest, "use_libtest_harness") {
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
		strings.Contains(read("crates/normal/BUILD.bazel"), "bin/feature-tool") {
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
	if !strings.Contains(read(buildFile), "tests/integration/added") {
		t.Fatal("The new test target is missing.")
	}
	if err := os.Remove(filepath.Join(root, "crates/probe/tests/added.rs")); err != nil {
		t.Fatal(err)
	}
	gazelle(0, "fix")
	if strings.Contains(read(buildFile), "tests/integration/added") {
		t.Fatal("The removed test target remains.")
	}
	gazelle(0, "diff")

	original := read(buildFile)
	write(buildFile, strings.Replace(original, "#CODEGEN:gazelle_rules_rs\n", "", 1))
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
	run(1, runner, "-repo_root="+root, "-index=false", "-rules_rs_config="+filepath.Join(root, "backend.json"), "-rules_rs_crate_scope="+scope, ".")
	unchanged(before)
	if read("fixtures/untouched/BUILD.bazel") != "# This fixture stays outside the generation scope.\n" {
		t.Fatal("Generation changed the outside-scope fixture.")
	}
	if read("crates/probe/fixtures/BUILD.bazel") != "filegroup(\n    name = \"untouched\",\n    srcs = [],\n)\n" {
		t.Fatal("Generation changed a nested BUILD file.")
	}
	gazelle(0, "diff")

	// A nested source package must fail before any generated file changes.
	write("crates/probe/tests/BUILD.bazel", "filegroup(name = \"nested\", srcs = [])\n")
	before = builds()
	gazelle(1, "fix")
	unchanged(before)
	if err := os.Remove(filepath.Join(root, "crates/probe/tests/BUILD.bazel")); err != nil {
		t.Fatal(err)
	}

	// Full workspace generation must follow the moved crate directory.
	if err := os.MkdirAll(filepath.Join(root, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, scope), filepath.Join(root, "project/components")); err != nil {
		t.Fatal(err)
	}
	write("Cargo.toml", strings.ReplaceAll(read("Cargo.toml"), scope+"/*", "project/components/*"))
	write("Cargo.toml", strings.ReplaceAll(read("Cargo.toml"), "crates/probe", "project/components/probe"))
	scope = "project/components"
	write(scope+"/root-user/Cargo.toml", strings.ReplaceAll(read(scope+"/root-user/Cargo.toml"), `path = "../.."`, `path = "../../.."`))
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
		t.Fatal("The generator did not use the moved crate directory.")
	}
	// Full workspace generation must preserve nested fixtures in both crate directories.
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
	gazelle(0, "fix")
	gazelle(0, "diff")
	if !strings.Contains(read("project/components/probe/BUILD.bazel"), "//shared/crates/normal:") {
		t.Fatal("The generator lost the dependency between crate directories.")
	}
	if read("shared/crates/normal/fixtures/BUILD.bazel") != "# Preserve this nested fixture.\n" {
		t.Fatal("Generation changed a nested fixture in the second crate directory.")
	}
}

// Validate kept source labels across a nested Bazel package before writes.
// This catches rejection of valid overrides and acceptance of kept paths across package boundaries.
// Existing source checks validate generated paths without source overrides in another Bazel package.
func TestKeptSourceBoundaries(t *testing.T) {
	runner, err := testfixture.Resolve(*runnerPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := testfixture.Stage(*fixturePath, root); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.Vendor(*vendorFixturePath, root); err != nil {
		t.Fatal(err)
	}
	if err := configureFixture(root, "Cargo.toml"); err != nil {
		t.Fatal(err)
	}
	run := func(mode string, success bool) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), runner, "-repo_root="+root, "-rules_rs_config="+filepath.Join(root, "backend.json"), "-index=false", "-r=true", "-mode="+mode, ".")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CARGO_NET_OFFLINE=true", "CARGO_HOME="+filepath.Join(root, ".cargo"), "HOME="+root)
		output, err := cmd.CombinedOutput()
		if success && err != nil {
			t.Fatalf("Generation rejected a valid kept source: %v\n%s", err, output)
		}
		if !success && (err == nil || !bytes.Contains(output, []byte("crosses Bazel package"))) {
			t.Fatalf("Generation did not reject an invalid kept source: %v\n%s", err, output)
		}
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	write := func(name, text string) {
		t.Helper()
		if err := testfixture.Write(root, name, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	run("fix", true)
	const buildFile = "crates/dev/BUILD.bazel"
	const sourceLabel = "//crates/dev/src:lib.rs"
	sourceBuild := read(buildFile)
	write("crates/dev/src/BUILD.bazel", "exports_files([\"lib.rs\"])\n")
	keptSource := regexp.MustCompile(`(?s)    srcs = glob\(.*?\n    \),`).ReplaceAllString(sourceBuild, `    srcs = ["`+sourceLabel+`"],  # keep`)
	keptSource = strings.ReplaceAll(keptSource, `    crate_root = "src/lib.rs",`, `    crate_root = "`+sourceLabel+`",  # keep`)
	write(buildFile, keptSource)
	run("fix", true)
	run("diff", true)
	if got := read(buildFile); !strings.Contains(got, sourceLabel) || !strings.Contains(got, "# keep") {
		t.Fatalf("Generation lost the kept source label:\n%s", got)
	}
	write(buildFile, strings.ReplaceAll(read(buildFile), sourceLabel, "src/lib.rs"))
	write("src/main.rs", "fn main() {}\n")
	before := map[string]string{}
	err = filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "BUILD.bazel" {
			rel, err := filepath.Rel(root, file)
			if err != nil {
				return err
			}
			before[rel] = read(rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	run("fix", false)
	for name, want := range before {
		if read(name) != want {
			t.Fatalf("Invalid kept sources changed %s", name)
		}
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
			put(workspace+"deep/member/Cargo.toml", "[package]\nname='member'\nversion='0.1.0'\nedition='2024'\n[dependencies]\nlayout={path='../..'}\n")
			put(workspace+"deep/member/src/lib.rs", "pub fn value() -> u32 { 42 }\n")
			if err := configureFixture(root, workspace+"Cargo.toml"); err != nil {
				t.Fatal(err)
			}
			run := func(want int, tool string, args ...string) {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), tool, args...)
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
			name := "lib"
			if bytes.Contains(before, []byte("-cargo-")) {
				t.Fatal("Uniform configured workspaces must use public compilation targets.")
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

func configureFixture(root, manifest string) error {
	_, err := testfixture.Configure(*backendConfigPath, root, manifest)
	return err
}

// The expectations follow Cargo library defaults and the public target contract.
// The nested library and build script opt in through shared metadata and a kept environment.
// This catches lost opt-ins that text-only generation tests cannot validate through compilation.
func TestUpstreamGeneration(t *testing.T) {
	runner, err := testfixture.Resolve(*runnerPath)
	if err != nil {
		t.Fatal(err)
	}
	rustcManifest, err := testfixture.Resolve(*rustcPath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(rustcManifest)
	if err != nil {
		t.Fatal(err)
	}
	rustc, err := testfixture.Resolve(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	ownedManifest, err := testfixture.Resolve(*fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	expectations := filepath.Join(filepath.Dir(filepath.Dir(ownedManifest)), "gazelle")
	t.Setenv("RUSTC", rustc)
	t.Setenv("CARGO_NET_OFFLINE", "true")
	t.Setenv("CARGO_HOME", filepath.Join(t.TempDir(), "cargo"))
	if os.Getenv("COVERAGE_DIR") != "" {
		t.Setenv("GOCOVERDIR", t.TempDir())
	}
	for _, name := range []string{"upstream", "handwritten_conflict"} {
		root := t.TempDir()
		if err := testfixture.Stage(*upstreamFixturePath, root); err != nil {
			t.Fatal(err)
		}
		if err := testfixture.CopyTree(filepath.Join(expectations, name), root); err != nil {
			t.Fatal(err)
		}
		if name == "upstream" {
			// The shared BUILD seed is an input. The reviewed output remains the golden file.
			if err := os.Rename(filepath.Join(root, "manifest_path_probe/BUILD.bazel"), filepath.Join(root, "manifest_path_probe/BUILD.gazelle_in")); err != nil {
				t.Fatal(err)
			}
		}
		if err := testfixture.Write(root, "MODULE.bazel", []byte("module(name = \"upstream_fixture\")\n")); err != nil {
			t.Fatal(err)
		}
		if err := configureFixture(root, "Cargo.toml"); err != nil {
			t.Fatal(err)
		}
		if err := testfixture.Write(root, "arguments.txt", []byte("-index=false\n-r=true\n-rules_rs_config=backend.json\n.\n")); err != nil {
			t.Fatal(err)
		}
		testtools.TestGazelleGenerationOnPath(t, &testtools.TestGazelleGenerationArgs{
			Name: name, TestDataPathAbsolute: root, GazelleBinaryPath: runner,
			BuildInSuffix: ".gazelle_in", BuildOutSuffix: ".gazelle_out",
		})
	}
}
