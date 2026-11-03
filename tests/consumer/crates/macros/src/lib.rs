extern crate proc_macro;

/// Return the answer as a Rust expression.
///
/// ```
/// assert_eq!(probe_macros::answer!(), 42);
/// ```
#[proc_macro]
pub fn answer(_: proc_macro::TokenStream) -> proc_macro::TokenStream {
    "42".parse().unwrap()
}
