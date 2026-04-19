use std::io::Result;
use std::path::PathBuf;

fn main() -> Result<()> {
    let manifest_dir =
        PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR not set"));
    // alm-core is at: mallow/almanac/crates/core
    // proto is at:   mallow/proto
    // → go up 3 levels: core → crates → almanac → mallow
    let proto_dir = manifest_dir
        .join("..") // crates/
        .join("..") // almanac/
        .join("..") // mallow/
        .join("proto");
        // .canonicalize()
        // .expect("failed to canonicalize proto dir — is mallow/proto/ present?");
    let proto_file = proto_dir.join("market.proto");

    println!("cargo:rerun-if-changed={}", proto_file.display());

    prost_build::compile_protos(
        &[proto_file.to_str().unwrap()],
        &[proto_dir.to_str().unwrap()],
    )?;
    Ok(())
}
