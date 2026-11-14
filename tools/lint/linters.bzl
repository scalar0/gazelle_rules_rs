"""Lint the languages used by the extension's development tools."""

load("@rules_lint//lint:buildifier.bzl", "lint_buildifier_aspect")
load("@rules_lint//lint:golangci_lint.bzl", "lint_golangci_lint_aspect")
load("@rules_lint//lint:lint_test.bzl", "lint_test")
load("@rules_lint//lint:shellcheck.bzl", "lint_shellcheck_aspect")

buildifier = lint_buildifier_aspect(
    binary = Label("@buildifier_prebuilt//:buildifier"),
    args = ["-mode=check", "-lint=warn"],
)

shellcheck = lint_shellcheck_aspect(
    binary = Label("@rules_lint//lint:shellcheck_bin"),
    config = Label("//:.shellcheckrc"),
)

golangci = lint_golangci_lint_aspect(
    binary = Label("@rules_lint//lint:golangci_lint_bin"),
    config = Label("//:.golangci.yml"),
)

golangci_test = lint_test(aspect = golangci)
