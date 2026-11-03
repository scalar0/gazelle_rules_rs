#![doc = env!("CARGO_PKG_DESCRIPTION")]

#[cfg(not(feature = "enabled"))]
compile_error!("The default feature is missing.");

#[cfg(target_os = "linux")]
const _: () = assert!(macro_support::context_value!() == 7);

pub fn value() -> u8 {
    renamed::value()
}

#[test]
fn dependencies_work() {
    assert_eq!(value(), dev_support::value());
    assert_eq!(env!("PROBE_BUILD"), "7");
}
