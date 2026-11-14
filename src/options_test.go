package rulesrs

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	bzl "github.com/bazelbuild/buildtools/build"
)

func TestMetadataValidation(t *testing.T) {
	for _, declaration := range []string{
		`null`, `[]`, `{"unknown": true}`, `{"testonly": "true"}`,
		`{"test_timeout": "fast"}`, `{"test_data": "fixture"}`, `{"test_data": [""]}`,
		`{"dep_override": {"prefix": "@crates//:fixture"}}`,
		`{"build_inputs": {"BAD-NAME": {"cargo_path": "vendor/input", "bazel_label": "//:input"}}}`,
		`{"build_inputs": {"INPUT": {"cargo_path": "../input", "bazel_label": "//:input"}}}`,
		`{"build_inputs": {"INPUT": {"cargo_path": "..", "bazel_label": "//:input"}}}`,
		`{"build_inputs": {"INPUT": {"cargo_path": "/input", "bazel_label": "//:input"}}}`,
		`{"build_inputs": {"INPUT": {"cargo_path": "vendor/../input", "bazel_label": "//:input"}}}`,
		`{"build_inputs": {"INPUT": {"cargo_path": "vendor\\input", "bazel_label": "//:input"}}}`,
		`{"build_inputs": {"INPUT": {"cargo_path": "vendor/input", "bazel_label": "//:*.json"}}}`,
		`{"build_inputs": {"INPUT": {"cargo_path": "vendor/input", "bazel_label": "//:input", "force": true}}}`,
	} {
		t.Run(declaration, func(t *testing.T) {
			_, err := declaredSettings(json.RawMessage(`{"rules_rs":`+declaration+`}`), "/repo/crate/Cargo.toml", "package")
			if err == nil || !strings.Contains(err.Error(), "/repo/crate/Cargo.toml") || !strings.Contains(err.Error(), "rules_rs") {
				t.Fatalf("Expected a manifest and field diagnostic, got %v", err)
			}
		})
	}
}

