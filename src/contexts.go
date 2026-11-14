// Context labels use a content hash that excludes absolute paths and Cargo unit indices.
package rulesrs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	bzl "github.com/bazelbuild/buildtools/build"
	"github.com/scalar0/gazelle_rules_rs/src/internal/cargo"
)

// Cargo documents this versioned interface under its public unit-graph option.
type unitGraph struct {
	Version int         `json:"version"`
	Units   []cargoUnit `json:"units"`
	Roots   []int       `json:"roots"`
}

type cargoUnit struct {
	Package      string                `json:"pkg_id"`
	Target       cargoTarget           `json:"target"`
	Platform     *string               `json:"platform"`
	Mode         string                `json:"mode"`
	Features     []string              `json:"features"`
	Dependencies []cargoUnitDependency `json:"dependencies"`
}

type cargoUnitDependency struct {
	Index int    `json:"index"`
	Name  string `json:"extern_crate_name"`
}

type contextTarget struct {
	Template string
	Source   string
	Kind     string
	Features []string
	Deps     []string
	Aliases  map[string]string
	Binaries map[string]string
	Label    string `json:"-"`
}

type packageContexts struct {
	Platforms []string
	Bindings  map[string]map[string]*contextTarget
	Variants  map[string]*contextTarget
}

func unitName(pkg cargoPackage, unit cargoUnit) (string, error) {
	info, err := targetDescriptor(pkg, unit.Target, unit.Mode)
	return info.BazelTarget, err
}

func readConfiguredContexts(ctx context.Context, root, tool string, packages map[string]cargoPackage, members []string, platforms []string) error {
	if len(platforms) == 0 {
		return fmt.Errorf("configure platform_triples for Cargo contexts")
	}
	for rel, pkg := range packages {
		pkg.Contexts = &packageContexts{Platforms: platforms, Bindings: map[string]map[string]*contextTarget{}, Variants: map[string]*contextTarget{}}
		packages[rel] = pkg
	}
	for _, platform := range platforms {
		commands := []string{"build", "examples", "test", "bench"}
		for _, pkg := range packages {
			for _, target := range pkg.Targets {
				if target.Doctest {
					commands = append(commands, "doc")
					break
				}
			}
			if slices.Contains(commands, "doc") {
				break
			}
		}
		for _, command := range commands {
			args := []string{command, "--workspace", "--unit-graph", "-Z", "unstable-options", "--target=" + platform}
			if command == "examples" {
				args[0] = "build"
				args = append(args, "--examples")
			}
			if command == "doc" {
				args[0] = "test"
				args = append(args, "--doc")
			}
			output, err := cargo.Output(ctx, root, tool, args...)
			if err != nil {
				return err
			}
			var graph unitGraph
			if err := json.Unmarshal(output, &graph); err != nil {
				return fmt.Errorf("read Cargo unit graph: %w", err)
			}
			if err := collectContexts(graph, platform, command, packages, members); err != nil {
				return err
			}
		}
	}
	for _, pkg := range packages {
		for _, target := range pkg.Targets {
			if len(target.RequiredFeatures) != 0 || len(target.Kind) != 1 || (target.Kind[0] != "lib" && target.Kind[0] != "proc-macro" && target.Kind[0] != "bin") {
				continue
			}
			name, err := unitName(pkg, cargoUnit{Target: target, Mode: "build"})
			if err != nil {
				return err
			}
			if len(pkg.Contexts.Bindings[name]) != len(platforms) {
				return fmt.Errorf("the Cargo unit graphs do not cover unconditional target %s", name)
			}
		}
	}
	compactContexts(packages)
	return nil
}

