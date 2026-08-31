# 02 — Regulación y licencias: qué se necesita legalmente para operar (2026)

> Documento del proyecto **AIBank**. Compara la **Ruta A (licencia propia)** y la **Ruta B (BaaS / banco aliado)** por país. Cifras en USD aproximadas a tipos de cambio de mediados de 2026; los capitales mínimos se ajustan periódicamente y los tiempos son promedios observados, no plazos garantizados.

---

## 1. Panorama por país

### Brasil — Banco Central do Brasil (BACEN)

El menú de licencias más granular de la región:
- **Instituição de Pagamento (IP)** en tres modalidades: emisora de moneda electrónica, credenciadora (adquirencia), iniciadora de pagos (ITP).
- **SCD** (Sociedade de Crédito Direto): presta con capital propio. **SEP**: P2P lending.
- **Banco pleno** (comercial/múltiple).

Capitales históricos: IP emisora R$ 2M, SCD/SEP R$ 1M, banco comercial R$ 17,5M (~USD 3,2M) ([NDM Advogados](https://ndmadvogados.com.br/artigo/guia-autorizacao-de-fintechs-no-bacen/)).

**Cambio clave 2025–2026**: la Resolução Conjunta nº 14 y la Resolução BCB nº 517 (nov-2025) reformulan el capital mínimo con metodología por actividades; una IP emisora pasa a exigir del orden de **R$ 11M (~USD 2M)**, con transición escalonada: reglas viejas hasta 30/06/2026, fase intermedia hasta dic-2027, exigencia plena desde 01/01/2028 ([Celcoin](https://celcoin.com.br/articles/captacao-de-capital-minimo-exigido-quais-os-passos-para-criar-um-banco-digital-no-brasil/); [CSMV](https://www.csmv.com.br/boletins/novas-regras-de-autorizacao-governanca-e-capital-minimo-para-instituicoes-financeiras-e-de-pagamento/)). Usar la palabra "banco" en la marca exigirá **R$ 30M adicionales** ([NDM](https://ndmadvogados.com.br/artigo/mudancas-no-capital-minimo-das-instituicoes-autorizadas/)).

El trámite de autorización puede tomar **hasta 360 días** para IP/SCD ([Celcoin](https://celcoin.com.br/articles/cadastro-e-licenca-banco-central/)); un banco pleno, 2+ años. **Combinación típica de neobanco: IP (cuentas/tarjetas/PIX) + SCD (crédito).**

### México — CNBV (Ley Fintech, 2018)

- **IFPE** (Institución de Fondos de Pago Electrónico): capital mínimo **500.000 UDIs (~USD 235K**; 700.000 con actividades adicionales). El problema no es el capital sino el tiempo: la autorización promedia **~781 días (~2 años)** ([Legal Paradox](https://www.legalparadox.com/es/ley-fintech); [ViaUno](https://viauno.mx/cnbv-nueva-regulacion-fintechs-2026/)). La actualización 2026 añade ciberseguridad obligatoria con reporte de incidentes en 72 h.
- **SOFIPO**: capital prudencial desde **100.000 UDIs (~USD 47K)** en Nivel I; permite captar ahorro **y prestar** — por eso Nubank y Ualá entraron por aquí. **Comprar una SOFIPO existente (12–18 meses de cambio de control) es la vía rápida al mercado mexicano con captación** ([CNBV](https://www.gob.mx/cnbv/acciones-y-programas/proceso-de-autorizacion-sofipos); [KYC Systems](https://kyc-systems.com/blog/sofipos)).
- **Banca múltiple**: ~90M UDIs (≈ USD 40M+) y proceso plurianual; es el endgame (Nu México fue la primera SOFIPO autorizada a convertirse en banco múltiple).

### Colombia — Superintendencia Financiera (SFC)

- **SEDPE**: la licencia de menor capital, **~USD 2M**; permite depósitos electrónicos, débito y pagos, pero **no crédito** ([Pomelo](https://pomelo.la/blog/servicios-financieros-colombia)).
- **Compañía de Financiamiento**: capital base COP 11.613M ajustado por IPC (>USD 8–10M en la práctica); permite crédito y captación a término — la vía de **Nu Colombia** ([leyes.co art. 80 EOSF](https://leyes.co/estatuto_organico_del_sistema_financiero/80.htm)).
- **Licencia bancaria plena**: capital base COP 45.085M ajustado por IPC; **Lulo Bank** se constituyó con COP 105.000M (~USD 28M) y fue el primer banco 100% digital licenciado ([La República](https://www.larepublica.co/finanzas/con-autorizacion-de-la-superfinanciera-a-lulo-bank-inicio-la-era-de-los-neobancos-3194924)). No existe "licencia digital" separada: es licencia plena con trámite ~2 años.

### Perú — SBS

Capitales mínimos actualizados trimestralmente por IPM (ene–mar 2025): **banco S/ 33,09M (~USD 8,9M)**, **financiera S/ 16,64M (~USD 4,5M)**, empresa de créditos S/ 1,5M ([Infobae](https://www.infobae.com/peru/2025/01/10/sbs-reduce-capital-minimo-de-empresas-supervisadas-las-que-necesitan-s15-millones-para-operar/)). La **EEDE** (Ley 29985 de dinero electrónico) exige **~S/ 3M (~USD 0,8M)**; permite emitir dinero electrónico y wallets pero **no otorgar crédito** ([SBS](https://www.sbs.gob.pe/supervisados-y-registros/empresas-supervisadas/directorio-de-empresas-de-servicios-complementarios-y-conexos/empresas-emisoras-de-dinero-electronico); [BCRP — Ley 29985](https://www.bcrp.gob.pe/transparencia/datos-generales/marco-legal/ley-del-dinero-electronico.html)). Trámite típico: 12–24 meses.

### Argentina — BCRA

No hay "licencia de neobanco": el vehículo estándar es el **PSPCP** (PSP que ofrece cuentas de pago), un **registro** —no licencia prudencial— ante el BCRA ([BCRA](https://www.bcra.gob.ar/en/registering-in-the-payment-service-provider-registry/)). Obligaciones núcleo: **100% de los fondos de clientes en cuentas a la vista en bancos locales**, separados de fondos propios; inversión en FCI money market solo a pedido del cliente ([TRSyM](https://www.trsym.com/reglamentacion-de-los-proveedores-de-servicios-de-pago/)). La **CVU** hace interoperables las cuentas de pago con el sistema bancario.

Novedades 2025–2026 (Comunicaciones "A" 8206/2025 y 8432/2026): Oficial de Cumplimiento ante la UIF obligatorio, y el modelo **"PSPCP as a Service"** exige autorización previa del BCRA por cada tomador ([Allende & Brea](https://allende.com/fintech/el-banco-central-introduce-nuevas-regulaciones-sobre-proveedores-de-servicios-de-pago-05-14-2026/)). Para prestar o captar depósitos a escala se requiere licencia de entidad financiera (Ley 21.526 — vía de Ualá al comprar Wilobank).

### Chile — CMF (Ley Fintech 21.521)

La Ley Fintech (2023) creó el **Registro de Prestadores de Servicios Financieros**, con requisitos de capital, garantías y gobernanza según la **NCG 502** ([CMF Educa](https://www.cmfchile.cl/educa/621/w3-article-85048.html); [Anguita Osorio](https://www.anguitaosorio.cl/es/ncg-502/)). Para un neobanco de cuentas + tarjeta, la figura operativa es el **emisor no bancario de tarjetas de prepago**: autorización expresa de la CMF, **capital mínimo UF 25.000 (~USD 1M)** más reservas de liquidez; marco modernizado por la NCG 541 (jul-2025) ([CMF](https://www.cmfchile.cl/portal/principal/623/w4-article-47006.html); [Carey](https://www.carey.cl/cmf-actualiza-marco-normativo-sobre-emisores-no-bancarios-y-operadores-de-tarjetas-de-pago/)). Pomelo ya está autorizado como emisor no bancario, habilitando BaaS local.

---

## 2. Tabla comparativa — Ruta A: licencia "mínima viable" por país

| País | Licencia | Regulador | Capital mínimo (≈USD) | Tiempo realista | Permite / No permite |
|---|---|---|---|---|---|
| Brasil | IP emisora + SCD | BACEN | ~2M (nueva regla, escalonada a 2028) + ~200K SCD | 9–18 meses | ✅ Cuentas, tarjetas, PIX, crédito con capital propio · ❌ depósitos remunerados |
| México | IFPE | CNBV | ~235K | **~781 días (~2 años)** | ✅ Wallet/cuentas de pago · ❌ crédito e intereses |
| México | SOFIPO (compra) | CNBV | desde ~47K prudencial (compra: millones) | 12–18 meses (cambio de control) | ✅ Captación de ahorro + crédito (topes por nivel) |
| Colombia | SEDPE | SFC | ~2M | 12–18 meses | ✅ Depósitos electrónicos, débito, pagos · ❌ crédito |
| Colombia | Cía. de Financiamiento | SFC | >8–10M | ~24 meses | ✅ Crédito + CDT · ❌ cuenta corriente |
| Perú | EEDE | SBS | ~0,8M | 12–24 meses | ✅ Dinero electrónico/wallet · ❌ crédito |
| Perú | Financiera / Banco | SBS | 4,5M / 8,9M | 24+ meses | ✅ Captación + crédito / banca plena |
| Argentina | PSPCP (registro) | BCRA | Sin capital prudencial relevante | **3–6 meses** | ✅ Cuentas de pago con CVU, tarjetas · ❌ crédito propio, depósitos |
| Chile | Emisor prepago no bancario | CMF | ~1M + liquidez | 12–18 meses | ✅ Cuentas prepago + tarjetas · ❌ crédito, depósitos |

---

## 3. Requisitos transversales (todas las jurisdicciones)

- **Gobierno corporativo**: directorio "fit and proper", estructura de riesgos, auditoría interna; en Brasil y México el plan de negocios y la gobernanza son el corazón de la evaluación.
- **AML/KYC**: estándares GAFI/FATF en todos los países; **oficial de cumplimiento registrado obligatorio** (UIF en Argentina, CNBV/UIF en México, Circular 3.978 en Brasil, SBS-UIF en Perú).
- **Protección de datos**: LGPD (BR), LFPDPPP (MX), Ley 1581 (CO), Ley 29733 (PE), Ley 25.326 (AR), Ley 21.719 (CL, vigencia 2026).
- **Ciberseguridad prudencial**: México exige reporte de incidentes en 72 h desde 2026; Brasil Res. 4.893/BCB 85; Chile vía NCG 502/541.
- **Protección al consumidor financiero**: CONDUSEF (MX), SFC (CO), Indecopi/SBS (PE), SERNAC/CMF (CL), BACEN/Procon (BR), transparencia BCRA (AR).
- **Corresponsalía**: figura legal en todos los países para cash-in/cash-out mediante agentes — clave para operar sin sucursales.

---

## 4. Ruta B: BaaS / banco aliado

**Estructura legal**: el proveedor licenciado (IP, EEDE, IFPE, emisor prepago o banco patrocinador) responde ante el regulador; el neobanco opera su marca como programa. En Brasil esto se formalizó con la **Resolução Conjunta nº 16/2025**, que disciplina el BaaS y clarifica responsabilidades ([Finsiders](https://finsidersbrasil.com.br/noticias-sobre-fintechs/banking-as-a-service/marco-regulatorio-de-baas-poe-fim-a-arranjos-informais/)). Tendencia regional: los reguladores están **formalizando y endureciendo** el BaaS, no prohibiéndolo.

**Proveedores principales 2026:**

| Proveedor | Cobertura | Nota |
|---|---|---|
| **Pomelo** | AR, BR, MX, CO, CL, PE | Único con emisión local multi-país hispano; licencia IP en Brasil y emisor prepago en Chile; USD 158M levantados |
| **Dock** | Brasil + 11 países | El mayor BaaS brasileño; +400 clientes (C6, Bitz, Caju); 38M cuentas activas |
| **Fitbank, Swap, Celcoin, Conductor/Caradhras, Grafeno** | Brasil | Ecosistema BaaS local profundo |
| **Bancos patrocinadores** (BV, BTG, Itaú, Santander) | Brasil y región | BaaS bancario con captación |
| **Galileo, Marqeta, Rapyd** | Global con presencia LatAm | Mejor para programas multi-región |
| **Prometeo, Belvo** | Regional | Open banking / datos, no emisión |

**Costos típicos** (raramente publicados): fee de plataforma **USD 1.000–25.000/mes**, setup USD 20K–100K+, ~USD 0,10/cuenta/mes, USD 3–7 por tarjeta física, fees por transacción, más **revenue share de interchange ~70/30** a favor de la fintech ([Boldrails](https://boldrails.com/resources/baas-pricing-transparency); [Synctera](https://www.synctera.com/post/interchange-revenue-guide)). El all-in con 50.000 usuarios rara vez baja de seis cifras anuales, pero el BaaS recorta el costo de infraestructura/licencias **60–70%** y comprime el time-to-market de años a semanas ([Coruzant](https://coruzant.com/fintech/banking-as-a-service/)).

---

## 5. Comparativa Ruta A vs Ruta B

| Dimensión | Ruta A: licencia propia | Ruta B: BaaS |
|---|---|---|
| Tiempo a lanzamiento | 12–36 meses | **2–6 meses** |
| Costo inicial | USD 1–5M (capital + legal + core + cumplimiento); banco pleno USD 10–40M+ | USD 150K–500K primer año |
| Control de producto | Total: pricing, crédito, tesorería, float | Limitado al catálogo del proveedor |
| Margen unitario | Alto (float, spread, 100% interchange) | Recortado (~30% del interchange + fees) |
| Riesgo principal | Regulatorio directo, capital atrapado | **Dependencia crítica del proveedor** |

### La lección Synapse (obligatoria para la Ruta B)

La quiebra de Synapse (EE.UU., 2024) dejó un hueco de hasta **USD 95M** entre los fondos en bancos y los saldos que los usuarios creían tener ([Banking Dive](https://www.bankingdive.com/news/5-lessons-learned-from-synapses-collapse/731543/); [InnReg](https://www.innreg.com/blog/synapse-baas-fintech-bankruptcy-and-collapse)). Mitigaciones que AIBank debe implementar desde el día 1:

1. **Ledger propio de reconciliación** — nunca depender solo del ledger del BaaS; reconciliación diaria contra el banco/proveedor.
2. **Plan de contingencia y portabilidad** contractual (derecho a migrar cuentas y datos).
3. **No depender de un solo proveedor/banco patrocinador** al ganar escala.

### Regla práctica de transición

**BaaS para validar mercado hasta ~100.000–300.000 usuarios; licencia propia cuando el interchange cedido (~30%) + fees supere el costo anualizado de mantener la licencia (típicamente USD 1–2M/año en cumplimiento).**

---

## 6. Open Finance por país

| País | Estado 2026 | Implicación para un entrante |
|---|---|---|
| Brasil | Obligatorio y maduro: +128M consentimientos activos (dic-2025) | Iniciación de pagos (ITP) y datos para underwriting disponibles ya |
| Colombia | **Obligatorio** por Decreto 0368 de 2026 (abril) | APIs estandarizadas; portabilidad financiera |
| Chile | Ley 21.521; entrada en vigor **2027** | Posicionarse antes de la apertura |
| México | Regulación secundaria **incompleta** (8 años de retraso) | Solo datos abiertos operativos; usar agregadores privados (Belvo) |
| Perú / Argentina | Sin mandato integral | Interoperabilidad de wallets (PE) y CVU (AR) como apertura de facto |

Fuentes: [Ozone API](https://ozoneapi.com/blog/the-status-of-open-finance-in-latin-america-in-2025/); [Latinia](https://latinia.com/en/resources/open-finance-in-latam-regulatory-map-2026); [El Cronista](https://www.cronista.com/colombia/finanzas-y-economia/que-es-el-open-finance-obligatorio-que-colombia-aprobo-y-como-afecta-a-los-usuarios/); [iupana](https://iupana.com/open-finance-en-latam-regulacion-2025/).

**Punto clave**: el open finance es **asimétricamente favorable al entrante** — obliga a los incumbentes a abrir datos que el neobanco usa para scoring y portabilidad; las obligaciones propias solo pesan al ganar escala.

---

## 7. Estrategia de entrada multi-país recomendada

**Qué hicieron los ganadores:**
- **Nubank**: licencia propia temprana en Brasil (IP + financiera); en México entró como SOFIPO y fue la primera autorizada a convertirse en banco múltiple; en Colombia tramita Compañía de Financiamiento; aval inicial de la OCC en EE.UU. (2026) ([Nu](https://international.nubank.com.br/company/nu-mexico-receives-banking-license-approval-paving-the-way-for-product-portfolio-expansion-and-increased-financial-inclusion/)).
- **Ualá**: PSP en Argentina, luego **compró entidades licenciadas** (Wilobank AR, ABC Capital MX) para saltar la fila regulatoria.
- **Global66**: mosaico de licencias ligeras — SEDPE (CO), EEDE (PE), emisor (CL) — y evalúa una licencia bancaria internacional ([LatamFintech](https://www.latamfintech.co/articles/fintech-chilena-global66-busca-una-licencia-bancaria-internacional-para-expandir-sus-servicios-financieros-en-multiples-paises)).

**Secuencia recomendada para AIBank:**

1. **Validar en Argentina (PSPCP, 3–6 meses, casi sin capital) o Brasil vía BaaS.** Brasil es el mercado más grande y con mejor infraestructura (PIX, open finance), pero exige más capital desde 2026.
2. **Solicitar licencia propia en Brasil (IP + SCD) en paralelo al lanzamiento BaaS** — el trámite de ~1 año se amortiza con el tamaño del mercado; presentar antes de que la transición de capital 2026–2028 encarezca el ticket.
3. **México: comprar una SOFIPO o IFPE ya autorizada** (ruta Nubank/Ualá) en lugar de esperar ~781 días; o lanzar sobre BaaS (Pomelo/Galileo) mientras tanto.
4. **Colombia y Perú**: entrar con SEDPE (~USD 2M) y EEDE (~USD 0,8M); escalar a Compañía de Financiamiento/Financiera solo cuando el crédito sea el motor.
5. **Chile**: emisor de prepago no bancario o BaaS sobre Pomelo; posicionarse para el open finance 2027.
