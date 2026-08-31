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

## Adaptadores (Go)

```bash
cd core && cargo run --bin aibank-core-server   # el core debe estar arriba
cd adapters && go test ./...                    # se saltan solos si el core no responde
```

`go.work` liga los módulos locales (`adapters`, `clients/go`) sin publicarlos.

El riel simulado (`adapters/rails/sim`) reproduce de forma determinista lo que un
riel real hace a diario: timeouts, rechazos, notificaciones duplicadas y entregas
fuera de orden. No es andamiaje temporal — se queda como herramienta de pruebas,
porque esos escenarios casi no se pueden provocar contra el sandbox de un proveedor.

## Servicios (Go)

```bash
cd services && go test ./...    # onboarding: requiere core + Postgres arriba
```

El alta de clientes es una máquina de estados persistida y reanudable: cada paso se
registra al completarse, así una caída se retoma donde quedó en vez de volver a
pedir documentos al cliente y a pagar verificaciones ya hechas. Los pasos y sus
resultados son además el rastro de auditoría que el supervisor puede exigir.

## BFF (API del canal móvil)

```bash
cd services && go test ./bff/     # requiere core + Postgres arriba
```

La autenticación es un PUERTO (`bff.Authenticator`), no una implementación: banca
se resuelve con passkeys FIDO2 vinculadas al dispositivo. El sustituto de
desarrollo vive en `bff/sim` para que no pueda cablearse a producción por descuido.

Dos reglas del canal: el dinero viaja como entero en unidades menores (nunca
decimal en JSON) y toda operación que mueve dinero exige `Idempotency-Key`.

## App (Flutter)

```bash
export PATH="/opt/homebrew/Caskroom/flutter/3.47.2/flutter/bin:$PATH"
cd app && flutter test && flutter analyze

# Contra el BFF real:
flutter run --dart-define=AIBANK_BFF_URL=http://localhost:8080 \
            --dart-define=AIBANK_TOKEN=... --dart-define=AIBANK_ACCOUNT_ID=...
```

Dos reglas del cliente: el dinero nunca es `double` (entero en centavos, se formatea
solo para mostrar) y un reintento de envío CONSERVA la clave de idempotencia — si
generara una nueva, un timeout que el servidor sí procesó cobraría dos veces.

## Tarjetas

Una compra con tarjeta son DOS momentos, no uno: la autorización retiene el dinero
(sale del saldo disponible pero sigue en el banco) y el cobro posterior lo saca de
verdad — por un monto que puede diferir del autorizado (propinas, combustible).
Modelarlo así es lo que hace que el saldo disponible del cliente sea correcto.

```bash
cd adapters && go test ./cards/...
```

## Conciliación

Es la contrapartida de haber construido un ledger propio: sin registro propio no
hay nada contra qué comparar. Cubre tres frentes — el ledger contra sí mismo, el
ledger contra el extracto del proveedor (cruzando por clave de idempotencia) y el
dinero detenido en cuentas de tránsito o retención.

```bash
cd core && SQLX_OFFLINE=true cargo test --test reconciliation
```

## Backoffice (TypeScript)

```bash
cd backoffice && npm install
node --test test/*.test.ts && npx tsc --noEmit

ALLOW_DEV_AUTH=true npm start   # consola en :8090
```

Node ejecuta los `.ts` eliminando tipos, sin empaquetador ni paso de compilación.
Eso descarta sintaxis que genere código: nada de *parameter properties*, enums ni
decoradores.

Regla del módulo: ninguna acción ocurre sin quedar atribuida a una persona, y los
intentos DENEGADOS se registran igual que los permitidos — un intento rechazado es
justo lo que interesa detectar. El rastro de auditoría es de solo inserción, como
el ledger.

## Observabilidad

Una misma traza une app → BFF → core (Rust) → evento del outbox → relay. Dos
propagaciones distintas hacen falta y ninguna es automática:

- **Entre procesos**: contexto W3C por metadata gRPC y cabeceras HTTP.
- **En el tiempo**: el evento del outbox guarda el `traceparent` de la transacción
  que lo originó, porque se publica después y en otro proceso. Sin eso la traza se
  corta en el COMMIT, justo donde hace falta para seguir un pago asíncrono.

Regla dura: en una traza NUNCA entran datos personales ni secretos. Hay una guarda
(`is_safe_attribute` / `IsSafeAttribute`) que filtra por subcadena y es
deliberadamente estricta — las trazas salen a herramientas de terceros.

## Convenciones

- Flujo git: cada feature en rama `feat/<nombre>`; revisión → merge a `main`. Estados en [roadmap.md](roadmap.md).
- Regla del core: `core/` no depende de SDKs de proveedores; todo lo externo entra por adaptadores.
- Montos: siempre enteros en unidades menores (centavos). Nunca coma flotante.
- El ledger es append-only: las correcciones son asientos de reversa, jamás UPDATE.
