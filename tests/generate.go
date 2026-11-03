package rulesrs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
)

func put(t *testing.T, root, name, text string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rendered(rules []*rule.Rule) string {
	file := rule.EmptyFile("BUILD.bazel", "crates/probe")
	for _, r := range rules {
		r.Insert(file)
	}
	return string(file.Format())
}

func TestConventionalCrate(t *testing.T) {
	root := t.TempDir()
	rel := "crates/probe"
	put(t, root, "Cargo.toml", "[workspace.lints.rust]\nunsafe_code = 'forbid'\n[workspace.lints.clippy]\nall = {level='warn', priority=-1}\nunwrap_used = 'deny'\n")
	put(t, root, rel+"/Cargo.toml", "[lints]\nworkspace = true\n")
	for _, name := range []string{"src/lib.rs", "src/helper.rs", "build.rs", "tests/roundtrip.rs"} {
		put(t, root, rel+"/"+name, "")
	}
	pkg := cargoPackage{Name: "renamed-package", Version: "1.2.3", Manifest: filepath.Join(root, rel, "Cargo.toml"), Features: []string{"default", "syntax"}}
	for _, target := range []struct {
		kind, file string
		test       bool
	}{{"lib", "src/lib.rs", true}, {"test", "tests/roundtrip.rs", true}, {"custom-build", "build.rs", false}} {
		pkg.Targets = append(pkg.Targets, cargoTarget{Name: strings.TrimSuffix(filepath.Base(target.file), ".rs"), Kind: []string{target.kind}, Source: filepath.Join(root, rel, target.file), Test: target.test})
	}
	rules, err := generate(root, rel, pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	output := rendered(rules)
	for _, want := range []string{
		"crate_name = crate_name()", "edition = edition()", "lint_config = lint_config()", "aliases = aliases()",
		"all_crate_deps(normal = True) + [\":probe-build-script\"]",
		"normal_dev = True", "all_crate_deps(build = True)",
		"probe-integration-tests/roundtrip", `srcs = glob(["tests/**/*.rs"])`, "src/**/*.rs",
		"--forbid=unsafe_code", "--warn=clippy::all", "--deny=clippy::unwrap_used", "syntax",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("Missing %q in:\n%s", want, output)
		}
	}
	for _, r := range rules {
		if r.Attr("visibility") != nil {
			t.Error("Generated rules must use the package default visibility.")
		}
		if r.Kind() == "cargo_build_script" && r.Attr("lint_config") != nil {
			t.Error("Build scripts cannot accept lint_config.")
		}
		if r.Name() == "probe-integration-tests/roundtrip" && !strings.Contains(rendered([]*rule.Rule{r}), ":probe-build-script") {
			t.Error("Integration tests need the build-script environment.")
		}
	}
	if err := os.Remove(filepath.Join(root, rel, "src/helper.rs")); err != nil {
		t.Fatal(err)
	}
	changed, err := generate(root, rel, pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	if rendered(changed) != output {
		t.Error("Library source changes must not change the generated glob.")
	}
}

func TestScope(t *testing.T) {
	for _, rel := range []string{"fixtures/language-layer", "other/crates/core", "crates/probe/src", "crates"} {
		if inScope("crates", rel) {
			t.Errorf("Unexpected scope: %s", rel)
		}
	}
	if !inScope("crates", "crates/new-crate") {
		t.Error("A new crate must be in scope.")
	}
}

func TestIntegrationTargetIdentity(t *testing.T) {
	root := t.TempDir()
	rel := "crates/probe"
	pkg := cargoPackage{
		Name: "cargo-package", Version: "1.2.3", Description: "This package tests target identity.",
		Manifest: filepath.Join(root, rel, "Cargo.toml"),
		Targets: []cargoTarget{
			{Name: "renamed-test", Kind: []string{"test"}, Source: filepath.Join(root, rel, "tests/check.rs")},
			{Name: "other", Kind: []string{"test"}, Source: filepath.Join(root, rel, "tests/other.rs")},
		},
	}
	rules, err := generate(root, rel, pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	for i, target := range pkg.Targets {
		r := rules[i]
		name, err := unitName(pkg, cargoUnit{Target: target, Mode: "test"})
		if err != nil || name != r.Name() || name != "probe-integration-tests/"+target.Name {
			t.Fatalf("Test and context names differ: %s, %s, %v", name, r.Name(), err)
		}
		if _, ok := rule.ParseGlobExpr(r.Attr("srcs")); !ok {
			t.Fatal("The test must include shared source modules.")
		}
		if r.AttrString("crate_root") != "tests/"+filepath.Base(target.Source) {
			t.Fatal("The test root changed.")
		}

		if r.AttrString("crate_name") != strings.ReplaceAll(target.Name, "-", "_") {
			t.Fatal("The Rust crate name must follow the Cargo target name.")
		}
		if !strings.Contains(rendered([]*rule.Rule{r}), `"CARGO_PKG_NAME": "cargo-package"`) {
			t.Fatal("The test must retain the Cargo package name.")
		}
		if !strings.Contains(rendered([]*rule.Rule{r}), `"CARGO_PKG_DESCRIPTION": "This package tests target identity."`) {
			t.Fatal("The test must retain the Cargo package description.")
		}
	}
	if rules[2].Name() != "probe-integration-tests" || len(rules[2].AttrStrings("tests")) != 2 {
		t.Fatal("The suite must contain both renamed test targets.")
	}
}

func TestAliasDependencyKinds(t *testing.T) {
	root := t.TempDir()
	pkg := cargoPackage{WorkspaceRoot: root, Dependencies: []cargoDependency{
		{Path: filepath.Join(root, "crates/normal")},
		{Path: filepath.Join(root, "crates/shared")},
		{Path: filepath.Join(root, "crates/shared"), Kind: "dev"},
		{Path: filepath.Join(root, "crates/support"), Kind: "dev"},
		{Path: filepath.Join(root, "crates/build"), Kind: "build"},
	}}
	for _, test := range []struct {
		kinds    []string
		excluded []string
		included []string
	}{
		{[]string{""}, []string{"build", "support"}, []string{"normal", "shared"}},
		{[]string{"", "dev"}, []string{"build"}, []string{"normal", "shared", "support"}},
		{[]string{"build"}, []string{"normal", "shared", "support"}, []string{"build"}},
		{nil, []string{"normal", "shared", "support", "build"}, nil},
	} {
		r := newRule("rust_library", "probe")
		setAttr(r, "aliases", targetAliases(pkg, test.kinds...))
		output := rendered([]*rule.Rule{r})
		for _, name := range test.excluded {
			if !strings.Contains(output, "//crates/"+name) {
				t.Errorf("Missing alias exclusion %s for kinds %v", name, test.kinds)
			}
		}
		for _, name := range test.included {
			if strings.Contains(output, "//crates/"+name) {
				t.Errorf("Excluded active alias %s for kinds %v", name, test.kinds)
			}
		}
	}
}

func TestCargoHarnessOptions(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Cargo.toml", `[lib]
harness = false
[[bin]]
name = "tool"
harness = false
[[test]]
name = "custom"
harness = false
[[test]]
name = "standard"
harness = true
`)
	pkg := cargoPackage{Manifest: filepath.Join(root, "Cargo.toml"), Targets: []cargoTarget{
		{Name: "lib", Kind: []string{"lib"}},
		{Name: "tool", Kind: []string{"bin"}},
		{Name: "custom", Kind: []string{"test"}},
		{Name: "standard", Kind: []string{"test"}},
		{Name: "automatic", Kind: []string{"test"}},
	}}
	if err := readHarnesses(&pkg); err != nil {
		t.Fatal(err)
	}
	for i, target := range pkg.Targets {
		r := newRule("rust_test", target.Name)
		setHarness(r, target)
		if (r.Attr("use_libtest_harness") != nil) != (i < 3) {
			t.Fatalf("Incorrect test harness for %s", target.Name)
		}
	}
}

func TestBinaryRuntimeInputs(t *testing.T) {
	root := t.TempDir()
	rel := "crates/probe"
	pkg := cargoPackage{Name: "probe", Description: "This package tests binary inputs.", Targets: []cargoTarget{
		{Name: "probe", Kind: []string{"bin"}, Source: filepath.Join(root, rel, "src/main.rs")},
		{Name: "check", Kind: []string{"test"}, Source: filepath.Join(root, rel, "tests/check.rs")},
	}}
	for _, test := range []struct {
		name string
		opts settings
		data string
	}{
		{"default", settings{}, `data = [":probe-bin"]`},
		{"file", settings{TestData: []string{"fixture.txt"}}, `data = ["fixture.txt"] + [":probe-bin"]`},
		{"glob", settings{TestData: []string{"fixtures/*"}}, `data = glob(["fixtures/*"]) + [":probe-bin"]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules, err := generate(root, rel, pkg, test.opts)
			if err != nil {
				t.Fatal(err)
			}
			output := rendered(rules)
			for _, want := range []string{"CARGO_BIN_EXE_probe", "$(rootpath :probe-bin)", `"CARGO_PKG_DESCRIPTION": "This package tests binary inputs."`, test.data} {
				if !strings.Contains(output, want) {
					t.Errorf("Missing binary runtime input %q in:\n%s", want, output)
				}
			}
		})
	}
}

