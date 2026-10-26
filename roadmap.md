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
| Scoring crediticio con datos transaccionales, explicable y auditable (45 tests) | Python | feat/credit-scoring | ✅ |
| Originación: límite, disposición y ciclo de tarjeta de crédito | Rust/Go | feat/credit-origination | ⬜ |

## Integración Mercatus (riel de pago para agentes de IA)

Análisis en [docs/09](docs/09-integracion-mercatus.md) · preguntas y respuestas en
[docs/10](docs/10-preguntas-mercatus.md) · datos para integrar en [docs/11](docs/11-spec-integracion-sandbox.md).

| # | Feature | Lenguaje | Rama | Estado |
|---|---|---|---|---|
| 1 | Migración del ledger a micras (10⁻⁶): sin esto no se puede representar $0.001 | todos | feat/micro-units | ✅ |
| 2 | OAuth2 client_credentials con scopes y rotación de secreto (26 tests) | Go | feat/oauth2 | ✅ |
| 3 | Primitiva de autorización en el core: retención → captura, vencimiento y liberación (25 tests) | Rust | feat/authorizations | ✅ |
| 4 | API REST: los siete endpoints del contrato (33 tests) | Go | feat/mercatus-api | ✅ |
| 5 | Sandbox: OpenAPI congelado, barrendero de vencidas, alta de integración | Go/Rust | feat/mercatus-api | ✅ |
| 6 | Reembolso parcial y total en la API (7 tests) | Go | feat/mercatus-refunds | ✅ |
| 7 | **Modelo B**: mandato de pago con topes, vigencia y revocación (21 tests) | Rust | feat/payment-mandates | ✅ |
| 8 | **Modelo B**: consentimiento, permiso delegado y pago bajo mandato (14 tests) | Go | feat/payment-mandates | ✅ |
| 9 | Pantallas de consentimiento y gestión de permisos del titular (22 tests) | Flutter | feat/app-mandates | ✅ |
| 10 | Sandbox entregable: entorno completo en contenedores, canal del titular y cliente de demostración | Go/Docker | feat/sandbox-entregable | ✅ |

Mercatus respondió y **aceptó las 24 preguntas y el calendario** — ver
[docs/10-preguntas-mercatus.md](docs/10-preguntas-mercatus.md) y
[docs/11-spec-integracion-sandbox.md](docs/11-spec-integracion-sandbox.md).

**Los dos modelos están construidos y probados en sandbox.** El A (ómnibus) y el B
(pago sobre la cuenta propia del cliente, con mandato delegado). Análisis en
[docs/12](docs/12-modelo-b-iniciacion-de-pagos.md), especificación conjunta para Mercatus
en [docs/13](docs/13-spec-modelos-a-y-b.md).

**Los dos modelos están completos**, backend y app, y el sandbox se levanta entero
con un comando ([SANDBOX.md](SANDBOX.md)) — es lo que le faltaba a Mercatus para
escribir su cliente HTTP. Lo único que falta para producción no es código: la
licencia o el proveedor BaaS en Perú.

**Sigue bloqueado, y no por nosotros:**
- **Constitución de Mercatus Technologies S.A.C. (Perú)**, en curso. Hasta que cierre no
  se puede hacer el KYB de la cuenta maestra, así que **el sandbox no lleva dinero real**.
- **Estructura de cuenta ómnibus**: necesita asesoría regulatoria local y que el proveedor
  licenciado la acepte. Bloquea el paso a producción, no la integración técnica. El modelo B
  no depende de ella.
- **Custodia de fondos de terceros en el modelo ómnibus**: Mercatus recibe dinero de sus
  clientes y lo mantiene como saldo de sub-ledger. Es la pregunta que su abogado tiene que
  responder antes del KYB, y puede exigir EEDE ([docs/12 §1](docs/12-modelo-b-iniciacion-de-pagos.md)).
- **Y el bloqueante real es nuestro**: sin licencia ni proveedor BaaS contratado, ningún
  modelo mueve dinero real. El sandbox funciona completo; producción no depende de Mercatus.

## App de clientes

Plan y razones en [docs/14](docs/14-ux-y-decision-movil.md). Para probarla: `app/tool/sandbox.sh ios|web`.

