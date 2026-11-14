package rulesrs

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	bzl "github.com/bazelbuild/buildtools/build"
)

// Generated expressions replace prior values but preserve explicit kept list items.
type generatedExpr struct {
	expr       bzl.Expr
	labelItems bool
}

func (value generatedExpr) BzlExpr() bzl.Expr { return value.expr }

func (value generatedExpr) Merge(previous bzl.Expr) bzl.Expr {
	kept, selections, err := keptMergeItems(previous)
	if err != nil {
		// The preflight check reports unsupported kept expressions before writes.
		return previous
	}
	generated := value.expr
	if kept != nil {
		if value.labelItems {
			kept = uniqueKeptLabels(kept)
			generated = excludeKeptGlobInputs(generated, kept)
		}
		if generated == nil {
			generated = kept
		} else if _, list := generated.(*bzl.ListExpr); list && !value.labelItems {
			generated = rule.MergeList(generated, kept)
		} else {
			var remaining *bzl.ListExpr
			generated, remaining = matchKeptListItems(generated, kept, value.labelItems)
			if len(remaining.List) != 0 {
				if _, list := generated.(*bzl.ListExpr); list {
					generated = rule.MergeList(generated, remaining)
				} else {
					generated = &bzl.BinaryExpr{X: generated, Op: "+", Y: rule.MergeList(nil, remaining)}
				}
			}
		}
	}
	for _, selection := range selections {
		if merged, ok := matchKeptSelection(generated, selection.(*bzl.CallExpr), value.labelItems); ok {
			generated = merged
		} else if generated == nil {
			generated = selection
		} else {
			generated = &bzl.BinaryExpr{X: generated, Op: "+", Y: selection}
		}
	}
	return generated
}

func localInputPath(value string) (string, bool) {
	value = strings.TrimPrefix(value, ":")
	return value, value != "" && !strings.Contains(value, ":") && !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "@")
}

func inputLabelKey(value string) string {
	if local, ok := localInputPath(value); ok {
		return ":" + local
	}
	return value
}

