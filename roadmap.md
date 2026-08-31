# Roadmap de construcción

Estados: ⬜ pendiente · 🔨 en curso · 👀 en revisión · ✅ merged
Flujo: cada feature en rama `feat/<nombre>` → revisión → commit/merge a `main`.
Stack por tarea: Rust (core) · Go (adaptadores, BFF) · Python (riesgo) · Flutter (app) · TS (backoffice).

## Sprint 1–2: Núcleo

| Feature | Lenguaje | Rama | Estado |
|---|---|---|---|
| Monorepo + entorno local (Cargo, docker-compose Postgres) | Rust | feat/core-ledger | ✅ |
| Core ledger: doble partida + motor de posting + invariantes (10 tests) | Rust | feat/core-ledger | ✅ |
| Cuentas + catálogo de productos, sin sobregiro y topes regulatorios (8 tests) | Rust | feat/core-accounts | ✅ |
| Contratos gRPC core↔adaptadores + servidor Rust + cliente Go (9 tests) | proto/Rust/Go | feat/contracts | ✅ |
| Bus de eventos: outbox transaccional + relay + Kafka (6 tests) | Rust | feat/events | ✅ |
| Adaptador de riel + simulador (11 tests): entrantes idempotentes, salientes en 2 fases | Go | feat/sim-baas-rails | ✅ |

## Sprint 3–4: Onboarding y canal

| Feature | Lenguaje | Rama | Estado |
|---|---|---|---|
| Onboarding: máquina de estados reanudable + KYC/AML simulados (9 tests) | Go | feat/onboarding | ✅ |
| BFF: API del canal móvil con autorización por titular (16 tests) | Go | feat/bff | ✅ |
| App v0: saldo, movimientos y transferencia (28 tests) | Flutter | feat/app-v0 | ✅ |

## Sprint 5–6: Tarjetas, reconciliación, operación

| Feature | Lenguaje | Rama | Estado |
|---|---|---|---|
| Tarjetas: autorización, retención, cobro y reversa (14 tests) | Go | feat/cards-sim | ✅ |
| Conciliación: interna, contra proveedor y limbo (11 tests) | Rust | feat/reconciliation | ✅ |
| Backoffice v0: cola de conciliación con roles y auditoría (18 tests) | TypeScript | feat/backoffice | ✅ |
| Observabilidad: trazas E2E entre Rust y Go (OpenTelemetry) | Rust/Go | feat/observability | ⬜ |

## Deuda técnica anotada

- El pool de PostgreSQL del core espera hasta 10 s por una conexión bajo carga en
  vez de fallar rápido: es un precipicio de latencia para el canal móvil. Revisar
  al hacer pruebas de carga.
- La deriva de saldo se detecta recorriendo todos los asientos. Sirve al volumen
  actual; al crecer habrá que conciliar por ventanas con saldos de corte.

## Backlog (post-sprint 6)

Sandbox BaaS real · notificaciones push · screening AML · asistente IA soporte · cuenta remunerada · hardening/pentest
