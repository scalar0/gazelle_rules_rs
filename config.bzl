"""Write Gazelle configuration from the consumer's backend settings."""

def _backend_config_impl(ctx):
    ctx.file("config.json", json.encode_indent({
        "crates_repository": ctx.attr.crates_repository,
        "manifest": ctx.attr.manifest,
        "platform_triples": ctx.attr.platform_triples,
        "rules_repository": ctx.attr.rules_repository,
    }) + "\n")
    ctx.file("BUILD.bazel", 'exports_files(["config.json"], visibility = ["//visibility:public"])\n')

# Pass the same platform list to this rule and crate.from_cargo.
backend_config = repository_rule(
    implementation = _backend_config_impl,
    attrs = {
        "crates_repository": attr.string(default = "crates"),
        "manifest": attr.string(default = "Cargo.toml"),
        "platform_triples": attr.string_list(mandatory = True),
        "rules_repository": attr.string(default = "rules_rs"),
    },
)
