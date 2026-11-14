// Command consumer-test stages the shared Cargo fixtures for Bazel checks.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scalar0/gazelle_rules_rs/src/internal/testfixture"
)

func main() {
	owned := flag.String("owned-manifest", "gazelle_rules_rs/testdata/workspace/Cargo.toml", "Owned fixture manifest runfile")
	upstream := flag.String("upstream-manifest", "rules_rs_manifest_fixture/Cargo.toml", "Upstream fixture manifest runfile")
	configuration := flag.String("configuration", "gazelle_rules_rs/tests/consumer/MODULE.bazel.in", "Consumer configuration runfile")
	backendConfig := flag.String("backend-config", "gazelle_rules_rs/testdata/workspace/backend.json", "Shared backend configuration runfile")
	handler := flag.String("handler", "gazelle_rules_rs/src/integration/metadata/handler.go", "Consumer metadata handler runfile")
	handlerSchema := flag.String("handler-schema", "gazelle_rules_rs/src/integration/metadata/resources.schema.json", "Consumer metadata schema runfile")
	vendor := flag.String("vendor-manifest", "fixture_itoa/Cargo.toml", "Offline registry crate manifest runfile")
	fixture := flag.String("fixture", "all", "Fixture: owned, upstream, or all")
	destination := flag.String("destination", "", "Empty destination directory")
	extensionRoot := flag.String("extension-root", "", "Absolute extension checkout path")
	flag.Parse()
	fail := func(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	if *destination == "" || !filepath.IsAbs(*extensionRoot) {
		fail(fmt.Errorf("set --destination and an absolute --extension-root"))
	}
	if err := testfixture.RequireEmpty(*destination); err != nil {
		fail(err)
	}
	fixtures := []struct{ name, manifest string }{{"owned", *owned}, {"upstream", *upstream}}
	found := false
	for _, item := range fixtures {
		if *fixture != "all" && *fixture != item.name {
			continue
		}
		found = true
		target := *destination
		if *fixture == "all" {
			target = filepath.Join(target, item.name)
		}
		consumer := testfixture.Consumer{
			Kind: item.name, Manifest: item.manifest,
			Configuration: *configuration, BackendConfig: *backendConfig,
			Handler: *handler, HandlerSchema: *handlerSchema,
			VendorManifest: *vendor, ExtensionRoot: *extensionRoot,
		}
		if err := consumer.Stage(target); err != nil {
			fail(err)
		}
		fmt.Println(target)
	}
	if !found {
		fail(fmt.Errorf("unknown fixture: %s", *fixture))
	}
}
