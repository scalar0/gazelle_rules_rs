package rulesrs

import (
	"strings"
	"testing"
)

func TestPlatformConfiguration(t *testing.T) {
	root := t.TempDir()
	put(t, root, "MODULE.bazel", `
other.from_cargo(name = "crates", platform_triples = ["wrong"])
renamed = use_extension("@rules_rs//rs:extensions.bzl", "crate")
renamed.from_cargo(name = "other", platform_triples = ["wrong"])
renamed.from_cargo(name = "crates", platform_triples = ["x86_64-unknown-linux-gnu", "aarch64-apple-darwin"])
`)
	platforms, err := readPlatforms(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(platforms, ",") != "aarch64-apple-darwin,x86_64-unknown-linux-gnu" {
		t.Fatalf("Wrong dependency platform configuration: %v", platforms)
	}
	for _, config := range []string{
		`renamed.from_cargo(name = "crates", platform_triples = platforms)`,
		`renamed.from_cargo(name = "crates", platform_triples = [])`,
		`renamed.from_cargo(name = "crates", platform_triples = [unknown])`,
		`renamed.from_cargo(name = "other", platform_triples = ["ignored"])`,
	} {
		put(t, root, "MODULE.bazel", `renamed = use_extension("@rules_rs//rs:extensions.bzl", "crate")`+"\n"+config)
		if _, err := readPlatforms(root); err == nil {
			t.Fatalf("Accepted an unknown platform configuration: %s", config)
		}
	}
}
