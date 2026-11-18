package rulesrs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bazel-contrib/bazel-gazelle/v2/language"
	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	bzl "github.com/bazelbuild/buildtools/build"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// HandlerSpec declares one metadata namespace and its generated rule kinds.
type HandlerSpec struct {
	Namespace string
	Schema    json.RawMessage
	Kinds     []rule.KindInfo
}

// MetadataContext contains independent copies of workspace and package metadata.
type MetadataContext struct {
	PackageName string
	Manifest    string
	Directory   string
	Workspace   json.RawMessage
	Package     json.RawMessage
	Targets     []TargetInfo
}

// TargetInfo identifies a public Cargo target and its generated output.
type TargetInfo struct {
	Name, Kind, CrateRoot, BazelTarget string
	Role, OutputType                   string
}

// TargetInputs adds declared inputs without replacing Cargo dependency edges.
type TargetInputs struct {
	Target                   string
	CompileData, Data        []string
	RustcEnv, BuildScriptEnv map[string]string
}

// Contribution contains additional rules and inputs for generated Cargo targets.
type Contribution struct {
	Rules  []*rule.Rule
	Inputs []TargetInputs
}

// MetadataHandler generates controlled additions from a validated metadata namespace.
type MetadataHandler interface {
	Spec() HandlerSpec
	Generate(context.Context, MetadataContext) (Contribution, error)
}

type registeredHandler struct {
	handler MetadataHandler
	spec    HandlerSpec
	schema  *jsonschema.Schema
}

// New registers metadata handlers and reserves the built-in rules_rs namespace.
// A custom gazelle_binary language package can call New from its NewV2 constructor.
func New(handlers ...MetadataHandler) (language.Language, error) {
	e := &extension{cfg: defaultBackendConfig(), kinds: kindMap()}
	namespaces := map[string]bool{metadataNamespace: true}
	for _, handler := range handlers {
		if handler == nil {
			return nil, fmt.Errorf("metadata handler must not be nil")
		}
		spec := handler.Spec()
		if spec.Namespace == "" || namespaces[spec.Namespace] {
			return nil, fmt.Errorf("metadata namespace %q has multiple handlers or is empty", spec.Namespace)
		}
		namespaces[spec.Namespace] = true
		id := "urn:gazelle:metadata:" + spec.Namespace
		schema, err := compileSchema(id, spec.Schema)
		if err != nil {
			return nil, fmt.Errorf("schema for %s: %w", spec.Namespace, err)
		}
		for _, kind := range spec.Kinds {
			if kind.Name == "" {
				return nil, fmt.Errorf("handler %s declares an unnamed rule kind", spec.Namespace)
			}
			if _, found := e.kinds[kind.Name]; found {
				return nil, fmt.Errorf("rule kind %s has multiple owners", kind.Name)
			}
			e.kinds[kind.Name] = kind
		}
		e.handlers = append(e.handlers, registeredHandler{handler, spec, schema})
	}
	return e, nil
}

func metadataNamespaceValue(raw json.RawMessage, namespace string) (json.RawMessage, error) {
	values := map[string]json.RawMessage{}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, err
		}
	}
	if value, ok := values[namespace]; ok {
		return slices.Clone(value), nil
	}
	return nil, nil
}