func TestBinaryUnitTests(t *testing.T) {
	root := t.TempDir()
	rel := "crates/probe"
	put(t, root, rel+"/src/bin/tool.rs", "")
	pkg := cargoPackage{Name: "probe", Targets: []cargoTarget{{
		Name: "tool", Kind: []string{"bin"}, Source: filepath.Join(root, rel, "src/bin/tool.rs"), Test: true,
	}}}
	rules, err := generate(root, rel, pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[1].AttrString("crate") != ":probe-bin-tool" {
		t.Fatal("The binary needs a linked unit test target.")
	}
	if _, ok := rule.ParseGlobExpr(rules[0].Attr("srcs")); !ok {
		t.Fatal("A conventional binary must use a source glob.")
	}
}

func TestTargetEdition(t *testing.T) {
	for _, kind := range []string{"lib", "proc-macro", "bin", "test", "custom-build"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			rel := "crates/probe"
			put(t, root, "Cargo.toml", "[workspace]\n")
			put(t, root, rel+"/Cargo.toml", "[package]\nname = 'probe'\n")
			pkg := cargoPackage{
				Name: "probe", Edition: "2024", Manifest: filepath.Join(root, rel, "Cargo.toml"),
				Targets: []cargoTarget{{
					Name: "probe", Kind: []string{kind},
					Source: filepath.Join(root, rel, "src/lib.rs"), Edition: "2021", Test: true,
				}},
			}
			rules, err := generate(root, rel, pkg, settings{})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rules {
				if r.Kind() != "test_suite" && r.AttrString("edition") != "2021" {
					t.Errorf("Target %s lost its Cargo edition override", r.Name())
				}
			}
		})
	}
}

