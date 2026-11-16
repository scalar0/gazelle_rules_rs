package rulesrs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	bzl "github.com/bazelbuild/buildtools/build"
	"github.com/pelletier/go-toml/v2"
)

// Read excluded packages from the declared workspace paths.
func readStandalonePackages(ctx context.Context, root, workspace, scope, tool string, cfg BackendConfig, packages map[string]cargoPackage) error {
	var manifest struct {
		Workspace struct{ Members, Exclude []string }
	}
	data, err := os.ReadFile(filepath.Join(workspace, "Cargo.toml"))
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return err
	}
	patterns := append(slices.Clone(manifest.Workspace.Members), manifest.Workspace.Exclude...)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(workspace, pattern, "Cargo.toml"))
		if err != nil {
			return err
		}
		for _, path := range matches {
			directory := filepath.Dir(path)
			rel, err := filepath.Rel(root, directory)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("standalone Cargo package must be inside the repository: %s", path)
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				rel = ""
			}
			if _, exists := packages[rel]; exists || (scope != "" && !inScope(scope, rel)) {
				continue
			}
			var candidate map[string]any
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := toml.Unmarshal(data, &candidate); err != nil {
				return fmt.Errorf("read excluded manifest %s: %w", path, err)
			}
			_, ownsWorkspace := candidate["workspace"]
			if candidate["package"] == nil || ownsWorkspace {
				continue
			}
			metadata, err := loadCargoMetadata(ctx, directory, root, tool, false)
			if err != nil {
				return fmt.Errorf("read standalone package %s: %w", rel, err)
			}
			selected := map[string]cargoPackage{}
			localContexts := map[string]string{}
			for _, dependency := range metadata.Packages {
				path, err := filepath.Rel(root, filepath.Dir(dependency.Manifest))
				if err != nil || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
					continue
				}
				if path == "." {
					localContexts[dependency.ID] = "//:lib"
				} else {
					localContexts[dependency.ID] = "//" + filepath.ToSlash(path) + ":lib"
				}
			}
			for _, pkg := range metadata.Packages {
				if !slices.Contains(metadata.Members, pkg.ID) || filepath.Dir(pkg.Manifest) != directory {
					continue
				}
				// Model packages depend on local runtime and model packages.
				// A separate crate repository must own standalone registry dependencies.
				for _, dependency := range pkg.Dependencies {
					if dependency.Path == "" {
						return fmt.Errorf("standalone package %s has registry dependencies. Configure its own crate repository before BUILD generation", rel)
					}
					if standaloneDependencyLabel(cargoPackage{RepoRoot: root}, dependency) == "" {
						return fmt.Errorf("the standalone dependency must be inside the repository: %s", dependency.Path)
					}
				}
				pkg.Standalone = true
				pkg.LocalContexts = localContexts
				pkg.StandaloneLints, err = buildLints(filepath.Join(directory, "Cargo.toml"), pkg.Manifest)
				if err != nil {
					return err
				}
				if err := readHarnesses(&pkg); err != nil {
					return err
				}
				selected[rel] = pkg
			}
			if len(selected) != 1 {
				return fmt.Errorf("the standalone manifest must identify one package: %s", path)
			}
			if err := readConfiguredContexts(ctx, directory, tool, selected, metadata.Members, cfg.PlatformTriples); err != nil {
				return err
			}
			for rel, pkg := range selected {
				packages[rel] = pkg
			}
		}
	}
	return nil
}

// Cargo contexts retain direct local edges without workspace helper lookups.
func materializeStandalone(rules []*rule.Rule, pkg cargoPackage) {
	crateName := strings.ReplaceAll(pkg.Name, "-", "_")
	for _, target := range pkg.Targets {
		if targetKind(target) == "lib" || targetKind(target) == "proc-macro" {
			crateName = strings.ReplaceAll(target.Name, "-", "_")
			break
		}
	}
	for _, r := range rules {
		r.DelAttr("lint_config")
		for _, key := range r.AttrKeys() {
			setAttr(r, key, bzl.Edit(r.Attr(key), func(expression bzl.Expr, _ []bzl.Expr) bzl.Expr {
				call, ok := expression.(*bzl.CallExpr)
				if !ok {
					return expression
				}
				name, ok := call.X.(*bzl.Ident)
				if !ok {
					return expression
				}
				switch name.Name {
				case "crate_name":
					return rule.ExprFromValue(crateName)
				case "edition":
					return rule.ExprFromValue(pkg.Edition)
				case "all_crate_deps":
					return standaloneDependencies(pkg, call)
				case "aliases":
					return standaloneAliases(pkg)
				default:
					return expression
				}
			}))
		}
		if len(pkg.StandaloneLints) != 0 && strings.HasPrefix(r.Kind(), "rust_") && r.Kind() != "rust_doc_test" {
			attribute := "rustc_flags"
			if r.Kind() == "rust_doc" {
				attribute = "rustdoc_flags"
			}
			flags := rule.ExprFromValue(pkg.StandaloneLints)
			if old := r.Attr(attribute); old != nil {
				flags = &bzl.BinaryExpr{X: old, Op: "+", Y: flags}
			}
			setAttr(r, attribute, flags)
		}
	}
}

func standaloneDependencyLabel(pkg cargoPackage, dependency cargoDependency) string {
	rel, err := filepath.Rel(packageRepoRoot(pkg), dependency.Path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if rel == "." {
		return "//:lib"
	}
	return "//" + filepath.ToSlash(rel)
}

func standaloneDependencies(pkg cargoPackage, call *bzl.CallExpr) bzl.Expr {
	if pkg.Contexts != nil {
		return rule.ExprFromValue([]string{})
	}
	kinds := map[string]bool{}
	for _, argument := range call.List {
		if value, ok := argument.(*bzl.AssignExpr); ok {
			if name, ok := value.LHS.(*bzl.Ident); ok {
				switch name.Name {
				case "normal":
					kinds[""] = true
				case "normal_dev":
					kinds["dev"] = true
				case "build":
					kinds["build"] = true
				}
			}
		}
	}
	var labels []string
	for _, dependency := range pkg.Dependencies {
		if kinds[dependency.Kind] {
			labels = append(labels, standaloneDependencyLabel(pkg, dependency))
		}
	}
	slices.Sort(labels)
	return rule.ExprFromValue(slices.Compact(labels))
}

func standaloneAliases(pkg cargoPackage) bzl.Expr {
	aliases := map[string]string{}
	if pkg.Contexts == nil {
		for _, dependency := range pkg.Dependencies {
			name := dependency.Name
			if dependency.Rename != "" {
				name = dependency.Rename
			}
			aliases[standaloneDependencyLabel(pkg, dependency)] = strings.ReplaceAll(name, "-", "_")
		}
	}
	return rule.ExprFromValue(aliases)
}
