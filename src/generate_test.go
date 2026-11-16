package rulesrs

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
	bzl "github.com/bazelbuild/buildtools/build"
)

// HTML selection must remain independent from doctest selection.
// Existing doctest fixtures do not protect default HTML target selection.
func TestDocumentationTargetSelection(t *testing.T) {
	root := t.TempDir()
	pkg := cargoPackage{Name: "probe-package", RepoRoot: root, Manifest: filepath.Join(root, "Cargo.toml"), Features: []string{"enabled"}}
	for _, item := range []struct {
		name, kind   string
		doc, doctest bool
		required     []string
	}{
		{"probe_lib", "lib", true, false, nil},
		{"probe-lib", "bin", true, false, nil},
		{"tool", "bin", true, false, nil},
		{"disabled", "bin", false, false, nil},
		{"gated", "bin", true, false, []string{"missing"}},
		{"example", "example", true, false, nil},
		{"integration", "test", true, false, nil},
	} {
		pkg.Targets = append(pkg.Targets, cargoTarget{Name: item.name, Kind: []string{item.kind}, Source: filepath.Join(root, item.name+".rs"), Doc: item.doc, Doctest: item.doctest, RequiredFeatures: item.required})
	}
	rules, err := generate(root, "", pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rules {
		if r.Kind() != "rust_doc" {
			continue
		}
		names = append(names, r.Name())
		if targetRole(r) != "documentation" || r.AttrString("crate") == "" {
			t.Fatalf("Incomplete documentation target: %s", rendered([]*rule.Rule{r}))
		}
		if !slices.Equal(r.AttrStrings("crate_features"), []string{"enabled"}) {
			t.Fatalf("Documentation lost features: %s", r.Name())
		}
		if r.Name() == "docs/bin/tool" && !slices.Equal(r.AttrStrings("rustdoc_flags"), []string{"--document-private-items"}) {
			t.Fatal("Binary documentation lost private items.")
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"docs/bin/tool", "docs/lib"}) {
		t.Fatalf("Documentation targets = %v", names)
	}
	pkg.Targets[0].Doc, pkg.Targets[0].Doctest = false, true
	rules, err = generate(root, "", pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	text := rendered(rules)
	if strings.Contains(text, "name = \"docs/lib\"") || !strings.Contains(text, "name = \"tests/doc\"") {
		t.Fatal("Documentation and doctest selection became coupled.")
	}
}

// Documentation must follow build contexts without test-only attributes.
// Existing context tests do not cover HTML rules or standalone rustdoc flags.
func TestDocumentationBuildContext(t *testing.T) {
	root := t.TempDir()
	pkg := cargoPackage{Name: "probe", RepoRoot: root, Manifest: filepath.Join(root, "Cargo.toml"), Targets: []cargoTarget{{Name: "probe", Kind: []string{"lib"}, Source: filepath.Join(root, "src/lib.rs"), Doc: true}}}
	rules, err := generate(root, "", pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	pkg.Contexts = &packageContexts{Platforms: []string{"x86_64-unknown-linux-gnu", "aarch64-unknown-linux-gnu"}, Bindings: map[string]map[string]*contextTarget{"lib": {"x86_64-unknown-linux-gnu": {Features: []string{"selected"}, Label: "//:lib"}}}, Variants: map[string]*contextTarget{}}
	rules, err = generateContexts("", pkg, rules)
	if err != nil {
		t.Fatal(err)
	}
	pkg.StandaloneLints = []string{"--deny=rustdoc::broken_intra_doc_links"}
	materializeStandalone(rules, pkg)
	for _, r := range rules {
		if r.Kind() != "rust_doc" {
			continue
		}
		for _, key := range []string{"deps", "aliases", "rustc_flags", "rustc_env", "data"} {
			if r.Attr(key) != nil {
				t.Fatalf("Documentation has test attribute %s", key)
			}
		}
		text := rendered([]*rule.Rule{r})
		for _, want := range []string{"selected", "@platforms//:incompatible", "rustdoc::broken_intra_doc_links"} {
			if !strings.Contains(text, want) {
				t.Fatalf("Documentation lost %s: %s", want, text)
			}
		}
		return
	}
	t.Fatal("The build context removed documentation.")
}

// The target contract covers public outputs that old naming tests did not cover.
func TestPublicTargetContract(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Cargo.toml", "[workspace]\n")
	pkg := cargoPackage{Name: "cargo-package", RepoRoot: root, Manifest: filepath.Join(root, "crates/probe/Cargo.toml")}
	for _, target := range []struct {
		name, kind, source string
		types              []string
	}{
		{"cargo_package", "lib", "src/lib.rs", []string{"rlib", "cdylib"}},
		{"cargo-package", "bin", "src/main.rs", nil},
		{"tool", "bin", "src/bin/tool.rs", nil},
		{"demo", "example", "examples/demo.rs", []string{"rlib", "staticlib"}},
		{"check", "test", "tests/check.rs", nil},
		{"speed", "bench", "benches/speed.rs", nil},
	} {
		pkg.Targets = append(pkg.Targets, cargoTarget{
			Name: target.name, Kind: []string{target.kind}, Source: filepath.Join(filepath.Dir(pkg.Manifest), target.source),
			CrateTypes: target.types, Test: true, Bench: true, Doctest: target.kind == "lib",
		})
	}
	rules, err := generate(root, "crates/probe", pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"lib", "lib/cdylib", "bin/cargo-package", "bin/tool", "examples/demo", "examples/demo/staticlib",
		"tests/unit/lib", "tests/unit/bin/cargo-package", "tests/unit/bin/tool", "tests/unit/examples/demo", "tests/doc",
		"tests/integration/check", "tests/integration", "benches/speed", "tests/bench/lib", "tests/bench/bin/cargo-package", "tests/bench/bin/tool", "probe",
	}
	var got []string
	for _, r := range rules {
		got = append(got, r.Name())
		info, ok := r.PrivateAttr("cargo_target_info").(TargetInfo)
		if !ok || info.BazelTarget != r.Name() || info.Role == "" {
			t.Fatalf("The public target %s has no descriptor.", r.Name())
		}
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("Public target names = %v, want %v", got, want)
	}
	for _, target := range pkg.Targets {
		for _, mode := range []string{"build", "test", "bench"} {
			if targetKind(target) == "example" && mode == "bench" {
				continue
			}
			name, err := unitName(pkg, cargoUnit{Target: target, Mode: mode})
			if err != nil || !slices.Contains(got, name) {
				t.Fatalf("Cargo context %s/%s has no public target: %s, %v", target.Name, mode, name, err)
			}
		}
	}
}

// Executable labels must end in leaves that cannot contain other executable paths.
// This catches output conflicts between libraries, default binaries, and other binaries.
// Naming tests do not check output prefixes or direct rule kinds.
func TestExecutablePathsUseLeafTargetNames(t *testing.T) {
	root := t.TempDir()
	pkg := cargoPackage{Name: "probe", RepoRoot: root, Manifest: filepath.Join(root, "Cargo.toml")}
	for _, target := range []struct{ name, kind, file string }{
		{"probe", "lib", "src/lib.rs"},
		{"probe", "bin", "src/main.rs"},
		{"tool", "bin", "src/bin/tool.rs"},
		{"lib", "bin", "src/bin/lib.rs"},
	} {
		pkg.Targets = append(pkg.Targets, cargoTarget{
			Name: target.name, Kind: []string{target.kind}, Source: filepath.Join(root, target.file), Test: true, Bench: true,
		})
	}
	rules, err := generate(root, "", pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*rule.Rule{}
	var outputs []string
	for _, r := range rules {
		byName[r.Name()] = r
		if r.Kind() == "test_suite" || r.Attr("binary_name") != nil || strings.HasPrefix(r.Name(), "_cargo_test_") {
			t.Fatalf("The target %s must use its direct rule and output name.", r.Name())
		}
		if r.Kind() == "rust_binary" || r.Kind() == "rust_test" {
			outputs = append(outputs, r.Name(), r.Name()+".runfiles_manifest")
		}
	}
	for _, name := range []string{"probe", "tool", "lib"} {
		if r := byName["bin/"+name]; r == nil || r.Kind() != "rust_binary" {
			t.Fatalf("The binary %s must use its Cargo target name.", name)
		}
	}
	for _, mode := range []string{"unit", "bench"} {
		for suffix, crate := range map[string]string{"lib": ":lib", "bin/probe": ":bin/probe", "bin/tool": ":bin/tool", "bin/lib": ":bin/lib"} {
			name := "tests/" + mode + "/" + suffix
			r := byName[name]
			if r == nil || r.Kind() != "rust_test" || r.AttrString("crate") != crate {
				t.Fatalf("The test %s must compile its crate directly.", name)
			}
			if slices.Contains(r.AttrStrings("tags"), "manual") != (mode == "bench") {
				t.Fatalf("The test %s changed its manual selection behavior.", name)
			}
		}
	}
	for i, output := range outputs {
		for j, other := range outputs {
			if i != j && (output == other || strings.HasPrefix(output, other+"/")) {
				t.Fatalf("Executable outputs conflict: %s and %s", output, other)
			}
		}
	}
}

// Package aliases must not hide compilation targets or create self references.
func TestPackageAliasContract(t *testing.T) {
	for _, tc := range []struct {
		name, rel, alias       string
		buildScript, collision bool
	}{
		{"root-package", "", "root-package", false, false},
		{"cargo-package", "crates/probe", "probe", false, false},
		{"cargo-package", "crates/lib", "", false, false},
		{"cargo-package", "crates/bin", "bin", false, false},
		{"cargo-package", "crates/build-script", "", true, true},
	} {
		t.Run(tc.name+"/"+tc.rel, func(t *testing.T) {
			root := t.TempDir()
			pkg := cargoPackage{Name: tc.name, Manifest: filepath.Join(root, tc.rel, "Cargo.toml")}
			pkg.Targets = []cargoTarget{{Kind: []string{"lib"}, Source: filepath.Join(root, tc.rel, "src/lib.rs")}}
			if tc.buildScript {
				pkg.Targets = append(pkg.Targets, cargoTarget{Kind: []string{"custom-build"}, Source: filepath.Join(root, tc.rel, "build.rs")})
			}
			rules, err := generate(root, tc.rel, pkg, settings{})
			if tc.collision {
				if err == nil || !strings.Contains(err.Error(), "collision") {
					t.Fatalf("A package alias collision must fail: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var aliases []string
			for _, r := range rules {
				if r.Kind() == "alias" {
					aliases = append(aliases, r.Name())
					if r.AttrString("actual") != ":lib" {
						t.Fatal("The package alias must use the library target.")
					}
				}
			}
			if tc.alias == "" && len(aliases) != 0 || tc.alias != "" && !slices.Equal(aliases, []string{tc.alias}) {
				t.Fatalf("Package aliases = %v, want %s", aliases, tc.alias)
			}
		})
	}
}

// Manifest access must require explicit inputs and environment values.
// This catches automatic manifest inputs and lost metadata or handler declarations.
// The previous test required manifest access on every generated target.
func TestCargoManifestInputs(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Cargo.toml", "[package]\nname = 'probe'\n")
	pkg := cargoPackage{Name: "probe", Description: "A fixture.", Manifest: filepath.Join(root, "Cargo.toml")}
	for _, kind := range []string{"lib", "custom-build", "bin", "test", "example", "bench"} {
		pkg.Targets = append(pkg.Targets, cargoTarget{Name: kind, Kind: []string{kind}, Source: filepath.Join(root, kind+".rs"), Test: true, Bench: true})
	}
	for _, mode := range []string{"default", "metadata", "handler"} {
		t.Run(mode, func(t *testing.T) {
			opts := settings{}
			if mode == "metadata" {
				opts = settings{
					LibraryCompileData: []string{"library.txt", "Cargo.toml"},
					UnitCompileData:    []string{"unit.txt", ":Cargo.toml"},
					TestCompileData:    []string{"test.txt", "Cargo.toml"},
					BuildCompileData:   []string{"compile.txt", "Cargo.toml", ":Cargo.toml"},
					BuildData:          []string{"runtime.txt", "Cargo.toml", ":Cargo.toml"},
					BuildInputs:        map[string]buildInput{"CARGO_MANIFEST_PATH": {BazelLabel: ":Cargo.toml", CargoPath: "Cargo.toml"}},
				}
			}
			rules, err := generate(root, "", pkg, opts)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "handler" {
				contribution := Contribution{}
				for _, r := range rules {
					if r.Kind() == "alias" || r.Kind() == "test_suite" {
						continue
					}
					input := TargetInputs{Target: r.Name(), CompileData: []string{"Cargo.toml"}, RustcEnv: map[string]string{"CARGO_MANIFEST_PATH": "$(execpath :Cargo.toml)"}}
					if r.Kind() == "cargo_build_script" {
						input.Data = []string{"Cargo.toml"}
						input.BuildScriptEnv = map[string]string{"CARGO_MANIFEST_PATH": "$(execpath :Cargo.toml)"}
					}
					contribution.Inputs = append(contribution.Inputs, input)
				}
				rules, err = applyContributions(rules, []Contribution{contribution})
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, r := range rules {
				if r.Kind() == "alias" || r.Kind() == "test_suite" {
					continue
				}
				for _, attr := range []string{"compile_data", "data"} {
					manifestCount := 0
					for _, input := range explicitSourcePaths(r.Attr(attr)) {
						if input == "Cargo.toml" || input == ":Cargo.toml" {
							manifestCount++
						}
					}
					want := 0
					if mode != "default" && (attr == "compile_data" || r.Kind() == "cargo_build_script") {
						want = 1
					}
					if manifestCount != want {
						t.Fatalf("Target %s has %d manifests in %s, want %d.", r.Name(), manifestCount, attr, want)
					}
				}
				env := literalStringDict(r.Attr("rustc_env"))
				if (env["CARGO_MANIFEST_PATH"] != "") != (mode == "handler") {
					t.Fatalf("Target %s has incorrect compiler manifest access: %v", r.Name(), env)
				}
				if env["CARGO_PKG_NAME"] != pkg.Name || env["CARGO_PKG_DESCRIPTION"] != pkg.Description {
					t.Fatalf("Target %s lost its Cargo package environment: %v", r.Name(), env)
				}
				if r.Kind() == "cargo_build_script" {
					buildEnv := literalStringDict(r.Attr("build_script_env"))
					if (buildEnv["CARGO_MANIFEST_PATH"] != "") != (mode != "default") {
						t.Fatalf("The build script has incorrect execution manifest access: %v", buildEnv)
					}
					if mode == "metadata" && (!slices.Contains(explicitSourcePaths(r.Attr("data")), "runtime.txt") || !slices.Contains(explicitSourcePaths(r.Attr("compile_data")), "compile.txt")) {
						t.Fatal("The build script lost its configured inputs.")
					}
				} else if mode == "metadata" && targetRole(r) == "unit-test" {
					if !slices.Contains(r.AttrStrings("compile_data"), "unit.txt") || !slices.Contains(r.AttrStrings("compile_data"), "library.txt") {
						t.Fatal("The unit test lost its configured compiler inputs.")
					}
				}
			}
		})
	}
}

// Kept manifest opt-ins must survive removal of automatic manifest access.
// This catches lost compiler and execution inputs, stale defaults, and unstable repeated merges.
// The manifest input test covers metadata and handlers without existing BUILD overrides.
func TestKeptManifestAccess(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Cargo.toml", "[package]\nname = 'probe'\n")
	pkg := cargoPackage{Name: "probe", Manifest: filepath.Join(root, "Cargo.toml"), Description: "A fixture."}
	for _, target := range []struct{ kind, name, source string }{
		{"lib", "probe", "src/lib.rs"}, {"bin", "probe", "src/main.rs"}, {"custom-build", "build-script", "build.rs"},
	} {
		pkg.Targets = append(pkg.Targets, cargoTarget{Name: target.name, Kind: []string{target.kind}, Source: filepath.Join(root, target.source)})
	}
	file, err := rule.LoadData("BUILD.bazel", "", []byte(marker+`
rust_library(
    name = "lib",
    compile_data = [
        ":Cargo.toml", # keep
    ],
    rustc_env = {
        "CARGO_MANIFEST_PATH": "$(execpath :Cargo.toml)",
        "CARGO_PKG_NAME": "probe",
        "CARGO_PKG_DESCRIPTION": "A fixture.",
    }, # keep
)
`+marker+`
rust_binary(
    name = "bin/probe",
    compile_data = [":Cargo.toml"],
    rustc_env = {"CARGO_MANIFEST_PATH": "$(execpath :Cargo.toml)"},
)
`+marker+`
cargo_build_script(
    name = "build-script",
    compile_data = [":Cargo.toml"],
    rustc_env = {"CARGO_MANIFEST_PATH": "$(execpath :Cargo.toml)"},
    data = [
        ":Cargo.toml", # keep
    ],
    build_script_env = {"CARGO_MANIFEST_PATH": "$(execpath :Cargo.toml)"}, # keep
)
`))
	if err != nil {
		t.Fatal(err)
	}
	previous := ""
	for attempt := 0; attempt < 2; attempt++ {
		fresh, err := generate(root, "", pkg, settings{})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range fresh {
			for _, attr := range r.AttrKeys() {
				setAttr(r, attr, r.Attr(attr))
			}
		}
		if _, err := reconcile(file, fresh); err != nil {
			t.Fatal(err)
		}
		for _, old := range file.Rules {
			for _, r := range fresh {
				if r.Name() == old.Name() {
					rule.MergeRules(r, old, kindMap()[old.Kind()].MergeableAttrs, file.Path)
				}
			}
		}
		library, binary, build := file.Rules[0], file.Rules[1], file.Rules[2]
		if !slices.Equal(library.AttrStrings("compile_data"), []string{":Cargo.toml"}) || literalStringDict(library.Attr("rustc_env"))["CARGO_MANIFEST_PATH"] == "" {
			t.Fatal("Regeneration removed a kept compiler manifest opt-in.")
		}
		if !slices.Equal(build.AttrStrings("data"), []string{":Cargo.toml"}) || literalStringDict(build.Attr("build_script_env"))["CARGO_MANIFEST_PATH"] == "" {
			t.Fatal("Regeneration removed a kept build-script manifest opt-in.")
		}
		for _, r := range []*rule.Rule{binary, build} {
			if len(r.AttrStrings("compile_data")) != 0 || literalStringDict(r.Attr("rustc_env"))["CARGO_MANIFEST_PATH"] != "" {
				t.Fatalf("Target %s retained automatic manifest compilation inputs.", r.Name())
			}
		}
		output := string(file.Format())
		if strings.Count(output, "# keep") != 4 {
			t.Fatalf("Regeneration changed the manifest keep comments:\n%s", output)
		}
		if attempt != 0 && output != previous {
			t.Fatal("Repeated manifest opt-in generation changed the BUILD file.")
		}
		previous = output
	}
}

// Explicit derived-target inputs can repeat inherited library inputs.
// This catches duplicate Bazel labels after handlers select all public outputs.
// Manifest input tests do not combine direct and inherited handler inputs.
func TestInheritedResourceInputsDeclareLabelsOnce(t *testing.T) {
	root := t.TempDir()
	pkg := cargoPackage{
		Name: "probe", RepoRoot: root, Manifest: filepath.Join(root, "Cargo.toml"),
		Targets: []cargoTarget{{
			Name: "probe", Kind: []string{"lib"}, CrateTypes: []string{"rlib", "cdylib", "staticlib"},
			Source: filepath.Join(root, "src/lib.rs"), Test: true, Bench: true,
		}},
	}
	rules, err := generate(root, "", pkg, settings{LibraryCompileData: []string{"resources/*"}})
	if err != nil {
		t.Fatal(err)
	}
	contribution := Contribution{}
	for _, target := range []string{"lib", "tests/unit/lib", "tests/bench/lib", "lib/cdylib", "lib/staticlib"} {
		contribution.Inputs = append(contribution.Inputs, TargetInputs{
			Target: target, CompileData: []string{":manifest_resources", "Cargo.toml"}, Data: []string{":runtime_resources"},
		})
	}
	rules, err = applyContributions(rules, []Contribution{contribution})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if r.Kind() == "alias" {
			continue
		}
		for attr, label := range map[string]string{"compile_data": ":manifest_resources", "data": ":runtime_resources"} {
			if got := strings.Count(bzl.FormatString(r.Attr(attr)), `"`+label+`"`); got != 1 {
				t.Fatalf("The target %s declares %s %d times in %s.", r.Name(), label, got, attr)
			}
		}
		compileData := bzl.FormatString(r.Attr("compile_data"))
		if strings.Count(compileData, `":Cargo.toml"`) != 1 || strings.Contains(compileData, `"Cargo.toml"`) {
			t.Fatalf("The target %s repeats its manifest input: %s", r.Name(), compileData)
		}
		if !strings.Contains(compileData, `glob(["resources/*"])`) {
			t.Fatalf("The target %s lost its configured resource glob.", r.Name())
		}
	}
}

func put(t *testing.T, root, name, text string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rendered(rules []*rule.Rule) string {
	file := rule.EmptyFile("BUILD.bazel", "crates/probe")
	for _, r := range rules {
		r.Insert(file)
	}
	return string(file.Format())
}

func TestConventionalCrate(t *testing.T) {
	root := t.TempDir()
	rel := "crates/probe"
	put(t, root, "Cargo.toml", "[workspace.lints.rust]\nunsafe_code = 'forbid'\n[workspace.lints.clippy]\nall = {level='warn', priority=-1}\nunwrap_used = 'deny'\n")
	put(t, root, rel+"/Cargo.toml", "[lints]\nworkspace = true\n")
	for _, name := range []string{"src/lib.rs", "src/helper.rs", "build.rs", "tests/roundtrip.rs"} {
		put(t, root, rel+"/"+name, "")
	}
	pkg := cargoPackage{Name: "renamed-package", Version: "1.2.3", Manifest: filepath.Join(root, rel, "Cargo.toml"), Features: []string{"default", "syntax"}}
	for _, target := range []struct {
		kind, file string
		test       bool
	}{{"lib", "src/lib.rs", true}, {"test", "tests/roundtrip.rs", true}, {"custom-build", "build.rs", false}} {
		pkg.Targets = append(pkg.Targets, cargoTarget{Name: strings.TrimSuffix(filepath.Base(target.file), ".rs"), Kind: []string{target.kind}, Source: filepath.Join(root, rel, target.file), Test: target.test})
	}
	rules, err := generate(root, rel, pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	output := rendered(rules)
	for _, want := range []string{
		"crate_name = crate_name()", "edition = edition()", "lint_config = lint_config()", "aliases = aliases()",
		"all_crate_deps(normal = True) + [\":build-script\"]",
		"normal_dev = True", "all_crate_deps(build = True)",
		"tests/integration/roundtrip", `srcs = ["tests/roundtrip.rs"]`, "src/**/*.rs",
		"--forbid=unsafe_code", "--warn=clippy::all", "--deny=clippy::unwrap_used", "syntax",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("Missing %q in:\n%s", want, output)
		}
	}
	for _, r := range rules {
		if r.Attr("visibility") != nil {
			t.Error("Generated rules must use the package default visibility.")
		}
		if r.Kind() == "cargo_build_script" && r.Attr("lint_config") != nil {
			t.Error("Build scripts cannot accept lint_config.")
		}
		if r.Name() == "tests/integration/roundtrip" && !strings.Contains(rendered([]*rule.Rule{r}), ":build-script") {
			t.Error("Integration tests need the build-script environment.")
		}
	}
	if err := os.Remove(filepath.Join(root, rel, "src/helper.rs")); err != nil {
		t.Fatal(err)
	}
	changed, err := generate(root, rel, pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	if rendered(changed) != output {
		t.Error("Library source changes must not change the generated glob.")
	}
}

func TestScope(t *testing.T) {
	for _, rel := range []string{"fixtures/language-layer", "other/crates/core", "crates/probe/src", "crates"} {
		if inScope("crates", rel) {
			t.Errorf("Unexpected scope: %s", rel)
		}
	}
	if !inScope("crates", "crates/new-crate") {
		t.Error("A new crate must be in scope.")
	}
}

func TestIntegrationTargetIdentity(t *testing.T) {
	root := t.TempDir()
	rel := "crates/probe"
	pkg := cargoPackage{
		Name: "cargo-package", Version: "1.2.3", Description: "This package tests target identity.",
		Manifest: filepath.Join(root, rel, "Cargo.toml"),
		Targets: []cargoTarget{
			{Name: "renamed-test", Kind: []string{"test"}, Source: filepath.Join(root, rel, "tests/check.rs")},
			{Name: "other", Kind: []string{"test"}, Source: filepath.Join(root, rel, "tests/other.rs")},
		},
	}
	rules, err := generate(root, rel, pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	for i, target := range pkg.Targets {
		r := rules[i]
		name, err := unitName(pkg, cargoUnit{Target: target, Mode: "test"})
		if err != nil || name != r.Name() || name != "tests/integration/"+target.Name {
			t.Fatalf("Test and context names differ: %s, %s, %v", name, r.Name(), err)
		}
		if got := r.AttrStrings("srcs"); len(got) != 1 || got[0] != "tests/"+filepath.Base(target.Source) {
			t.Fatal("The test must list its crate root.")
		}
		if r.AttrString("crate_root") != "tests/"+filepath.Base(target.Source) {
			t.Fatal("The test root changed.")
		}

		if r.AttrString("crate_name") != strings.ReplaceAll(target.Name, "-", "_") {
			t.Fatal("The Rust crate name must follow the Cargo target name.")
		}
		if !strings.Contains(rendered([]*rule.Rule{r}), `"CARGO_PKG_NAME": "cargo-package"`) {
			t.Fatal("The test must retain the Cargo package name.")
		}
		if !strings.Contains(rendered([]*rule.Rule{r}), `"CARGO_PKG_DESCRIPTION": "This package tests target identity."`) {
			t.Fatal("The test must retain the Cargo package description.")
		}
	}
	if rules[2].Name() != "tests/integration" || len(rules[2].AttrStrings("tests")) != 2 {
		t.Fatal("The suite must contain both renamed test targets.")
	}
}

func TestAliasDependencyKinds(t *testing.T) {
	root := t.TempDir()
	pkg := cargoPackage{WorkspaceRoot: root, Dependencies: []cargoDependency{
		{Path: filepath.Join(root, "crates/normal")},
		{Path: filepath.Join(root, "crates/shared")},
		{Path: filepath.Join(root, "crates/shared"), Kind: "dev"},
		{Path: filepath.Join(root, "crates/support"), Kind: "dev"},
		{Path: filepath.Join(root, "crates/build"), Kind: "build"},
	}}
	for _, test := range []struct {
		kinds    []string
		excluded []string
		included []string
	}{
		{[]string{""}, []string{"build", "support"}, []string{"normal", "shared"}},
		{[]string{"", "dev"}, []string{"build"}, []string{"normal", "shared", "support"}},
		{[]string{"build"}, []string{"normal", "shared", "support"}, []string{"build"}},
		{nil, []string{"normal", "shared", "support", "build"}, nil},
	} {
		r := newRule("rust_library", "probe")
		setAttr(r, "aliases", targetAliases(pkg, test.kinds...))
		output := rendered([]*rule.Rule{r})
		for _, name := range test.excluded {
			if !strings.Contains(output, "//crates/"+name) {
				t.Errorf("Missing alias exclusion %s for kinds %v", name, test.kinds)
			}
		}
		for _, name := range test.included {
			if strings.Contains(output, "//crates/"+name) {
				t.Errorf("Excluded active alias %s for kinds %v", name, test.kinds)
			}
		}
	}
}

func TestCargoHarnessOptions(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Cargo.toml", `[lib]
harness = false
[[bin]]
name = "tool"
harness = false
[[test]]
name = "custom"
harness = false
[[test]]
name = "standard"
harness = true
`)
	pkg := cargoPackage{Manifest: filepath.Join(root, "Cargo.toml"), Targets: []cargoTarget{
		{Name: "lib", Kind: []string{"lib"}},
		{Name: "tool", Kind: []string{"bin"}},
		{Name: "custom", Kind: []string{"test"}},
		{Name: "standard", Kind: []string{"test"}},
		{Name: "automatic", Kind: []string{"test"}},
	}}
	if err := readHarnesses(&pkg); err != nil {
		t.Fatal(err)
	}
	for i, target := range pkg.Targets {
		r := newRule("rust_test", target.Name)
		setHarness(r, target)
		if (r.Attr("use_libtest_harness") != nil) != (i < 3) {
			t.Fatalf("Incorrect test harness for %s", target.Name)
		}
	}
}

func TestBinaryRuntimeInputs(t *testing.T) {
	root := t.TempDir()
	rel := "crates/probe"
	pkg := cargoPackage{Name: "probe", Description: "This package tests binary inputs.", Targets: []cargoTarget{
		{Name: "probe", Kind: []string{"bin"}, Source: filepath.Join(root, rel, "src/main.rs")},
		{Name: "check", Kind: []string{"test"}, Source: filepath.Join(root, rel, "tests/check.rs")},
	}}
	for _, test := range []struct {
		name string
		opts settings
		data string
	}{
		{"default", settings{}, `data = [":bin/probe"]`},
		{"file", settings{TestData: []string{"fixture.txt"}}, `data = ["fixture.txt"] + [":bin/probe"]`},
		{"glob", settings{TestData: []string{"fixtures/*"}}, `data = glob(["fixtures/*"]) + [":bin/probe"]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules, err := generate(root, rel, pkg, test.opts)
			if err != nil {
				t.Fatal(err)
			}
			output := rendered(rules)
			for _, want := range []string{"CARGO_BIN_EXE_probe", "$(rootpath :bin/probe)", `"CARGO_PKG_DESCRIPTION": "This package tests binary inputs."`, test.data} {
				if !strings.Contains(output, want) {
					t.Errorf("Missing binary runtime input %q in:\n%s", want, output)
				}
			}
		})
	}
}

func TestBinaryUnitTests(t *testing.T) {
	root := t.TempDir()
	rel := "crates/probe"
	put(t, root, rel+"/src/bin/tool.rs", "")
	pkg := cargoPackage{Name: "probe", Targets: []cargoTarget{{
		Name: "tool", Kind: []string{"bin"}, Source: filepath.Join(root, rel, "src/bin/tool.rs"), Test: true,
	}}}
	rules, err := generate(root, rel, pkg, settings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[1].AttrString("crate") != ":bin/tool" {
		t.Fatal("The binary needs a linked unit test target.")
	}
	if _, ok := rule.ParseGlobExpr(rules[0].Attr("srcs")); !ok {
		t.Fatal("A conventional binary must use a source glob.")
	}
}

func TestTargetEdition(t *testing.T) {
	for _, kind := range []string{"lib", "proc-macro", "bin", "test", "custom-build"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			rel := "crates/probe"
			put(t, root, "Cargo.toml", "[workspace]\n")
			put(t, root, rel+"/Cargo.toml", "[package]\nname = 'probe'\n")
			pkg := cargoPackage{
				Name: "probe", Edition: "2024", Manifest: filepath.Join(root, rel, "Cargo.toml"),
				Targets: []cargoTarget{{
					Name: "probe", Kind: []string{kind},
					Source: filepath.Join(root, rel, "src/lib.rs"), Edition: "2021", Test: true,
				}},
			}
			rules, err := generate(root, rel, pkg, settings{})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rules {
				if r.Kind() != "test_suite" && r.Kind() != "alias" && r.AttrString("edition") != "2021" {
					t.Errorf("Target %s lost its Cargo edition override", r.Name())
				}
			}
		})
	}
}

func TestLintOverrideAndPriority(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Cargo.toml", "[workspace.lints.rust]\nunsafe_code = 'forbid'\n")
	put(t, root, "crate.toml", "[lints.rust]\nunused = {level='deny', priority=-1}\nunused_imports = 'allow'\nunexpected_cfgs = {level='deny', check-cfg=['cfg(custom)']}\n")
	flags, err := buildLints(filepath.Join(root, "Cargo.toml"), filepath.Join(root, "crate.toml"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(flags, " ")
	if strings.Contains(joined, "unsafe_code") {
		t.Fatal("Package lints must not inherit workspace lints implicitly.")
	}
	if !strings.HasPrefix(joined, "--deny=unused ") || !strings.Contains(joined, "--check-cfg=cfg(custom)") {
		t.Fatalf("Unexpected flags: %s", joined)
	}
}

func TestExpressionMerge(t *testing.T) {
	file, err := rule.LoadData("BUILD.bazel", "crates/probe", []byte(`rust_library(
    name = "probe",
    srcs = ["src/lib.rs"],
    deps = all_crate_deps(normal = True),
    compile_data = [],
    visibility = ["//:workspace_internal"],
)`))
	if err != nil {
		t.Fatal(err)
	}
	fresh := newRule("rust_library", "probe")
	setAttr(fresh, "srcs", librarySources("src/lib.rs"))
	setAttr(fresh, "deps", deps([]string{"normal"}, ":build-script"))
	setAttr(fresh, "compile_data", inputs([]string{"resources/*", "//schemas:all"}))
	mergeable := kindMap()["rust_library"].MergeableAttrs
	rule.MergeRules(fresh, file.Rules[0], mergeable, "BUILD.bazel")
	first := string(file.Format())
	for _, want := range []string{"all_crate_deps(normal = True) + [\":build-script\"]", "glob([\"resources/*\"]) + [\"//schemas:all\"]", "src/**/*.rs", "src/main.rs"} {
		if !strings.Contains(first, want) {
			t.Fatalf("Missing %q after merge:\n%s", want, first)
		}
	}
	if file.Rules[0].Attr("visibility") != nil {
		t.Fatal("Merge must remove the old generated visibility.")
	}
	rule.MergeRules(fresh, file.Rules[0], mergeable, "BUILD.bazel")
	if string(file.Format()) != first {
		t.Fatal("Repeated expression merge changed the output.")
	}
	removed := rule.NewRule("rust_library", "probe")
	for key := range mergeable {
		if file.Rules[0].Attr(key) != nil {
			removed.SetAttr(key, generatedExpr{})
		}
	}
	rule.MergeRules(removed, file.Rules[0], mergeable, "BUILD.bazel")
	for _, key := range []string{"srcs", "deps", "compile_data"} {
		if file.Rules[0].Attr(key) != nil {
			t.Errorf("Removed rule retains %s", key)
		}
	}
}

// Expression replacement must retain kept items without retaining stale items.
func TestOwnedExpressionItemKeeps(t *testing.T) {
	file, err := rule.LoadData("BUILD.bazel", "crates/probe", []byte(`rust_library(
    name = "lib",
    srcs = [
        "src/stale.rs",
        "src/manual.rs", # keep
    ],
    deps = all_crate_deps(normal = True) + [
        ":stale",
        ":manual", # keep
    ],
)`))
	if err != nil {
		t.Fatal(err)
	}
	fresh := newRule("rust_library", "lib")
	setAttr(fresh, "srcs", librarySources("src/lib.rs"))
	setAttr(fresh, "deps", deps([]string{"normal"}, ":fresh", ":manual"))
	rule.MergeRules(fresh, file.Rules[0], kindMap()["rust_library"].MergeableAttrs, "BUILD.bazel")
	first := string(file.Format())
	for _, stale := range []string{"src/stale.rs", ":stale"} {
		if strings.Contains(first, stale) {
			t.Fatalf("Expression replacement retained stale item %s:\n%s", stale, first)
		}
	}
	for _, kept := range []string{`"src/manual.rs",  # keep`, `":manual",  # keep`, `":fresh"`, `"src/**/*.rs"`} {
		if !strings.Contains(first, kept) {
			t.Fatalf("Expression replacement lost %s:\n%s", kept, first)
		}
	}
	if strings.Count(first, `":manual"`) != 1 {
		t.Fatalf("Expression replacement duplicated a kept item:\n%s", first)
	}
	rule.MergeRules(fresh, file.Rules[0], kindMap()["rust_library"].MergeableAttrs, "BUILD.bazel")
	if string(file.Format()) != first {
		t.Fatal("Repeated item-keep merge changed the output.")
	}
	removed := rule.NewRule("rust_library", "lib")
	for _, attr := range []string{"srcs", "deps"} {
		removed.SetAttr(attr, generatedExpr{})
	}
	rule.MergeRules(removed, file.Rules[0], kindMap()["rust_library"].MergeableAttrs, "BUILD.bazel")
	for attr, want := range map[string]string{"srcs": "src/manual.rs", "deps": ":manual"} {
		if got := file.Rules[0].AttrStrings(attr); !slices.Equal(got, []string{want}) {
			t.Fatalf("Removed %s items = %v, want [%s]", attr, got, want)
		}
	}
}

// Kept file inputs must occur once after generated globs expand.
// This catches duplicate Bazel labels and an empty glob after its last match moves to the kept list.
// Earlier keep tests checked comments and literal duplicates without checking glob overlap.
func TestKeptGlobInputs(t *testing.T) {
	for _, tc := range []struct {
		name, kept string
		fresh      func() bzl.Expr
	}{
		{"matching-file", "src/manual.rs", func() bzl.Expr { return librarySources("src/lib.rs") }},
		{"local-label", ":src/manual.rs", func() bzl.Expr { return librarySources("src/lib.rs") }},
		{"only-match", "src/lib.rs", func() bzl.Expr { return librarySources("src/lib.rs") }},
		{"literal-after-glob", ":src/main.rs", func() bzl.Expr { return librarySources("src/main.rs") }},
		{"literal-before-glob", ":src/main.rs", func() bzl.Expr {
			return &bzl.BinaryExpr{X: rule.ExprFromValue([]string{"src/main.rs"}), Op: "+", Y: librarySources("src/lib.rs")}
		}},
		{"external-label", "//other:manual.rs", func() bzl.Expr { return librarySources("src/lib.rs") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := rule.LoadData("BUILD.bazel", "crates/probe", []byte(fmt.Sprintf(`rust_library(
    name = "lib",
    srcs = [
        %q, # keep
    ],
)`, tc.kept)))
			if err != nil {
				t.Fatal(err)
			}
			fresh := newRule("rust_library", "lib")
			setAttr(fresh, "srcs", tc.fresh())
			original := bzl.FormatString(fresh.Attr("srcs"))
			mergeable := kindMap()["rust_library"].MergeableAttrs
			rule.MergeRules(fresh, file.Rules[0], mergeable, "BUILD.bazel")
			first := string(file.Format())
			if !strings.Contains(first, fmt.Sprintf("%q,  # keep", tc.kept)) {
				t.Fatalf("The kept file or its comment changed:\n%s", first)
			}
			if bzl.FormatString(fresh.Attr("srcs")) != original {
				t.Fatal("The merge changed the fresh expression.")
			}
			path := strings.TrimPrefix(tc.kept, ":")
			literals, globs := 0, 0
			var inspect func(bzl.Expr)
			inspect = func(expr bzl.Expr) {
				if glob, ok := rule.ParseGlobExpr(expr); ok {
					globs++
					if !slices.Equal(glob.Patterns, []string{"src/**/*.rs"}) || !slices.Contains(glob.Excludes, "src/main.rs") {
						t.Fatalf("The merge changed the source patterns or removed an exclusion: %+v", glob)
					}
					if !strings.HasPrefix(path, "//") && !slices.Contains(glob.Excludes, path) {
						t.Fatalf("The glob can repeat the kept file %s: %+v", path, glob)
					}
					if strings.HasPrefix(path, "//") && slices.Contains(glob.Excludes, path) {
						t.Fatal("A cross-package label must not become a glob pattern.")
					}
					if tc.name == "only-match" && !glob.AllowEmpty {
						t.Fatal("The remaining glob must permit an empty result.")
					}
					seen := map[string]bool{}
					for _, exclude := range glob.Excludes {
						if seen[exclude] {
							t.Fatalf("The glob repeats exclusion %q.", exclude)
						}
						seen[exclude] = true
					}
					return
				}
				switch expr := expr.(type) {
				case *bzl.BinaryExpr:
					inspect(expr.X)
					inspect(expr.Y)
				case *bzl.ListExpr:
					for _, item := range expr.List {
						if text, ok := item.(*bzl.StringExpr); ok && strings.TrimPrefix(text.Value, ":") == path {
							literals++
						}
					}
				}
			}
			inspect(file.Rules[0].Attr("srcs"))
			if globs != 1 || literals != 1 {
				t.Fatalf("The merged input has %d globs and %d copies of the kept file.", globs, literals)
			}
			rule.MergeRules(fresh, file.Rules[0], mergeable, "BUILD.bazel")
			if string(file.Format()) != first {
				t.Fatal("Repeated kept-file generation changed the output.")
			}
		})
	}
}

// Label normalization must not change command arguments.
// This catches accidental deduplication of arguments that differ by a leading colon.
// Input-label regressions do not cover attributes whose strings are not labels.
func TestKeptArgumentsRetainDistinctValues(t *testing.T) {
	file, err := rule.LoadData("BUILD.bazel", "probe", []byte(`rust_test(
    name = "tests/integration/args",
    args = [
        ":input", # keep
    ],
)`))
	if err != nil {
		t.Fatal(err)
	}
	fresh := newRule("rust_test", "tests/integration/args")
	setAttr(fresh, "args", rule.ExprFromValue([]string{"input"}))
	rule.MergeRules(fresh, file.Rules[0], kindMap()["rust_test"].MergeableAttrs, "BUILD.bazel")
	if got := file.Rules[0].AttrStrings("args"); !slices.Equal(got, []string{":input", "input"}) {
		t.Fatalf("The merge changed distinct command arguments: %v", got)
	}
}

// Kept conditional items must retain their original selection coverage.
func TestConditionalExpressionItemKeeps(t *testing.T) {
	file, err := rule.LoadData("BUILD.bazel", "crates/probe", []byte(`rust_library(
    name = "lib",
    deps = select({
        ":linux": [
            ":manual", # keep
            ":stale-linux",
        ],
        ":windows": [":stale-windows"],
    }),
)`))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGeneratedMerge(file.Rules[0].Attr("deps"), nil); err != nil {
		t.Fatal(err)
	}
	fresh := newRule("rust_library", "lib")
	setAttr(fresh, "deps", deps([]string{"normal"}, ":fresh"))
	rule.MergeRules(fresh, file.Rules[0], kindMap()["rust_library"].MergeableAttrs, "BUILD.bazel")
	first := string(file.Format())
	for _, want := range []string{`":linux": [`, `":manual",  # keep`, `":windows": []`, `":fresh"`} {
		if !strings.Contains(first, want) {
			t.Fatalf("Conditional item replacement lost %s:\n%s", want, first)
		}
	}
	if strings.Contains(first, ":stale") {
		t.Fatalf("Conditional item replacement retained stale items:\n%s", first)
	}
	rule.MergeRules(fresh, file.Rules[0], kindMap()["rust_library"].MergeableAttrs, "BUILD.bazel")
	if string(file.Format()) != first {
		t.Fatal("Repeated conditional item merge changed the output.")
	}
	conditional := newRule("rust_library", "lib")
	setAttr(conditional, "deps", expressionSelect(map[string]bzl.Expr{
		":linux":   rule.ExprFromValue([]string{":manual", ":fresh"}),
		":windows": rule.ExprFromValue([]string{}),
	}))
	rule.MergeRules(conditional, file.Rules[0], kindMap()["rust_library"].MergeableAttrs, "BUILD.bazel")
	matched := string(file.Format())
	if strings.Count(matched, `":manual"`) != 1 || strings.Count(matched, "select(") != 1 || !strings.Contains(matched, `":manual",  # keep`) {
		t.Fatalf("Matching conditional items must retain one kept value:\n%s", matched)
	}
	rule.MergeRules(conditional, file.Rules[0], kindMap()["rust_library"].MergeableAttrs, "BUILD.bazel")
	if string(file.Format()) != matched {
		t.Fatal("Repeated matching conditional merge changed the output.")
	}
}

// Keep dependency labels unique when generated selection branches change.
// This catches duplicate selected dependencies and keep comments that lose their condition.
// Existing conditional keep tests use identical branch keys without overlapping unconditional dependencies.
func TestKeptDependencySelectionChanges(t *testing.T) {
	for _, tc := range []struct {
		name, generated string
		selected        map[string][]string
		fallback        []string
		defaultKeep     bool
	}{
		{
			name: "added-branch", generated: `select({":linux": [":manual", ":fresh"], ":windows": [":fresh"], ":mac": [":manual"], "//conditions:default": []})`,
			selected: map[string][]string{":linux": {":fresh", ":manual"}, ":windows": {":fresh"}, ":mac": {":manual"}},
		},
		{
			name: "removed-empty-branch", generated: `select({":linux": [":manual", ":fresh"], "//conditions:default": []})`,
			selected: map[string][]string{":linux": {":fresh", ":manual"}},
		},
		{
			name: "default-keep", generated: `select({":linux": [":fresh"], ":windows": [":fresh"], ":mac": [":manual", ":fresh"], "//conditions:default": []})`,
			selected: map[string][]string{":linux": {":fresh"}, ":windows": {":fresh"}, ":mac": {":fresh", ":manual"}},
			fallback: []string{":manual"}, defaultKeep: true,
		},
		{name: "unconditional", generated: `[":manual", ":fresh"]`, fallback: []string{":fresh", ":manual"}},
		{name: "local-label-spelling", generated: `["manual", ":fresh"]`, fallback: []string{":fresh", ":manual"}},
		{name: "helper-and-unconditional", generated: `all_crate_deps(normal = True) + [":manual", ":fresh"]`, fallback: []string{":fresh", ":manual"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			linux, fallback := `[":manual", # keep
]`, `[]`
			if tc.defaultKeep {
				linux, fallback = fallback, linux
			}
			file, err := rule.LoadData("BUILD.bazel", "crates/probe", []byte(fmt.Sprintf(marker+`
rust_library(
    name = "lib",
    deps = select({
        ":linux": %s,
        ":windows": [],
        "//conditions:default": %s,
    }),
)
`, linux, fallback)))
			if err != nil {
				t.Fatal(err)
			}
			source, err := rule.LoadData("BUILD.bazel", "crates/probe", []byte(`rust_library(name = "lib", deps = `+tc.generated+`)`))
			if err != nil {
				t.Fatal(err)
			}
			fresh := newRule("rust_library", "lib")
			setAttr(fresh, "deps", source.Rules[0].Attr("deps"))
			original := bzl.FormatString(fresh.Attr("deps"))
			if _, err := reconcile(file, []*rule.Rule{fresh}); err != nil {
				t.Fatal(err)
			}
			mergeable := kindMap()["rust_library"].MergeableAttrs
			check := func() {
				t.Helper()
				for _, condition := range []string{":linux", ":windows", ":mac", "//conditions:default"} {
					var labels []string
					var kept []string
					var inspect func(bzl.Expr)
					inspect = func(expr bzl.Expr) {
						switch expr := expr.(type) {
						case *bzl.ListExpr:
							for _, item := range expr.List {
								text := item.(*bzl.StringExpr)
								labels = append(labels, ":"+strings.TrimPrefix(text.Value, ":"))
								if rule.ShouldKeep(text) {
									kept = append(kept, ":"+strings.TrimPrefix(text.Value, ":"))
								}
							}
						case *bzl.BinaryExpr:
							inspect(expr.X)
							inspect(expr.Y)
						case *bzl.CallExpr:
							if expr.X.(*bzl.Ident).Name != "select" {
								return
							}
							var fallback bzl.Expr
							for _, branch := range expr.List[0].(*bzl.DictExpr).List {
								key := branch.Key.(*bzl.StringExpr).Value
								if key == condition {
									inspect(branch.Value)
									return
								}
								if key == "//conditions:default" {
									fallback = branch.Value
								}
							}
							if fallback != nil {
								inspect(fallback)
							}
						}
					}
					inspect(file.Rules[0].Attr("deps"))
					slices.Sort(labels)
					if len(slices.Compact(slices.Clone(labels))) != len(labels) {
						t.Fatalf("The %s dependencies contain duplicates: %v\n%s", condition, labels, file.Format())
					}
					wantKeep := condition == ":linux"
					if tc.defaultKeep {
						wantKeep = condition != ":linux" && condition != ":windows"
					}
					if wantKeep != slices.Contains(kept, ":manual") {
						t.Fatalf("The kept dependency changed its condition on %s: %v", condition, kept)
					}
					want := tc.fallback
					if selected, ok := tc.selected[condition]; ok {
						want = selected
					}
					if !slices.Equal(labels, want) {
						t.Fatalf("The %s dependencies are %v, want %v", condition, labels, want)
					}
				}
			}
			rule.MergeRules(fresh, file.Rules[0], mergeable, "BUILD.bazel")
			check()
			first := string(file.Format())
			rule.MergeRules(fresh, file.Rules[0], mergeable, "BUILD.bazel")
			check()
			if string(file.Format()) != first {
				t.Fatal("Repeated dependency generation changed the output.")
			}
			if bzl.FormatString(fresh.Attr("deps")) != original {
				t.Fatal("The merge changed the generated dependency expression.")
			}
		})
	}
}

func TestCompactTargetSources(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Cargo.toml", "[package]\nname = 'probe'\n")
	for _, tc := range []struct{ kind, file string }{
		{"custom-build", "build.rs"},
		{"test", "tests/first.rs"},
		{"example", "examples/demo.rs"},
		{"bench", "benches/speed.rs"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			put(t, root, tc.file, "")
			put(t, root, filepath.Join(filepath.Dir(tc.file), "unrelated.rs"), "")
			pkg := cargoPackage{Name: "probe", Manifest: filepath.Join(root, "Cargo.toml"), Targets: []cargoTarget{{Name: "probe", Kind: []string{tc.kind}, Source: filepath.Join(root, tc.file)}}}
			rules, err := generate(root, "", pkg, settings{})
			if err != nil {
				t.Fatal(err)
			}
			got := rules[0].AttrStrings("srcs")
			if len(got) != 1 || got[0] != tc.file {
				t.Fatalf("Source inputs = %v, want [%s]", got, tc.file)
			}
		})
	}
}

func TestKeptSourceOverride(t *testing.T) {
	file, err := rule.LoadData("BUILD.bazel", "crates/probe", []byte(`rust_test(
    name = "tests/integration/check",
    srcs = ["tests/check.rs", "tests/shared/mod.rs"], # keep
)`))
	if err != nil {
		t.Fatal(err)
	}
	fresh := newRule("rust_test", "tests/integration/check")
	setAttr(fresh, "srcs", []string{"tests/check.rs"})
	rule.MergeRules(fresh, file.Rules[0], kindMap()["rust_test"].MergeableAttrs, "BUILD.bazel")
	if got := file.Rules[0].AttrStrings("srcs"); len(got) != 2 || got[1] != "tests/shared/mod.rs" {
		t.Fatalf("The kept shared module must remain: %v", got)
	}
}
