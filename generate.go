package rulesrs

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	bzl "github.com/bazelbuild/buildtools/build"
)

// Generated expressions replace prior values through Gazelle's public merge API.
type generatedExpr struct{ expr bzl.Expr }

func (value generatedExpr) BzlExpr() bzl.Expr       { return value.expr }
func (value generatedExpr) Merge(bzl.Expr) bzl.Expr { return value.expr }

func setAttr(r *rule.Rule, name string, value interface{}) {
	if expr, ok := value.(bzl.Expr); ok {
		value = generatedExpr{expr}
	}
	r.SetAttr(name, value)
}

func addData(r *rule.Rule, data bzl.Expr) {
	if existing := r.Attr("data"); existing != nil {
		list, literal := existing.(*bzl.ListExpr)
		if !literal || len(list.List) != 0 {
			data = &bzl.BinaryExpr{X: existing, Op: "+", Y: data}
		}
	}
	setAttr(r, "data", data)
}

func call(name string, flags ...string) bzl.Expr {
	expr := &bzl.CallExpr{X: &bzl.Ident{Name: name}}
	for _, flag := range flags {
		expr.List = append(expr.List, &bzl.AssignExpr{LHS: &bzl.Ident{Name: flag}, Op: "=", RHS: &bzl.Ident{Name: "True"}})
	}
	return expr
}

func deps(flags []string, local ...string) bzl.Expr {
	resolved := call("all_crate_deps", flags...)
	if len(local) == 0 {
		return resolved
	}
	return &bzl.BinaryExpr{X: resolved, Op: "+", Y: rule.ExprFromValue(local)}
}

func newRule(kind, name string) *rule.Rule {
	r := rule.NewRule(kind, name)
	r.AddComment(marker)
	return r
}

func librarySources(root string) bzl.Expr {
	if !strings.HasPrefix(root, "src/") {
		return targetSources(root)
	}
	covered := strings.HasPrefix(root, "src/") && strings.HasSuffix(root, ".rs") && root != "src/main.rs"
	glob := rule.ExprFromValue(rule.GlobValue{
		Patterns:   []string{"src/**/*.rs"},
		Excludes:   []string{"src/main.rs"},
		AllowEmpty: !covered,
	})
	if covered {
		return glob
	}
	return &bzl.BinaryExpr{X: glob, Op: "+", Y: rule.ExprFromValue([]string{root})}
}

func setEdition(r *rule.Rule, pkg cargoPackage, target cargoTarget) {
	if target.Edition != "" && target.Edition != pkg.Edition {
		setAttr(r, "edition", target.Edition)
	} else {
		setAttr(r, "edition", call("edition"))
	}
}

func uniformValues(values map[string][]string) ([]string, bool) {
	var first []string
	seen := false
	for _, value := range values {
		if seen && !slices.Equal(first, value) {
			return nil, false
		}
		first, seen = value, true
	}
	return first, true
}

func platformValues(values map[string][]string) bzl.Expr {
	if common, constant := uniformValues(values); constant {
		return rule.ExprFromValue(common)
	}
	branches := map[string][]string{}
	for platform, value := range values {
		branches["@rules_rs//rs/platforms/config:"+platform] = value
	}
	return rule.ExprFromValue(rule.SelectStringListValue(branches))
}

// cargoEnvironment supplies package values that Cargo exposes during compilation.
func cargoEnvironment(pkg cargoPackage) map[string]string {
	return map[string]string{
		"CARGO_PKG_NAME":        pkg.Name,
		"CARGO_PKG_DESCRIPTION": pkg.Description,
	}
}

func rustAttrs(r *rule.Rule, pkg cargoPackage, target cargoTarget) {
	kinds := []string{""}
	if r.Kind() == "rust_test" {
		kinds = append(kinds, "dev")
	}
	setAttr(r, "aliases", targetAliases(pkg, kinds...))
	setEdition(r, pkg, target)
	setAttr(r, "lint_config", call("lint_config"))
	setAttr(r, "crate_features", rule.ExprFromValue(pkg.Features))
	setAttr(r, "version", pkg.Version)
	setAttr(r, "rustc_env", cargoEnvironment(pkg))
}

