fn main() {
    assert_eq!(probe::value(), 7);
}

#[test]
fn dependencies_work() {
    assert_eq!(env!("PROBE_BUILD"), "7");
    assert_eq!(dev_support::value(), 7);
}
