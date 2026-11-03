package rulesrs

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func contextFixture(t *testing.T, root string) (map[string]cargoPackage, unitGraph) {
	t.Helper()
	platform := "x86_64-unknown-linux-gnu"
	packages := map[string]cargoPackage{}
	put(t, root, "Cargo.toml", "[workspace]\n")
	for _, name := range []string{"a", "b"} {
		rel := "crates/" + name
		put(t, root, rel+"/Cargo.toml", "[package]\nname = '"+name+"'\n")
		packages[rel] = cargoPackage{
			ID: name, Name: name, Manifest: filepath.Join(root, rel, "Cargo.toml"),
			Targets:  []cargoTarget{{Kind: []string{"lib"}, Source: filepath.Join(root, rel, "src/lib.rs")}},
			Contexts: &packageContexts{Platforms: []string{platform}, Bindings: map[string]map[string]*contextTarget{}, Variants: map[string]*contextTarget{}},
		}
	}
	b := packages["crates/b"]
	b.Targets = append(b.Targets, cargoTarget{Kind: []string{"custom-build"}, Source: filepath.Join(root, "crates/b/build.rs")})
	packages["crates/b"] = b
	return packages, unitGraph{Version: 1, Roots: []int{0, 4}, Units: []cargoUnit{
		{Package: "a", Target: packages["crates/a"].Targets[0], Mode: "build", Platform: &platform, Features: []string{"target"}},
		{Package: "a", Target: packages["crates/a"].Targets[0], Mode: "build", Features: []string{"host"}},
		{Package: "b", Target: b.Targets[1], Mode: "build", Dependencies: []cargoUnitDependency{{Index: 1, Name: "renamed"}}},
		{Package: "b", Target: b.Targets[1], Mode: "run-custom-build", Platform: &platform, Dependencies: []cargoUnitDependency{{Index: 2}}},
		{Package: "b", Target: b.Targets[0], Mode: "build", Platform: &platform, Dependencies: []cargoUnitDependency{{Index: 0, Name: "renamed"}, {Index: 3}}},
	}}
}

func TestCargoContextBindings(t *testing.T) {
	root := t.TempDir()
	packages, graph := contextFixture(t, root)
	if err := collectContexts(graph, "x86_64-unknown-linux-gnu", "build", packages, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	a, b := packages["crates/a"], packages["crates/b"]
	contextFeature := func(label string) string {
		t.Helper()
		_, name, _ := strings.Cut(label, ":")
		return strings.Join(a.Contexts.Variants[name].Features, ",")
	}
	library := b.Contexts.Bindings["b"]["x86_64-unknown-linux-gnu"]
	if len(library.Aliases) != 1 {
		t.Fatal("The library needs one local Rust dependency alias.")
	}
	for label, name := range library.Aliases {
		if name != "renamed" || contextFeature(label) != "target" {
			t.Fatal("The library did not bind its target feature context.")
		}
	}
	build := b.Contexts.Bindings["b-build-script"]["x86_64-unknown-linux-gnu"]
	if len(build.Deps) != 1 || contextFeature(build.Deps[0]) != "host" {
		t.Fatal("The build script did not bind its host feature context.")
	}
	templates, err := generate(root, "crates/b", b, settings{})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := generateContexts("crates/b", b, templates)
	if err != nil {
		t.Fatal(err)
	}
	output := rendered(rules)
	for _, want := range []string{"cargo_only = True", "aliases() |", "manual", `name = "b-build-script"`, build.Label} {
		if !strings.Contains(output, want) {
			t.Errorf("Missing context declaration %q in:\n%s", want, output)
		}
	}
}

// Public labels must preserve the Cargo unit's Rust type identity.
func TestPublicLibrariesUseCargoContextIdentity(t *testing.T) {
	root := t.TempDir()
	packages, graph := contextFixture(t, root)
	platform := "x86_64-unknown-linux-gnu"
	if err := collectContexts(graph, platform, "build", packages, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		rel := "crates/" + name
		pkg := packages[rel]
		templates, err := generate(root, rel, pkg, settings{})
		if err != nil {
			t.Fatal(err)
		}
		rules, err := generateContexts(rel, pkg, templates)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rules {
			if r.Name() != name {
				continue
			}
			found = true
			if r.Kind() != "alias" || r.AttrString("actual") != pkg.Contexts.Bindings[name][platform].Label {
				t.Fatalf("Public library %s must use its Cargo unit instead of a second Rust compilation.", name)
			}
		}
		if !found {
			t.Fatalf("Public library %s is missing.", name)
		}
	}
}

func TestCargoContextIdentity(t *testing.T) {
	var identities [][]string
	for attempt := 0; attempt < 2; attempt++ {
		packages, graph := contextFixture(t, t.TempDir())
		if attempt == 1 {
			slices.Reverse(graph.Units)
			for i := range graph.Units {
				for j := range graph.Units[i].Dependencies {
					graph.Units[i].Dependencies[j].Index = len(graph.Units) - 1 - graph.Units[i].Dependencies[j].Index
				}
			}
			for i := range graph.Roots {
				graph.Roots[i] = len(graph.Units) - 1 - graph.Roots[i]
			}
		}
		if err := collectContexts(graph, "x86_64-unknown-linux-gnu", "build", packages, []string{"a", "b"}); err != nil {
			t.Fatal(err)
		}
		var names []string
		for rel, pkg := range packages {
			for name := range pkg.Contexts.Variants {
				names = append(names, rel+":"+name)
			}
		}
		slices.Sort(names)
		identities = append(identities, names)
	}
	if !slices.Equal(identities[0], identities[1]) {
		t.Fatal("Context labels depend on workspace paths or Cargo unit ordering.")
	}
}

func TestInvalidCargoUnitGraphs(t *testing.T) {
	for _, invalid := range []string{"version", "edge", "root", "cycle", "scope"} {
		t.Run(invalid, func(t *testing.T) {
			packages, graph := contextFixture(t, t.TempDir())
			switch invalid {
			case "version":
				graph.Version = 2
			case "edge":
				graph.Units[4].Dependencies[0].Index = 99
			case "root":
				graph.Roots[0] = -1
			case "cycle":
				graph.Units[4].Dependencies[0].Index = 4
			case "scope":
				delete(packages, "crates/a")
			}
			if err := collectContexts(graph, "x86_64-unknown-linux-gnu", "build", packages, []string{"a", "b"}); err == nil {
				t.Fatalf("Accepted invalid Cargo unit graph: %s", invalid)
			}
		})
	}
}
