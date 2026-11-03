fn main() {
    assert!(cfg!(feature = "linux-only"));
}

#[test]
fn feature_tool() {
    main();
    assert_eq!(normal_support::value(), 7);
    assert_eq!(env!("NORMAL_PLATFORM_FEATURE"), "true");
}
