package rulesrs

import (
	"context"
	"encoding/json"
	"flag"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazel-contrib/bazel-gazelle/v2/config"
	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
)

type fixtureHandler struct {
	name   string
	schema json.RawMessage
	output Contribution
	visit  func(MetadataContext)
}

func (h fixtureHandler) Spec() HandlerSpec {
	schema := h.schema
	if schema == nil {
		schema = json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["value"],"additionalProperties":false,"properties":{"value":{"type":"string"}}}`)
	}
	return HandlerSpec{Namespace: h.name, Schema: schema, Kinds: []rule.KindInfo{{Name: "filegroup", MergeableAttrs: map[string]bool{"srcs": true}, NonEmptyAttrs: map[string]bool{"srcs": true}}}}
}

func (h fixtureHandler) Generate(_ context.Context, input MetadataContext) (Contribution, error) {
	if h.visit != nil {
		h.visit(input)
	}
	return h.output, nil
}

// Protect the required configuration flag before Cargo or Gazelle starts generation.
// This catches an implicit backend configuration or a lost custom BUILD name.
// Configuration validation tests call the configuration reader directly.
func TestRequiredBackendConfiguration(t *testing.T) {
	root := t.TempDir()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	e := &extension{}
	e.RegisterFlags(fs, "update", nil)
	c := config.New()
	c.RepoRoot = root
	c.ValidBuildFileNames = []string{"CUSTOM.build"}
	if err := e.CheckFlags(fs, c); err == nil || !strings.Contains(err.Error(), "-rules_rs_config") {
		t.Fatalf("Expected the required configuration flag, got %v", err)
	}
	put(t, root, "config.json", `{"platform_triples":["x86_64-unknown-linux-gnu"]}`)
	if err := fs.Set("rules_rs_config", filepath.Join(root, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := e.CheckFlags(fs, c); err != nil {
		t.Fatal(err)
	}
	c.ValidBuildFileNames[0] = "changed"
	if len(e.buildFileNames()) != 1 || e.buildFileNames()[0] != "CUSTOM.build" {
		t.Fatal("Generation did not retain the configured BUILD filename.")
	}
}

// Protect explicit Cargo input relationships without relying on generated name prefixes.
// This catches unrelated rules that happen to start with a Cargo variant name.
// The handler contract test uses one rule without related targets.
func TestMetadataInputRelationships(t *testing.T) {
	base := newRule("rust_library", "lib")
	variant := newRule("rust_library", "context/library/1")
	variant.SetPrivateAttr("cargo_input_target", "lib")
	unit := newRule("rust_test", "tests/unit/lib")
	unit.SetPrivateAttr("cargo_input_target", "lib")
	doc := newRule("rust_doc_test", "tests/doc")
	doc.SetPrivateAttr("cargo_input_target", "lib")
	unrelated := newRule("rust_binary", "lib-cargo-unrelated")
	rules := []*rule.Rule{base, variant, unit, doc, unrelated}
	_, err := applyContributions(rules, []Contribution{{Inputs: []TargetInputs{{Target: "lib", CompileData: []string{":resource"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*rule.Rule{base, variant, unit} {
		if got := r.AttrStrings("compile_data"); len(got) != 1 || got[0] != ":resource" {
			t.Fatalf("Target %s did not get the declared input: %v", r.Name(), got)
		}
	}
	for _, r := range []*rule.Rule{doc, unrelated} {
		if r.Attr("compile_data") != nil {
			t.Fatalf("Target %s got unrelated compiler inputs", r.Name())
		}
	}
	if _, err := applyContributions(rules, []Contribution{{Inputs: []TargetInputs{{Target: "tests/doc", CompileData: []string{":resource"}}}}}); err == nil {
		t.Fatal("A doc test accepted direct compiler inputs.")
	}
}

// Protect public target roles and output types in the metadata API.
// This catches handlers that cannot select a public test or a library output.
// The handler contract test has no Cargo targets.
func TestMetadataTargetDescriptors(t *testing.T) {
	root := t.TempDir()
	seen := map[string]TargetInfo{}
	handler := fixtureHandler{name: "extra", visit: func(input MetadataContext) {
		for _, target := range input.Targets {
			seen[target.BazelTarget] = target
		}
	}}
	language, err := New(handler)
	if err != nil {
		t.Fatal(err)
	}
	pkg := cargoPackage{Name: "probe", Manifest: filepath.Join(root, "crates/probe/Cargo.toml"), RepoRoot: root, Metadata: json.RawMessage(`{"extra":{"value":"x"}}`), Targets: []cargoTarget{
		{Name: "probe", Kind: []string{"lib"}, CrateTypes: []string{"rlib", "cdylib"}, Source: filepath.Join(root, "crates/probe/src/lib.rs"), Test: true, Bench: true, Doctest: true},
		{Name: "probe", Kind: []string{"bin"}, Source: filepath.Join(root, "crates/probe/src/main.rs"), Test: true, Bench: true},
		{Name: "check", Kind: []string{"test"}, Source: filepath.Join(root, "crates/probe/tests/check.rs")},
	}}
	if _, err := language.(*extension).metadataContributions(context.Background(), "crates/probe", pkg); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ name, role, output string }{
		{"lib", "library", "rlib"},
		{"lib/cdylib", "library", "cdylib"},
		{"tests/unit/lib", "unit-test", "bin"},
		{"tests/bench/lib", "benchmark-test", "bin"},
		{"bin/probe", "bin", "bin"},
		{"tests/unit/bin/probe", "unit-test", "bin"},
		{"tests/bench/bin/probe", "benchmark-test", "bin"},
		{"tests/doc", "doc-test", "rlib"},
		{"tests/integration/check", "integration-test", "bin"},
	} {
		got := seen[want.name]
		if got.Role != want.role || got.OutputType != want.output {
			t.Errorf("Target %s has role %q and output %q", want.name, got.Role, got.OutputType)
		}
	}
	language, err = New(fixtureHandler{name: "extra", output: Contribution{Inputs: []TargetInputs{{Target: "private-variant", CompileData: []string{"x"}}}}, visit: func(input MetadataContext) {
		input.Targets[0].BazelTarget = "private-variant"
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := language.(*extension).metadataContributions(context.Background(), "crates/probe", pkg); err == nil {
		t.Fatal("A handler selected a target outside the public Cargo descriptors.")
	}
}

// Protect Cargo-owned inputs from conflicting metadata environment values.
// This catches replacement of Cargo identity and modification of helper rules.
// The handler contract test checks rule names but has no environment values.
func TestMetadataInputConflicts(t *testing.T) {
	r := newRule("rust_library", "lib")
	setAttr(r, "rustc_env", map[string]string{"CARGO_PKG_NAME": "probe"})
	if _, err := applyContributions([]*rule.Rule{r}, []Contribution{{Inputs: []TargetInputs{{Target: "lib", RustcEnv: map[string]string{"CARGO_PKG_NAME": "other"}}}}}); err == nil {
		t.Fatal("Metadata replaced a Cargo environment value.")
	}
	helper := rule.NewRule("filegroup", "helper")
	if _, err := applyContributions([]*rule.Rule{r}, []Contribution{{Rules: []*rule.Rule{helper}}, {Inputs: []TargetInputs{{Target: "helper", CompileData: []string{"x"}}}}}); err == nil {
		t.Fatal("Metadata inputs selected a helper rule.")
	}
}

func TestMetadataHandlerContract(t *testing.T) {
	resource := rule.NewRule("filegroup", "resources")
	resource.SetAttr("srcs", []string{"message.txt"})
	handler := fixtureHandler{name: "extra", output: Contribution{Rules: []*rule.Rule{resource}, Inputs: []TargetInputs{{Target: "lib", CompileData: []string{":resources"}}}}}
	language, err := New(handler)
	if err != nil {
		t.Fatal(err)
	}
	e := language.(*extension)
	for _, raw := range []string{`{"extra":{}}`, `{"extra":{"value":1}}`, `{"extra":{"value":"x","unknown":true}}`} {
		if _, err := e.metadataContributions(context.Background(), "", cargoPackage{Metadata: json.RawMessage(raw)}); err == nil {
			t.Fatalf("Accepted invalid metadata: %s", raw)
		}
	}
	root := t.TempDir()
	pkg := cargoPackage{Name: "probe", Manifest: filepath.Join(root, "Cargo.toml"), RepoRoot: root, Metadata: json.RawMessage(`{"extra":{"value":"x"},"unrelated":{"anything":true}}`), Targets: []cargoTarget{
		{Name: "probe", Kind: []string{"lib"}, Source: filepath.Join(root, "src/lib.rs")},
	}}
	parts, err := e.metadataContributions(context.Background(), "", pkg)
	if err != nil {
		t.Fatal(err)
	}
	lib := newRule("rust_library", "lib")
	rules, err := applyContributions([]*rule.Rule{lib}, parts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || !strings.Contains(rendered(rules), ":resources") {
		t.Fatal("The handler inputs are missing.")
	}
	if _, err := New(handler, handler); err == nil {
		t.Fatal("Duplicate handlers must fail.")
	}
	if _, err := New(fixtureHandler{name: "rules_rs"}); err == nil {
		t.Fatal("The built-in namespace must have one owner.")
	}
	collision := rule.NewRule("filegroup", "lib")
	if _, err := applyContributions([]*rule.Rule{lib}, []Contribution{{Rules: []*rule.Rule{collision}}}); err == nil {
		t.Fatal("A handler replaced a Cargo target.")
	}
	if _, err := applyContributions([]*rule.Rule{lib}, []Contribution{{Inputs: []TargetInputs{{Target: "missing", Data: []string{"x"}}}}}); err == nil {
		t.Fatal("An unknown input target must fail.")
	}
}

// Protect exact numbers in handler schemas and metadata.
// This catches number rounding that accepts invalid metadata.
// Existing handler tests validate string fields without numeric constraints.
func TestMetadataSchemaNumbers(t *testing.T) {
	language, err := New(fixtureHandler{name: "extra", schema: json.RawMessage(`{
        "$schema":"https://json-schema.org/draft/2020-12/schema",
        "type":"object",
        "properties":{"value":{"const":9007199254740993}}
    }`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"workspace", "package"} {
		for _, value := range []string{"9007199254740992", "9007199254740993", "9007199254740994"} {
			t.Run(owner+"/"+value, func(t *testing.T) {
				pkg := cargoPackage{Manifest: "/repo/Cargo.toml"}
				raw := json.RawMessage(`{"extra":{"value":` + value + `}}`)
				if owner == "workspace" {
					pkg.WorkspaceMetadata = raw
				} else {
					pkg.Metadata = raw
				}
				_, err := language.(*extension).metadataContributions(t.Context(), "", pkg)
				if (err == nil) != (value == "9007199254740993") {
					t.Fatalf("Incorrect numeric metadata validation: %v", err)
				}
			})
		}
	}
}

func TestBackendConfigurationValidation(t *testing.T) {
	root := t.TempDir()
	for _, raw := range []string{`{"platform_triples":[]}`, `{"platform_triples":["x"],"manifest":"../Cargo.toml"}`, `{"platform_triples":["x"],"unknown":true}`, `{"platform_triples":["x"],"crates_repository":"@bad"}`} {
		put(t, root, "config.json", raw)
		if _, err := readBackendConfig(root + "/config.json"); err == nil {
			t.Fatalf("Accepted invalid configuration: %s", raw)
		}
	}
	put(t, root, "config.json", `{"platform_triples":["x","x"],"manifest":"nested/Cargo.toml","crates_repository":"dependencies"}`)
	config, err := readBackendConfig(root + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if config.Manifest != "nested/Cargo.toml" || config.CratesRepository != "dependencies" || len(config.PlatformTriples) != 1 {
		t.Fatal("Configuration did not preserve the consumer settings.")
	}
}
