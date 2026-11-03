pub fn value() -> u8 {
    7
}

#[test]
fn platform_feature() {
    assert_eq!(cfg!(feature = "linux-only"), cfg!(target_os = "linux"));
    assert!(!cfg!(feature = "host-only"));
    assert_eq!(
        env!("NORMAL_PLATFORM_FEATURE") == "true",
        cfg!(feature = "linux-only")
    );
}
