fn main() -> Result<(), Box<dyn std::error::Error>> {
    let proto = "../contracts/proto/aibank/core/v1/core.proto";

    tonic_build::configure()
        .build_client(false)
        .compile_protos(&[proto], &["../contracts/proto"])?;

    println!("cargo:rerun-if-changed={proto}");
    Ok(())
}
