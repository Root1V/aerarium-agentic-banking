# Core bancario (Rust): servidor gRPC y barrendero de retenciones.
#
# El contexto de build es la RAÍZ del repositorio, no core/: el core compila los
# .proto de contracts/ y sin ellos no hay build.
FROM rust:1.98-slim-bookworm AS build

# protoc genera el servidor gRPC en tiempo de compilación, y libprotobuf-dev trae
# los .proto de los tipos comunes (Timestamp) que el contrato importa.
#
# El resto es para librdkafka, que rdkafka-sys compila desde fuente. El relay de
# eventos no entra en esta imagen, pero la dependencia es del crate y no del
# binario, así que se compila igual. Es coste de la etapa de build y no viaja a
# la imagen final.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      protobuf-compiler libprotobuf-dev \
      build-essential cmake pkg-config libssl-dev libsasl2-dev zlib1g-dev \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY contracts/proto ./contracts/proto
COPY core ./core

WORKDIR /src/core
# Sin base de datos en el build: sqlx valida las consultas contra el caché
# commiteado en core/.sqlx. Si alguien cambió una consulta y no regeneró el
# caché, la imagen no compila — que es exactamente lo que queremos que pase.
ENV SQLX_OFFLINE=true
RUN cargo build --release \
      --bin aibank-core-server \
      --bin aibank-authorization-sweeper

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 aibank
COPY --from=build /src/core/target/release/aibank-core-server /usr/local/bin/
COPY --from=build /src/core/target/release/aibank-authorization-sweeper /usr/local/bin/
USER aibank
CMD ["aibank-core-server"]
