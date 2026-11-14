// Package cargo runs the declared Cargo and Rust compiler tools.
package cargo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

// Bazel sets these runfile paths through x_defs.
var (
	CargoRunfile string
	RustcRunfile string
)

// PinnedCargo finds the declared Cargo executable.
func PinnedCargo() (string, error) {
	if CargoRunfile == "" {
		return "", fmt.Errorf("configure the Cargo runfile")
	}
	tool, err := runfiles.Rlocation(CargoRunfile)
	if err != nil {
		return "", fmt.Errorf("find declared Cargo: %w", err)
	}
	return tool, nil
}

// Output runs Cargo with the declared compiler and the workspace lockfile.
func Output(ctx context.Context, root, tool string, args ...string) ([]byte, error) {
	if tool == "" {
		return nil, fmt.Errorf("configure the Cargo executable")
	}
	if RustcRunfile == "" {
		return nil, fmt.Errorf("configure the Rust compiler manifest runfile")
	}
	rustc, err := compiler(RustcRunfile, runfiles.Rlocation)
	if err != nil {
		return nil, err
	}
	return output(ctx, root, tool, rustc, args...)
}

func compiler(location string, resolve func(string) (string, error)) (string, error) {
	manifest, err := resolve(location)
	if err != nil {
		return "", fmt.Errorf("find declared Rust compiler manifest: %w", err)
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return "", fmt.Errorf("read declared Rust compiler manifest: %w", err)
	}
	location = strings.TrimSpace(string(data))
	if location == "" {
		return "", fmt.Errorf("the Rust compiler manifest must name a runfile")
	}
	rustc, err := resolve(location)
	if err != nil {
		return "", fmt.Errorf("find declared Rust compiler: %w", err)
	}
	return rustc, nil
}

func output(ctx context.Context, root, tool, rustc string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Args = append(cmd.Args, "--locked", "--manifest-path", filepath.Join(root, "Cargo.toml"))
	cmd.Dir = root
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "RUSTC" && key != "RUSTC_WRAPPER" && key != "RUSTC_WORKSPACE_WRAPPER" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "RUSTC="+rustc)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("run Cargo in %s (%s): %w\n%s", root, strings.Join(cmd.Args[1:], " "), err, stderr.String())
	}
	return data, nil
}
