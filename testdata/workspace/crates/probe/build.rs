fn main() {
    let input = std::env::var("PROBE_INPUT").expect("The build input must have a path.");
    assert_eq!(
        std::fs::read_to_string(input)
            .expect("The build input must be readable.")
            .trim(),
        "default"
    );
    println!("cargo:rustc-env=PROBE_BUILD={}", build_support::value());
}
