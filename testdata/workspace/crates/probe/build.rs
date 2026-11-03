fn main() {
    println!("cargo:rustc-env=PROBE_BUILD={}", build_support::value());
}
