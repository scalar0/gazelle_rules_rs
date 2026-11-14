package rulesrs

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bazel-contrib/bazel-gazelle/v2/rule"
)

func targetRole(r *rule.Rule) string {
	info, _ := r.PrivateAttr("cargo_target_info").(TargetInfo)
	return info.Role
}

func outputDescriptor(info TargetInfo, outputType string) TargetInfo {
	info.BazelTarget += "/" + outputType
	info.OutputType = outputType
	return info
}

func integrationSuiteDescriptor() TargetInfo {
	return TargetInfo{BazelTarget: "tests/integration", Role: "test-suite"}
}

func packageAliasDescriptor(pkg cargoPackage) TargetInfo {
	return TargetInfo{BazelTarget: packageBase(pkg), Role: "package-alias"}
}

// targetDescriptor gives generation and Cargo contexts the same target identity.
func targetDescriptor(pkg cargoPackage, target cargoTarget, mode string) (TargetInfo, error) {
	info := TargetInfo{Name: target.Name, Kind: targetKind(target), CrateRoot: target.Source}
	source, err := filepath.Rel(filepath.Dir(pkg.Manifest), target.Source)
	if err != nil || source == ".." || strings.HasPrefix(source, ".."+string(filepath.Separator)) {
		return info, fmt.Errorf("the Cargo target source is outside package %s", pkg.Name)
	}
	info.Role, info.OutputType = info.Kind, "bin"
	switch info.Kind {
	case "lib", "proc-macro":
		info.BazelTarget, info.Role, info.OutputType = "lib", "library", "rlib"
		if info.Kind == "proc-macro" {
			info.OutputType = "proc-macro"
		}
	case "bin":
		info.BazelTarget = "bin/" + target.Name
	case "test":
		info.BazelTarget, info.Role = "tests/integration/"+target.Name, "integration-test"
		return info, nil
	case "bench":
		info.BazelTarget, info.Role = "benches/"+target.Name, "benchmark"
		return info, nil
	case "example":
		info.BazelTarget = "examples/" + target.Name
		if len(target.CrateTypes) != 0 {
			info.OutputType = target.CrateTypes[0]
		}
	case "custom-build":
		info.BazelTarget, info.Role = "build-script", "build-script"
		return info, nil
	default:
		return info, fmt.Errorf("the Cargo target has unsupported kind %s", info.Kind)
	}
	switch mode {
	case "doctest":
		info.BazelTarget, info.Role = "tests/doc", "doc-test"
	case "test", "bench":
		prefix, role := "tests/unit", "unit-test"
		if mode == "bench" {
			prefix, role = "tests/bench", "benchmark-test"
		}
		prefix += "/" + info.BazelTarget
		info.BazelTarget, info.Role, info.OutputType = prefix, role, "bin"
	}
	return info, nil
}

func packageBase(pkg cargoPackage) string {
	base := filepath.Base(filepath.Dir(pkg.Manifest))
	if filepath.Dir(pkg.Manifest) == packageRepoRoot(pkg) {
		base = pkg.Name
	}
	return base
}

func targetEnabled(pkg cargoPackage, target cargoTarget) bool {
	for _, feature := range target.RequiredFeatures {
		found := false
		for _, enabled := range pkg.Features {
			found = found || feature == enabled
		}
		if !found {
			return false
		}
	}
	return true
}

// packageTargetInfos lists public targets and rejects ambiguous Bazel labels.
func packageTargetInfos(pkg cargoPackage) ([]TargetInfo, error) {
	var result []TargetInfo
	seen := map[string]bool{}
	add := func(info TargetInfo) error {
		if seen[info.BazelTarget] {
			return fmt.Errorf("generated target name %s has a collision", info.BazelTarget)
		}
		seen[info.BazelTarget] = true
		result = append(result, info)
		return nil
	}
	hasLib, hasTests := false, false
	for _, target := range pkg.Targets {
		if !targetEnabled(pkg, target) {
			continue
		}
		info, err := targetDescriptor(pkg, target, "build")
		if err != nil {
			return nil, err
		}
		if err := add(info); err != nil {
			return nil, err
		}
		hasLib = hasLib || info.Role == "library"
		hasTests = hasTests || info.Role == "integration-test"
		if info.Role != "library" && info.Kind != "bin" && info.Kind != "example" {
			continue
		}
		for _, mode := range []struct {
			name string
			use  bool
		}{{"test", target.Test}, {"bench", target.Bench && info.Kind != "example"}, {"doctest", target.Doctest && info.Role == "library"}} {
			if mode.use {
				derived, err := targetDescriptor(pkg, target, mode.name)
				if err != nil {
					return nil, err
				}
				if err := add(derived); err != nil {
					return nil, err
				}
			}
		}
		for index, typ := range target.CrateTypes {
			if info.Role == "library" && (typ == "lib" || typ == "rlib" || typ == "proc-macro") || info.Kind == "example" && index == 0 {
				continue
			}
			if info.Kind != "example" && info.Role != "library" {
				continue
			}
			output := outputDescriptor(info, typ)
			if err := add(output); err != nil {
				return nil, err
			}
		}
	}
	if hasTests {
		if err := add(integrationSuiteDescriptor()); err != nil {
			return nil, err
		}
	}
	if hasLib && packageBase(pkg) != "lib" {
		if err := add(packageAliasDescriptor(pkg)); err != nil {
			return nil, err
		}
	}
	return result, nil
}
