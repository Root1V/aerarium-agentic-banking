# Aerarium — Agentic Banking Core

*Production-grade banking core for agentic commerce — the embryo of a bank where AI agents
are first-class account holders. Append-only double-entry ledger in micro-units (10⁻⁶) so a
$0.001 agent call actually settles, hold→capture authorizations, and consent-backed payment
mandates that prove a human agreed. Rust core, Go partner API and mobile BFF, Flutter app,
Python credit scoring, TypeScript back office. One-command self-hosted sandbox. No BaaS lock-in.*

The *aerarium* was Rome's treasury — where the state kept what it owned and what it owed,
under the temple of Saturn, with the Senate auditing the books. This is that institution
rebuilt for a world where the account holder is a machine: an agent that discovers a service,
pays a tenth of a cent for one call, and has to be told **no** the moment it goes past what
its owner authorized.

Not a wallet sitting on someone else's bank. The ledger is here and it is the source of truth:
double-entry, append-only, with the invariants enforced by PostgreSQL triggers instead of by
application code that a future service can forget to call. Corrections are reversing entries,
never UPDATEs — the lesson Synapse taught the industry at its customers' expense.

> **Naming note**: [`mercatus`](https://github.com/Root1V/mercatus-agentic-payments) — the
> market — is the counterpart repo, where agents discover and pay for services. `aerarium` is
> the treasury they pay from. Same story, two sides of the counter.
>
> **Language note**: this header is in English; the research, specifications and design
> documents below are in Spanish, the language they were written and negotiated in.

## Why "embryo" and not "demo"

Everything a bank needs to open its doors is built and tested — **369 tests across five
languages** — but a bank is not software: it is a licence plus software. What exists here is
the part that takes years to get right and that nobody can buy off the shelf.

**Built and green**: double-entry ledger with regulatory limits, hold→capture authorizations
with expiry and sweeping, delegated payment mandates with caps and revocation, OAuth2 with
secret rotation, card authorization/capture/reversal, resumable KYC onboarding, daily
reconciliation, explainable credit scoring, mobile app, back office with an append-only audit
trail, and end-to-end tracing that survives the async hop through the outbox.

**Deliberately absent**: an e-money licence or a BaaS provider in Peru, the anchor market.
Until that closes, no model moves real money — and that is a contract to sign, not code to
write.

---

Investigación (agosto 2026, fuentes citadas en cada documento) y plan integral — negocio + plataforma tecnológica — para lanzar un neobanco multi-país en LatAm.

## Resumen ejecutivo

**La oportunidad**: el mercado de neobanca LatAm (~USD 17–18 mil millones en 2025) crece a doble dígito y quedan ~300M de adultos no/sub-bancarizados, pero la ventana de la "wallet gratuita" cerró — los rieles públicos (PIX, Bre-B, SPEI/DiMo, Yape/Plin) comoditizaron los pagos. En 2026 se gana con **crédito basado en scoring alternativo, rendimiento sobre saldos y experiencia multiproducto AI-native**, con un costo de servicio radicalmente bajo (benchmark Nubank: USD 0,80/cliente/mes contra ARPAC de USD 12,2).

**La estrategia recomendada (ruta híbrida)**:
1. **Lanzar MVP sobre BaaS en 4–6 meses (USD 150–400K de construcción)** en **Perú**, el país ancla decidido ([doc 04 §3](docs/04-plan-negocio.md)) — lo define el primer cliente institucional, no la velocidad regulatoria.
2. **Tramitar la EEDE peruana en paralelo** (~USD 0,8M, 12–24 meses) y migrar al superar ~100–300K usuarios. Ojo: la EEDE **no permite crédito** — encenderlo exige una Financiera (~USD 4,5M), que hay que presupuestar desde ahora.
3. **Monetizar con crédito como motor** (~50% del ingreso objetivo) — el interchange LatAm (0,5–0,9%) no sostiene el negocio solo.
4. **Expandir después** a México (compra de SOFIPO) o Colombia (SEDPE ~USD 2M) replicando la plataforma multi-país.

**La plataforma**: comprar la infraestructura regulada (BaaS, emisión de tarjetas, KYC, antifraude) y construir lo diferencial — la app (Flutter), el orquestador de onboarding, el **ledger propio de doble partida con reconciliación diaria** (la lección Synapse), y el **motor de scoring con datos transaccionales**. Monolito modular en Kotlin/JVM + PostgreSQL + Kafka sobre AWS/Kubernetes, con adaptadores por proveedor y por país que permiten cambiar de BaaS a licencia propia sin reescribir el producto.

**Capital**: USD 0,8–1,5M al MVP; USD 3–6M a 100K usuarios; USD 10–20M para encender crédito con licencia propia (~año 3, donde se estima el punto de equilibrio con 400–700K usuarios activos).

## Documentos

| # | Documento | Contenido |
|---|---|---|
| 01 | [Análisis de mercado](docs/01-analisis-mercado.md) | Tamaño y crecimiento, bancarización por país, mapa competitivo con cifras (Nubank, Mercado Pago, Ualá, Stori…), benchmarks de unit economics, tendencias 2026, lecciones de fracasos |
| 02 | [Regulación y licencias](docs/02-regulacion-licencias.md) | Licencias por país (capital, tiempos, permisos), Ruta A (propia) vs Ruta B (BaaS) con costos, riesgo Synapse, open finance, estrategia de entrada secuencial |
| 03 | [Servicios y productos](docs/03-servicios-productos.md) | Catálogo por fases (MVP → crecimiento → expansión → PYMES), matriz producto↔licencia, modelo de ingresos consolidado |
| 04 | [Plan de negocio](docs/04-plan-negocio.md) | Tesis, propuesta de valor, mercado de entrada, unit economics objetivo, capital por fase, equipo, riesgos, KPIs, criterios go/no-go |
| 05 | [Arquitectura de la plataforma](docs/05-arquitectura-plataforma.md) | Principios, diagrama de referencia, ledger y reconciliación, abstracción de rieles de pago, multi-país, seguridad y cumplimiento técnico, IA |
| 06 | [Stack tecnológico](docs/06-stack-tecnologico.md) | Matriz build vs buy por componente con proveedores, stack recomendado (Kotlin, Flutter, PostgreSQL, Kafka, AWS) y trade-offs explícitos |
| 07 | [Roadmap de desarrollo](docs/07-roadmap-desarrollo.md) | Fases 0–4 con entregables, equipo, costos, calendario (Gantt), gates de avance y riesgos de ejecución |
| 08 | [Decisión del core y plan de construcción](docs/08-plan-de-construccion.md) | **La decisión cerrada**: core delgado propio (ledger+cuentas+catálogo+posting), tabla definitiva construir/alquilar/simular, estructura del monorepo y backlog de los primeros 6 sprints |
| 09 | [Integración Mercatus](docs/09-integracion-mercatus.md) | Riel de pago para agentes de IA: análisis del contrato, bloqueantes (unidad monetaria, estructura de cuentas) y plan |
| 10 | [Preguntas para Mercatus](docs/10-preguntas-mercatus.md) | **Documento para compartir**: qué acepta AIBank, 24 preguntas con supuesto por defecto, y contrapropuesta de calendario |
| 11 | [Datos de integración del sandbox](docs/11-spec-integracion-sandbox.md) | **Documento para compartir**: credenciales, endpoints con respuestas reales, 20 escenarios de prueba, límites y tabla de errores |
| 12 | [Modelo B: iniciación de pagos](docs/12-modelo-b-iniciacion-de-pagos.md) | Respuesta a Q25: pagar sobre la cuenta propia del cliente en vez de un sub-ledger ómnibus — por qué sí, por qué no con `client_credentials`, y qué haría falta |
| 13 | [Especificación de los modelos A y B](docs/13-spec-modelos-a-y-b.md) | **Documento para compartir**: cómo integrarse en los dos modelos, flujo de consentimiento, 30 escenarios de prueba y qué cambia al pasar a cuentas reales |
| — | [Sandbox](SANDBOX.md) | **Documento para compartir**: levantar el entorno completo con un comando, credenciales, recorrido de demostración y qué cambia en producción |

## Decisiones estructurales clave

- **Ruta regulatoria**: híbrida — BaaS para validar, licencia propia para monetizar (regla de transición: ~100–300K usuarios).
- **Core bancario delgado PROPIO desde el día 1** ([doc 08](docs/08-plan-de-construccion.md)): ledger de doble partida + cuentas + catálogo + motor de posting como fuente de verdad operativa; el BaaS queda relegado a conectividad regulada detrás de adaptadores, con reconciliación diaria. Los proveedores se **simulan** al inicio para construir sin esperar contratos.
- **Crédito es el negocio**; la plataforma de datos que lo alimenta se construye 12+ meses antes de prestar.
- **Todo proveedor detrás de un adaptador** (KYC, tarjetas, rieles, core): cambiar de proveedor o de país es configuración + un adaptador, no una reescritura.
- **Independencia progresiva ("escalera de internalización", [doc 06 §4](docs/06-stack-tecnologico.md))**: la meta es la autonomía tipo Revolut (~99% de tecnología propia), pero se alcanza por peldaños financiados con ingresos — se construye barato lo que crea independencia permanente (ledger, datos, adaptadores, app) y se alquila lo demás con cláusulas de salida, internalizando licencia → core → riesgo → procesamiento a medida que el volumen lo paga.
- **IA integrada de origen**: asistente de soporte, antifraude y scoring — no un añadido posterior.

## Próximos pasos sugeridos

1. ~~Decidir el país de validación~~ — **decidido: Perú** ([doc 04 §3](docs/04-plan-negocio.md)).
2. Contactar proveedores BaaS con cobertura en Perú y un asesor regulatorio ante la SBS para cotizaciones reales. **Es el bloqueante de producción**: sin esto, ningún modelo de integración mueve dinero real.
3. Iniciar la Fase 0 del [roadmap](docs/07-roadmap-desarrollo.md): fundaciones técnicas + expediente regulatorio.

---

*Los datos citados provienen de fuentes públicas de 2025–2026 (enlaces en cada documento). Capitales mínimos y tiempos regulatorios cambian periódicamente: validar con asesores legales locales antes de comprometer capital. Este plan es análisis estratégico, no asesoría legal ni financiera.*
