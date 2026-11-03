package rulesrs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/pelletier/go-toml/v2"
)

// Cargo owns the metadata contract and feature resolution.
type metadata struct {
	Packages []cargoPackage  `json:"packages"`
	Members  []string        `json:"workspace_members"`
	Metadata json.RawMessage `json:"metadata"`
}

type cargoPackage struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Description      string              `json:"description"`
	Version          string              `json:"version"`
	Edition          string              `json:"edition"`
	Manifest         string              `json:"manifest_path"`
	Readme           string              `json:"readme"`
	Metadata         json.RawMessage     `json:"metadata"`
	Settings         settings            `json:"-"`
	Targets          []cargoTarget       `json:"targets"`
	Features         []string            `json:"-"`
	DeclaredFeatures map[string][]string `json:"features"`
	Contexts         *packageContexts    `json:"-"`
	WorkspaceRoot    string              `json:"-"`
	Dependencies     []cargoDependency   `json:"dependencies"`
}

type cargoDependency struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type cargoTarget struct {
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
	}
	var manifest struct {
		Lib  targetOptions
		Bin  []targetOptions
		Test []targetOptions
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
		if len(target.Kind) != 1 {
			continue
		}
		var options []targetOptions
		switch target.Kind[0] {
		case "lib", "proc-macro":
			target.Harness = manifest.Lib.Harness
		case "bin":
			options = manifest.Bin
		case "test":
			options = manifest.Test
		}
		for _, option := range options {
			if option.Name == target.Name || (option.Path != "" && filepath.Join(filepath.Dir(pkg.Manifest), option.Path) == target.Source) {
				target.Harness = option.Harness
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

func cargoToolOutput(ctx context.Context, root, tool string, args ...string) ([]byte, error) {
	args = append(args, "--locked", "--manifest-path", filepath.Join(root, "Cargo.toml"))
	manifest, err := runfiles.Rlocation(rustcRunfile)
	if err != nil {
		return nil, fmt.Errorf("find declared Rust compiler manifest: %w", err)
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return nil, err
	}
	rustc, err := runfiles.Rlocation(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("find declared Rust compiler: %w", err)
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = root
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "RUSTC" && key != "RUSTC_WRAPPER" && key != "RUSTC_WORKSPACE_WRAPPER" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "RUSTC="+rustc)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cargo %s: %w\n%s", args[0], err, stderr.String())
	}
	return output, nil
}

func pinnedCargo() (string, error) {
	tool, err := runfiles.Rlocation(cargoRunfile)
	if err != nil {
		return "", fmt.Errorf("Find pinned Cargo: %w", err)
	}
	return tool, nil
}

func loadCargoMetadata(ctx context.Context, root, tool string, noDeps bool) (metadata, error) {
	args := []string{"metadata", "--format-version=1"}
	if noDeps {
		args = append(args, "--no-deps")
	}
	output, err := cargoToolOutput(ctx, root, tool, args...)
	if err != nil {
		return metadata{}, err
	}
	var data metadata
	if err := json.Unmarshal(output, &data); err != nil {
		return metadata{}, err
	}
	defaults, err := declaredSettings(data.Metadata, filepath.Join(root, "Cargo.toml"), "workspace")
	if err != nil {
		return metadata{}, err
	}
	for i := range data.Packages {
		pkg := &data.Packages[i]
		if !slices.Contains(data.Members, pkg.ID) {
			continue
		}
		pkg.WorkspaceRoot = root
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

func readMetadata(ctx context.Context, root, scope string) (map[string]cargoPackage, error) {
	tool, err := pinnedCargo()
	if err != nil {
		return nil, err
	}
	data, err := loadCargoMetadata(ctx, root, tool, false)
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
		if inScope(scope, rel) {
			if err := readHarnesses(&pkg); err != nil {
				return nil, err
			}
			result[rel] = pkg
		}
	}
	if err := readContexts(ctx, root, tool, result, data.Members); err != nil {
		return nil, err
	}
	return result, nil
}