func TestWorkspaceMetadataDefaults(t *testing.T) {
	defaults, err := declaredSettings(json.RawMessage(`{"rules_rs":{
        "library_compile_data":["shared.json"], "unit_compile_data":["unit.json"],
        "test_timeout":"long", "testonly":true,
        "build_inputs":{"INPUT":{"cargo_path":"vendor/input", "bazel_label":"//:input"}}
    }}`), "/repo/Cargo.toml", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	pkg := cargoPackage{Manifest: "/repo/crates/probe/Cargo.toml", WorkspaceRoot: "/repo"}
	inherited, err := resolveSettings(defaults, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inherited.LibraryCompileData, []string{"shared.json"}) || inherited.TestTimeout != "long" || inherited.TestOnly == nil || !*inherited.TestOnly || len(inherited.BuildInputs) != 1 {
		t.Fatalf("Workspace defaults were not inherited: %+v", inherited)
	}
	pkg.Metadata = json.RawMessage(`{"rules_rs":{
        "library_compile_data":[], "testonly":false, "build_inputs":{}
    }}`)
	overridden, err := resolveSettings(defaults, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(overridden.LibraryCompileData) != 0 || *overridden.TestOnly || len(overridden.BuildInputs) != 0 || !slices.Equal(overridden.UnitCompileData, []string{"unit.json"}) {
		t.Fatalf("Explicit empty and false values did not replace defaults: %+v", overridden)
	}
	if len(defaults) != 5 {
		t.Fatal("Package resolution changed workspace defaults.")
	}
}

// Protect package-relative build input labels at the repository root and in nested workspaces.
// This catches a dot in the package name and labels resolved from the Cargo workspace root.
// Existing settings tests use absolute labels, and standalone tests cover only nested packages.
func TestPackageBuildInputLabels(t *testing.T) {
	for _, input := range []struct{ directory, workspace string }{
		{"", ""},
		{"project", "project"},
		{"project/deep/member", "project"},
	} {
		t.Run(input.directory, func(t *testing.T) {
			pkg := cargoPackage{
				RepoRoot: "/repo", WorkspaceRoot: filepath.Join("/repo", input.workspace),
				Manifest: filepath.Join("/repo", input.directory, "Cargo.toml"),
				Metadata: json.RawMessage(`{"rules_rs":{"build_inputs":{"INPUT":{"cargo_path":"input.txt","bazel_label":":input.txt"}}}}`),
			}
			got, err := resolveSettings(nil, pkg)
			if err != nil {
				t.Fatal(err)
			}
			if got.BuildInputs["INPUT"].BazelLabel != "//"+input.directory+":input.txt" {
				t.Fatalf("Incorrect build input label: %s", got.BuildInputs["INPUT"].BazelLabel)
			}
		})
	}
}

func TestCompilerInputInheritance(t *testing.T) {
	root := t.TempDir()
	rel := "crates/probe"
	pkg := cargoPackage{Name: "probe", Readme: "GUIDE.md", Targets: []cargoTarget{
		{Name: "probe", Kind: []string{"lib"}, Source: filepath.Join(root, rel, "src/lib.rs"), Test: true},
		{Name: "roundtrip", Kind: []string{"test"}, Source: filepath.Join(root, rel, "tests/roundtrip.rs")},
	}}
	opts := settings{
		LibraryCompileData: []string{"schema.json", "GUIDE.md"},
		UnitCompileData:    []string{"unit.json", "schema.json"},
		TestCompileData:    []string{"test.json"},
	}
	rules, err := generate(root, rel, pkg, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"lib":                         {"GUIDE.md", "schema.json"},
		"tests/unit/lib":              {"GUIDE.md", "schema.json", "unit.json"},
		"tests/integration/roundtrip": {"GUIDE.md", "test.json"},
	}
	for _, r := range rules {
		if expected, ok := want[r.Name()]; ok && !slices.Equal(r.AttrStrings("compile_data"), expected) {
			t.Fatalf("Incorrect compiler inputs for %s: %v", r.Name(), r.AttrStrings("compile_data"))
		}
	}
	pkg.Readme = ""
	rules, err = generate(root, rel, pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if values := r.AttrStrings("compile_data"); len(values) != 0 {
			t.Fatalf("A package without a README gained an automatic input: %s", rendered(rules))
		}
	}
	if strings.Contains(rendered(rules), "*.md") {
		t.Fatal("README discovery must not use a Markdown glob.")
	}
}

func TestSharedReadmeLabel(t *testing.T) {
	root := t.TempDir()
	put(t, root, "BUILD.bazel", "exports_files([\"README.md\"])\n")
	inputs, err := readmeInput(root, "crates/probe", cargoPackage{Readme: "../../README.md"})
	if err != nil || !slices.Equal(inputs, []string{"//:README.md"}) {
		t.Fatalf("Incorrect shared README input: %v, %v", inputs, err)
	}
	if _, err := readmeInput(root, "crates/probe", cargoPackage{Readme: "../../../outside.md"}); err == nil {
		t.Fatal("A README outside the repository must cause an error.")
	}
}

func TestBuildInputsInContexts(t *testing.T) {
	root := t.TempDir()
	packages, graph := contextFixture(t, root)
	if err := collectContexts(graph, "x86_64-unknown-linux-gnu", "build", packages, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	pkg := packages["crates/b"]
	opts := settings{
		BuildData:   []string{"manual.txt", "//schemas:input"},
		BuildInputs: map[string]buildInput{"INPUT": {CargoPath: "vendor/input", BazelLabel: "//schemas:input"}},
	}
	templates, err := generate(root, "crates/b", pkg, opts)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := generateContexts("crates/b", pkg, templates)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, r := range rules {
		if r.Kind() != "cargo_build_script" {
			continue
		}
		count++
		data := r.AttrStrings("data")
		if !strings.Contains(rendered([]*rule.Rule{r}), `"INPUT": "$(execpath //schemas:input)"`) || len(data) != 2 || !slices.Contains(data, "//schemas:input") || !slices.Contains(data, "manual.txt") {
			t.Fatalf("Build-script inputs were not copied to %s: %s", r.Name(), rendered(rules))
		}
	}
	if count == 0 {
		t.Fatal("The context fixture must create a build-script target.")
	}
}

// Protect package ownership for explicit generated Rust paths.
// This catches sources below a nested BUILD file, including custom BUILD names.
// Generator tests use temporary paths without nested Bazel packages.
func TestExplicitRustSourceBoundaries(t *testing.T) {
	for _, buildName := range []string{"BUILD.bazel", "BUILD", "CUSTOM.build"} {
		t.Run(buildName, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "crates/probe/tests/"+buildName, "")
			put(t, root, "crates/probe/tests/nested/check.rs", "")
			for _, key := range []string{"crate_root", "srcs"} {
				r := newRule("rust_test", "check")
				if key == "crate_root" {
					setAttr(r, key, "tests/nested/check.rs")
				} else {
					setAttr(r, key, []string{"tests/nested/check.rs"})
				}
				err := validateSourceBoundaries(root, "crates/probe", []*rule.Rule{r}, []string{buildName})
				if err == nil || !strings.Contains(err.Error(), "crates/probe/tests/"+buildName) {
					t.Fatalf("Expected a source ownership error for %s, got %v", key, err)
				}
			}
		})
	}
}

// Protect validation of package-relative Rust paths without restricting resource labels.
// This catches absolute paths, parent traversal, and unclean generated source paths.
// Existing metadata tests validate resource settings, not generated source paths.
func TestRustSourcePathContract(t *testing.T) {
	root := t.TempDir()
	put(t, root, "crates/probe/tests/BUILD.bazel", "")
	put(t, root, "crates/probe/src/lib.rs", "")
	for _, source := range []string{"", ".", "..", "../outside.rs", "/outside.rs", "src/../lib.rs", `src\lib.rs`} {
		r := newRule("rust_library", "probe")
		setAttr(r, "crate_root", source)
		if err := validateSourceBoundaries(root, "crates/probe", []*rule.Rule{r}, []string{"BUILD.bazel"}); err == nil {
			t.Errorf("Accepted invalid source path %q", source)
		}
	}
	r := newRule("rust_library", "probe")
	setAttr(r, "crate_root", "src/lib.rs")
	setAttr(r, "srcs", librarySources("src/lib.rs"))
	setAttr(r, "compile_data", []string{"tests/input.json", "//resources:data"})
	setAttr(r, "data", []string{"tests/input.json"})
	resource := newRule("filegroup", "resource")
	setAttr(resource, "srcs", []string{"tests/input.json"})
	labeled := newRule("rust_test", "labeled")
	setAttr(labeled, "srcs", []string{"//shared:source.rs", "@external//:source.rs"})
	if err := validateSourceBoundaries(root, "crates/probe", []*rule.Rule{r, resource, labeled}, []string{"BUILD.bazel"}); err != nil {
		t.Fatal(err)
	}
	setAttr(r, "srcs", []string{"tests/check.rs"})
	if err := validateSourceBoundaries(root, "crates/probe", []*rule.Rule{r}, []string{"CUSTOM.build"}); err != nil {
		t.Fatalf("An unconfigured BUILD name created a package boundary: %v", err)
	}
}

// Protect explicit sources inside conditional lists combined with glob expressions.
// This catches a nested source that a direct-list check would miss.
// The other boundary tests use direct source attributes.
func TestConditionalRustSourceBoundary(t *testing.T) {
	root := t.TempDir()
	put(t, root, "crates/probe/tests/BUILD.bazel", "")
	put(t, root, "crates/probe/tests/check.rs", "")
	r := newRule("rust_test", "check")
	setAttr(r, "srcs", &bzl.BinaryExpr{X: librarySources("src/lib.rs"), Op: "+", Y: rule.ExprFromValue(rule.SelectStringListValue{
		"//conditions:default": {"tests/check.rs"},
	})})
	if err := validateSourceBoundaries(root, "crates/probe", []*rule.Rule{r}, []string{"BUILD.bazel"}); err == nil {
		t.Fatal("A conditional source crossed a Bazel package boundary.")
	}
}
