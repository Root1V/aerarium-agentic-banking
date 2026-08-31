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

## Convenciones

- Flujo git: cada feature en rama `feat/<nombre>`; revisión → merge a `main`. Estados en [roadmap.md](roadmap.md).
- Regla del core: `core/` no depende de SDKs de proveedores; todo lo externo entra por adaptadores.
- Montos: siempre enteros en unidades menores (centavos). Nunca coma flotante.
- El ledger es append-only: las correcciones son asientos de reversa, jamás UPDATE.
