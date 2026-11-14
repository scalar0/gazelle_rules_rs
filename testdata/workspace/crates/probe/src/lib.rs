#![doc = env!("CARGO_PKG_DESCRIPTION")]

#[cfg(not(feature = "enabled"))]
compile_error!("The default feature is missing.");

#[cfg(target_os = "linux")]
const _: () = assert!(macro_support::context_value!() == 7);

/// Return the fixture value.
///
/// ```
/// assert_eq!(probe::value(), 7);
/// ```
pub fn value() -> u8 {
    assert_eq!(itoa::Buffer::new().format(7), "7");
    assert_eq!(include_str!("../resources/message.txt").trim(), "answer");
    renamed::value()
}

#[test]
fn dependencies_work() {
    assert_eq!(value(), dev_support::value());
    assert_eq!(env!("PROBE_BUILD"), "7");
}