func collectContexts(graph unitGraph, platform, command string, packages map[string]cargoPackage, members []string) error {
	if graph.Version != 1 {
		return fmt.Errorf("the Cargo unit graph uses unsupported version %d", graph.Version)
	}
	locations := map[string]string{}
	localContexts := map[string]string{}
	for rel, pkg := range packages {
		locations[pkg.ID] = rel
		for id, label := range pkg.LocalContexts {
			localContexts[id] = label
		}
	}
	for _, unit := range graph.Units {
		for _, dep := range unit.Dependencies {
			if dep.Index < 0 || dep.Index >= len(graph.Units) {
				return fmt.Errorf("the Cargo unit graph has an invalid dependency index %d", dep.Index)
			}
		}
	}
	for _, index := range graph.Roots {
		if index < 0 || index >= len(graph.Units) {
			return fmt.Errorf("the Cargo unit graph has an invalid root index %d", index)
		}
	}
	if command == "bench" {
		for i := range graph.Units {
			if graph.Units[i].Mode == "test" {
				graph.Units[i].Mode = "bench"
			}
		}
	}
	resolved := map[int]*contextTarget{}
	visiting := map[int]bool{}
	var visit func(int) (*contextTarget, error)
	visit = func(index int) (*contextTarget, error) {
		if result, ok := resolved[index]; ok {
			return result, nil
		}
		if visiting[index] {
			return nil, fmt.Errorf("the Cargo unit graph contains a dependency cycle")
		}
		visiting[index] = true
		defer delete(visiting, index)
		unit := graph.Units[index]
		rel, scoped := locations[unit.Package]
		if !scoped {
			if label, exists := localContexts[unit.Package]; exists {
				return &contextTarget{Label: label, Kind: targetKind(unit.Target)}, nil
			}
			if slices.Contains(members, unit.Package) {
				return nil, fmt.Errorf("the Cargo context dependency %s is outside the configured generation scope", unit.Package)
			}
			return nil, nil
		}
		pkg := packages[rel]
		name, err := unitName(pkg, unit)
		if err != nil {
			return nil, err
		}
		source, _ := filepath.Rel(filepath.Dir(pkg.Manifest), unit.Target.Source)
		result := &contextTarget{
			Template: name, Source: filepath.ToSlash(source), Kind: targetKind(unit.Target),
			Features: slices.Clone(unit.Features), Aliases: map[string]string{}, Binaries: map[string]string{},
		}
		slices.Sort(result.Features)
		dependencies := unit.Dependencies
		if unit.Mode == "run-custom-build" {
			dependencies = nil
			for _, dep := range unit.Dependencies {
				compiled := graph.Units[dep.Index]
				if compiled.Package == unit.Package && compiled.Mode == "build" && slices.Equal(compiled.Target.Kind, []string{"custom-build"}) {
					dependencies = append(dependencies, compiled.Dependencies...)
				} else {
					dependencies = append(dependencies, dep)
				}
			}
		}
		for _, dep := range dependencies {
			child := graph.Units[dep.Index]
			dependency, err := visit(dep.Index)
			if err != nil {
				return nil, err
			}
			if dependency == nil {
				continue
			}
			// The doctest crate already carries its build-script provider.
			if unit.Mode == "doctest" && dependency.Kind == "custom-build" {
				continue
			}
			if dependency.Kind == "bin" {
				result.Binaries[child.Target.Name] = dependency.Label
				continue
			}
			result.Deps = append(result.Deps, dependency.Label)
			if dependency.Kind != "custom-build" {
				result.Aliases[dependency.Label] = dep.Name
			}
		}
		slices.Sort(result.Deps)
		result.Deps = slices.Compact(result.Deps)
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(encoded)
		variant := fmt.Sprintf("%s-cargo-%x", name, digest[:8])
		result.Label = "//" + rel + ":" + variant
		resolved[index] = result
		if unit.Mode != "test" && unit.Mode != "doctest" && unit.Mode != "bench" {
			pkg.Contexts.Variants[variant] = result
		}
		pkg = packages[rel]
		pkg.Features = append(pkg.Features, result.Features...)
		slices.Sort(pkg.Features)
		pkg.Features = slices.Compact(pkg.Features)
		packages[rel] = pkg
		return result, nil
	}
	for index, unit := range graph.Units {
		rel, scoped := locations[unit.Package]
		if !scoped || (slices.Equal(unit.Target.Kind, []string{"custom-build"}) && unit.Mode != "run-custom-build") {
			continue
		}
		result, err := visit(index)
		if err != nil {
			return err
		}
		canonical := command == "test" && (unit.Mode == "test" || ((result.Kind == "example" || result.Kind == "bench") && unit.Platform != nil))
		if command == "doc" {
			canonical = unit.Mode == "doctest"
		}
		if command == "bench" {
			canonical = unit.Mode == "bench"
		}
		if command == "build" || command == "examples" {
			canonical = unit.Platform != nil || (result.Kind == "proc-macro" && slices.Contains(graph.Roots, index))
		}
		if canonical {
			bindings := packages[rel].Contexts.Bindings
			if bindings[result.Template] == nil {
				bindings[result.Template] = map[string]*contextTarget{}
			}
			bindings[result.Template][platform] = result
		}
	}
	return nil
}

