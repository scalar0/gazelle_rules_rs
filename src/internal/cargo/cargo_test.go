package cargo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCompilerManifest(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "rustc.runfile")
	if err := os.WriteFile(manifest, []byte(" toolchain/bin/rustc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var locations []string
	resolve := func(location string) (string, error) {
		locations = append(locations, location)
		switch location {
		case "toolchain/rustc.runfile":
			return manifest, nil
		case "toolchain/bin/rustc":
			return "/declared/rustc", nil
		default:
			return "", fmt.Errorf("unexpected runfile %q", location)
		}
	}
	tool, err := compiler("toolchain/rustc.runfile", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if tool != "/declared/rustc" || !reflect.DeepEqual(locations, []string{"toolchain/rustc.runfile", "toolchain/bin/rustc"}) {
		t.Fatalf("Incorrect compiler manifest resolution: %q, %v", tool, locations)
	}
	if err := os.WriteFile(manifest, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := compiler("toolchain/rustc.runfile", resolve); err == nil {
		t.Fatal("An empty compiler manifest must fail.")
	}
}

func TestMissingConfiguredTools(t *testing.T) {
	cargoRunfile, rustcRunfile := CargoRunfile, RustcRunfile
	t.Cleanup(func() { CargoRunfile, RustcRunfile = cargoRunfile, rustcRunfile })
	CargoRunfile, RustcRunfile = "", ""
	if _, err := PinnedCargo(); err == nil || !strings.Contains(err.Error(), "configure the Cargo runfile") {
		t.Fatalf("A missing Cargo runfile must fail: %v", err)
	}
	if _, err := Output(context.Background(), t.TempDir(), "", "metadata"); err == nil || !strings.Contains(err.Error(), "configure the Cargo executable") {
		t.Fatalf("A missing Cargo executable must fail: %v", err)
	}
	if _, err := Output(context.Background(), t.TempDir(), "/declared/cargo", "metadata"); err == nil || !strings.Contains(err.Error(), "configure the Rust compiler manifest") {
		t.Fatalf("A missing compiler manifest must fail: %v", err)
	}
}

type invocation struct {
	Args        []string
	Root        string
	Rustc       string
	Wrapper     bool
	WorkWrapper bool
	Retained    string
}

func helperCommand(t *testing.T, mode string) (string, []string) {
	t.Helper()
	tool, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RULES_RS_CARGO_HELPER", mode)
	return tool, []string{"-test.run=^TestCargoHelperProcess$", "--"}
}

func TestOutputInvocation(t *testing.T) {
	tool, args := helperCommand(t, "inspect")
	t.Setenv("RUSTC", "/ambient/rustc")
	t.Setenv("RUSTC_WRAPPER", "/ambient/wrapper")
	t.Setenv("RUSTC_WORKSPACE_WRAPPER", "/ambient/workspace-wrapper")
	t.Setenv("RULES_RS_RETAINED", "keep")
	root := t.TempDir()
	args = append(args, "metadata", "--no-deps")
	// Protect caller-owned arguments when Cargo adds its required flags.
	// This catches flag insertion that overwrites another slice of the same array.
	// Existing invocation checks do not expose writes beyond the argument slice.
	storage := make([]string, len(args)+3)
	copy(storage, args)
	storage[len(args)] = "caller-owned"
	args = storage[:len(args)]
	data, err := output(context.Background(), root, tool, "/declared/rustc", args...)
	if err != nil {
		t.Fatal(err)
	}
	if storage[len(args)] != "caller-owned" {
		t.Fatal("The Cargo adapter changed caller-owned arguments.")
	}
	var got invocation
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := invocation{
		Args:     []string{"metadata", "--no-deps", "--locked", "--manifest-path", filepath.Join(root, "Cargo.toml")},
		Root:     root,
		Rustc:    "/declared/rustc",
		Retained: "keep",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Incorrect Cargo invocation:\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestOutputFailure(t *testing.T) {
	tool, args := helperCommand(t, "fail")
	if _, err := output(context.Background(), t.TempDir(), tool, "/declared/rustc", args...); err == nil || !strings.Contains(err.Error(), "owned diagnostic") {
		t.Fatalf("A Cargo failure must include its diagnostic: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := output(ctx, t.TempDir(), tool, "/declared/rustc", args...); !errors.Is(err, context.Canceled) {
		t.Fatalf("A canceled Cargo invocation must return cancellation: %v", err)
	}
}

func TestCargoHelperProcess(t *testing.T) {
	switch os.Getenv("RULES_RS_CARGO_HELPER") {
	case "inspect":
		var args []string
		for i, arg := range os.Args {
			if arg == "--" {
				args = os.Args[i+1:]
				break
			}
		}
		root, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		_, wrapper := os.LookupEnv("RUSTC_WRAPPER")
		_, workWrapper := os.LookupEnv("RUSTC_WORKSPACE_WRAPPER")
		if err := json.NewEncoder(os.Stdout).Encode(invocation{
			Args: args, Root: root, Rustc: os.Getenv("RUSTC"), Wrapper: wrapper,
			WorkWrapper: workWrapper, Retained: os.Getenv("RULES_RS_RETAINED"),
		}); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "owned diagnostic")
		os.Exit(7)
	}
}
