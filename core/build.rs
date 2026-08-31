fn main() -> Result<(), Box<dyn std::error::Error>> {
    let include = ["../contracts/proto"];
    let core_proto = "../contracts/proto/aibank/core/v1/core.proto";
    let events_proto = "../contracts/proto/aibank/events/v1/events.proto";

    tonic_build::configure()
        .build_client(false)
        .compile_protos(&[core_proto], &include)?;

    // events.proto reutiliza Money y Direction de core.proto. `extern_path` evita
    // regenerar esos tipos y los apunta al módulo donde ya viven, de modo que un
    // Money del contrato gRPC y uno de un evento son EL MISMO tipo en Rust.
    // events.proto no declara servicios, así que se genera con prost directamente.
    let mut events = prost_build::Config::new();
    events.extern_path(".aibank.core.v1", "crate::grpc::pb");
    events.compile_protos(&[events_proto], &include)?;

    for proto in [core_proto, events_proto] {
        println!("cargo:rerun-if-changed={proto}");
    }
    Ok(())
}
