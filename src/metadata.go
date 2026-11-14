package rulesrs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	"github.com/pelletier/go-toml/v2"
	"github.com/scalar0/gazelle_rules_rs/src/internal/cargo"
)

// Cargo owns the metadata contract and feature resolution.
type metadata struct {
	Root     string          `json:"workspace_root"`
	Packages []cargoPackage  `json:"packages"`
	Members  []string        `json:"workspace_members"`
	Metadata json.RawMessage `json:"metadata"`
}

type cargoPackage struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	Version           string            `json:"version"`
	Edition           string            `json:"edition"`
	Manifest          string            `json:"manifest_path"`
	Readme            string            `json:"readme"`
	Metadata          json.RawMessage   `json:"metadata"`
	Settings          settings          `json:"-"`
	Targets           []cargoTarget     `json:"targets"`
	Features          []string          `json:"-"`
	Contexts          *packageContexts  `json:"-"`
	WorkspaceMetadata json.RawMessage   `json:"-"`
	RepoRoot          string            `json:"-"`
	WorkspaceRoot     string            `json:"-"`
	Dependencies      []cargoDependency `json:"dependencies"`
	Standalone        bool              `json:"-"`
	StandaloneLints   []string          `json:"-"`
	LocalContexts     map[string]string `json:"-"`
}

type cargoDependency struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Rename string `json:"rename"`
}

type cargoTarget struct {
	CrateTypes       []string `json:"crate_types"`
	Doctest          bool     `json:"doctest"`
	Bench            bool     `json:"-"`
	Doc              bool     `json:"doc"`
	Name             string   `json:"name"`
	Kind             []string `json:"kind"`
	Source           string   `json:"src_path"`
	Edition          string   `json:"edition"`
	Test             bool     `json:"test"`
	RequiredFeatures []string `json:"required-features"`
	Harness          *bool    `json:"-"`
}

// Cargo metadata omits the test harness setting.
func readHarnesses(pkg *cargoPackage) error {
	type targetOptions struct {
		Name    string
		Path    string
		Harness *bool
		Bench   *bool
	}
	var manifest struct {
		Lib     targetOptions
		Bin     []targetOptions
		Test    []targetOptions
		Example []targetOptions
		Bench   []targetOptions
	}
	data, err := os.ReadFile(pkg.Manifest)
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("read target options from %s: %w", pkg.Manifest, err)
	}
	for i := range pkg.Targets {
		target := &pkg.Targets[i]
		target.Bench = true
		var options []targetOptions
		switch targetKind(*target) {
		case "lib", "proc-macro":
			target.Harness = manifest.Lib.Harness
			if manifest.Lib.Bench != nil {
				target.Bench = *manifest.Lib.Bench
			}
		case "bin":
			options = manifest.Bin
		case "test":
			options = manifest.Test
		case "example":
			options = manifest.Example
		case "bench":
			options = manifest.Bench
		}
		for _, option := range options {
			if option.Name == target.Name || (option.Path != "" && filepath.Join(filepath.Dir(pkg.Manifest), option.Path) == target.Source) {
				target.Harness = option.Harness
				if option.Bench != nil {
					target.Bench = *option.Bench
				}
			}
		}
	}
	return nil
}

func inScope(scope, rel string) bool {
	for _, directory := range strings.Split(scope, ",") {
		if directory != "" && strings.HasPrefix(rel, directory+"/") && strings.Count(rel, "/") == strings.Count(directory, "/")+1 {
			return true
		}
	}
	return false
}

// Resolve resource labels from the Bazel repository, including standalone Cargo packages.
func loadCargoMetadata(ctx context.Context, workspace, root, tool string, noDeps bool) (metadata, error) {
	args := []string{"metadata", "--format-version=1"}
	if noDeps {
		args = append(args, "--no-deps")
	}
	output, err := cargo.Output(ctx, workspace, tool, args...)
	if err != nil {
		return metadata{}, err
	}
	var data metadata
	if err := json.Unmarshal(output, &data); err != nil {
		return metadata{}, fmt.Errorf("read Cargo metadata in %s: %w", workspace, err)
	}
	if data.Root == "" {
		data.Root = workspace
	}
	defaults, err := declaredSettings(data.Metadata, filepath.Join(data.Root, "Cargo.toml"), "workspace")
	if err != nil {
		return metadata{}, err
	}
	for i := range data.Packages {
		pkg := &data.Packages[i]
		if !slices.Contains(data.Members, pkg.ID) {
			continue
		}
		pkg.WorkspaceMetadata = data.Metadata
		pkg.WorkspaceRoot = data.Root
		pkg.RepoRoot = root
		pkg.Settings, err = resolveSettings(defaults, *pkg)
		if err != nil {
			return metadata{}, err
		}
		// Check all package directives before either generator writes output.
		for _, name := range []string{"BUILD.bazel", "BUILD"} {
			file := filepath.Join(filepath.Dir(pkg.Manifest), name)
			if _, err := os.Stat(file); os.IsNotExist(err) {
				continue
			}
			build, err := rule.LoadFile(file, "")
			if err != nil {
				return metadata{}, err
			}
			if err := rejectLegacyDirectives(build); err != nil {
				return metadata{}, fmt.Errorf("%s: %w", file, err)
			}
		}
	}
	if _, err := vendorInputs(data); err != nil {
		return metadata{}, err
	}
	return data, nil
}

func readConfiguredMetadata(ctx context.Context, root, scope string, cfg BackendConfig) (map[string]cargoPackage, error) {
	if len(cfg.PlatformTriples) == 0 {
		return nil, fmt.Errorf("configure platform_triples for Cargo contexts")
	}
	workspace := filepath.Dir(filepath.Join(root, cfg.Manifest))
	resolved, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return nil, fmt.Errorf("the Cargo workspace must be inside the repository")
	}
	tool, err := cargo.PinnedCargo()
	if err != nil {
		return nil, err
	}
	data, err := loadCargoMetadata(ctx, workspace, root, tool, false)
	if err != nil {
		return nil, err
	}
	result := map[string]cargoPackage{}
	for _, pkg := range data.Packages {
		if !slices.Contains(data.Members, pkg.ID) {
			continue
		}
		rel, err := filepath.Rel(root, filepath.Dir(pkg.Manifest))
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("workspace member %s is outside the repository", pkg.Name)
		}
		if scope == "" || inScope(scope, rel) {
			if err := readHarnesses(&pkg); err != nil {
				return nil, err
			}
			result[rel] = pkg
		}
	}
	workspace = data.Root
	if err := readConfiguredContexts(ctx, workspace, tool, result, data.Members, cfg.PlatformTriples); err != nil {
		return nil, err
	}
	if err := readStandalonePackages(ctx, root, workspace, scope, tool, cfg, result); err != nil {
		return nil, err
	}
	return result, nil
}
