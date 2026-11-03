package rulesrs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	vendorBlockStart = "# BEGIN GENERATED rules_rs vendor inputs"
	vendorBlockEnd   = "# END GENERATED rules_rs vendor inputs"
)

func vendorInputs(data metadata) (map[string]buildInput, error) {
	result := map[string]buildInput{}
	owners := map[string]string{}
	for _, pkg := range data.Packages {
		if !slices.Contains(data.Members, pkg.ID) {
			continue
		}
		for name, input := range pkg.Settings.BuildInputs {
			if previous, ok := result[name]; ok && previous != input {
				return nil, fmt.Errorf("Conflicting build_inputs.%s declarations in %s and %s", name, owners[name], pkg.Manifest)
			}
			result[name], owners[name] = input, pkg.Manifest
		}
	}
	return result, nil
}

func renderVendorConfig(original []byte, inputs map[string]buildInput) ([]byte, error) {
	text := string(original)
	if strings.Count(text, vendorBlockStart) != 1 || strings.Count(text, vendorBlockEnd) != 1 {
		return nil, fmt.Errorf("The Cargo vendor block must have one start marker and one end marker")
	}
	start, end := strings.Index(text, vendorBlockStart), strings.Index(text, vendorBlockEnd)
	if start >= end || (start != 0 && text[start-1] != '\n') || (end != 0 && text[end-1] != '\n') {
		return nil, fmt.Errorf("The Cargo vendor block markers must use separate lines in the correct order")
	}
	prefix, suffix := text[:start], text[end+len(vendorBlockEnd):]
	afterStart := text[start+len(vendorBlockStart):]
	if !strings.HasPrefix(afterStart, "\n") && !strings.HasPrefix(afterStart, "\r\n") {
		return nil, fmt.Errorf("The Cargo vendor start marker must use a separate line")
	}
	if suffix != "" && suffix[0] != '\n' && !strings.HasPrefix(suffix, "\r\n") {
		return nil, fmt.Errorf("The Cargo vendor end marker must use a separate line")
	}
	newline := "\n"
	if strings.Contains(prefix, "\r\n") {
		newline = "\r\n"
	}
	// Check the enclosing table without changing unrelated TOML text.
	const probe = "__RULES_RS_VENDOR_BLOCK__"
	var location struct{ Env map[string]any }
	if err := toml.Unmarshal([]byte(prefix+probe+" = true"+newline), &location); err != nil {
		return nil, fmt.Errorf("Read the Cargo vendor block location: %w", err)
	}
	if location.Env[probe] != true {
		return nil, fmt.Errorf("The Cargo vendor block must be inside the env table")
	}
	var manual struct{ Env map[string]any }
	if err := toml.Unmarshal([]byte(prefix+suffix), &manual); err != nil {
		return nil, fmt.Errorf("Read unmanaged Cargo configuration: %w", err)
	}
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		if _, exists := manual.Env[name]; exists {
			return nil, fmt.Errorf("Manual Cargo env.%s conflicts with a generated vendor input", name)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	var block strings.Builder
	block.WriteString(vendorBlockStart + newline)
	for _, name := range names {
		fmt.Fprintf(&block, "%s = {relative = true, value = %s}%s", name, strconv.Quote(inputs[name].CargoPath), newline)
	}
	block.WriteString(vendorBlockEnd)
	result := []byte(prefix + block.String() + suffix)
	var parsed map[string]any
	if err := toml.Unmarshal(result, &parsed); err != nil {
		return nil, fmt.Errorf("Validate generated Cargo configuration: %w", err)
	}
	return result, nil
}

// SyncVendorConfig checks or updates Cargo vendor defaults from workspace metadata.
func SyncVendorConfig(root string, write bool) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	tool, err := pinnedCargo()
	if err != nil {
		return err
	}
	data, err := loadCargoMetadata(context.Background(), root, tool, true)
	if err != nil {
		return err
	}
	inputs, err := vendorInputs(data)
	if err != nil {
		return err
	}
	return syncVendorConfig(root, inputs, write)
}

func syncVendorConfig(root string, inputs map[string]buildInput, write bool) error {
	file := filepath.Join(root, ".cargo", "config.toml")
	original, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	result, err := renderVendorConfig(original, inputs)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if bytes.Equal(original, result) {
		return nil
	}
	if !write {
		return fmt.Errorf("The Cargo vendor block is stale: %s", file)
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(file), ".vendor-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(result); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), file)
}
