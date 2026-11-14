"""Expose the registered Rust compiler and its runtime files to Gazelle."""

def _rustc_runfiles_impl(ctx):
    toolchain = ctx.toolchains["@rules_rust//rust:toolchain_type"]
    manifest = ctx.actions.declare_file(ctx.label.name + "_path")
    path = toolchain.rustc.short_path.removeprefix("../")
    ctx.actions.write(manifest, path + "\n")
    sysroot_manifest = ctx.actions.declare_file(ctx.label.name + "_sysroot")
    anchor = toolchain.sysroot_anchor.short_path
    anchor = (
        anchor.removeprefix("../") if anchor.startswith("../") else "_main/" + anchor
    )
    ctx.actions.write(sysroot_manifest, anchor + "\n")
    return [
        DefaultInfo(
            files = depset([manifest]),
            runfiles = ctx.runfiles(
                files = [sysroot_manifest],
                transitive_files = toolchain.all_files,
            ),
        ),
        OutputGroupInfo(sysroot = depset([sysroot_manifest])),
    ]

rustc_runfiles = rule(
    implementation = _rustc_runfiles_impl,
    toolchains = ["@rules_rust//rust:toolchain_type"],
)
