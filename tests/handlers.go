package rulesrs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
)

type fixtureHandler struct {
	name   string
	output Contribution
}

func (h fixtureHandler) Spec() HandlerSpec {
	return HandlerSpec{Namespace: h.name, Schema: json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["value"],"additionalProperties":false,"properties":{"value":{"type":"string"}}}`), Kinds: []rule.KindInfo{{Name: "filegroup", MergeableAttrs: map[string]bool{"srcs": true}, NonEmptyAttrs: map[string]bool{"srcs": true}}}}
}
func (h fixtureHandler) Generate(_ context.Context, _ MetadataContext) (Contribution, error) {
	return h.output, nil
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
	parts, err := e.metadataContributions(context.Background(), "", cargoPackage{Metadata: json.RawMessage(`{"extra":{"value":"x"},"unrelated":{"anything":true}}`)})
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