// Alias keys are dependency edges in Bazel, including aliases absent from deps.
// Filter local aliases by Cargo dependency kind before Bazel analyzes them.
func targetAliases(pkg cargoPackage, kinds ...string) bzl.Expr {
	if pkg.Contexts != nil {
		// Cargo contexts supply every local alias, including workspace-root dependencies.
		return &bzl.Comprehension{
			Curly: true,
			Body:  &bzl.KeyValueExpr{Key: &bzl.Ident{Name: "label"}, Value: &bzl.Ident{Name: "name"}},
			Clauses: []bzl.Expr{
				&bzl.ForClause{Vars: &bzl.TupleExpr{NoBrackets: true, List: []bzl.Expr{&bzl.Ident{Name: "label"}, &bzl.Ident{Name: "name"}}}, X: &bzl.CallExpr{X: &bzl.DotExpr{X: call("aliases"), Name: "items"}}},
				&bzl.IfClause{Cond: &bzl.CallExpr{X: &bzl.DotExpr{X: &bzl.Ident{Name: "label"}, Name: "startswith"}, List: []bzl.Expr{rule.ExprFromValue("@")}}},
			},
		}
	}
	local, allowed := map[string]bool{}, map[string]bool{}
	for _, dependency := range pkg.Dependencies {
		if dependency.Path == "" {
			continue
		}
		rel, err := filepath.Rel(packageRepoRoot(pkg), dependency.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		label := "//" + filepath.ToSlash(rel)
		local[label] = true
		allowed[label] = allowed[label] || slices.Contains(kinds, dependency.Kind)
	}
	var excluded []string
	for label := range local {
		if !allowed[label] {
			excluded = append(excluded, label)
		}
	}
	if len(excluded) == 0 {
		return call("aliases")
	}
	slices.Sort(excluded)
	return &bzl.Comprehension{
		Curly: true,
		Body:  &bzl.KeyValueExpr{Key: &bzl.Ident{Name: "label"}, Value: &bzl.Ident{Name: "name"}},
		Clauses: []bzl.Expr{
			&bzl.ForClause{
				Vars: &bzl.TupleExpr{NoBrackets: true, List: []bzl.Expr{&bzl.Ident{Name: "label"}, &bzl.Ident{Name: "name"}}},
				X:    &bzl.CallExpr{X: &bzl.DotExpr{X: call("aliases"), Name: "items"}},
			},
			&bzl.IfClause{Cond: &bzl.BinaryExpr{X: &bzl.Ident{Name: "label"}, Op: "not in", Y: rule.ExprFromValue(excluded)}},
		},
	}
}

func integrationName(base string, target cargoTarget) string {
	return base + "-integration-tests/" + target.Name
}

func unitTest(pkg cargoPackage, target cargoTarget, crate string, compileData bzl.Expr) *rule.Rule {
	unit := newRule("rust_test", crate+"-unit-tests")
	unit.SetPrivateAttr("cargo_input_target", crate)
	rustAttrs(unit, pkg, target)
	setAttr(unit, "crate", ":"+crate)
	setAttr(unit, "compile_data", compileData)
	setAttr(unit, "deps", deps([]string{"normal", "normal_dev"}))
	setHarness(unit, target)
	return unit
}

func setHarness(r *rule.Rule, target cargoTarget) {
	if target.Harness != nil && !*target.Harness {
		setAttr(r, "use_libtest_harness", false)
	}
}

func runtimeInputs(pkg cargoPackage, binaries map[string]string) (bzl.Expr, bzl.Expr) {
	env := cargoEnvironment(pkg)
	var data []string
	for name, label := range binaries {
		env["CARGO_BIN_EXE_"+name] = "$(rootpath " + label + ")"
		data = append(data, label)
	}
	sort.Strings(data)
	return rule.ExprFromValue(env), rule.ExprFromValue(data)
}

// Settings contain labels or package-relative glob patterns.
func inputs(patterns []string) bzl.Expr {
	labels, globs := []string{}, []string{}
	for _, pattern := range patterns {
		if strings.ContainsAny(pattern, "*?[") {
			globs = append(globs, pattern)
		} else {
			labels = append(labels, pattern)
		}
	}
	if len(globs) == 0 {
		return rule.ExprFromValue(labels)
	}
	glob := rule.ExprFromValue(rule.GlobValue{Patterns: globs})
	if len(labels) == 0 {
		return glob
	}
	return &bzl.BinaryExpr{X: glob, Op: "+", Y: rule.ExprFromValue(labels)}
}

func generate(root, rel string, pkg cargoPackage, opts settings) ([]*rule.Rule, error) {
	if pkg.WorkspaceRoot == "" {
		pkg.WorkspaceRoot = root
	}
	pkg.RepoRoot = root
	dir := filepath.Join(root, rel)
	base := path.Base(rel)
	if rel == "" || rel == "." {
		base = pkg.Name
	}
	var rules []*rule.Rule
	readme, err := readmeInput(root, rel, pkg)
	if err != nil {
		return nil, err
	}
	libraryData := uniqueInputs(readme, opts.LibraryCompileData)
	unitData := uniqueInputs(libraryData, opts.UnitCompileData)
	testData := uniqueInputs(readme, opts.TestCompileData)
	buildData := uniqueInputs(opts.BuildData)
	buildEnv := map[string]string{}
	for name, input := range opts.BuildInputs {
		buildData = uniqueInputs(buildData, []string{input.BazelLabel})
		buildEnv[name] = "$(execpath " + input.BazelLabel + ")"
	}
	slices.Sort(buildData)
	hasLib, hasBuild := false, false
	relative := func(file string) (string, error) {
		value, err := filepath.Rel(dir, file)
		if err != nil || (value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator))) {
			return "", fmt.Errorf("target source must be inside %s: %s", rel, file)
		}
		return filepath.ToSlash(value), nil
	}
	for _, target := range pkg.Targets {
		if targetKind(target) == "" {
			return nil, fmt.Errorf("Cargo target %q has unsupported kinds %v", target.Name, target.Kind)
		}
		switch targetKind(target) {
		case "lib", "proc-macro":
			hasLib = true
		case "custom-build":
			hasBuild = true
		}
	}
	features := map[string]bool{}
	for _, feature := range pkg.Features {
		features[feature] = true
	}
	var tests []string
	binaries := map[string]string{}
	for _, target := range pkg.Targets {
		enabled := true
		for _, feature := range target.RequiredFeatures {
			if !features[feature] {
				enabled = false
			}
		}
		if !enabled {
			continue
		}
		file, err := relative(target.Source)
		if err != nil {
			return nil, err
		}
		kind := targetKind(target)
		switch kind {
		case "lib", "proc-macro":
			ruleKind := "rust_library"
			if kind == "proc-macro" {
				ruleKind = "rust_proc_macro"
			}
			r := newRule(ruleKind, base)
			rustAttrs(r, pkg, target)
			setAttr(r, "crate_name", call("crate_name"))
			setAttr(r, "crate_root", file)
			setAttr(r, "srcs", librarySources(file))
			setAttr(r, "compile_data", inputs(libraryData))
			var local []string
			if hasBuild {
				local = append(local, ":"+base+"-build-script")
			}
			setAttr(r, "deps", deps([]string{"normal"}, local...))
			rules = append(rules, r)
			if target.Test {
				rules = append(rules, unitTest(pkg, target, base, inputs(unitData)))
			}
			if target.Bench {
				rules = append(rules, benchmark(pkg, target, base, inputs(unitData)))
			}
			for _, typ := range target.CrateTypes {
				if typ == "lib" || typ == "rlib" || typ == "proc-macro" {
					continue
				}
				kind := crateRuleKind(typ)
				if kind == "" {
					return nil, fmt.Errorf("unsupported library crate type %s", typ)
				}
				output := cloneContextRule(r, base+"-"+typ)
				output.SetKind(kind)
				output.SetPrivateAttr("cargo_template", base)
				output.SetPrivateAttr("cargo_input_target", base)
				rules = append(rules, output)
			}
			if target.Doctest {
				doc := newRule("rust_doc_test", base+"-doc-tests")
				setAttr(doc, "crate", ":"+base)
				setAttr(doc, "deps", deps([]string{"normal", "normal_dev"}))
				rules = append(rules, doc)
			}

		case "custom-build":
			r := newRule("cargo_build_script", base+"-build-script")
			setAttr(r, "srcs", targetSources(file))
			setAttr(r, "crate_root", file)
			setEdition(r, pkg, target)
			setAttr(r, "aliases", targetAliases(pkg, "build"))
			setAttr(r, "crate_features", rule.ExprFromValue(pkg.Features))
			setAttr(r, "version", pkg.Version)
			setAttr(r, "pkg_name", pkg.Name)
			setAttr(r, "deps", deps([]string{"build"}))
			setAttr(r, "compile_data", inputs(opts.BuildCompileData))
			setAttr(r, "data", inputs(buildData))
			setAttr(r, "build_script_env", buildEnv)
			flags, err := buildLints(filepath.Join(pkg.WorkspaceRoot, "Cargo.toml"), pkg.Manifest)
			if err != nil {
				return nil, err
			}
			setAttr(r, "rustc_flags", rule.UnsortedStrings(flags))
			rules = append(rules, r)
		case "test", "bench":
			name := integrationName(base, target)
			if kind == "bench" {
				name = base + "-benches/" + target.Name
			}
			r := newRule("rust_test", name)
			rustAttrs(r, pkg, target)
			setHarness(r, target)
			setAttr(r, "crate_root", file)
			setAttr(r, "crate_name", strings.ReplaceAll(target.Name, "-", "_"))
			setAttr(r, "srcs", targetSources(file))
			setAttr(r, "compile_data", inputs(testData))
			setAttr(r, "data", inputs(opts.TestData))
			var local []string
			if hasLib {
				local = append(local, ":"+base)
			}
			if hasBuild {
				local = append(local, ":"+base+"-build-script")
			}
			setAttr(r, "deps", deps([]string{"normal", "normal_dev"}, local...))
			if kind == "bench" {
				setAttr(r, "tags", []string{"manual"})
				setAttr(r, "args", []string{"--bench"})
			} else {
				setAttr(r, "tags", []string{"restrict_" + base + "-integration-tests"})
			}
			rules = append(rules, r)
			if kind == "test" {
				tests = append(tests, ":"+name)
			}
		case "bin", "example":
			name := base + "-bin"
			if file != "src/main.rs" {
				name += "-" + target.Name
			}
			if kind == "example" {
				name = base + "-examples/" + target.Name
			} else {
				binaries[target.Name] = ":" + name
			}
			ruleKind := "rust_binary"
			if kind == "example" && len(target.CrateTypes) != 0 && target.CrateTypes[0] != "bin" {
				ruleKind = crateRuleKind(target.CrateTypes[0])
				if ruleKind == "" {
					return nil, fmt.Errorf("unsupported example crate type %s", target.CrateTypes[0])
				}
			}
			r := newRule(ruleKind, name)
			if kind == "example" {
				r.SetPrivateAttr("cargo_dev", true)
				setAttr(r, "testonly", true)
			}
			rustAttrs(r, pkg, target)
			setAttr(r, "crate_name", strings.ReplaceAll(target.Name, "-", "_"))
			setAttr(r, "crate_root", file)
			setAttr(r, "srcs", librarySources(file))
			setAttr(r, "compile_data", inputs(libraryData))
			var local []string
			if hasLib {
				local = append(local, ":"+base)
			}
			if hasBuild {
				local = append(local, ":"+base+"-build-script")
			}
			flags := []string{"normal"}
			if kind == "example" {
				flags = append(flags, "normal_dev")
				setAttr(r, "aliases", targetAliases(pkg, "", "dev"))
			}
			setAttr(r, "deps", deps(flags, local...))
			rules = append(rules, r)
			if kind == "example" && len(target.CrateTypes) > 1 {
				for _, typ := range target.CrateTypes[1:] {
					outputKind := crateRuleKind(typ)
					if outputKind == "" {
						return nil, fmt.Errorf("unsupported example crate type %s", typ)
					}
					output := cloneContextRule(r, name+"-"+typ)
					output.SetKind(outputKind)
					output.SetPrivateAttr("cargo_template", name)
					output.SetPrivateAttr("cargo_input_target", name)
					rules = append(rules, output)
				}
			}
			if target.Test {
				rules = append(rules, unitTest(pkg, target, name, inputs(unitData)))
			}
			if target.Bench && kind == "bin" {
				rules = append(rules, benchmark(pkg, target, name, inputs(unitData)))
			}
		default:
			return nil, fmt.Errorf("Cargo target kind %s is not supported", kind)
		}
	}
	if pkg.Contexts == nil && len(binaries) != 0 {
		for _, r := range rules {
			if r.Kind() == "rust_test" && r.Attr("crate") == nil {
				env, data := runtimeInputs(pkg, binaries)
				setAttr(r, "rustc_env", env)
				addData(r, data)
			}
		}
	}
	if len(tests) > 0 {
		suite := newRule("test_suite", base+"-integration-tests")
		sort.Strings(tests)
		setAttr(suite, "tests", tests)
		setAttr(suite, "tags", []string{"restrict_" + base + "-integration-tests"})
		rules = append(rules, suite)
	}
	applySettings(rules, opts)
	return rules, nil
}

