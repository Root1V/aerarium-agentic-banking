# 04 — Plan de negocio

> Documento del proyecto **AIBank**. Sintetiza la estrategia: dónde entrar, para quién, cómo ganar dinero, con cuánto capital y qué riesgos gestionar. Se apoya en [01-analisis-mercado](01-analisis-mercado.md), [02-regulacion-licencias](02-regulacion-licencias.md) y [03-servicios-productos](03-servicios-productos.md).

---

## 1. Tesis del negocio

**Los rieles públicos de pago (PIX, Bre-B, SPEI/DiMo, Yape/Plin) comoditizaron los pagos; la oportunidad de un neobanco nuevo en 2026 está en el crédito con scoring alternativo, el rendimiento sobre saldos y la experiencia multiproducto, sobre una base de costos radicalmente menor a la banca tradicional (benchmark: USD 0,80/mes de costo de servicio por cliente).**

La ventana de entrada existe porque:
- ~300M de adultos siguen no/sub-bancarizados; México (45–53% de bancarización) y Colombia (57–65%) son las mayores brechas.
- El crédito formal sigue sin llegar a la mayoría: los ganadores prestan con datos transaccionales, no con buró.
- El open finance (obligatorio en Brasil y Colombia) es asimétricamente favorable al entrante.
- Los incumbentes digitales suben de peso (licencias bancarias plenas) y dejan espacio en nichos que no atienden.

## 2. Propuesta de valor y segmento

**Segmento primario**: adultos sub-bancarizados de 20–45 años con smartphone, ingresos informales o mixtos, que ya usan el riel de pagos local pero no tienen crédito formal ni rendimiento sobre su dinero.

**Propuesta de valor**:
1. Cuenta gratis en 5 minutos, sin sucursal, sin saldo mínimo.
2. Tu dinero **rinde todos los días** (cuenta remunerada — el arma de captación que Ualá usa a 9–11% en México).
3. **Crédito que crece contigo**: límite inicial pequeño aprobado con tu comportamiento transaccional, no con buró.
4. Todo el ecosistema de pagos local (riel instantáneo, QR, servicios) + tarjeta internacional.

