package metadata

import (
	"context"
	"encoding/json"

	"github.com/bazel-contrib/bazel-gazelle/v2/language"
	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	rulesrs "github.com/scalar0/gazelle_rules_rs"
)

type resources struct{}

func (resources) Spec() rulesrs.HandlerSpec {
	return rulesrs.HandlerSpec{
		Namespace: "resources",
		Schema:    json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"required":["files"],"properties":{"files":{"type":"array","items":{"type":"string"}}}}`),
		Kinds:     []rule.KindInfo{{Name: "filegroup", MergeableAttrs: map[string]bool{"srcs": true}, NonEmptyAttrs: map[string]bool{"srcs": true}}},
	}
}

func (resources) Generate(_ context.Context, input rulesrs.MetadataContext) (rulesrs.Contribution, error) {
	var config struct{ Files []string }
	if err := json.Unmarshal(input.Package, &config); err != nil {
		return rulesrs.Contribution{}, err
	}
	r := rule.NewRule("filegroup", "manifest_resources")
	r.SetAttr("srcs", config.Files)
	return rulesrs.Contribution{Rules: []*rule.Rule{r}, Inputs: []rulesrs.TargetInputs{{Target: "probe", CompileData: []string{":manifest_resources"}}}}, nil
}

func NewV2() language.Language {
	result, err := rulesrs.New(resources{})
	if err != nil {
		panic(err)
	}
	return result
}
