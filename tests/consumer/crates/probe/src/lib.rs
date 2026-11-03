pub fn answer() -> u32 {
    42
}

#[test]
fn returns_answer() {
    assert_eq!(answer(), 42);
}
