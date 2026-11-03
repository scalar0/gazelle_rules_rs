mod shared;
#[test]
fn shared_module() { assert_eq!(probe::answer(), shared::expected()); }
