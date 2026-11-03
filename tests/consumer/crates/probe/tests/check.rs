mod shared;
#[test]
fn shared_module() {
    assert_eq!(probe::answer(), shared::expected());
}

#[test]
fn binary() {
    let output = std::process::Command::new(env!("CARGO_BIN_EXE_probe"))
        .output()
        .unwrap();
    assert!(output.status.success());
    assert_eq!(String::from_utf8(output.stdout).unwrap().trim(), "42");
}
