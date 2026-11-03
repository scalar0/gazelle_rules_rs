#[proc_macro]
pub fn context_value(_input: proc_macro::TokenStream) -> proc_macro::TokenStream {
    let value = if cfg!(feature = "linux-only") {
        "7"
    } else {
        "0"
    };
    value
        .parse()
        .expect("The token stream must contain an integer.")
}
