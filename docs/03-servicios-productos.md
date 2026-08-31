# 03 — Catálogo de servicios y productos por fases

> Documento del proyecto **AIBank**. Define QUÉ ofrece el neobanco, en qué orden, y cómo genera ingresos cada pieza. Basado en el análisis de mercado ([doc 01](01-analisis-mercado.md)) y las restricciones regulatorias por licencia ([doc 02](02-regulacion-licencias.md)).

---

## Principio de diseño

El catálogo sigue la escalera validada por Nubank/Ualá/Mercado Pago:

```
Adquisición (gratis) → Engagement (pagos diarios) → Monetización (crédito + float) → Retención (ahorro/inversión/seguros)
```

Dos reglas duras extraídas de la investigación:
1. **El interchange en LatAm (0,5–0,9%) no sostiene el negocio por sí solo** — cada fase debe acercar el crédito.
2. **La integración al riel de pagos instantáneos local es requisito de entrada**, no diferenciador (PIX, SPEI/DiMo, Bre-B, Yape/Plin, Transferencias 3.0).

---

## Fase 1 — MVP (lanzamiento, meses 0–9)

Objetivo: validar adquisición y engagement con costo mínimo, sobre BaaS.

| # | Servicio/Producto | Descripción | Ingreso | Depende de |
|---|---|---|---|---|
| 1.1 | **Cuenta digital gratuita** | Onboarding 100% remoto en <5 min con KYC biométrico (liveness + documento + validación gubernamental) | — (gancho) | Proveedor KYC + BaaS |
| 1.2 | **Pagos instantáneos por riel local** | Enviar/recibir por el riel del país (CVU/Transferencias 3.0 en AR; PIX en BR; según país ancla), P2P por alias/QR | — (engagement diario) | BaaS con acceso al riel |
| 1.3 | **Tarjeta débito/prepago virtual + física** | Visa/Mastercard, funciona en e-commerce y POS; virtual instantánea al abrir la cuenta | Interchange (~70% del bruto vía revenue share BaaS) | Emisor (Pomelo/Dock) |
| 1.4 | **Notificaciones y control** | Push en tiempo real, congelar tarjeta, CVV dinámico, límites por canal | — (confianza) | Plataforma propia |
| 1.5 | **Recargas y pago de servicios** | Celular, servicios públicos, transporte | Comisión por transacción (1–3%) | Agregador de pagos local |

**KPI de salida de fase**: >50.000 cuentas activadas, >35% actividad mensual (MAU/registrados), CAC < USD 8, NPS > 60.

---

## Fase 2 — Monetización (meses 9–24)

Objetivo: encender los motores de ingreso. Requiere licencia con capacidad de crédito (SCD en Brasil, SOFIPO en México, o socio prestamista).

