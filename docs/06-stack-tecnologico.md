# 06 — Stack tecnológico y matriz build vs buy

> Documento del proyecto **AIBank**. Decisiones concretas de tecnología con justificación y alternativas. Complementa la arquitectura del [doc 05](05-arquitectura-plataforma.md).

---

## 1. Matriz build vs buy por componente

| Componente | Decisión | Recomendado | Alternativas | Justificación |
|---|---|---|---|---|
| **Core bancario delgado** (ledger, cuentas, catálogo, posting) | **BUILD** — decisión cerrada en [doc 08](08-plan-de-construccion.md) | PostgreSQL doble partida append-only; TigerBeetle como motor al escalar | Fineract/Midaz (open source), Pismo/Mambu (solo plan B) | El sistema de registro es donde no queremos depender de terceros; es barato a esta escala y evita la migración de contabilidad con clientes vivos |
| Conectividad regulada Fase 1 (custodia de fondos, acceso a riel) | **Rent (BaaS)** | Pomelo (hispanoamérica multi-país) | Dock (foco Brasil), Galileo, bancos patrocinadores | Es regulatorio, no software: licencias y membresías, no código; se internaliza en el peldaño 1 de la escalera (§4) |
| **Reconciliación** core propio ↔ proveedor | **BUILD** | Motor propio, corrida diaria | — | Activo anti-Synapse; condición para conectar producción |
| Emisión/procesamiento de tarjetas | **Buy** | Pomelo | Dock, Marqeta, Rain (stablecoin-settled) | PCI Level 1 del proveedor reduce el alcance PCI propio a ~cero |
| KYC / verificación de identidad | **Buy** | Incode/Metamap | Truora, Sumsub, Veriff, Unico (BR) | Integración más profunda con fuentes gubernamentales LatAm ([Signzy](https://www.signzy.com/blogs/best-kyc-verification-platforms-mexico)) |
| Orquestador de onboarding | **BUILD** | Flujo propio sobre APIs del proveedor | — | El funnel de conversión es diferencial; los proveedores se cambian por país |
| Screening AML / sanciones | **Buy** | Sumsub o ComplyAdvantage | Refinitiv World-Check | Commodity regulado |
| Monitoreo antifraude | **Buy → hybrid** | Sardine (device + fraude + AML unificado) | Unit21, Feedzai, Lynx | Time-to-value inmediato; features propias se agregan encima |
| **Motor de scoring crediticio** | **BUILD** | Modelos propios sobre datos transaccionales + open finance | Buró tradicional como complemento | Es EL activo diferencial del negocio (patrón Nubank/Hyperplane, UaláScore) |
| Rieles de pago | **Buy/Integrar** | Adaptador por país: PIX, SPEI/DiMo, Bre-B, CCE, Transf. 3.0 | Vía BaaS en Fase 1; directo con licencia propia | Requisito de entrada por país |
| Open finance | **Buy** | Belvo (MX/BR/CO; 60+ instituciones, 80M+ cuentas — [Belvo](https://belvo.com/)) | Prometeo (11 países) | Alimenta scoring y agregación |
| Crypto/stablecoins (Fase 3) | **Buy (partner)** | Exchange regulado local (p. ej. Bitso) | Rain (emisión stablecoin) | Licencias VASP del partner |
| App móvil | **BUILD** | Flutter | React Native, nativo | Ver §2 |
| Backoffice (operaciones, cumplimiento, soporte) | **BUILD** (con low-code interno si acelera) | Web propia | Retool para herramientas internas tempranas | Flujos regulatorios propios |
| Notificaciones | **Buy** | Push/SMS/WhatsApp vía proveedor (Twilio/Meta BSP) | — | Commodity |
| Asistente IA de soporte | **BUILD sobre API** | Claude API / LLM con RAG sobre base de conocimiento propia y acciones tool-use | Proveedores CX con IA | Nubank: 55% de N1 resuelto con IA ([OpenAI](https://openai.com/index/nubank/)) |

**Regla general (consenso 2026)**: *comprar* la infraestructura regulada y commodity; *construir* la experiencia, el ledger de reconciliación y el motor de riesgo/datos. Nubank construyó todo, pero con un capital y talento que un MVP no tiene ([Purrweb](https://www.purrweb.com/blog/neobank-development-cost/)).

---

## 2. Stack recomendado

### Backend
- **Lenguaje**: **Kotlin/JVM** (o Java 21+). Es la norma en fintech LatAm (talento abundante, ecosistema maduro de librerías financieras); Nubank usa Clojure (también JVM) y Stori Java sobre AWS ([GoGloby](https://gogloby.com/insights/fintech-talent-latam-unicorns/)). **Go** para adaptadores de rieles y servicios de alto rendimiento si el equipo lo domina.
- **Estructura**: monolito modular (módulos = dominios del doc 05) desplegado como un servicio; extracción a microservicios solo cuando un dominio lo exija (equipo o volumen).
- **Framework**: Spring Boot (madurez, seguridad, contrataciones) o Ktor si el equipo es Kotlin-first.

### Datos y eventos
- **PostgreSQL** como base transaccional (una por país); esquema de ledger append-only con constraints de doble partida.
- **Kafka** (MSK) como bus de eventos; outbox pattern para consistencia entre DB y bus.
- **Data platform**: eventos → lakehouse (S3 + Iceberg) → dbt → warehouse (Snowflake/BigQuery o Trino) para riesgo, finanzas y regulatorio.
- **CQRS selectivo**: proyecciones de saldo/movimientos para lecturas de la app; el ledger nunca se lee en caliente para renderizar la home.

### Mobile
- **Flutter** — la elección dominante de los neobancos grandes; Nubank migró a Flutter tras evaluar React Native y Kotlin nativo, buscando un solo lenguaje y arquitectura para todos los equipos ([Building Nubank](https://building.nubank.com/why-we-think-flutter-will-help-us-scale-mobile-development-at-nubank/); [flutter.dev showcase](https://flutter.dev/showcase/nubank)). Un solo equipo cubre iOS+Android con UI pixel-perfect.
- Seguridad en el cliente: certificate pinning, detección de root/jailbreak, secure storage, device binding, passkeys.

### Infraestructura
- **AWS** (multi-AZ; región primaria según país ancla; revisar requisitos CNBV/BACEN para cloud — [doc 05 §3.5](05-arquitectura-plataforma.md)).
- **Kubernetes (EKS) + Terraform + GitOps (ArgoCD)** — estándar de la industria.
- **Observabilidad**: OpenTelemetry + Grafana stack o Datadog; trazas distribuidas obligatorias para conciliar pagos asíncronos; alerting con SLOs por dominio.
- **CI/CD**: trunk-based, feature flags (LaunchDarkly/Unleash), despliegues canary. En banca los flags son además una herramienta de cumplimiento (apagar un producto por país).

### Seguridad
- Secretos: AWS Secrets Manager/Vault; claves: KMS + CloudHSM (FIPS 140-2/3).
- SAST/DAST/dependencias en pipeline; pentest externo anual; programa de divulgación.
- IAM interno con acceso just-in-time y auditoría total del backoffice (el fraude interno es un vector real en banca).

### IA
- **Claude API (claude-fable-5 / claude-sonnet-5 según costo-capacidad)** para el asistente de soporte con RAG + tool-use sobre acciones de cuenta (consultar movimientos, disputar cargos), con escalamiento humano.
- Modelos propios (scikit-learn/XGBoost → feature store) para scoring y fraude; los LLM no deciden crédito, generan features y explican decisiones.

---

## 3. Trade-offs explícitos

| Decisión | Elegido | Descartado | Por qué |
|---|---|---|---|
| Mobile | Flutter | React Native | Validación de los neobancos más grandes de la región; un solo equipo; RN cerró brecha de rendimiento pero fragmenta la experiencia financiera pixel-perfect |
| Backend | Kotlin/JVM | Clojure (Nubank), Elixir | Talento contratable en LatAm; Clojure funciona para Nubank pero achica el pool de contratación |
| Ledger Fase 1 | PostgreSQL append-only propio | TigerBeetle desde el día 1 | A <1M cuentas, PostgreSQL bien diseñado sobra; TigerBeetle (100K–500K TPS) entra cuando el volumen lo pida sin cambiar el modelo contable |
| Core Fase 2 | SaaS (Pismo/Mambu) | Core custom | Custom = USD 15–25M y 24–36 meses; solo se justifica con millones de clientes |
| Monolito modular | Sí | Microservicios día 1 | Un equipo de 12–20 personas no amortiza la complejidad operativa de microservicios |
| BaaS inicial | Pomelo | Dock | Cobertura hispanoamérica multi-país (AR/MX/CO/CL/PE) alineada con la secuencia de expansión; Dock si el país ancla fuera Brasil |

---

## 4. Independencia de terceros con presupuesto limitado: la escalera de internalización

**El dilema**: la meta es no depender de terceros (el modelo Revolut: ~99% de tecnología propia, core incluido — es lo que le permite lanzar productos en semanas y entrar a países sin renegociar contratos), pero un negocio que recién empieza no puede pagarla — Revolut la construyó en 10 años con miles de millones, y un core bancario propio completo cuesta USD 15–25M+ y 24–36 meses. **Intentar construir todo el día 1 con presupuesto de arranque no da independencia: da un lanzamiento que nunca llega** (mientras tanto, el competidor sobre BaaS ya está aprendiendo del mercado).

La resolución del dilema tiene tres piezas:

### 4.1 No todas las dependencias son iguales

| Tipo de dependencia | Ejemplos | ¿Evitable al inicio? | Estrategia |
|---|---|---|---|
| **Regulatoria** | Licencia del BaaS, BIN sponsor, acceso al riel de pagos | **No** — evitarla cuesta USD 1–5M de capital + 12–36 meses de trámite | Es la única que se "compra" con el tiempo: la licencia propia ES la verdadera independencia, más que cualquier software |
| **De datos** | Ledger en manos del proveedor, historial transaccional, modelos de scoring | **Sí, y es la crítica** | NUNCA cederla: ledger espejo propio, eventos propios, features propias desde el día 1 |
| **De software reemplazable** | KYC, screening AML, notificaciones, señal antifraude | Aceptable | Detrás de adaptadores + cláusulas de portabilidad; cambiar de proveedor = semanas, no reescritura |
| **De software estructural** | Core banking SaaS, procesador de tarjetas | Aceptable con plan de salida | Contrato con derecho a exportar datos completos; el ledger espejo hace la migración posible |
| **De infraestructura** | Cloud (AWS) | Aceptable | Kubernetes + Terraform estándar minimizan el lock-in real |

**Lección clave**: lo que te ata de por vida no es usar un proveedor — es **no tener tus propios datos y tu propia contabilidad** cuando quieras irte (el hueco de USD 95M de Synapse fue exactamente eso). La independencia se protege con el ledger espejo y los adaptadores, no construyendo cada pieza.

### 4.2 Construir barato lo que crea independencia, alquilar lo que no

Con presupuesto de arranque, el dinero de ingeniería se concentra en los 4 activos que generan independencia permanente y son *baratos* de construir bien al inicio:

1. **Ledger propio de doble partida** (PostgreSQL append-only): ~2–4 semanas-ingeniero bien hecho; es el activo anti-lock-in número 1.
2. **Capa de adaptadores** (puertos e interfaces propias sobre cada proveedor): disciplina de diseño, costo marginal ~cero.
3. **Plataforma de eventos y datos propia** (Kafka + lakehouse): cada transacción alimenta TU historial, no solo el del proveedor — es la materia prima del scoring futuro.
4. **La experiencia (app + onboarding)**: nadie la alquila bien y es la cara del negocio.

Todo lo demás se alquila en Fase 1 **a sabiendas de que es temporal**, con dos cláusulas innegociables en cada contrato: (a) exportación completa de datos en formato utilizable, (b) plan de transición asistida si se termina la relación.

### 4.3 La escalera: internalizar con hitos de ingreso, no con fe

Cada peldaño se financia con los ingresos del anterior — así la independencia crece al ritmo que el negocio puede pagarla:

| Peldaño | Hito que lo dispara | Qué se internaliza | Por qué ya se puede pagar |
|---|---|---|---|
| **0. Lanzamiento** (hoy) | — | Ledger espejo, adaptadores, app, datos | Cuesta poco; el resto se alquila |
| **1. Licencia propia** | 100–300K usuarios o costo BaaS > USD 1–2M/año | La relación regulatoria (cuentas propias, acceso directo al riel) | El ahorro de fees BaaS + 100% del interchange + float pagan la licencia y el cumplimiento |
| **2. Core propio de cuentas** | Licencia operando + volumen estable | El ledger espejo se promueve a fuente primaria (TigerBeetle/Fineract como motor); el SaaS core se evita o se abandona | El equipo ya conoce el dominio; la migración es "cambiar de adaptador" gracias al diseño de Fase 0 |
| **3. Riesgo propio** | 12+ meses de datos + crédito encendido | Scoring y antifraude pasan de proveedor a modelos propios (el proveedor queda como señal complementaria) | Es el moat del negocio; los datos ya son tuyos porque los capturaste desde el día 0 |
| **4. Procesamiento propio** | Millones de tarjetas | Procesamiento/switch de tarjetas in-house (lo último que internalizó hasta Nubank) | Solo a esa escala el ahorro supera el costo de operar certificado PCI completo |

**Regla de oro**: en cada compra de Fase 1, la pregunta no es "¿esto me hace dependiente?" sino **"¿puedo irme en 6 meses sin perder datos ni clientes?"**. Si la respuesta es sí (adaptador + ledger propio + cláusula de salida), la dependencia es alquiler, no hipoteca.

### 4.4 El papel del open source

Para bajar el costo de los peldaños 2–3 sin comprar SaaS: **Apache Fineract** (core completo, 400+ instituciones), **Midaz** (ledger multi-moneda, source-available), **TigerBeetle** (motor contable, 100K–500K TPS) y **Formance** (orquestación de flujos). El trade-off honesto: se paga en tiempo de ingeniería y operación (un equipo pequeño operando un core self-hosted asume riesgo operativo real), por eso entran en la escalera cuando hay equipo para sostenerlos — no en el MVP.
