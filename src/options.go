// Aspect sets `-r=true` because Gazelle version 2 does not recurse by default when directory arguments are present.
// Gazelle excludes nested directories from its BUILD-file walk.
// This also prevents load and format changes in nested fixture BUILD files.
package rulesrs

import (
	"bytes"
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

	"github.com/bazel-contrib/bazel-gazelle/v2/label"
	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	bzl "github.com/bazelbuild/buildtools/build"
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
	DepOverrides       []dependencyOverride  `json:"dep_overrides"`
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
	return compileSchema("urn:kernel-toolchain:rules_rs", optionsSchema)
})

func compileSchema(identifier string, data []byte) (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(identifier, document); err != nil {
		return nil, err
	}
	return compiler.Compile(identifier)
}

// Read and validate only this tool's namespace from Cargo metadata.
func declaredSettings(raw json.RawMessage, manifest, owner string) (map[string]json.RawMessage, error) {
	var metadata map[string]json.RawMessage
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return nil, fmt.Errorf("read metadata from %s: %w", manifest, err)
		}
	}
	declaration, exists := metadata[metadataNamespace]
	if !exists {
		return map[string]json.RawMessage{}, nil
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(declaration))
	if err != nil {
		return nil, err
	}
	schema, err := compiledOptionsSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(value); err != nil {
		return nil, fmt.Errorf("invalid %s.metadata.%s in %s: %w", owner, metadataNamespace, manifest, err)
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
			return nil, fmt.Errorf("invalid %s.metadata.%s.build_inputs.%s.cargo_path in %s: use a clean repository-relative path", owner, metadataNamespace, name, manifest)
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
	if len(result.BuildInputs) != 0 {
		rel, err := filepath.Rel(packageRepoRoot(pkg), filepath.Dir(pkg.Manifest))
		if err != nil {
			return settings{}, err
		}
		if rel == "." {
			rel = ""
		}
		for name, input := range result.BuildInputs {
			resolved, err := label.Parse(input.BazelLabel)
			if err != nil {
				return settings{}, fmt.Errorf("invalid build_inputs.%s.bazel_label in %s: %w", name, pkg.Manifest, err)
			}
			input.BazelLabel = resolved.Abs("", filepath.ToSlash(rel)).String()
			result.BuildInputs[name] = input
		}
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
		return nil, fmt.Errorf("read package.readme in %s: %w", pkg.Manifest, err)
	}
	return []string{label}, nil
}

func workspaceFileLabel(root, file string) (string, error) {
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("the input file must be inside the repository: %s", file)
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
			return "", fmt.Errorf("the input file has no Bazel package: %s", file)
		}
	}
}

// Check explicit Rust paths before Gazelle writes any BUILD file.
func validateSourceBoundaries(root, rel string, rules []*rule.Rule, buildNames []string) error {
	packageDir := filepath.Join(root, rel)
	coreKinds := kindMap()
	for _, r := range rules {
		if _, core := coreKinds[r.Kind()]; !core {
			continue
		}
		for _, key := range []string{"crate_root", "srcs"} {
			for _, source := range explicitSourcePaths(r.Attr(key)) {
				if strings.HasPrefix(source, "//") || strings.HasPrefix(source, "@") {
					continue
				}
				source = strings.TrimPrefix(source, ":")
				if source == "" || source == "." || source == ".." || filepath.IsAbs(source) ||
					path.Clean(source) != source || strings.HasPrefix(source, "../") || strings.ContainsAny(source, "\\\r\n") {
					return fmt.Errorf("target %s has invalid %s path %q", r.Name(), key, source)
				}
				file := filepath.Join(packageDir, filepath.FromSlash(source))
				for dir := filepath.Dir(file); dir != packageDir; dir = filepath.Dir(dir) {
					for _, name := range buildNames {
						boundary := filepath.Join(dir, name)
						info, err := os.Stat(boundary)
						if os.IsNotExist(err) {
							continue
						}
						if err != nil {
							return fmt.Errorf("check source boundary %s: %w", boundary, err)
						}
						if !info.IsDir() {
							boundary, _ = filepath.Rel(root, boundary)
							return fmt.Errorf("target %s %s path %q crosses Bazel package %s", r.Name(), key, source, filepath.ToSlash(boundary))
						}
					}
				}
			}
		}
	}
	return nil
}

// Glob expressions already stop at Bazel package boundaries.
func explicitSourcePaths(expr bzl.Expr) []string {
	var result []string
	switch value := expr.(type) {
	case *bzl.StringExpr:
		result = append(result, value.Value)
	case *bzl.ListExpr:
		for _, item := range value.List {
			result = append(result, explicitSourcePaths(item)...)
		}
	case *bzl.BinaryExpr:
		result = append(result, explicitSourcePaths(value.X)...)
		result = append(result, explicitSourcePaths(value.Y)...)
	case *bzl.DictExpr:
		for _, item := range value.List {
			result = append(result, explicitSourcePaths(item.Value)...)
		}
	case *bzl.CallExpr:
		if function, ok := value.X.(*bzl.Ident); ok && function.Name == "select" {
			for _, argument := range value.List {
				result = append(result, explicitSourcePaths(argument)...)
			}
		}
	}
	return result
}

func rejectLegacyDirectives(file *rule.File) error {
	if file == nil {
		return nil
	}
	for _, directive := range file.Directives {
		if strings.HasPrefix(directive.Key, "rules_rs_") {
			return fmt.Errorf("move legacy directive %q to package.metadata.%s in Cargo.toml", directive.Key, metadataNamespace)
		}
	}
	return nil
}