func applySettings(rules []*rule.Rule, opts settings) {
	for _, r := range rules {
		if timeout := opts.TestTimeout; timeout != "" && r.Kind() == "rust_test" && strings.Contains(r.Name(), "-integration-tests/") {
			setAttr(r, "timeout", timeout)
		}
		if opts.TestOnly != nil {
			setAttr(r, "testonly", *opts.TestOnly)
		}
		if opts.DepOverride != nil {
			override := []string{opts.DepOverride.Prefix, opts.DepOverride.Target}
			if value := r.Attr("deps"); value != nil {
				setAttr(r, "deps", overrideDependencies(value, override))
			}
			if value := r.Attr("aliases"); value != nil {
				setAttr(r, "aliases", overrideAliases(value, override))
			}
		}
	}
}

// Replace an external fixture label while retaining upstream dependency selection.
func overrideLabel(name string, override []string) bzl.Expr {
	return &bzl.ConditionalExpr{
		Then: rule.ExprFromValue(override[1]),
		Test: &bzl.CallExpr{X: &bzl.DotExpr{X: &bzl.Ident{Name: name}, Name: "startswith"}, List: []bzl.Expr{rule.ExprFromValue(override[0])}},
		Else: &bzl.Ident{Name: name},
	}
}

func overrideDependencies(value bzl.Expr, override []string) bzl.Expr {
	if sum, ok := value.(*bzl.BinaryExpr); ok && sum.Op == "+" {
		return &bzl.BinaryExpr{X: overrideDependencies(sum.X, override), Op: "+", Y: overrideDependencies(sum.Y, override)}
	}
	if selected, ok := overrideSelect(value, override, overrideDependencies); ok {
		return selected
	}
	return &bzl.Comprehension{Body: overrideLabel("dep", override), Clauses: []bzl.Expr{&bzl.ForClause{Vars: &bzl.Ident{Name: "dep"}, X: value}}}
}

