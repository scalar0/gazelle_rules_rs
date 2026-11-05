package rulesrs

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
)

func TestStandaloneRulesUseCargoContextEdges(t *testing.T) {
	root := t.TempDir()
	packages, graph := contextFixture(t, root)
	platform := "x86_64-unknown-linux-gnu"
	if err := collectContexts(graph, platform, "build", packages, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	compactContexts(packages)
	pkg := packages["crates/b"]
	pkg.Name = "independent-model"
	pkg.Edition = "2024"
	pkg.Standalone = true
	pkg.Targets[0].Name = "independent_model"
	pkg.Manifest = filepath.Join(root, "crates/b/Cargo.toml")
	templates, err := generate(root, "crates/b", pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := generateContexts("crates/b", pkg, templates)
	if err != nil {
		t.Fatal(err)
	}
	materializeStandalone(rules, pkg)
	output := rendered(rules)
	for _, unwanted := range []string{"all_crate_deps(", "crate_name()", "edition()", "lint_config(", "aliases()"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("The standalone package retained workspace helper %q:\n%s", unwanted, output)
		}
	}
	for _, wanted := range []string{`crate_name = "independent_model"`, `edition = "2024"`, "//crates/a:", `"renamed"`} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("The standalone package lost Cargo context %q:\n%s", wanted, output)
		}
	}
	file := rule.EmptyFile("BUILD.bazel", "crates/b")
	for _, r := range rules {
		r.Insert(file)
	}
	result, err := reconcile(file, rules)
	if err != nil || len(result.Empty) != 0 {
		t.Fatalf("Repeated standalone generation deleted owned rules: %v", err)
	}
}
