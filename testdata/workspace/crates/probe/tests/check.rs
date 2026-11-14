mod shared;
#[test]
fn check() {
    assert_eq!(probe::value(), shared::expected());
    assert_eq!(env!("CARGO_PKG_NAME"), "probe");
    let r#gen = dev_support::value();
    assert_eq!(probe::value(), dev_support::value());
    assert_eq!(env!("PROBE_BUILD"), "7");
    assert_eq!(r#gen, 7);
    let status = std::process::Command::new(env!("CARGO_BIN_EXE_probe"))
        .status()
        .expect("The binary must start.");
    assert!(status.success());
}