func (e *extension) metadataContributions(ctx context.Context, rel string, pkg cargoPackage) ([]Contribution, error) {
	targets, err := packageTargetInfos(pkg)
	if err != nil {
		return nil, err
	}
	var result []Contribution
	for _, registered := range e.handlers {
		workspace, err := metadataNamespaceValue(pkg.WorkspaceMetadata, registered.spec.Namespace)
		if err != nil {
			return nil, err
		}
		local, err := metadataNamespaceValue(pkg.Metadata, registered.spec.Namespace)
		if err != nil {
			return nil, err
		}
		// Unregistered namespaces remain untouched. An absent optional namespace needs no handler call.
		if workspace == nil && local == nil {
			continue
		}
		for _, raw := range []json.RawMessage{workspace, local} {
			if raw == nil {
				continue
			}
			value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				return nil, err
			}
			if err := registered.schema.Validate(value); err != nil {
				return nil, fmt.Errorf("%s metadata %s: %w", pkg.Manifest, registered.spec.Namespace, err)
			}
		}
		if workspace == nil {
			workspace = json.RawMessage(`{}`)
		}
		if local == nil {
			local = json.RawMessage(`{}`)
		}
		contribution, err := registered.handler.Generate(ctx, MetadataContext{PackageName: pkg.Name, Manifest: pkg.Manifest, Directory: rel, Workspace: workspace, Package: local, Targets: slices.Clone(targets)})
		if err != nil {
			return nil, fmt.Errorf("%s metadata %s: %w", pkg.Manifest, registered.spec.Namespace, err)
		}
		for _, input := range contribution.Inputs {
			if !slices.ContainsFunc(targets, func(target TargetInfo) bool { return target.BazelTarget == input.Target }) {
				return nil, fmt.Errorf("handler %s input target %s is not a public Cargo target", registered.spec.Namespace, input.Target)
			}
		}
		for _, r := range contribution.Rules {
			if r == nil {
				return nil, fmt.Errorf("handler %s returned a nil rule", registered.spec.Namespace)
			}
			declared := slices.ContainsFunc(registered.spec.Kinds, func(kind rule.KindInfo) bool { return kind.Name == r.Kind() })
			if !declared {
				return nil, fmt.Errorf("handler %s returned undeclared rule kind %s", registered.spec.Namespace, r.Kind())
			}
			if r.Name() == "" {
				return nil, fmt.Errorf("handler %s returned an unnamed rule", registered.spec.Namespace)
			}
			if !ownsRule(r) {
				r.AddComment(marker)
			}
		}
		result = append(result, contribution)
	}
	return result, nil
}

func addInputList(r *rule.Rule, key string, values []string) {
	if len(values) == 0 {
		return
	}
	setAttr(r, key, appendInputs(r.Attr(key), values))
}

func applyContributions(rules []*rule.Rule, additions []Contribution) ([]*rule.Rule, error) {
	cargoRules := slices.Clone(rules)
	names := map[string]bool{}
	for _, r := range rules {
		names[r.Name()] = true
	}
	for _, contribution := range additions {
		for _, input := range contribution.Inputs {
			matched := false
			for _, r := range cargoRules {
				if r.Name() != input.Target && r.PrivateAttr("cargo_input_target") != input.Target {
					continue
				}
				matched = true
				if r.Kind() == "alias" {
					continue
				}
				if r.Kind() == "rust_doc_test" || r.Kind() == "rust_doc" || r.Kind() == "test_suite" {
					if r.Name() == input.Target {
						return nil, fmt.Errorf("target %s does not accept compiler inputs", input.Target)
					}
					continue
				}
				addInputList(r, "compile_data", input.CompileData)
				addInputList(r, "data", input.Data)
				for key, values := range map[string]map[string]string{"rustc_env": input.RustcEnv, "build_script_env": input.BuildScriptEnv} {
					if len(values) == 0 {
						continue
					}
					if key == "build_script_env" && r.Kind() != "cargo_build_script" {
						return nil, fmt.Errorf("target %s is not a build script", input.Target)
					}
					current := literalStringDict(r.Attr(key))
					if current == nil {
						current = map[string]string{}
						if r.Attr(key) != nil {
							return nil, fmt.Errorf("target %s has conditional %s", input.Target, key)
						}
					}
					for name, value := range values {
						if old, exists := current[name]; exists && old != value {
							return nil, fmt.Errorf("target %s has conflicting %s.%s", input.Target, key, name)
						}
						current[name] = value
					}
					setAttr(r, key, current)
				}
			}
			if !matched {
				return nil, fmt.Errorf("metadata input target %s does not exist", input.Target)
			}
		}
		for _, r := range contribution.Rules {
			if names[r.Name()] {
				return nil, fmt.Errorf("metadata rule %s conflicts with another generated target", r.Name())
			}
			names[r.Name()] = true
			rules = append(rules, r)
		}
	}
	return rules, nil
}

func literalStringDict(expr bzl.Expr) map[string]string {
	result := map[string]string{}
	if expr == nil {
		return result
	}
	dict, ok := expr.(*bzl.DictExpr)
	if !ok {
		return nil
	}
	for _, item := range dict.List {
		key, k := item.Key.(*bzl.StringExpr)
		value, v := item.Value.(*bzl.StringExpr)
		if !k || !v {
			return nil
		}
		result[key.Value] = value.Value
	}
	return result
}
