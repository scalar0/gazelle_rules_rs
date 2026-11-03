package rulesrs

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
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
	put(t, root, rel+"/src/lib.rs", "")
	put(t, root, rel+"/tests/roundtrip.rs", "")
	rules, err := generate(root, rel, pkg, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"probe":                             {"GUIDE.md", "schema.json"},
		"probe-unit-tests":                  {"GUIDE.md", "schema.json", "unit.json"},
		"probe-integration-tests/roundtrip": {"GUIDE.md", "test.json"},
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
		if r.Attr("compile_data") != nil && len(r.AttrStrings("compile_data")) != 0 {
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
