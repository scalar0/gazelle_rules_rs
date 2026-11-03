// Aspect sets `-r=true` because Gazelle version 2 does not recurse by default when directory arguments are present.
// Gazelle excludes nested directories from its BUILD-file walk.
// This also prevents load and format changes in nested fixture BUILD files.
package rulesrs

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/rule"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const metadataNamespace = "rules_rs"

//go:embed metadata.schema.json
var optionsSchema []byte

type settings struct {
	LibraryCompileData []string              `json:"library_compile_data"`
	UnitCompileData    []string              `json:"unit_compile_data"`
	TestCompileData    []string              `json:"test_compile_data"`
	TestData           []string              `json:"test_data"`
	BuildCompileData   []string              `json:"build_compile_data"`
	BuildData          []string              `json:"build_data"`
	TestTimeout        string                `json:"test_timeout"`
	TestOnly           *bool                 `json:"testonly"`
	DepOverride        *dependencyOverride   `json:"dep_override"`
	BuildInputs        map[string]buildInput `json:"build_inputs"`
}

type dependencyOverride struct {
	Prefix string `json:"prefix"`
	Target string `json:"target"`
}

type buildInput struct {
	CargoPath  string `json:"cargo_path"`
	BazelLabel string `json:"bazel_label"`
}

var compiledOptionsSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	var document any
	if err := json.Unmarshal(optionsSchema, &document); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	const identifier = "urn:kernel-toolchain:rules_rs"
	if err := compiler.AddResource(identifier, document); err != nil {
		return nil, err
	}
	return compiler.Compile(identifier)
})

// Read and validate only this tool's namespace from Cargo metadata.
func declaredSettings(raw json.RawMessage, manifest, owner string) (map[string]json.RawMessage, error) {
	var metadata map[string]json.RawMessage
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return nil, fmt.Errorf("Read metadata from %s: %w", manifest, err)
		}
	}
	declaration, exists := metadata[metadataNamespace]
	if !exists {
		return map[string]json.RawMessage{}, nil
	}
	var value any
	if err := json.Unmarshal(declaration, &value); err != nil {
		return nil, err
	}
	schema, err := compiledOptionsSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(value); err != nil {
		return nil, fmt.Errorf("Invalid %s.metadata.%s in %s: %w", owner, metadataNamespace, manifest, err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(declaration, &result); err != nil {
		return nil, err
	}
	var decoded settings
	if err := json.Unmarshal(declaration, &decoded); err != nil {
		return nil, err
	}
	for name, input := range decoded.BuildInputs {
		if input.CargoPath == "." || input.CargoPath == ".." || path.IsAbs(input.CargoPath) || path.Clean(input.CargoPath) != input.CargoPath ||
			strings.HasPrefix(input.CargoPath, "../") || strings.ContainsAny(input.CargoPath, "\\\r\n") {
			return nil, fmt.Errorf("Invalid %s.metadata.%s.build_inputs.%s.cargo_path in %s: use a clean repository-relative path", owner, metadataNamespace, name, manifest)
		}
	}
	return result, nil
}

func resolveSettings(defaults map[string]json.RawMessage, pkg cargoPackage) (settings, error) {
	declaration, err := declaredSettings(pkg.Metadata, pkg.Manifest, "package")
	if err != nil {
		return settings{}, err
	}
	effective := map[string]json.RawMessage{}
	maps.Copy(effective, defaults)
	maps.Copy(effective, declaration)
	data, err := json.Marshal(effective)
	if err != nil {
		return settings{}, err
	}
	var result settings
	if err := json.Unmarshal(data, &result); err != nil {
		return settings{}, err
	}
	for name, input := range result.BuildInputs {
		rel, err := filepath.Rel(pkg.WorkspaceRoot, filepath.Dir(pkg.Manifest))
		if err != nil {
			return settings{}, err
		}
		resolved, err := label.Parse(input.BazelLabel)
		if err != nil {
			return settings{}, fmt.Errorf("Invalid build_inputs.%s.bazel_label in %s: %w", name, pkg.Manifest, err)
		}
		input.BazelLabel = resolved.Abs("", filepath.ToSlash(rel)).String()
		result.BuildInputs[name] = input
	}
	return result, nil
}

// Preserve declaration order while removing repeated input expressions.
func uniqueInputs(groups ...[]string) []string {
	result := []string{}
	for _, group := range groups {
		for _, value := range group {
			if !slices.Contains(result, value) {
				result = append(result, value)
			}
		}
	}
	return result
}

func readmeInput(root, rel string, pkg cargoPackage) ([]string, error) {
	if pkg.Readme == "" {
		return nil, nil
	}
	file := pkg.Readme
	if !filepath.IsAbs(file) {
		file = filepath.Join(root, rel, file)
	}
	local, err := filepath.Rel(filepath.Join(root, rel), file)
	if err != nil {
		return nil, err
	}
	if local != ".." && !strings.HasPrefix(local, ".."+string(filepath.Separator)) {
		return []string{filepath.ToSlash(local)}, nil
	}
	// A shared README belongs to the nearest enclosing Bazel package.
	label, err := workspaceFileLabel(root, file)
	if err != nil {
		return nil, fmt.Errorf("Read package.readme in %s: %w", pkg.Manifest, err)
	}
	return []string{label}, nil
}

func workspaceFileLabel(root, file string) (string, error) {
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("The input file must be inside the repository: %s", file)
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		for _, name := range []string{"BUILD.bazel", "BUILD"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				pkg, _ := filepath.Rel(root, dir)
				name, _ := filepath.Rel(dir, file)
				if pkg == "." {
					pkg = ""
				}
				return "//" + filepath.ToSlash(pkg) + ":" + filepath.ToSlash(name), nil
			}
		}
		if dir == root {
			return "", fmt.Errorf("The input file has no Bazel package: %s", file)
		}
	}
}

func rejectLegacyDirectives(file *rule.File) error {
	if file == nil {
		return nil
	}
	for _, directive := range file.Directives {
		if strings.HasPrefix(directive.Key, "rules_rs_") {
			return fmt.Errorf("Move legacy directive %q to package.metadata.%s in Cargo.toml", directive.Key, metadataNamespace)
		}
	}
	return nil
}
