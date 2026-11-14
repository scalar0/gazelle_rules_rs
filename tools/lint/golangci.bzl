"""Run the pinned Go linter with the selected Bazel Go SDK."""

def _golangci_release_impl(ctx):
    return [platform_common.ToolchainInfo(binary = ctx.file.binary)]

golangci_release = rule(
    implementation = _golangci_release_impl,
    attrs = {
        "binary": attr.label(allow_single_file = True, cfg = "exec", mandatory = True),
    },
)

def _runfile(ctx, file):
    if file.short_path.startswith("../"):
        return file.short_path[3:]
    return ctx.workspace_name + "/" + file.short_path

def _golangci_lint_impl(ctx):
    sdk = ctx.toolchains["@rules_go//go:toolchain"].sdk
    binary = ctx.toolchains["//tools/lint:golangci_toolchain_type"].binary
    launcher = ctx.actions.declare_file(ctx.label.name + ".bash")
    ctx.actions.expand_template(
        template = ctx.file._launcher,
        output = launcher,
        substitutions = {
            "@@BINARY@@": _runfile(ctx, binary),
            "@@CONFIG@@": _runfile(ctx, ctx.file.config),
            "@@SDK_ROOT@@": _runfile(ctx, sdk.root_file),
        },
        is_executable = True,
    )
    runfiles = ctx.runfiles(
        files = [binary, ctx.file.config, sdk.go, sdk.root_file],
        transitive_files = depset(transitive = [sdk.srcs, sdk.tools, sdk.headers, sdk.libs]),
    ).merge(ctx.attr._runfiles[DefaultInfo].default_runfiles)
    return [DefaultInfo(executable = launcher, runfiles = runfiles)]

golangci_lint = rule(
    implementation = _golangci_lint_impl,
    executable = True,
    attrs = {
        "config": attr.label(allow_single_file = True, mandatory = True),
        "_launcher": attr.label(allow_single_file = True, default = "//tools/lint:golangci.bash.tpl"),
        "_runfiles": attr.label(default = "@rules_shell//shell/runfiles"),
    },
    toolchains = ["@rules_go//go:toolchain", "//tools/lint:golangci_toolchain_type"],
)
