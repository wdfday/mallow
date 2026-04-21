use std::io::Result;
use std::path::PathBuf;

fn main() -> Result<()> {
    let manifest_dir =
        PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR not set"));

    // Candidates for `proto/`, ordered by most-specific-first:
    //   1. PROTO_DIR env — opt-in override for exotic layouts / Nix / CI.
    //   2. `mallow/proto` (repo layout) — the canonical checkout.
    //   3. `/proto` — docker layout where `Dockerfile.herald` loads the
    //      proto directory via `additional_contexts: proto=../proto`.
    //      The dockerfile mounts it at `/proto` so the container mirrors
    //      the source-tree invariant that `proto/` sits one level above
    //      `almanac/` — here `almanac` lives at `/app`, so `/proto` is
    //      what `../../../proto` would resolve to if we had a true repo
    //      root at `/`.
    let repo_proto = manifest_dir.join("..").join("..").join("..").join("proto");
    let docker_proto = PathBuf::from("/proto");

    let proto_dir = std::env::var("PROTO_DIR")
        .ok()
        .map(PathBuf::from)
        .into_iter()
        .chain([repo_proto, docker_proto])
        .find(|p| p.join("market.proto").exists())
        .expect(
            "market.proto not found — tried PROTO_DIR, ../../../proto (repo), /proto (docker). \
             Set PROTO_DIR or provide a proto/ sibling to almanac/.",
        );

    let proto_file = proto_dir.join("market.proto");

    println!("cargo:rerun-if-changed={}", proto_file.display());

    prost_build::compile_protos(
        &[proto_file.to_str().unwrap()],
        &[proto_dir.to_str().unwrap()],
    )?;
    Ok(())
}
