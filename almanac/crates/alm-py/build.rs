fn main() {
    // macOS: Python symbols (_PyBool_Type, etc.) are provided at runtime by the
    // interpreter that dlopen()s this extension module. Tell the linker not to
    // treat them as errors at link time.
    // maturin sets this flag automatically; for plain `cargo build` we need it here.
    #[cfg(target_os = "macos")]
    {
        println!("cargo:rustc-link-arg=-undefined");
        println!("cargo:rustc-link-arg=dynamic_lookup");
    }
}
