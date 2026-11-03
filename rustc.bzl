"""Expose the registered Rust compiler and its runtime files to Gazelle."""

def _rustc_runfiles_impl(ctx):
    toolchain = ctx.toolchains["@rules_rust//rust:toolchain_type"]
    manifest = ctx.actions.declare_file(ctx.label.name + "_path")
    path = toolchain.rustc.short_path.removeprefix("../")
    ctx.actions.write(manifest, path + "\n")
    sysroot_manifest = ctx.actions.declare_file(ctx.label.name + "_sysroot")
    anchor = toolchain.sysroot_anchor.short_path
    anchor = anchor.removeprefix("../") if anchor.startswith("../") else "_main/" + anchor
    ctx.actions.write(sysroot_manifest, anchor + "\n")
    return [
        DefaultInfo(
            files = depset([manifest]),
            runfiles = ctx.runfiles(files = [sysroot_manifest], transitive_files = toolchain.all_files),
        ),
        OutputGroupInfo(sysroot = depset([sysroot_manifest])),
    ]

rustc_runfiles = rule(
    implementation = _rustc_runfiles_impl,
    toolchains = ["@rules_rust//rust:toolchain_type"],
)

def _tool_paths_impl(ctx):
    def runfile(file):
        path = file.short_path
        return path[3:] if path.startswith("../") else ctx.workspace_name + "/" + path

    output = ctx.actions.declare_file(ctx.label.name + ".go")
    ctx.actions.write(output, "package rulesrs\nconst cargoRunfile = %s\nconst rustcRunfile = %s\n" % (
        json.encode(runfile(ctx.file.cargo)),
        json.encode(runfile(ctx.file.rustc)),
    ))
    return [DefaultInfo(files = depset([output]))]

# Resolve tool locations when Bazel configures the extension.
tool_paths = rule(
    implementation = _tool_paths_impl,
    attrs = {
        "cargo": attr.label(allow_single_file = True, mandatory = True),
        "rustc": attr.label(allow_single_file = True, mandatory = True),
    },
)