func uniqueKeptLabels(kept *bzl.ListExpr) *bzl.ListExpr {
	copy := *kept
	copy.List = nil
	seen := map[string]bool{}
	for _, item := range kept.List {
		if text, ok := item.(*bzl.StringExpr); ok {
			key := inputLabelKey(text.Value)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		copy.List = append(copy.List, item)
	}
	return &copy
}

// Exclude unconditional kept files before any literal operand consumes their comments.
func excludeKeptGlobInputs(expr bzl.Expr, kept *bzl.ListExpr) bzl.Expr {
	var files []string
	for _, item := range kept.List {
		if text, ok := item.(*bzl.StringExpr); ok {
			if local, ok := localInputPath(text.Value); ok {
				files = append(files, local)
			}
		}
	}
	if expr == nil || len(files) == 0 {
		return expr
	}
	slices.Sort(files)
	var exclude func(bzl.Expr) bzl.Expr
	exclude = func(expr bzl.Expr) bzl.Expr {
		if glob, ok := rule.ParseGlobExpr(expr); ok {
			glob.Excludes = uniqueInputs(glob.Excludes, files)
			glob.AllowEmpty = true
			return glob.BzlExpr()
		}
		switch value := expr.(type) {
		case *bzl.BinaryExpr:
			copy := *value
			copy.X, copy.Y = exclude(value.X), exclude(value.Y)
			return &copy
		case *bzl.CallExpr:
			copy := *value
			copy.List = make([]bzl.Expr, len(value.List))
			for i, item := range value.List {
				copy.List[i] = exclude(item)
			}
			return &copy
		case *bzl.DictExpr:
			copy := *value
			copy.List = make([]*bzl.KeyValueExpr, len(value.List))
			for i, item := range value.List {
				entry := *item
				entry.Value = exclude(item.Value)
				copy.List[i] = &entry
			}
			return &copy
		}
		return expr
	}
	return exclude(expr)
}

// Merge overlapping values within their original conditions when generated branches change.
func matchKeptSelection(expr bzl.Expr, selection *bzl.CallExpr, labels bool) (bzl.Expr, bool) {
	kept := selection.List[0].(*bzl.DictExpr)
	switch value := expr.(type) {
	case *bzl.ListExpr:
		if !labels {
			break
		}
		conditional := map[string]bool{}
		for _, branch := range kept.List {
			for _, item := range branch.Value.(*bzl.ListExpr).List {
				if text, ok := item.(*bzl.StringExpr); ok {
					conditional[inputLabelKey(text.Value)] = true
				}
			}
		}
		moved, remaining := &bzl.ListExpr{}, *value
		remaining.List = nil
		for _, item := range value.List {
			if text, ok := item.(*bzl.StringExpr); ok && conditional[inputLabelKey(text.Value)] {
				moved.List = append(moved.List, item)
			} else {
				remaining.List = append(remaining.List, item)
			}
		}
		if len(moved.List) == 0 {
			break
		}
		branches := *kept
		branches.List = nil
		hasDefault := false
		for _, branch := range kept.List {
			entry := *branch
			entry.Value = (generatedExpr{expr: moved, labelItems: true}).Merge(branch.Value)
			branches.List = append(branches.List, &entry)
			hasDefault = hasDefault || branch.Key.(*bzl.StringExpr).Value == "//conditions:default"
		}
		if !hasDefault {
			branches.List = append(branches.List, &bzl.KeyValueExpr{Key: rule.ExprFromValue("//conditions:default"), Value: moved})
		}
		merged := *selection
		merged.List = []bzl.Expr{&branches}
		if len(remaining.List) == 0 {
			return &merged, true
		}
		return &bzl.BinaryExpr{X: &remaining, Op: "+", Y: &merged}, true
	case *bzl.CallExpr:
		name, ok := value.X.(*bzl.Ident)
		if !ok || name.Name != "select" || len(value.List) != 1 {
			break
		}
		branches, ok := value.List[0].(*bzl.DictExpr)
		if !ok {
			break
		}
		keys := map[string]bool{}
		for _, branch := range branches.List {
			key, ok := branch.Key.(*bzl.StringExpr)
			if !ok {
				return expr, false
			}
			keys[key.Value] = true
		}
		values := map[string]bzl.Expr{}
		for _, branch := range kept.List {
			key := branch.Key.(*bzl.StringExpr).Value
			if key != "//conditions:default" && !keys[key] && len(branch.Value.(*bzl.ListExpr).List) != 0 {
				return expr, false
			}
			values[key] = branch.Value
		}
		merged := *branches
		merged.List = nil
		for _, branch := range branches.List {
			key := branch.Key.(*bzl.StringExpr).Value
			previous := values[key]
			if previous == nil {
				previous = values["//conditions:default"]
			}
			entry := *branch
			entry.Value = (generatedExpr{expr: branch.Value, labelItems: labels}).Merge(previous)
			merged.List = append(merged.List, &entry)
		}
		if fallback := values["//conditions:default"]; fallback != nil && !keys["//conditions:default"] {
			merged.List = append(merged.List, &bzl.KeyValueExpr{Key: rule.ExprFromValue("//conditions:default"), Value: fallback})
		}
		copy := *value
		copy.List = []bzl.Expr{&merged}
		return &copy, true
	case *bzl.BinaryExpr:
		if value.Op == "+" {
			copy := *value
			if merged, ok := matchKeptSelection(value.X, selection, labels); ok {
				copy.X = merged
				return &copy, true
			}
			if merged, ok := matchKeptSelection(value.Y, selection, labels); ok {
				copy.Y = merged
				return &copy, true
			}
		}
	}
	return expr, false
}

// The preflight check rejects keep comments that the merge cannot preserve.
func validateGeneratedMerge(previous, generated bzl.Expr) error {
	kept, selections, err := keptMergeItems(previous)
	if err != nil {
		return err
	}
	hasGlob := false
	if generated != nil {
		bzl.Walk(generated, func(node bzl.Expr, _ []bzl.Expr) {
			_, glob := rule.ParseGlobExpr(node)
			hasGlob = hasGlob || glob
		})
	}
	if hasGlob {
		for _, selection := range selections {
			bzl.Walk(selection, func(node bzl.Expr, _ []bzl.Expr) {
				if text, ok := node.(*bzl.StringExpr); ok && rule.ShouldKeep(text) {
					if _, local := localInputPath(text.Value); local {
						err = fmt.Errorf("cannot combine conditional item keeps with a generated glob. Use an attribute keep comment")
					}
				}
			})
		}
		if err != nil {
			return err
		}
	}
	preserved := map[bzl.Expr]bool{}
	mark := func(expr bzl.Expr) {
		if expr != nil {
			bzl.Walk(expr, func(node bzl.Expr, _ []bzl.Expr) { preserved[node] = true })
		}
	}
	if kept != nil {
		mark(kept)
	}
	for _, selection := range selections {
		mark(selection)
	}
	if previous != nil {
		bzl.Walk(previous, func(node bzl.Expr, _ []bzl.Expr) {
			if rule.ShouldKeep(node) && !preserved[node] {
				err = fmt.Errorf("cannot preserve an item keep comment in this expression. Use an attribute keep comment")
			}
		})
	}
	return err
}

// Gazelle retains kept items within lists and conditional list branches.
func keptMergeItems(expr bzl.Expr) (*bzl.ListExpr, []bzl.Expr, error) {
	hasKeep := false
	if expr != nil {
		bzl.Walk(expr, func(node bzl.Expr, _ []bzl.Expr) { hasKeep = hasKeep || rule.ShouldKeep(node) })
	}
	if !hasKeep {
		return nil, nil, nil
	}
	switch expr := expr.(type) {
	case *bzl.ListExpr:
		return rule.MergeList(nil, expr), nil, nil
	case *bzl.BinaryExpr:
		if expr.Op == "+" {
			x, xSelections, err := keptMergeItems(expr.X)
			if err != nil {
				return nil, nil, err
			}
			y, ySelections, err := keptMergeItems(expr.Y)
			return rule.MergeList(x, y), append(xSelections, ySelections...), err
		}
	case *bzl.CallExpr:
		if name, ok := expr.X.(*bzl.Ident); ok && name.Name == "select" && len(expr.List) == 1 {
			if branches, ok := expr.List[0].(*bzl.DictExpr); ok {
				// Empty branches retain the original selection coverage.
				empty := &bzl.DictExpr{}
				for _, branch := range branches.List {
					empty.List = append(empty.List, &bzl.KeyValueExpr{Key: branch.Key, Value: &bzl.ListExpr{}})
				}
				kept, err := rule.MergeDict(empty, branches)
				if err != nil {
					return nil, nil, err
				}
				if kept != nil {
					return nil, []bzl.Expr{&bzl.CallExpr{X: expr.X, List: []bzl.Expr{kept}}}, nil
				}
			}
		}
	}
	return nil, nil, fmt.Errorf("cannot preserve an item keep comment in this expression. Use an attribute keep comment")
}

// Keep comments stay on generated items when their values still occur.
func matchKeptListItems(expr bzl.Expr, kept *bzl.ListExpr, labels bool) (bzl.Expr, *bzl.ListExpr) {
	switch value := expr.(type) {
	case *bzl.ListExpr:
		values := map[string]bool{}
		key := func(value string) string {
			if labels {
				return inputLabelKey(value)
			}
			return value
		}
		for _, item := range value.List {
			if item, ok := item.(*bzl.StringExpr); ok {
				values[key(item.Value)] = true
			}
		}
		matching, remaining := &bzl.ListExpr{}, &bzl.ListExpr{}
		spellings := map[string]string{}
		for _, item := range kept.List {
			if text, ok := item.(*bzl.StringExpr); ok {
				if values[key(text.Value)] {
					spellings[key(text.Value)] = text.Value
					matching.List = append(matching.List, item)
					continue
				}
			}
			remaining.List = append(remaining.List, item)
		}
		copy := *value
		copy.List = slices.Clone(value.List)
		for i, item := range copy.List {
			if text, ok := item.(*bzl.StringExpr); ok {
				if spelling, found := spellings[key(text.Value)]; found && text.Value != spelling {
					copyText := *text
					copyText.Value = spelling
					copy.List[i] = &copyText
				}
			}
		}
		merged := rule.MergeList(&copy, matching)
		if merged == nil {
			return value, remaining
		}
		return merged, remaining
	case *bzl.BinaryExpr:
		if value.Op == "+" {
			copy := *value
			copy.X, kept = matchKeptListItems(value.X, kept, labels)
			copy.Y, kept = matchKeptListItems(value.Y, kept, labels)
			return &copy, kept
		}
	}
	return expr, kept
}

func setAttr(r *rule.Rule, name string, value interface{}) {
	if expr, ok := value.(bzl.Expr); ok {
		labels := name == "srcs" || name == "deps" || name == "data" || name == "compile_data" || name == "tests"
		value = generatedExpr{expr: expr, labelItems: labels}
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
		return rule.ExprFromValue([]string{root})
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

func unitTest(pkg cargoPackage, target cargoTarget, crate string, compileData bzl.Expr) *rule.Rule {
	info, _ := targetDescriptor(pkg, target, "test")
	unit := newRule("rust_test", info.BazelTarget)
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
			// Bazel treats these manifest labels as the same input.
			if pattern == "Cargo.toml" {
				pattern = ":Cargo.toml"
			}
			if !slices.Contains(labels, pattern) {
				labels = append(labels, pattern)
			}
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

// A direct input and an inherited input must declare each label once.
func appendInputs(existing bzl.Expr, patterns []string) bzl.Expr {
	labels := map[string]bool{}
	var collect func(bzl.Expr)
	collect = func(expr bzl.Expr) {
		switch expr := expr.(type) {
		case *bzl.ListExpr:
			for _, item := range expr.List {
				if text, ok := item.(*bzl.StringExpr); ok {
					labels[inputLabelKey(text.Value)] = true
				}
			}
		case *bzl.BinaryExpr:
			if expr.Op == "+" {
				collect(expr.X)
				collect(expr.Y)
			}
		}
	}
	collect(existing)
	var added []string
	for _, pattern := range patterns {
		if !strings.ContainsAny(pattern, "*?[") {
			key := inputLabelKey(pattern)
			if labels[key] {
				continue
			}
			labels[key] = true
		}
		added = append(added, pattern)
	}
	if len(added) == 0 {
		return existing
	}
	addition := inputs(added)
	if existing != nil {
		addition = &bzl.BinaryExpr{X: existing, Op: "+", Y: addition}
	}
	return addition
}

func generate(root, rel string, pkg cargoPackage, opts settings) ([]*rule.Rule, error) {
	if pkg.WorkspaceRoot == "" {
		pkg.WorkspaceRoot = root
	}
	pkg.RepoRoot = root
	dir := filepath.Join(root, rel)
	if pkg.Manifest == "" {
		pkg.Manifest = filepath.Join(dir, "Cargo.toml")
	}
	base := packageBase(pkg)
	descriptors, err := packageTargetInfos(pkg)
	if err != nil {
		return nil, err
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
			return nil, fmt.Errorf("the Cargo target %q has unsupported kinds %v", target.Name, target.Kind)
		}
		switch targetKind(target) {
		case "lib", "proc-macro":
			hasLib = true
		case "custom-build":
			hasBuild = true
		}
	}
	var tests []string
	binaries := map[string]string{}
	for _, target := range pkg.Targets {
		if !targetEnabled(pkg, target) {
			continue
		}
		info, err := targetDescriptor(pkg, target, "build")
		if err != nil {
			return nil, err
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
			r := newRule(ruleKind, info.BazelTarget)
			rustAttrs(r, pkg, target)
			setAttr(r, "crate_name", call("crate_name"))
			setAttr(r, "crate_root", file)
			setAttr(r, "srcs", librarySources(file))
			setAttr(r, "compile_data", inputs(libraryData))
			var local []string
			if hasBuild {
				local = append(local, ":build-script")
			}
			setAttr(r, "deps", deps([]string{"normal"}, local...))
			rules = append(rules, r)
			if target.Test {
				rules = append(rules, unitTest(pkg, target, "lib", inputs(unitData)))
			}
			if target.Bench {
				rules = append(rules, benchmark(pkg, target, "lib", inputs(unitData)))
			}
			for _, typ := range target.CrateTypes {
				if typ == "lib" || typ == "rlib" || typ == "proc-macro" {
					continue
				}
				kind := crateRuleKind(typ)
				if kind == "" {
					return nil, fmt.Errorf("unsupported library crate type %s", typ)
				}
				output := cloneContextRule(r, outputDescriptor(info, typ).BazelTarget)
				output.SetKind(kind)
				output.SetPrivateAttr("cargo_template", "lib")
				output.SetPrivateAttr("cargo_input_target", "lib")
				rules = append(rules, output)
			}
			if target.Doctest {
				docInfo, _ := targetDescriptor(pkg, target, "doctest")
				doc := newRule("rust_doc_test", docInfo.BazelTarget)
				doc.SetPrivateAttr("cargo_input_target", "lib")
				setAttr(doc, "crate", ":lib")
				setAttr(doc, "deps", deps([]string{"normal", "normal_dev"}))
				rules = append(rules, doc)
			}

		case "custom-build":
			r := newRule("cargo_build_script", info.BazelTarget)
			setAttr(r, "srcs", rule.ExprFromValue([]string{file}))
			setAttr(r, "crate_root", file)
			setEdition(r, pkg, target)
			setAttr(r, "aliases", targetAliases(pkg, "build"))
			setAttr(r, "crate_features", rule.ExprFromValue(pkg.Features))
			setAttr(r, "version", pkg.Version)
			setAttr(r, "pkg_name", pkg.Name)
			setAttr(r, "deps", deps([]string{"build"}))
			setAttr(r, "rustc_env", cargoEnvironment(pkg))
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
			name := info.BazelTarget
			r := newRule("rust_test", name)
			rustAttrs(r, pkg, target)
			setHarness(r, target)
			setAttr(r, "crate_root", file)
			setAttr(r, "crate_name", strings.ReplaceAll(target.Name, "-", "_"))
			setAttr(r, "srcs", rule.ExprFromValue([]string{file}))
			setAttr(r, "compile_data", inputs(testData))
			setAttr(r, "data", inputs(opts.TestData))
			var local []string
			if hasLib {
				local = append(local, ":lib")
			}
			if hasBuild {
				local = append(local, ":build-script")
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
			name := info.BazelTarget
			if kind == "bin" {
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
				local = append(local, ":lib")
			}
			if hasBuild {
				local = append(local, ":build-script")
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
					output := cloneContextRule(r, outputDescriptor(info, typ).BazelTarget)
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
			return nil, fmt.Errorf("the Cargo target has unsupported kind %s", kind)
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
		suite := newRule("test_suite", integrationSuiteDescriptor().BazelTarget)
		sort.Strings(tests)
		setAttr(suite, "tests", tests)
		setAttr(suite, "tags", []string{"restrict_" + base + "-integration-tests"})
		rules = append(rules, suite)
	}
	if hasLib && base != "lib" {
		alias := newRule("alias", packageAliasDescriptor(pkg).BazelTarget)
		setAttr(alias, "actual", ":lib")
		rules = append(rules, alias)
	}
	for _, info := range descriptors {
		for _, r := range rules {
			if r.Name() == info.BazelTarget {
				r.SetPrivateAttr("cargo_target_info", info)
			}
		}
	}
	applySettings(rules, opts)
	return rules, nil
}

func applySettings(rules []*rule.Rule, opts settings) {
	for _, r := range rules {
		if timeout := opts.TestTimeout; timeout != "" && r.Kind() == "rust_test" && targetRole(r) == "integration-test" {
			setAttr(r, "timeout", timeout)
		}
		if opts.TestOnly != nil {
			setAttr(r, "testonly", *opts.TestOnly)
		}
		overrides := slices.Clone(opts.DepOverrides)
		if opts.DepOverride != nil {
			overrides = append([]dependencyOverride{*opts.DepOverride}, overrides...)
		}
		for _, replacement := range overrides {
			override := []string{replacement.Prefix, replacement.Target}
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

// Cargo uses the test harness with --bench for implicit benchmark targets.
func benchmark(pkg cargoPackage, target cargoTarget, crate string, data bzl.Expr) *rule.Rule {
	r := unitTest(pkg, target, crate, data)
	info, _ := targetDescriptor(pkg, target, "bench")
	r.SetName(info.BazelTarget)
	setAttr(r, "args", []string{"--bench"})
	setAttr(r, "tags", []string{"manual"})
	return r
}