| # | Servicio/Producto | Descripción | Ingreso | Referente |
|---|---|---|---|---|
| 2.1 | **Tarjeta de crédito** | Límite inicial bajo con buildup por comportamiento; scoring alternativo con datos transaccionales propios | Intereses (motor #1) + interchange crédito | Nubank (mayor emisor de tarjetas nuevas en MX); Stori (99% aprobación) |
| 2.2 | **Préstamos personales** | Pre-aprobados in-app sobre historial transaccional; montos crecientes | Intereses | DaviPlata (desembolsos +203% a/a) |
| 2.3 | **Cuenta remunerada / ahorro con rendimiento** | Rendimiento diario sobre saldo (vía money market o captación propia según licencia); metas de ahorro ("cajitas") | Float / spread de tesorería | Ualá MX (9–11% anual); Nubank "Cajitas"; Mercado Pago (AUM USD 19.000M) |
| 2.4 | **Cobro para comercios (QR + link de pago)** | Micro-comercios cobran con QR interoperable del riel local; liquidación inmediata | Comisión de adquirencia (MDR) | Yape Empresa, Mercado Pago Point |
| 2.5 | **Suscripción por tiers** | 2–3 niveles que empaquetan cashback/puntos, retiros gratis, FX sin comisión, soporte prioritario, tarjeta metálica; en Fase 3 se enriquecen con eSIM/seguros de viaje | Suscripción mensual recurrente | **Revolut: ~16% de su ingreso, +67% a/a** ([Finextra](https://www.finextra.com/blogposting/31353/deep-dive-an-analytical-breakdown-of-revoluts-2025-performance)) — casi nadie lo hace bien en LatAm; Nubank Ultravioleta |

**KPI de salida de fase**: ARPAC > USD 4/mes, cartera de crédito con NPL < 6%, ratio depósito de nómina > 15% de activos (mejor predictor de LTV según [Spark](https://www.spark.money/research/neobank-business-model-analysis)).

---

## Fase 3 — Expansión de portafolio (meses 24–42)

Objetivo: subir ARPAC hacia el benchmark Nubank (USD 12+/mes) y ampliar el moat.

| # | Servicio/Producto | Descripción | Ingreso |
|---|---|---|---|
| 3.1 | **Remesas / transferencias internacionales** | Corredores clave (EE.UU.→MX/CO/PE); rieles stablecoin como backend (~40% más barato que rieles tradicionales) | FX spread + fee fijo |
| 3.2 | **Inversiones** | Fondos money market, CDT/CDs, acciones fraccionadas (partner broker) | Comisión de gestión / distribución |
| 3.3 | **Crypto / stablecoins** | Compra-venta integrada; cuenta en "dólar digital" (USDC/USDT) — demanda fuerte en AR y de cobertura cambiaria en toda la región | Spread de compra-venta |
| 3.4 | **Seguros embebidos** | Vida, protección de celular, protección de compras/fraude — distribución in-app con partner asegurador | Comisión de distribución (margen alto) |
| 3.5 | **BNPL / cuotas** | Pago en cuotas en comercios aliados y sobre la tarjeta | Intereses + fee al comercio |
| 3.6 | **Nómina y "adelanto de sueldo"** | Portabilidad de nómina + acceso anticipado a salario devengado | Ancla de LTV + fee de adelanto |

**KPI de salida de fase**: ARPAC > USD 8/mes, >3 productos por cliente activo en cohortes de 12+ meses.

---

## Fase 4 — Segundo mercado y PYMES (meses 36+)

| # | Servicio/Producto | Descripción | Ingreso |
|---|---|---|---|
| 4.1 | **Cuenta empresa / PYME** | Onboarding KYB, multi-usuario, conciliación, facturación | Suscripción + interchange |
| 4.2 | **Adquirencia completa** | POS físico + online para PYMES | MDR |
| 4.3 | **Crédito de capital de trabajo** | Sobre flujo de cobros observado (adelanto de ventas) | Intereses |
| 4.4 | **Spend management** | Tarjetas corporativas con controles (modelo Clara) | Interchange + SaaS |
| 4.5 | **Expansión geográfica** | Replicar Fases 1–2 en el segundo país (ver secuencia en [doc 02 §7](02-regulacion-licencias.md)) | — |

---

## Matriz servicio ↔ requisito regulatorio

| Producto | Con BaaS | Con licencia de pagos propia (IP/IFPE/EEDE/SEDPE/PSPCP) | Requiere licencia de crédito/captación |
|---|---|---|---|
| Cuenta + pagos + débito | ✅ | ✅ | — |
| Tarjeta de crédito | ⚠️ (con socio emisor de crédito) | ❌ | ✅ (SCD, SOFIPO, financiera) |
| Préstamos | ⚠️ (originando para un tercero) | ❌ | ✅ |
| Cuenta remunerada | ⚠️ (vía fondos money market, según país) | ⚠️ | ✅ (captación) |
| Remesas | ✅ (con partner regulado) | ✅ | — |
| Crypto | ✅ (partner exchange regulado, p.ej. Bitso) | ✅ | Registro VASP según país |
| Seguros | ✅ (como comercializador) | ✅ | Registro de corredor según país |
| Inversiones | ✅ (partner broker) | ⚠️ | Licencia de intermediación según país |

⚠️ = posible con estructura de socios; revisar por país en [doc 02](02-regulacion-licencias.md).

**Consecuencia de diseño**: la plataforma debe modelar cada producto detrás de una **capa de "proveedor de capacidad"** (propio vs socio) para poder cambiar de BaaS a licencia propia sin reescribir el producto — ver [doc 05](05-arquitectura-plataforma.md).

---

## Modelo de ingresos consolidado (objetivo a 4 años)

Mezcla objetivo basada en los benchmarks de la región (Nubank: ARPAC USD 12,2/mes con costo de servicio USD 0,80/mes — [Nu Q2'25](https://international.nubank.com.br/company/nu-holdings-ltd-reports-second-quarter-2025-financial-results/)):

| Fuente | % del ingreso objetivo | Nota |
|---|---|---|
| Intereses de crédito | 45–55% | Tarjeta + préstamos + BNPL |
| Float / tesorería | 12–18% | Crece con licencia de captación |
| Interchange | 10–15% | Estructuralmente bajo en LatAm |
| Comisiones (FX, remesas, retiros, crypto) | 10–15% | |
| Suscripciones + seguros + inversiones | 8–12% | Margen alto |
