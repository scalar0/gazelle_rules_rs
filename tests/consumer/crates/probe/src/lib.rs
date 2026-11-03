/// Return the answer.
///
/// ```
/// assert_eq!(probe::answer(), 42);
/// ```
pub fn answer() -> u32 {
    assert_eq!(itoa::Buffer::new().format(42), "42");
    assert_eq!(include_str!("../resources/message.txt").trim(), "answer");
    42
}

#[test]
fn returns_answer() {
    assert_eq!(answer(), 42);
}
