// The pinned `rules_rs` build-script macro does not accept `lint_config` correctly.
// The extension reads Cargo lint tables and emits the public `rustc_flags` attribute for build scripts.
// It preserves workspace inheritance, lint levels, priority order, and `check-cfg` values.
// Other Rust targets use `lint_config()` directly.
package rulesrs

import (
	"fmt"
	"os"
	"sort"

	"github.com/pelletier/go-toml/v2"
)

// Build scripts lack lint_config, so use their public rustc_flags attribute.
func buildLints(rootManifest, manifest string) ([]string, error) {
	read := func(path string) (map[string]interface{}, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var doc map[string]interface{}
		err = toml.Unmarshal(data, &doc)
		return doc, err
	}
	doc, err := read(manifest)
	if err != nil {
		return nil, err
	}
	lints, _ := doc["lints"].(map[string]interface{})
	if inherited, _ := lints["workspace"].(bool); inherited {
		doc, err = read(rootManifest)
		if err != nil {
			return nil, err
		}
		workspace, _ := doc["workspace"].(map[string]interface{})
		lints, _ = workspace["lints"].(map[string]interface{})
	}
	type lint struct {
		name, level string
		priority    int64
		cfg         []string
	}
	var entries []lint
	for _, tool := range []string{"rust", "clippy", "rustdoc"} {
		table, _ := lints[tool].(map[string]interface{})
		for name, value := range table {
			entry := lint{name: name}
			if tool != "rust" {
				entry.name = tool + "::" + name
			}
			switch value := value.(type) {
			case string:
				entry.level = value
			case map[string]interface{}:
				entry.level, _ = value["level"].(string)
				entry.priority, _ = value["priority"].(int64)
				if cfg, ok := value["check-cfg"].([]interface{}); ok {
					for _, item := range cfg {
						text, ok := item.(string)
						if !ok {
							return nil, fmt.Errorf("invalid check-cfg for %s", name)
						}
						entry.cfg = append(entry.cfg, text)
					}
				}
			}
			switch entry.level {
			case "allow", "warn", "deny", "forbid", "force-warn":
			default:
				return nil, fmt.Errorf("invalid Cargo lint level for %s", name)
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].priority != entries[j].priority {
			return entries[i].priority < entries[j].priority
		}
		return entries[i].name < entries[j].name
	})
	var flags []string
	for _, entry := range entries {
		flags = append(flags, "--"+entry.level+"="+entry.name)
		for _, cfg := range entry.cfg {
			flags = append(flags, "--check-cfg="+cfg)
		}
	}
	return flags, nil
}