func overrideAliases(value bzl.Expr, override []string) bzl.Expr {
	if selected, ok := overrideSelect(value, override, overrideAliases); ok {
		return selected
	}
	return &bzl.Comprehension{
		Curly: true,
		Body:  &bzl.KeyValueExpr{Key: overrideLabel("label", override), Value: &bzl.Ident{Name: "name"}},
		Clauses: []bzl.Expr{&bzl.ForClause{
			Vars: &bzl.TupleExpr{NoBrackets: true, List: []bzl.Expr{&bzl.Ident{Name: "label"}, &bzl.Ident{Name: "name"}}},
			X:    &bzl.CallExpr{X: &bzl.DotExpr{X: value, Name: "items"}},
		}},
	}
}

// Transform each explicit select branch before Bazel evaluates the expression.
func overrideSelect(value bzl.Expr, override []string, transform func(bzl.Expr, []string) bzl.Expr) (bzl.Expr, bool) {
	call, ok := value.(*bzl.CallExpr)
	if !ok || len(call.List) != 1 {
		return nil, false
	}
	name, ok := call.X.(*bzl.Ident)
	if !ok || name.Name != "select" {
		return nil, false
	}
	branches, ok := call.List[0].(*bzl.DictExpr)
	if !ok {
		return nil, false
	}
	entries := make([]*bzl.KeyValueExpr, 0, len(branches.List))
	for _, branch := range branches.List {
		entries = append(entries, &bzl.KeyValueExpr{Key: branch.Key, Value: transform(branch.Value, override)})
	}
	return &bzl.CallExpr{X: call.X, List: []bzl.Expr{&bzl.DictExpr{List: entries, ForceMultiLine: true}}}, true
}

