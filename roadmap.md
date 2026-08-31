# Roadmap de construcción

Estados: ⬜ pendiente · 🔨 en curso · 👀 en revisión · ✅ merged
Flujo: cada feature en rama `feat/<nombre>` → revisión → commit/merge a `main`.

## Sprint 1–2: Núcleo

| Feature | Rama | Estado |
|---|---|---|
| Monorepo + entorno local (Gradle, docker-compose Postgres) | feat/core-ledger | ⬜ |
| Core ledger: esquema doble partida + motor de posting + invariantes (tests) | feat/core-ledger | ⬜ |
| Cuentas + catálogo de productos v1 (producto "cuenta simple") | feat/core-accounts | ⬜ |
| Bus de eventos (Kafka + esquema de eventos del ledger) | feat/events | ⬜ |
| Simuladores: ProveedorCuentas (BaaS) + RielPagos | feat/sim-baas-rails | ⬜ |

## Sprint 3–4: Onboarding y canal

| Feature | Rama | Estado |
|---|---|---|
| Orquestador de onboarding + simulador KYC | feat/onboarding | ⬜ |
| BFF v1 (API para la app) | feat/bff | ⬜ |
| App Flutter v0 (alta, saldo, movimientos, transferir) | feat/app-v0 | ⬜ |

## Sprint 5–6: Tarjetas, reconciliación, operación

| Feature | Rama | Estado |
|---|---|---|
| Adaptador tarjetas (simulado) + webhooks de autorización | feat/cards-sim | ⬜ |
| Motor de reconciliación v1 | feat/reconciliation | ⬜ |
| Backoffice v0 + observabilidad (trazas E2E) | feat/backoffice | ⬜ |

## Backlog (post-sprint 6)

Sandbox BaaS real · notificaciones push · screening AML · asistente IA soporte · cuenta remunerada · hardening/pentest
