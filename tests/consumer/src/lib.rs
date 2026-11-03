pub fn answer() -> u32 {
    probe::answer()
}
#[test]
fn check() {
    assert_eq!(answer(), 42);
}
