# Desarrollo

Stack políglota por tarea (justificación en [docs/06](docs/06-stack-tecnologico.md) §2):
Rust (core) · Go (adaptadores, BFF) · Python (riesgo/ML) · Dart/Flutter (app) · TypeScript (backoffice).

## Requisitos

- Rust estable (`curl https://sh.rustup.rs -sSf | sh`)
- Docker Desktop corriendo
- Go 1.27+ (adaptadores, aún no iniciados)

## Core (Rust)

```bash
docker compose -f platform/docker-compose.yml up -d   # Postgres en localhost:5434
cd core && SQLX_OFFLINE=true cargo test               # 10 tests de invariantes del ledger
```

Las migraciones se aplican solas al correr los tests (`sqlx::migrate!`).

### Consultas verificadas en compilación

`sqlx` valida cada consulta SQL contra el esquema real **en tiempo de compilación**. Dos modos:

- **Offline (por defecto en CI)**: usa el caché commiteado en `core/.sqlx`. No requiere base de datos.
  ```bash
  SQLX_OFFLINE=true cargo build
  ```
- **En vivo (al cambiar SQL)**: apunta a una base migrada y regenera el caché.
  ```bash
  export DATABASE_URL="postgres://aibank:aibank_dev@localhost:5434/aibank"
  cargo sqlx prepare   # regenera core/.sqlx — commitear el resultado
  ```

Si cambias una consulta o el esquema, **regenera el caché o CI fallará** — que es exactamente el punto.

## Contratos (Protobuf)

`contracts/proto` es la fuente de verdad de la frontera entre el core (Rust) y los
adaptadores (Go). Un cambio incompatible rompe la compilación de ambos lados.

```bash
make -C contracts lint       # valida los .proto
make -C contracts generate   # regenera el cliente Go (el core Rust lo hace en cargo build)
```

## Servidor del core y cliente Go

```bash
cd core && DATABASE_URL="postgres://aibank:aibank_dev@localhost:5434/aibank" \
  cargo run --bin aibank-core-server        # gRPC en 127.0.0.1:50051

cd clients/go && go test ./...              # tests de integración contra el core real
```

Los tests de Go se saltan solos si el core no está escuchando.

## Eventos (outbox → Kafka)

El core NO publica directamente: escribe el evento en la tabla `outbox` dentro de la
misma transacción que los asientos, y el relay lo publica después. Así es imposible
que exista un asiento sin evento o un evento de una transacción revertida.

```bash
docker compose -f platform/docker-compose.yml up -d          # Postgres + Redpanda
docker exec aibank-redpanda rpk topic create aibank.ledger.v1 -p 3

cd core && DATABASE_URL="postgres://aibank:aibank_dev@localhost:5434/aibank" \
  KAFKA_BROKERS=localhost:9092 cargo run --bin aibank-outbox-relay

docker exec aibank-redpanda rpk topic consume aibank.ledger.v1 --offset start
```

Sin `KAFKA_BROKERS` el relay publica al log (modo desarrollo, sin bus).

Entrega **at-least-once**: todo consumidor debe deduplicar por `event_id` (viaja
como header y dentro del payload). Salud del relay: `outbox` con `published_at IS NULL`
creciendo de forma sostenida = bus o relay caídos.

## Convenciones

- Flujo git: cada feature en rama `feat/<nombre>`; revisión → merge a `main`. Estados en [roadmap.md](roadmap.md).
- Regla del core: `core/` no depende de SDKs de proveedores; todo lo externo entra por adaptadores.
- Montos: siempre enteros en unidades menores (centavos). Nunca coma flotante.
- El ledger es append-only: las correcciones son asientos de reversa, jamás UPDATE.
