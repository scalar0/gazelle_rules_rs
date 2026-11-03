fn main() {
    let enabled = cfg!(feature = "linux-only");
    assert_eq!(
        enabled,
        std::env::var_os("CARGO_FEATURE_LINUX_ONLY").is_some()
    );
    println!("cargo:rustc-env=NORMAL_PLATFORM_FEATURE={enabled}");
}
