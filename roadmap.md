# Roadmap de construcción

Estados: ⬜ pendiente · 🔨 en curso · 👀 en revisión · ✅ merged
Flujo: cada feature en rama `feat/<nombre>` → revisión → commit/merge a `main`.
Stack por tarea: Rust (core) · Go (adaptadores, BFF) · Python (riesgo) · Flutter (app) · TS (backoffice).

## Sprint 1–2: Núcleo

| Feature | Lenguaje | Rama | Estado |
|---|---|---|---|
| Monorepo + entorno local (Cargo, docker-compose Postgres) | Rust | feat/core-ledger | ✅ |
| Core ledger: doble partida + motor de posting + invariantes (10 tests) | Rust | feat/core-ledger | ✅ |
| Cuentas + catálogo de productos, sin sobregiro y topes regulatorios (8 tests) | Rust | feat/core-accounts | 👀 |
| Contratos Protobuf/OpenAPI entre core y adaptadores | proto | feat/contracts | ⬜ |
| Bus de eventos (Kafka + esquema de eventos del ledger) | Rust | feat/events | ⬜ |
| Simuladores: ProveedorCuentas (BaaS) + RielPagos | Go | feat/sim-baas-rails | ⬜ |

## Sprint 3–4: Onboarding y canal

| Feature | Lenguaje | Rama | Estado |
|---|---|---|---|
| Orquestador de onboarding + simulador KYC | Go | feat/onboarding | ⬜ |
| BFF v1 (API para la app) | Go | feat/bff | ⬜ |
| App v0 (alta, saldo, movimientos, transferir) | Flutter | feat/app-v0 | ⬜ |

## Sprint 5–6: Tarjetas, reconciliación, operación

| Feature | Lenguaje | Rama | Estado |
|---|---|---|---|
| Adaptador tarjetas (simulado) + webhooks de autorización | Go | feat/cards-sim | ⬜ |
| Motor de reconciliación v1 | Rust | feat/reconciliation | ⬜ |
| Backoffice v0 + observabilidad (trazas E2E) | TypeScript | feat/backoffice | ⬜ |

## Backlog (post-sprint 6)

Sandbox BaaS real · notificaciones push · screening AML · asistente IA soporte · cuenta remunerada · hardening/pentest
