package rulesrs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vendorConfigFixture() string {
	return "[build]\n# Keep this comment.\nrustflags = [\"-C\", \"debuginfo=1\"]\n\n[env]\nMANUAL = \"keep\"\n" + vendorBlockStart + "\nOLD = \"stale\"\n" + vendorBlockEnd + "\n\n[alias]\ninspect = \"metadata\"\n"
}

func TestVendorConfigRendering(t *testing.T) {
	original := vendorConfigFixture()
	inputs := map[string]buildInput{
		"Z_INPUT": {CargoPath: "vendor/missing z.json", BazelLabel: "//:z"},
		"A_INPUT": {CargoPath: "vendor/missing-a.json", BazelLabel: "//:a"},
	}
	result, err := renderVendorConfig([]byte(original), inputs)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	start, end := strings.Index(original, vendorBlockStart), strings.Index(original, vendorBlockEnd)+len(vendorBlockEnd)
	if !strings.HasPrefix(text, original[:start]) || !strings.HasSuffix(text, original[end:]) {
		t.Fatal("Generation changed unmanaged Cargo configuration.")
	}
	if strings.Contains(text, "OLD =") || strings.Index(text, "A_INPUT =") > strings.Index(text, "Z_INPUT =") {
		t.Fatalf("Incorrect generated vendor entries: %s", text)
	}
	repeated, err := renderVendorConfig(result, inputs)
	if err != nil || !bytes.Equal(result, repeated) {
		t.Fatalf("Repeated generation changed the configuration: %v", err)
	}
	removed, err := renderVendorConfig(result, map[string]buildInput{})
	if err != nil || strings.Contains(string(removed), "_INPUT =") {
		t.Fatalf("Obsolete inputs remain: %s, %v", removed, err)
	}
	crlf := strings.ReplaceAll(original, "\n", "\r\n")
	converted, err := renderVendorConfig([]byte(crlf), inputs)
	if err != nil || strings.Contains(strings.ReplaceAll(string(converted), "\r\n", ""), "\n") {
		t.Fatalf("Generation changed the CRLF convention: %v", err)
	}
}

func TestInvalidVendorConfig(t *testing.T) {
	original := vendorConfigFixture()
	for _, text := range []string{
		strings.ReplaceAll(original, vendorBlockStart, ""),
		original + vendorBlockStart + "\n",
		strings.ReplaceAll(original, vendorBlockEnd, ""),
		strings.ReplaceAll(original, vendorBlockStart, vendorBlockStart+" extra"),
		strings.ReplaceAll(original, "[env]", "[other]"),
		strings.ReplaceAll(original, "MANUAL = \"keep\"", "INPUT = \"manual\""),
		strings.ReplaceAll(original, "MANUAL = \"keep\"", "MANUAL = ["),
		strings.ReplaceAll(original, vendorBlockStart, vendorBlockEnd+"\n"+vendorBlockStart),
	} {
		if _, err := renderVendorConfig([]byte(text), map[string]buildInput{"INPUT": {CargoPath: "vendor/input"}}); err == nil {
			t.Fatalf("Accepted invalid Cargo configuration: %s", text)
		}
	}
}

func TestVendorConfigCheckAndWrite(t *testing.T) {
	root := t.TempDir()
	put(t, root, ".cargo/config.toml", vendorConfigFixture())
	file := filepath.Join(root, ".cargo/config.toml")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string]buildInput{"INPUT": {CargoPath: "vendor/absent.json", BazelLabel: "//:input"}}
	if err := syncVendorConfig(root, inputs, false); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("Check mode must report stale entries: %v", err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("Check mode changed the configuration.")
	}
	if err := syncVendorConfig(root, inputs, true); err != nil {
		t.Fatal(err)
	}
	if err := syncVendorConfig(root, inputs, false); err != nil {
		t.Fatal(err)
	}
	put(t, root, ".cargo/config.toml", strings.ReplaceAll(string(before), "MANUAL", "INPUT"))
	invalid, _ := os.ReadFile(file)
	if err := syncVendorConfig(root, inputs, true); err == nil {
		t.Fatal("A manual conflict must fail before writing output.")
	}
	after, _ = os.ReadFile(file)
	if !bytes.Equal(invalid, after) {
		t.Fatal("Validation failure changed the configuration.")
	}
}

func TestVendorInputOwnership(t *testing.T) {
	input := buildInput{CargoPath: "vendor/input", BazelLabel: "//:input"}
	data := metadata{Members: []string{"first", "second"}, Packages: []cargoPackage{
		{ID: "first", Manifest: "/first/Cargo.toml", Settings: settings{BuildInputs: map[string]buildInput{"INPUT": input}}},
		{ID: "second", Manifest: "/second/Cargo.toml", Settings: settings{BuildInputs: map[string]buildInput{"INPUT": input}}},
		{ID: "dependency", Settings: settings{BuildInputs: map[string]buildInput{"IGNORED": input}}},
	}}
	inputs, err := vendorInputs(data)
	if err != nil || len(inputs) != 1 {
		t.Fatalf("Identical mappings must merge and external packages must be ignored: %v, %v", inputs, err)
	}
	data.Packages[1].Settings.BuildInputs["INPUT"] = buildInput{CargoPath: "vendor/other", BazelLabel: "//:other"}
	if _, err := vendorInputs(data); err == nil || !strings.Contains(err.Error(), "/first/Cargo.toml") || !strings.Contains(err.Error(), "/second/Cargo.toml") {
		t.Fatalf("Conflicting mappings must identify both owners: %v", err)
	}
}
