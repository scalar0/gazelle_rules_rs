// Package testfixture stages declared fixture files in writable workspaces.
package testfixture

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/pelletier/go-toml/v2"
)

// Resolve gets an absolute path from a runfile or an existing file.
func Resolve(name string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	return runfiles.Rlocation(name)
}

// CopyTree copies a fixture and removes the .in template suffix.
func CopyTree(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		source, err = filepath.EvalSymlinks(source)
		if err != nil {
			return err
		}
	}
	return filepath.WalkDir(source, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(file)
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
		}
		rel, err := filepath.Rel(source, file)
		if err != nil {
			return err
		}
		if rel == "BUILD.bazel" || rel == "MODULE.bazel" || rel == "REPO.bazel" {
			// Upstream Bazel files do not belong to the generated consumer.
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		return Write(destination, strings.TrimSuffix(rel, ".in"), data)
	})
}

// Write writes a file relative to a staged workspace.
func Write(root, name string, data []byte) error {
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o644)
}

// Stage copies one canonical Cargo fixture from its declared manifest.
func Stage(manifest, destination string) error {
	source, err := Resolve(manifest)
	if err != nil {
		return err
	}
	return CopyTree(filepath.Dir(source), destination)
}

// Configure copies the shared backend configuration and sets its manifest path.
func Configure(configuration, destination, manifest string) ([]byte, error) {
	source, err := Resolve(configuration)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	config["manifest"] = manifest
	data, err = json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return data, Write(destination, "backend.json", append(data, '\n'))
}

// Vendor adds a declared registry crate for offline Cargo resolution.
func Vendor(manifest, destination string) error {
	source, err := Resolve(manifest)
	if err != nil {
		return err
	}
	crateData, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	var crate struct {
		Package struct{ Name, Version string }
	}
	if err := toml.Unmarshal(crateData, &crate); err != nil {
		return err
	}
	lockData, err := os.ReadFile(filepath.Join(destination, "Cargo.lock"))
	if err != nil {
		return err
	}
	var lock struct {
		Package []struct{ Name, Version, Checksum string }
	}
	if err := toml.Unmarshal(lockData, &lock); err != nil {
		return err
	}
	checksum := ""
	for _, pkg := range lock.Package {
		if pkg.Name == crate.Package.Name && pkg.Version == crate.Package.Version {
			checksum = pkg.Checksum
		}
	}
	if checksum == "" {
		return nil
	}
	target := filepath.Join(destination, "vendor", crate.Package.Name)
	if err := CopyTree(filepath.Dir(source), target); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"files": map[string]string{}, "package": checksum})
	if err != nil {
		return err
	}
	return Write(target, ".cargo-checksum.json", data)
}

// Consumer names the declared inputs for one Rust consumer workspace.
type Consumer struct {
	Kind, Manifest, Configuration, BackendConfig          string
	Handler, HandlerSchema, VendorManifest, ExtensionRoot string
}

// Stage adds the consumer configuration to a shared Cargo fixture.
func (c Consumer) Stage(destination string) error {
	if err := Stage(c.Manifest, destination); err != nil {
		return err
	}
	if err := Vendor(c.VendorManifest, destination); err != nil {
		return err
	}
	config, err := Resolve(c.Configuration)
	if err != nil {
		return err
	}
	if err := CopyTree(filepath.Dir(config), destination); err != nil {
		return err
	}
	handlerPath, err := Resolve(c.Handler)
	if err != nil {
		return err
	}
	handlerSource, err := os.ReadFile(handlerPath)
	if err != nil {
		return err
	}
	if err := Write(destination, "metadata/handler.go", handlerSource); err != nil {
		return err
	}
	schemaPath, err := Resolve(c.HandlerSchema)
	if err != nil {
		return err
	}
	schemaSource, err := os.ReadFile(schemaPath)
	if err != nil {
		return err
	}
	if err := Write(destination, "metadata/resources.schema.json", schemaSource); err != nil {
		return err
	}
	buildPath := filepath.Join(destination, "BUILD.bazel")
	build, err := os.ReadFile(buildPath)
	if err != nil {
		return err
	}
	if err := Write(destination, "BUILD.bazel", []byte(strings.ReplaceAll(string(build), "FIXTURE_KIND", c.Kind))); err != nil {
		return err
	}
	module, err := os.ReadFile(filepath.Join(destination, "MODULE.bazel"))
	if err != nil {
		return err
	}
	backend, err := Configure(c.BackendConfig, destination, "Cargo.toml")
	if err != nil {
		return err
	}
	var settings struct {
		PlatformTriples  []string `json:"platform_triples"`
		RulesRepository  string   `json:"rules_repository"`
		CratesRepository string   `json:"crates_repository"`
	}
	if err := json.Unmarshal(backend, &settings); err != nil {
		return err
	}
	platforms, err := json.Marshal(settings.PlatformTriples)
	if err != nil {
		return err
	}
	replacements := strings.NewReplacer(
		"EXTENSION_ROOT", strconv.Quote(c.ExtensionRoot),
		"FIXTURE_PLATFORMS", string(platforms),
		"FIXTURE_RULES_REPOSITORY", settings.RulesRepository,
		"FIXTURE_CRATES_REPOSITORY", settings.CratesRepository,
	)
	module = []byte(replacements.Replace(string(module)))
	if err := Write(destination, "MODULE.bazel", module); err != nil {
		return err
	}
	return nil
}

// RequireEmpty prevents a stage operation from replacing a previous workspace.
func RequireEmpty(destination string) error {
	entries, err := os.ReadDir(destination)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("destination must be empty: %s", destination)
	}
	return nil
}