**Diferenciador operativo (2026)**: banca *AI-native* — asistente financiero conversacional, scoring y antifraude con IA desde el día 1 (Nubank ya resuelve el 55% de consultas de nivel 1 con IA — [OpenAI](https://openai.com/index/nubank/)); para un entrante, esto baja el costo de servicio y CAC de soporte desde el inicio en lugar de ser un retrofit.

## 3. Mercado de entrada y secuencia

Análisis de opciones (detalle regulatorio en [doc 02 §7](02-regulacion-licencias.md)):

| Opción | A favor | En contra |
|---|---|---|
| **Argentina primero** | Registro PSPCP en 3–6 meses casi sin capital; población financieramente sofisticada; demanda de rendimiento y dólar | Mercado saturado por Mercado Pago/Ualá; macro volátil; monetización por crédito restringida sin entidad financiera |
| **México primero** | Mayor brecha de bancarización; remesas USD 61.800M; momentum inversor | IFPE tarda ~781 días; comprar SOFIPO exige capital; competencia fuerte ya instalada (Nu, Stori, Klar, Ualá) |
| **Colombia primero** | Bre-B recién lanzado nivela el campo de pagos; open finance obligatorio 2026; SEDPE ~USD 2M | SEDPE no permite crédito; Nequi/DaviPlata dominan wallets |
| **Brasil primero** | Mercado más grande; infra madura (PIX, open finance); licencia IP+SCD ~USD 2,2M en 9–18 meses permite crédito propio | Competencia máxima (Nubank, Inter, C6, PicPay); requiere portugués y capital mayor desde la reforma 2026–2028 |

**Recomendación**: **estrategia de dos tiempos.**
- **T0 (validación)**: lanzar en **Argentina** vía PSPCP propio (o país equivalente vía BaaS con Pomelo) — es el camino más barato y rápido a producto real con usuarios reales.
- **T1 (monetización)**: entrada a **México comprando una SOFIPO** (crédito + captación en el mercado con más espacio) o a **Brasil con IP+SCD** si el equipo tiene capacidad de operar en portugués. La decisión se toma con los datos de T0.
- **Perú y Colombia** como tercera ola con licencias ligeras (EEDE ~USD 0,8M / SEDPE ~USD 2M), aprovechando la interoperabilidad regional emergente (Yape→Nequi).

## 4. Unit economics objetivo

Benchmarks de la industria como guía ([Nu Q2'25](https://international.nubank.com.br/company/nu-holdings-ltd-reports-second-quarter-2025-financial-results/); [Spark](https://www.spark.money/research/neobank-business-model-analysis)):

| Métrica | Año 1 | Año 2 | Año 3–4 | Benchmark |
|---|---|---|---|---|
| Usuarios activos | 50–100K | 300–500K | 1M+ | — |
| ARPAC mensual | USD 0,5–1 | USD 3–5 | USD 8–12 | Nubank: 12,2 (27,3 en cohortes maduras) |
| Costo de servicio/cliente/mes | < USD 1,5 | < USD 1,2 | < USD 1,0 | Nubank: 0,80 |
| CAC | < USD 8 | < USD 10 | < USD 12 | Se duplicó en la región 2021–2024; el orgánico es el moat |
| LTV/CAC | — | > 3x | > 5x | |
| NPL 90+ (crédito) | — | < 7% | < 5% | |
| % clientes con nómina/depósito recurrente | 5% | 15% | 25% | Mejor predictor de LTV |

**Punto de equilibrio estimado**: 400–700K usuarios activos con crédito encendido (~año 3), consistente con el patrón regional de que el interchange solo no cierra y el crédito debe aportar ~50% del ingreso.

## 5. Necesidades de capital por fase

Rangos construidos con los costos investigados (BaaS: USD 150–500K año 1; licencias: ver [doc 02](02-regulacion-licencias.md); plataforma: ver [doc 07](07-roadmap-desarrollo.md)):

| Fase | Horizonte | Uso | Rango (USD) |
|---|---|---|---|
| **Pre-seed / Fase 0–1** | Meses 0–9 | Equipo fundador + MVP sobre BaaS + legal + lanzamiento | **0,8–1,5M** |
| **Seed / Fase 2a** | Meses 9–18 | Crecimiento a 100K+ usuarios, inicio de trámite de licencia propia, equipo 15–25 | **3–6M** |
| **Serie A / Fase 2b** | Meses 18–30 | Licencia + capital regulatorio (~2–5M según país), capital para prestar, equipo 40+ | **10–20M** |
| **Serie B / Fase 3–4** | Meses 30+ | Escalar cartera de crédito, segundo país | **30M+** |

Referencias de mercado: Klar levantó USD 190M en Serie C a valuación USD 800M; Ualá acumula USD 366M en Serie E a USD 2.750M — el capital para la fase de crédito existe en la región para quien muestra unit economics sanos.

## 6. Equipo

| Fase | Tamaño | Roles clave |
|---|---|---|
| Fase 0–1 (MVP) | 12–20 | CEO, CTO, CPO, **Compliance Officer (obligatorio ante regulador)**, 6–10 backend, 2–3 mobile (Flutter), 1–2 SRE, 1 seguridad, 1–2 datos/riesgo, diseño, soporte |
| Fase 2 | 25–45 | + Head de Crédito/Riesgo, equipo AML/fraude, tesorería, legal interno, growth |
| Fase 3+ | 60+ | + Country managers, equipo por vertical de producto |

## 7. Riesgos principales y mitigaciones

| Riesgo | Probabilidad | Impacto | Mitigación |
|---|---|---|---|
| **Dependencia del BaaS** (lección Synapse: hueco de USD 95M) | Media | Crítico | Ledger propio de reconciliación diaria desde el día 1; cláusulas de portabilidad; segundo proveedor identificado |
| **Regulatorio** (negativa o demora de licencia — caso Bnext) | Media | Crítico | Compliance officer senior desde el inicio; ruta BaaS como plan B permanente; no depender de un solo país |
| **NPL fuera de control al encender crédito** | Alta | Alto | Límites iniciales bajos con buildup; 80% del libro con colateral/nómina al inicio (modelo C6); scoring conservador con datos propios de 12+ meses |
| **CAC insostenible** | Alta | Alto | Motor de referidos y viralidad P2P antes de pagar adquisición; no comprar crecimiento sin LTV demostrado |
| **Competencia de gigantes** (Nu, Mercado Pago) | Alta | Medio | Nicho de entrada definido; velocidad de producto; no competir en subsidios |
| **Macro/FX** (Argentina, devaluaciones) | Media | Medio | Tesorería multi-moneda; producto de "dólar digital" como cobertura y feature |
| **Fraude/identidad sintética** (hasta 80% del fraude de cuenta nueva ya es sintético con IA) | Alta | Alto | Liveness + device binding obligatorios; monitoreo transaccional en tiempo real (Sardine o equivalente) desde el MVP |

## 8. KPIs de gobierno del negocio

- **Norte**: ARPAC, costo de servicio, LTV/CAC, NPL 90+.
- **Semanales**: cuentas activadas, MAU/registrados, TPV por riel, tasa de aprobación KYC, fraude bps sobre TPV.
- **Regulatorios**: reportes AML a tiempo, incidentes de ciberseguridad (72 h en MX), ratio de capital vs mínimo.

## 9. Criterios go/no-go entre fases

1. **Encender Fase 2 (crédito)** solo si: MAU > 35%, CAC < USD 8, y 12+ meses de datos transaccionales para el scoring.
2. **Migrar de BaaS a licencia propia** solo si: usuarios > 100–300K y el costo cedido al BaaS supera USD 1–2M/año.
3. **Abrir segundo país** solo si: el primero tiene contribución positiva por cliente y el equipo de cumplimiento está duplicado.