// Give a uniform public binding its normal label before any package emits rules.
// Rewrite all dependency edges together to preserve Rust type identity.
func compactContexts(packages map[string]cargoPackage) {
	replacements := map[string]string{}
	public := map[string]bool{}
	for rel, pkg := range packages {
		contexts := pkg.Contexts
		for name, bindings := range contexts.Bindings {
			if len(bindings) != len(contexts.Platforms) {
				continue
			}
			label := ""
			uniform := true
			for _, binding := range bindings {
				if label != "" && label != binding.Label {
					uniform = false
					break
				}
				label = binding.Label
			}
			oldName := strings.TrimPrefix(label, "//"+rel+":")
			variant := contexts.Variants[oldName]
			if !uniform || variant == nil {
				continue
			}
			replacement := "//" + rel + ":" + name
			replacements[label] = replacement
			public[replacement] = true
			delete(contexts.Variants, oldName)
			contexts.Variants[name] = variant
		}
	}
	replace := func(label string) string {
		if replacement, ok := replacements[label]; ok {
			return replacement
		}
		return label
	}
	seen := map[*contextTarget]bool{}
	rewrite := func(target *contextTarget) {
		if seen[target] {
			return
		}
		seen[target] = true
		target.Label = replace(target.Label)
		for i, dep := range target.Deps {
			target.Deps[i] = replace(dep)
		}
		slices.Sort(target.Deps)
		aliases := map[string]string{}
		for label, name := range target.Aliases {
			aliases[replace(label)] = name
		}
		target.Aliases = aliases
		for name, label := range target.Binaries {
			target.Binaries[name] = replace(label)
		}
	}
	for _, pkg := range packages {
		for _, variant := range pkg.Contexts.Variants {
			rewrite(variant)
		}
		for _, bindings := range pkg.Contexts.Bindings {
			for _, binding := range bindings {
				rewrite(binding)
			}
		}
	}
	for rel, pkg := range packages {
		// The backend assumes directory-name labels for local dependencies.
		// Keep explicit edges for workspace-root dependencies and nested workspaces.
		simple := pkg.WorkspaceRoot == packageRepoRoot(pkg)
		for _, dep := range pkg.Dependencies {
			if dep.Path != "" && dep.Path == packageRepoRoot(pkg) {
				simple = false
			}
		}
		for name, variant := range pkg.Contexts.Variants {
			if name != variant.Template {
				simple = false
			}
		}
		for _, bindings := range pkg.Contexts.Bindings {
			if len(bindings) != len(pkg.Contexts.Platforms) {
				simple = false
			}
			for _, binding := range bindings {
				if !slices.Equal(binding.Features, pkg.Features) {
					simple = false
				}
				for _, dep := range binding.Deps {
					if !public[dep] {
						simple = false
					}
				}
				for _, binary := range binding.Binaries {
					if !public[binary] {
						simple = false
					}
				}
			}
		}
		if simple {
			pkg.Contexts = nil
			packages[rel] = pkg
		}
	}
}

func cloneContextRule(template *rule.Rule, name string) *rule.Rule {
	r := newRule(template.Kind(), name)
	for _, key := range []string{"cargo_template", "cargo_input_target", "cargo_dev", "cargo_target_info"} {
		r.SetPrivateAttr(key, template.PrivateAttr(key))
	}
	for _, attr := range template.AttrKeys() {
		if attr != "name" {
			setAttr(r, attr, template.Attr(attr))
		}
	}
	return r
}

