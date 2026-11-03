package rulesrs

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// BackendConfig contains the consumer settings that Cargo does not provide.
type BackendConfig struct {
	Manifest         string   `json:"manifest"`
	PlatformTriples  []string `json:"platform_triples"`
	CratesRepository string   `json:"crates_repository"`
	RulesRepository  string   `json:"rules_repository"`
}

func defaultBackendConfig() BackendConfig {
	return BackendConfig{Manifest: "Cargo.toml", CratesRepository: "crates", RulesRepository: "rules_rs"}
}

//go:embed configuration.schema.json
var configurationSchema []byte

func readBackendConfig(file string) (BackendConfig, error) {
	c := defaultBackendConfig()
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) && !filepath.IsAbs(file) {
		location := strings.TrimPrefix(strings.TrimPrefix(file, "external/"), "../")
		resolved, resolveErr := runfiles.Rlocation(location)
		if resolveErr == nil {
			data, err = os.ReadFile(resolved)
		}
	}
	if err != nil {
		return c, err
	}
	var document, value any
	if err := json.Unmarshal(configurationSchema, &document); err != nil {
		return c, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("urn:gazelle:backend", document); err != nil {
		return c, err
	}
	schema, err := compiler.Compile("urn:gazelle:backend")
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return c, err
	}
	if err := schema.Validate(value); err != nil {
		return c, fmt.Errorf("backend configuration: %w", err)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("read backend configuration: %w", err)
	}
	if !json.Valid(data) {
		return c, fmt.Errorf("backend configuration must contain one JSON value")
	}
	if filepath.IsAbs(c.Manifest) || filepath.ToSlash(filepath.Clean(c.Manifest)) != c.Manifest || strings.HasPrefix(c.Manifest, "../") || filepath.Base(c.Manifest) != "Cargo.toml" {
		return c, fmt.Errorf("manifest must be a repository-relative Cargo.toml path")
	}
	for _, name := range []string{c.CratesRepository, c.RulesRepository} {
		if name == "" || strings.ContainsAny(name, "@/:\\ \t\r\n") {
			return c, fmt.Errorf("repository names must omit label punctuation")
		}
	}
	if len(c.PlatformTriples) == 0 {
		return c, fmt.Errorf("platform_triples must not be empty")
	}
	for _, triple := range c.PlatformTriples {
		if triple == "" || strings.ContainsAny(triple, "/\\ \t\r\n") {
			return c, fmt.Errorf("invalid platform triple %q", triple)
		}
	}
	slices.Sort(c.PlatformTriples)
	c.PlatformTriples = slices.Compact(c.PlatformTriples)
	return c, nil
}
