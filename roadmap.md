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
| Observabilidad: trazas E2E entre Rust y Go, incluido el salto asíncrono (17 tests) | Rust/Go | feat/observability | ✅ |

## Fase 2: crédito (el motor de ingresos del plan)

| Feature | Lenguaje | Rama | Estado |
|---|---|---|---|
| Scoring crediticio con datos transaccionales, explicable y auditable (45 tests) | Python | feat/credit-scoring | 👀 |
| Originación: límite, disposición y ciclo de tarjeta de crédito | Rust/Go | feat/credit-origination | ⬜ |

## Integración Mercatus (riel de pago para agentes de IA)

Análisis y bloqueantes en [docs/09-integracion-mercatus.md](docs/09-integracion-mercatus.md).
Las tres primeras no dependen de los bloqueantes y son deuda propia que había que pagar igual.

| # | Feature | Lenguaje | Rama | Estado |
|---|---|---|---|---|
| 1 | Migración del ledger a micras (10⁻⁶): sin esto no se puede representar $0.001 | todos | feat/micro-units | ⬜ |
| 2 | Autenticación OAuth2 con scopes (hoy solo hay un puerto con sustituto) | Go/Rust | feat/oauth2 | ⬜ |
| 3 | Primitiva de autorización en el core (retención → captura, con expiración) | Rust | feat/authorizations | ⬜ |
| 4 | API REST de Mercatus: los cinco endpoints del contrato | Go | feat/mercatus-api | ⬜ |
| 5 | Sandbox: entorno separado, saldos configurables, rate limiting | Go | feat/sandbox | ⬜ |
| 6 | Reembolsos (fase 2 del propio contrato) | Go | feat/mercatus-refunds | ⬜ |

**Bloqueado, requiere decisión externa:**
- Estructura de cuenta ómnibus (no se le puede abrir cuenta a un software) — necesita
  asesoría regulatoria y que el proveedor BaaS la acepte.
- Fecha de sandbox comprometida (2026-09-02) inalcanzable: el alcance real es de 4–6 semanas.
- Tres huecos del contrato por aclarar con Mercatus: expiración de autorizaciones,
  semántica de `recipient_mismatch`/`amount_mismatch`, y ventana de idempotencia.

## Deuda técnica anotada

- El pool de PostgreSQL del core espera hasta 10 s por una conexión bajo carga en
  vez de fallar rápido: es un precipicio de latencia para el canal móvil. Revisar
  al hacer pruebas de carga.
- La deriva de saldo se detecta recorriendo todos los asientos. Sirve al volumen
  actual; al crecer habrá que conciliar por ventanas con saldos de corte.

## Backlog (post-sprint 6)

Sandbox BaaS real · notificaciones push · screening AML · asistente IA soporte · cuenta remunerada · hardening/pentest