func contextAliases(pkg cargoPackage, aliases map[string]string) bzl.Expr {
	if len(aliases) == 0 {
		return targetAliases(pkg)
	}
	return &bzl.BinaryExpr{X: targetAliases(pkg), Op: "|", Y: rule.ExprFromValue(aliases)}
}

func contextDeps(kind string, local bzl.Expr) bzl.Expr {
	flags := []string{"normal", "cargo_only"}
	switch kind {
	case "cargo_build_script":
		flags = []string{"build", "cargo_only"}
	case "rust_test", "rust_doc_test", "example":
		flags = []string{"normal", "normal_dev", "cargo_only"}
	}
	return &bzl.BinaryExpr{X: call("all_crate_deps", flags...), Op: "+", Y: local}
}

func expressionSelect(values map[string]bzl.Expr) bzl.Expr {
	var keys []string
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if len(keys) != 0 {
		first := bzl.FormatString(values[keys[0]])
		constant := true
		for _, key := range keys[1:] {
			constant = constant && bzl.FormatString(values[key]) == first
		}
		if constant {
			return values[keys[0]]
		}
	}
	entries := make([]*bzl.KeyValueExpr, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, &bzl.KeyValueExpr{Key: rule.ExprFromValue(key), Value: values[key]})
	}
	return &bzl.CallExpr{X: &bzl.Ident{Name: "select"}, List: []bzl.Expr{&bzl.DictExpr{List: entries, ForceMultiLine: true}}}
}

