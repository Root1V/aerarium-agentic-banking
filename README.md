# AIBank — Plan completo para un neobanco en Latinoamérica

Investigación (agosto 2026, fuentes citadas en cada documento) y plan integral — negocio + plataforma tecnológica — para lanzar un neobanco multi-país en LatAm.

## Resumen ejecutivo

**La oportunidad**: el mercado de neobanca LatAm (~USD 17–18 mil millones en 2025) crece a doble dígito y quedan ~300M de adultos no/sub-bancarizados, pero la ventana de la "wallet gratuita" cerró — los rieles públicos (PIX, Bre-B, SPEI/DiMo, Yape/Plin) comoditizaron los pagos. En 2026 se gana con **crédito basado en scoring alternativo, rendimiento sobre saldos y experiencia multiproducto AI-native**, con un costo de servicio radicalmente bajo (benchmark Nubank: USD 0,80/cliente/mes contra ARPAC de USD 12,2).

**La estrategia recomendada (ruta híbrida)**:
1. **Lanzar MVP sobre BaaS en 4–6 meses (USD 150–400K de construcción)** en el país de validación — Argentina (registro PSPCP en 3–6 meses casi sin capital) o el país ancla elegido vía Pomelo/Dock.
2. **Tramitar licencia propia en paralelo** (IP+SCD en Brasil ~USD 2,2M / compra de SOFIPO en México) y migrar al superar ~100–300K usuarios, cuando el costo cedido al BaaS supere el de mantener la licencia.
3. **Monetizar con crédito como motor** (~50% del ingreso objetivo) — el interchange LatAm (0,5–0,9%) no sostiene el negocio solo.
4. **Expandir con licencias ligeras** (EEDE Perú ~USD 0,8M, SEDPE Colombia ~USD 2M) replicando la plataforma multi-país.

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

## Decisiones estructurales clave

- **Ruta regulatoria**: híbrida — BaaS para validar, licencia propia para monetizar (regla de transición: ~100–300K usuarios).
- **Core bancario delgado PROPIO desde el día 1** ([doc 08](docs/08-plan-de-construccion.md)): ledger de doble partida + cuentas + catálogo + motor de posting como fuente de verdad operativa; el BaaS queda relegado a conectividad regulada detrás de adaptadores, con reconciliación diaria. Los proveedores se **simulan** al inicio para construir sin esperar contratos.
- **Crédito es el negocio**; la plataforma de datos que lo alimenta se construye 12+ meses antes de prestar.
- **Todo proveedor detrás de un adaptador** (KYC, tarjetas, rieles, core): cambiar de proveedor o de país es configuración + un adaptador, no una reescritura.
- **Independencia progresiva ("escalera de internalización", [doc 06 §4](docs/06-stack-tecnologico.md))**: la meta es la autonomía tipo Revolut (~99% de tecnología propia), pero se alcanza por peldaños financiados con ingresos — se construye barato lo que crea independencia permanente (ledger, datos, adaptadores, app) y se alquila lo demás con cláusulas de salida, internalizando licencia → core → riesgo → procesamiento a medida que el volumen lo paga.
- **IA integrada de origen**: asistente de soporte, antifraude y scoring — no un añadido posterior.

## Próximos pasos sugeridos

1. Decidir el país de validación (T0) — ver análisis en [doc 04 §3](docs/04-plan-negocio.md).
2. Contactar 2–3 proveedores BaaS (Pomelo, Dock) y asesores regulatorios del país elegido para cotizaciones reales.
3. Iniciar la Fase 0 del [roadmap](docs/07-roadmap-desarrollo.md): fundaciones técnicas + expediente regulatorio.

---

*Los datos citados provienen de fuentes públicas de 2025–2026 (enlaces en cada documento). Capitales mínimos y tiempos regulatorios cambian periódicamente: validar con asesores legales locales antes de comprometer capital. Este plan es análisis estratégico, no asesoría legal ni financiera.*