func packageRepoRoot(pkg cargoPackage) string {
	if pkg.RepoRoot != "" {
		return pkg.RepoRoot
	}
	return pkg.WorkspaceRoot
}

// Cargo reports library crate types in both kind and crate_types.
func targetKind(t cargoTarget) string {
	if len(t.Kind) == 0 {
		return ""
	}
	for _, k := range t.Kind {
		switch k {
		case "lib", "rlib", "dylib", "cdylib", "staticlib":
		default:
			if len(t.Kind) == 1 {
				return k
			}
			return ""
		}
	}
	return "lib"
}

func crateRuleKind(kind string) string {
	switch kind {
	case "lib", "rlib":
		return "rust_library"
	case "dylib":
		return "rust_dylib_library"
	case "cdylib":
		return "rust_shared_library"
	case "staticlib":
		return "rust_static_library"
	case "proc-macro":
		return "rust_proc_macro"
	}
	return ""
}

// Include shared modules without guessing Rust module declarations.
func targetSources(root string) bzl.Expr {
	dir := path.Dir(root)
	pattern := "**/*.rs"
	if dir != "." {
		pattern = dir + "/**/*.rs"
	}
	return rule.ExprFromValue(rule.GlobValue{Patterns: []string{pattern}})
}

// Cargo uses the test harness with --bench for implicit benchmark targets.
func benchmark(pkg cargoPackage, target cargoTarget, crate string, data bzl.Expr) *rule.Rule {
	r := unitTest(pkg, target, crate, data)
	r.SetName(crate + "-bench-test")
	setAttr(r, "args", []string{"--bench"})
	setAttr(r, "tags", []string{"manual"})
	return r
}