func generateContexts(rel string, pkg cargoPackage, templates []*rule.Rule) ([]*rule.Rule, error) {
	byName := map[string]*rule.Rule{}
	for _, r := range templates {
		byName[r.Name()] = r
	}
	var result []*rule.Rule
	for _, template := range templates {
		if targetRole(template) == "package-alias" {
			result = append(result, template)
			continue
		}
		if template.Kind() == "test_suite" {
			var tests []string
			for _, label := range template.AttrStrings("tests") {
				if len(pkg.Contexts.Bindings[strings.TrimPrefix(label, ":")]) != 0 {
					tests = append(tests, label)
				}
			}
			if len(tests) != 0 {
				setAttr(template, "tests", tests)
				result = append(result, template)
			}
			continue
		}
		bindingName := template.Name()
		if name, ok := template.PrivateAttr("cargo_template").(string); ok {
			bindingName = name
		}
		bindings := pkg.Contexts.Bindings[bindingName]
		if len(bindings) == 0 {
			continue
		}
		r := cloneContextRule(template, template.Name())
		if crate := r.AttrString("crate"); crate != "" && r.Kind() != "rust_doc_test" {
			base := byName[strings.TrimPrefix(crate, ":")]
			if base == nil {
				return nil, fmt.Errorf("context test %s has no source target", r.Name())
			}
			if !compatibleTestContext(pkg.Contexts, strings.TrimPrefix(crate, ":"), bindingName) {
				r.DelAttr("crate")
				for _, attr := range []string{"srcs", "crate_root", "crate_name"} {
					setAttr(r, attr, base.Attr(attr))
				}
				if base.Kind() == "rust_proc_macro" {
					setAttr(r, "rustc_flags", rule.UnsortedStrings{"--extern=proc_macro"})
				}
			}
		}
		features, deps, compatible := map[string][]string{}, map[string][]string{}, map[string][]string{}
		aliases, actual := map[string]bzl.Expr{}, map[string]bzl.Expr{}
		docCrates := map[string]bzl.Expr{}
		environments, runtimeData := map[string]bzl.Expr{}, map[string]bzl.Expr{}
		hasBinaries := false
		for _, platform := range pkg.Contexts.Platforms {
			condition := "@rules_rs//rs/platforms/config:" + platform
			context := bindings[platform]
			if context == nil {
				compatible[platform] = []string{"@platforms//:incompatible"}
				features[platform], deps[platform] = nil, nil
				aliases[condition] = targetAliases(pkg)
				environments[condition], runtimeData[condition] = runtimeInputs(pkg, nil)
				continue
			}
			features[platform], deps[platform], compatible[platform] = context.Features, context.Deps, nil
			aliases[condition] = contextAliases(pkg, context.Aliases)
			actual[condition] = rule.ExprFromValue(context.Label)
			if r.Kind() == "rust_doc_test" {
				for _, dep := range context.Deps {
					variant := strings.TrimPrefix(dep, "//"+rel+":")
					if local := pkg.Contexts.Variants[variant]; local != nil && (local.Kind == "lib" || local.Kind == "proc-macro") {
						docCrates[condition] = rule.ExprFromValue(dep)
						// The crate attribute already supplies this dependency.
						deps[platform] = slices.DeleteFunc(slices.Clone(deps[platform]), func(label string) bool { return label == dep })
					}
				}
			}
			environments[condition], runtimeData[condition] = runtimeInputs(pkg, context.Binaries)
			hasBinaries = hasBinaries || len(context.Binaries) != 0
		}
		if (r.Kind() == "cargo_build_script" || r.Kind() == "rust_proc_macro" || (r.Kind() == "rust_library" && template.PrivateAttr("cargo_template") == nil)) && !publicContext(rel, r.Name(), bindings) {
			r = newRule("alias", r.Name())
			r.SetPrivateAttr("cargo_target_info", template.PrivateAttr("cargo_target_info"))
			r.SetPrivateAttr("cargo_input_target", template.PrivateAttr("cargo_input_target"))
			setAttr(r, "actual", expressionSelect(actual))
		} else {
			setAttr(r, "crate_features", platformValues(features))
			depKind := r.Kind()
			if template.PrivateAttr("cargo_dev") == true {
				depKind = "example"
			}
			setAttr(r, "deps", contextDeps(depKind, platformValues(deps)))
			if r.Kind() != "rust_doc_test" {
				setAttr(r, "aliases", expressionSelect(aliases))
			}
		}
		if len(docCrates) != 0 {
			setAttr(r, "crate", expressionSelect(docCrates))
		}
		if hasBinaries {
			setAttr(r, "rustc_env", expressionSelect(environments))
			addData(r, expressionSelect(runtimeData))
		}
		if common, constant := uniformValues(compatible); !constant || len(common) != 0 {
			setAttr(r, "target_compatible_with", platformValues(compatible))
		}
		result = append(result, r)
	}
	var variants []string
	for name := range pkg.Contexts.Variants {
		variants = append(variants, name)
	}
	slices.Sort(variants)
	for _, name := range variants {
		context := pkg.Contexts.Variants[name]
		if name == context.Template {
			continue
		}
		template := byName[context.Template]
		if template == nil {
			return nil, fmt.Errorf("the Cargo context %s has no target template in %s", context.Template, rel)
		}
		r := cloneContextRule(template, name)
		r.SetPrivateAttr("cargo_input_target", context.Template)
		setAttr(r, "crate_features", rule.ExprFromValue(context.Features))
		depKind := r.Kind()
		if template.PrivateAttr("cargo_dev") == true {
			depKind = "example"
		}
		setAttr(r, "deps", contextDeps(depKind, rule.ExprFromValue(context.Deps)))
		setAttr(r, "aliases", contextAliases(pkg, context.Aliases))
		setAttr(r, "tags", append(r.AttrStrings("tags"), "manual"))
		result = append(result, r)
	}
	return result, nil
}

func publicContext(rel, name string, bindings map[string]*contextTarget) bool {
	for _, binding := range bindings {
		if binding.Label != "//"+rel+":"+name {
			return false
		}
	}
	return len(bindings) != 0
}

// A crate-based test inherits its crate's features, dependencies, and aliases.
func compatibleTestContext(contexts *packageContexts, crate, test string) bool {
	for _, platform := range contexts.Platforms {
		base, unit := contexts.Bindings[crate][platform], contexts.Bindings[test][platform]
		if base == nil || unit == nil || !slices.Equal(base.Features, unit.Features) {
			return false
		}
		for _, dep := range base.Deps {
			if !slices.Contains(unit.Deps, dep) {
				return false
			}
		}
		for label, name := range base.Aliases {
			if unit.Aliases[label] != name {
				return false
			}
		}
	}
	return true
}