| # | Feature | Lenguaje | Rama | Estado |
|---|---|---|---|---|
| 1 | La app corre: iOS, Android, portal web; entrada de sandbox; tokens de diseño | Flutter/Go | feat/app-runnable | ✅ |
| 2 | Disponible frente a retenido, con los pagos en curso agrupados por plataforma | Rust/Go/Flutter | feat/app-saldo-retenido | ✅ |
| 3 | Alta de nivel 1: DNI verificado, cuenta básica, una persona un alta | Go/Flutter | feat/app-onboarding | ✅ |
| 3b | Alta de nivel 2: prueba de vida con la cámara, sube los topes | Swift/Kotlin/Rust | — | ⬜ bloqueado |
| 4 | Centro de control: gasto de hoy y del mes por plataforma, historial agrupado | Rust/Go/Flutter | feat/app-agentes | ✅ |
| 4b | Umbral de aprobación por pago y «qué se compró» | Rust/Go/Flutter | — | ⬜ propuesto a Mercatus (AE-09) |
| 5 | Passkeys: el banco como Relying Party, sesiones, entrar y proteger la cuenta en iOS | Go/Rust/Swift/Flutter | feat/app-auth-passkeys | 👀 |
| 5b | Passkeys en Android | Kotlin | — | ⬜ sin SDK en esta máquina |
| 5c | Atestación de la app (App Attest, Play Integrity) y vinculación del dispositivo | Swift/Kotlin/Go | — | ⬜ |

**Por qué el nivel 2 está bloqueado:** la prueba de vida necesita la cámara y el SDK
nativo del proveedor de identidad (Incode, Metamap), y el core no sabe cambiar el
producto de una cuenta —que es lo que sube los topes—. Lo segundo es trabajo nuestro en
el core; lo primero espera a elegir proveedor. Tampoco hay todavía aceptación de
términos en el alta: no existen los términos.

**Lo que falta para que el 5 funcione en un teléfono:** elegir el dominio del banco. La
passkey queda atada a él para siempre —cambiarlo después obliga a cada cliente a registrar
otra—, así que es una decisión de negocio, no de código: está en `PASSKEY_RP_ID`. Con el
dominio elegido hay que servir `/.well-known/apple-app-site-association` por HTTPS y añadir
el entitlement `webcredentials:<dominio>` a la app. Hasta entonces el código iOS compila
pero el sistema rechaza la ceremonia, y la app lo dice («este teléfono todavía no puede
usar passkeys»). El servidor está probado de punta a punta con un autenticador virtual que
firma de verdad.

**Por qué el 4b espera:** cambia el contrato con Mercatus —un pago que queda pendiente de
aprobación es un estado nuevo que su cliente tiene que manejar— y **no sirve sin
notificaciones push**: sin aviso, la persona no se entera de que tiene algo que aprobar y la
retención vence. Propuesto en el canal (AE-09).

## Deuda técnica anotada

- **Una passkey sincronizada no es un dispositivo.** iOS copia las passkeys a los otros
  equipos de la misma cuenta de iCloud. Firma la persona, con biometría, pero no
  necesariamente desde el teléfono donde se registró. Para operaciones de alto riesgo
  hará falta atar la sesión al dispositivo (5c).
- **Si la app se cierra entre el alta aprobada y «Protege tu cuenta»**, la referencia
  del alta se pierde (vive en memoria) y la persona queda con cuenta pero sin forma de
  entrar: hoy, soporte. Guardarla en el llavero del sistema lo resuelve.

- El pool de PostgreSQL del core espera hasta 10 s por una conexión bajo carga en
  vez de fallar rápido: es un precipicio de latencia para el canal móvil. Revisar
  al hacer pruebas de carga.
- La deriva de saldo se detecta recorriendo todos los asientos. Sirve al volumen
  actual; al crecer habrá que conciliar por ventanas con saldos de corte.
- **Lo retenido no incluye las retenciones de tarjeta.** El adaptador de tarjetas
  retiene con sus propios asientos, fuera de la tabla de autorizaciones del core. Hoy no
  importa —las tarjetas no están en la app—, pero el día que entren, «retenido» tiene que
  sumarlas o mentirá por omisión.
- **El límite del alta identifica el origen por la IP de la conexión.** Detrás de un
  proxy todas las peticiones llegan con la IP del proxy y comparten el límite. Hay que
  leer la IP real desde una cabecera que ponga el proxy —nunca desde una que pueda
  escribir el cliente— antes de publicar el alta detrás de Caddy.
- El límite de tasa de la API de socio es POR INSTANCIA. Con más de una réplica el
  límite efectivo se multiplica; hay que moverlo a un almacén compartido antes de
  escalar horizontalmente — o antes de abrir el sandbox alojado a un segundo socio.

## Backlog (post-sprint 6)

Sandbox BaaS real · notificaciones push · screening AML · asistente IA soporte · cuenta remunerada · hardening/pentest
