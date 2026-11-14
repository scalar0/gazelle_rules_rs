package metadata

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/bazel-contrib/bazel-gazelle/v2/language"
	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	rulesrs "github.com/scalar0/gazelle_rules_rs/src"
)

//go:embed resources.schema.json
var resourcesSchema []byte

type resources struct{}

func (resources) Spec() rulesrs.HandlerSpec {
	return rulesrs.HandlerSpec{
		Namespace: "resources",
		Schema:    resourcesSchema,
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
	result := rulesrs.Contribution{Rules: []*rule.Rule{r}}
	for _, target := range input.Targets {
		if target.Role == "library" || target.Role == "unit-test" || target.Role == "benchmark-test" {
			result.Inputs = append(result.Inputs, rulesrs.TargetInputs{Target: target.BazelTarget, CompileData: []string{":manifest_resources"}})
		}
	}
	return result, nil
}

func NewV2() language.Language {
	result, err := rulesrs.New(resources{})
	if err != nil {
		panic(err)
	}
	return result
}
