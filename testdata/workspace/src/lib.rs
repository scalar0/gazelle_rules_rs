pub fn value() -> u8 {
    probe::value()
}

#[test]
fn root_package_uses_member() {
    assert_eq!(value(), 7);
}
