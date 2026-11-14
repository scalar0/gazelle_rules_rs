package rulesrs_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

func TestExcludedPackageRetainsCargoDrivenBuildRules(t *testing.T) {
	resolve := func(name string) string {
		t.Helper()
		path, err := runfiles.Rlocation(name)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	cargo := resolve(*cargoPath)
	runner := resolve(*runnerPath)
	rustc := resolve(strings.TrimSpace(read(resolve(*rustcPath))))
	sysroot := filepath.Dir(resolve(strings.TrimSpace(read(resolve(*sysrootPath)))))
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(tool string, args ...string) {
		t.Helper()
		command := exec.CommandContext(t.Context(), tool, args...)
		command.Dir = root
		command.Env = append(os.Environ(),
			"BUILD_WORKSPACE_DIRECTORY="+root,
			"BUILD_WORKING_DIRECTORY="+root,
			"CARGO_HOME="+filepath.Join(root, "cargo-home"),
			"CARGO_NET_OFFLINE=true",
			"RUSTC="+rustc,
			"RUSTFLAGS=--sysroot="+sysroot,
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", tool, args, err, output)
		}
	}
	const nestedBuild = "load(\"@third_party_crates//:defs.bzl\", \"aliases\")\nfilegroup(name=\"nested\",srcs=[])\n"
	write("crates/model/fixtures/BUILD.bazel", nestedBuild)
	workspace := "[workspace]\nmembers = ['crates/*']\nresolver = '3'\n"
	write("MODULE.bazel", "module(name='standalone_fixture')\n")
	write("BUILD.bazel", "")
	write("Cargo.toml", workspace)
	if err := configureFixture(root, "Cargo.toml"); err != nil {
		t.Fatal(err)
	}
	write("crates/runtime/Cargo.toml", "[package]\nname='runtime'\nversion='0.1.0'\nedition='2024'\n")
	write("crates/runtime/src/lib.rs", "pub struct Handle;\n")
	// Protect package-relative build inputs when a member becomes a standalone package.
	// This catches labels resolved from the Cargo workspace instead of the Bazel repository.
	// Existing standalone checks have no build inputs with relative labels.
	write("crates/model/Cargo.toml", "[package]\nname='independent-model'\nversion='0.1.0'\nedition='2024'\n[lib]\nname='independent_model'\n[dependencies]\nrenamed_runtime={package='runtime',version='0.1.0',path='../runtime'}\n[package.metadata.rules_rs.build_inputs.CARGO_MANIFEST_PATH]\ncargo_path='crates/model/Cargo.toml'\nbazel_label=':Cargo.toml'\n")
	write("crates/model/build.rs", "fn main() {}\n")
	write("crates/model/src/lib.rs", "pub use renamed_runtime::Handle;\n")
	run(cargo, "generate-lockfile", "--offline")
	generate := func(mode string) {
		run(runner, "-repo_root="+root, "-index=false", "-r=true", "-rules_rs_crate_scope=crates", "-rules_rs_config="+filepath.Join(root, "backend.json"), "-mode="+mode, "crates")
	}
	generate("fix")
	build := filepath.Join(root, "crates/model/BUILD.bazel")
	if !strings.Contains(read(build), "all_crate_deps(") {
		t.Fatal("The workspace mode must use the normal dependency helpers.")
	}
	write("Cargo.toml", workspace+"exclude = ['crates/model']\n")
	run(cargo, "generate-lockfile", "--offline")
	run(cargo, "generate-lockfile", "--offline", "--manifest-path", filepath.Join(root, "crates/model/Cargo.toml"))
	generate("fix")
	standalone := read(build)
	for _, want := range []string{`name = "lib"`, `crate_name = "independent_model"`, "//crates/runtime:lib", "renamed_runtime", "$(execpath //crates/model:Cargo.toml)"} {
		if !strings.Contains(standalone, want) {
			t.Fatalf("The standalone rules lost %q:\n%s", want, standalone)
		}
	}
	if strings.Contains(standalone, "all_crate_deps(") || strings.Contains(standalone, "lint_config(") {
		t.Fatalf("The standalone rules still depend on workspace lookups:\n%s", standalone)
	}
	generate("diff")
	if read(build) != standalone {
		t.Fatal("The standalone check changed the BUILD file.")
	}
	run(cargo, "check", "--offline", "--locked", "--manifest-path", filepath.Join(root, "crates/model/Cargo.toml"))
	write("Cargo.toml", workspace)
	run(cargo, "generate-lockfile", "--offline")
	generate("fix")
	if !strings.Contains(read(build), "all_crate_deps(") {
		t.Fatal("The workspace toggle did not restore the normal BUILD rules.")
	}
	write("Cargo.toml", workspace+"exclude = ['crates/runtime']\n")
	run(cargo, "generate-lockfile", "--offline")
	run(cargo, "generate-lockfile", "--offline", "--manifest-path", filepath.Join(root, "crates/runtime/Cargo.toml"))
	manifest := read(filepath.Join(root, "crates/model/Cargo.toml"))
	write("crates/model/Cargo.toml", manifest+"[package.metadata.rules_rs]\ndep_overrides=[{prefix='@third_party_crates//:runtime-0.1.0',target='//crates/runtime'}]\n")
	generate("fix")
	mixed := read(build)
	for _, want := range []string{`"//crates/runtime"`, `"@third_party_crates//:runtime-0.1.0"`} {
		if !strings.Contains(mixed, want) {
			t.Fatalf("The mixed workspace lost its local dependency override %q:\n%s", want, mixed)
		}
	}
	generate("diff")
	write("Cargo.toml", "[workspace]\nmembers = ['crates/runtime']\nexclude = ['crates/model']\nresolver = '3'\n")
	run(cargo, "generate-lockfile", "--offline")
	run(cargo, "generate-lockfile", "--offline", "--manifest-path", filepath.Join(root, "crates/model/Cargo.toml"))
	generate("fix")
	if !strings.Contains(read(build), "//crates/runtime:lib") || strings.Contains(read(build), "all_crate_deps(") {
		t.Fatalf("The explicit exclusion lost standalone package rules:\n%s", read(build))
	}
	generate("diff")
	write("crates/separate/Cargo.toml", "[workspace]\n[package]\nname='separate'\nversion='0.1.0'\nedition='2024'\n")
	write("crates/separate/src/lib.rs", "pub struct Separate;\n")
	write("crates/separate/BUILD.bazel", "filegroup(\n    name = \"separate\",\n    srcs = [\"src/lib.rs\"],\n)\n")
	write("Cargo.toml", "[workspace]\nmembers = ['crates/runtime']\nexclude = ['crates/model', 'crates/separate']\nresolver = '3'\n")
	run(cargo, "generate-lockfile", "--offline")
	generate("diff")
	if read(filepath.Join(root, "crates/model/fixtures/BUILD.bazel")) != nestedBuild {
		t.Fatal("Legacy scope generation changed a nested BUILD file.")
	}
}
