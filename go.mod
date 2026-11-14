module github.com/scalar0/gazelle_rules_rs

go 1.26.0

toolchain go1.26.7

require (
	github.com/bazel-contrib/bazel-gazelle/v2 v2.0.0-beta.2
	github.com/bazelbuild/buildtools v0.0.0-20260904073137-eaa4d125b423
	github.com/bazelbuild/rules_go v0.65.0
	github.com/pelletier/go-toml/v2 v2.4.3
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
)

require (
	// Match the legacy API to the Gazelle Bazel module.
	github.com/bazelbuild/bazel-gazelle v1.0.0-beta.1.0.20261008232422-160271019d4c // indirect
	github.com/bmatcuk/doublestar/v4 v4.10.4 // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/tools/go/vcs v0.1.0-deprecated // indirect
)
