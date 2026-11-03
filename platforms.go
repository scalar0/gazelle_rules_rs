package rulesrs

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	bzl "github.com/bazelbuild/buildtools/build"
)

// Read the same platform list that configures the public dependency helpers.
func readPlatforms(root string) ([]string, error) {
	file := filepath.Join(root, "MODULE.bazel")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	module, err := bzl.ParseModule(file, data)
	if err != nil {
		return nil, err
	}
	extensions := map[string]bool{}
	for _, statement := range module.Stmt {
		assignment, ok := statement.(*bzl.AssignExpr)
		if !ok {
			continue
		}
		name, named := assignment.LHS.(*bzl.Ident)
		call, called := assignment.RHS.(*bzl.CallExpr)
		if !named || !called || len(call.List) < 2 {
			continue
		}
		function, direct := call.X.(*bzl.Ident)
		label, literalLabel := call.List[0].(*bzl.StringExpr)
		kind, literalKind := call.List[1].(*bzl.StringExpr)
		if direct && function.Name == "use_extension" && literalLabel && literalKind &&
			label.Value == "@rules_rs//rs:extensions.bzl" && kind.Value == "crate" {
			extensions[name.Name] = true
		}
	}
	for _, statement := range module.Stmt {
		call, ok := statement.(*bzl.CallExpr)
		if !ok {
			continue
		}
		method, ok := call.X.(*bzl.DotExpr)
		if !ok || method.Name != "from_cargo" {
			continue
		}
		receiver, ok := method.X.(*bzl.Ident)
		if !ok || !extensions[receiver.Name] {
			continue
		}
		attrs := map[string]bzl.Expr{}
		for _, arg := range call.List {
			if assignment, ok := arg.(*bzl.AssignExpr); ok {
				if name, ok := assignment.LHS.(*bzl.Ident); ok {
					attrs[name.Name] = assignment.RHS
				}
			}
		}
		name, ok := attrs["name"].(*bzl.StringExpr)
		if !ok || name.Value != "crates" {
			continue
		}
		list, ok := attrs["platform_triples"].(*bzl.ListExpr)
		if !ok || len(list.List) == 0 {
			return nil, fmt.Errorf("MODULE.bazel: crates.platform_triples must be a nonempty literal list")
		}
		var platforms []string
		for _, item := range list.List {
			value, ok := item.(*bzl.StringExpr)
			if !ok || value.Value == "" {
				return nil, fmt.Errorf("MODULE.bazel: crates.platform_triples must contain literal triples")
			}
			platforms = append(platforms, value.Value)
		}
		slices.Sort(platforms)
		return slices.Compact(platforms), nil
	}
	return nil, fmt.Errorf("MODULE.bazel: cannot find the rules_rs from_cargo call named crates")
}