func TestLintOverrideAndPriority(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Cargo.toml", "[workspace.lints.rust]\nunsafe_code = 'forbid'\n")
	put(t, root, "crate.toml", "[lints.rust]\nunused = {level='deny', priority=-1}\nunused_imports = 'allow'\nunexpected_cfgs = {level='deny', check-cfg=['cfg(custom)']}\n")
	flags, err := buildLints(filepath.Join(root, "Cargo.toml"), filepath.Join(root, "crate.toml"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(flags, " ")
	if strings.Contains(joined, "unsafe_code") {
		t.Fatal("Package lints must not inherit workspace lints implicitly.")
	}
	if !strings.HasPrefix(joined, "--deny=unused ") || !strings.Contains(joined, "--check-cfg=cfg(custom)") {
		t.Fatalf("Unexpected flags: %s", joined)
	}
}

func TestExpressionMerge(t *testing.T) {
	file, err := rule.LoadData("BUILD.bazel", "crates/probe", []byte(`rust_library(
    name = "probe",
    srcs = ["src/lib.rs"],
    deps = all_crate_deps(normal = True),
    compile_data = [],
    visibility = ["//:workspace_internal"],
)`))
	if err != nil {
		t.Fatal(err)
	}
	fresh := newRule("rust_library", "probe")
	setAttr(fresh, "srcs", librarySources("src/lib.rs"))
	setAttr(fresh, "deps", deps([]string{"normal"}, ":probe-build-script"))
	setAttr(fresh, "compile_data", inputs([]string{"resources/*", "//schemas:all"}))
	mergeable := kindMap()["rust_library"].MergeableAttrs
	rule.MergeRules(fresh, file.Rules[0], mergeable, "BUILD.bazel")
	first := string(file.Format())
	for _, want := range []string{"all_crate_deps(normal = True) + [\":probe-build-script\"]", "glob([\"resources/*\"]) + [\"//schemas:all\"]", "src/**/*.rs", "src/main.rs"} {
		if !strings.Contains(first, want) {
			t.Fatalf("Missing %q after merge:\n%s", want, first)
		}
	}
	if file.Rules[0].Attr("visibility") != nil {
		t.Fatal("Merge must remove the old generated visibility.")
	}
	rule.MergeRules(fresh, file.Rules[0], mergeable, "BUILD.bazel")
	if string(file.Format()) != first {
		t.Fatal("Repeated expression merge changed the output.")
	}
	removed := rule.NewRule("rust_library", "probe")
	for key := range mergeable {
		if file.Rules[0].Attr(key) != nil {
			removed.SetAttr(key, generatedExpr{})
		}
	}
	rule.MergeRules(removed, file.Rules[0], mergeable, "BUILD.bazel")
	for _, key := range []string{"srcs", "deps", "compile_data"} {
		if file.Rules[0].Attr(key) != nil {
			t.Errorf("Removed rule retains %s", key)
		}
	}
}
