# Desarrollo

Requisitos: JDK 21 (`brew install openjdk@21`), Docker Desktop corriendo.

```bash
# Tests (levantan PostgreSQL efímero vía Testcontainers)
./gradlew :core:test

# Entorno local persistente (Postgres en localhost:5433, user/pass: aibank/aibank_dev)
docker compose -f platform/docker-compose.yml up -d
```

Notas:
- `gradle.properties` fija el JDK en `/opt/homebrew/opt/openjdk@21`; ajústalo si tu ruta difiere.
- `~/.testcontainers.properties` apunta al socket de Docker Desktop (`docker.host=unix:///Users/<usuario>/.docker/run/docker.sock`).
- Flujo git: cada feature en rama `feat/<nombre>`; revisión → merge a `main`. Estados en [roadmap.md](roadmap.md).
- Regla del core: `core/` no importa SDKs de proveedores; todo lo externo entra por adaptadores.
