fn main() {
    assert!(cfg!(test));
    assert_eq!(probe::value(), dev_support::value());
    let status = std::process::Command::new(env!("CARGO_BIN_EXE_probe"))
        .status()
        .expect("The binary must start.");
    assert!(status.success());
}
