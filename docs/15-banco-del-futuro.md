# 15 — «El banco del futuro»: qué se sostiene y qué tomamos

> Revisión de la investigación «El banco del futuro: visión estratégica 2027–2035» (corte 27
> de octubre de 2026), que nos compartieron. Tres cosas: **corroborar** sus afirmaciones con
> las fuentes, **contrastarla** con lo que Aerarium ya construyó, y **convertir sus ideas en
> features** del backlog. Octubre 2026.

## Resumen

La tesis central se sostiene y nos favorece: el banco pasa de vender productos a **gestionar
resultados financieros**, con agentes que actúan **dentro de límites explícitos** y una
**capa de control** común (consentimiento, identidad del agente, políticas, aprobación
humana). Aerarium ya tiene piezas de esa capa que el documento describe como apuesta:

- mandatos con topes para pagos de agentes (modelo B);
- confirmación reforzada con el monto y el destinatario firmados;
- crédito con decisión explicable y expediente archivado;
- teléfono vinculado al chip.

De las 13 afirmaciones verificables, **9 se confirman**, 2 se confirman con matices y 2 no
se pudieron sostener tal como están escritas (sección 1). Ninguna de las que fallan cambia la
tesis.

Lo que tomamos son **12 features** (sección 3), ordenadas por lo que un neobanco retail en
Perú puede hacer ya con lo construido. Lo que no tomamos todavía —pymes, seguros, inversión,
dinero tokenizado— queda como exploración, con el motivo (sección 4).

---

## 1. Corroboración

| # | Afirmación del documento | Veredicto | Lo que dice la fuente |
|---|---|---|---|
| 1 | Banco de Inglaterra, 26/10/2026: el 80 % tiene agentes de IA desplegados y solo el 2 % con autonomía amplia | ✅ con matiz | Correcto, pero el 80 % es de las **105** entidades que completaron el cuestionario completo (de 196), y los agentes sobre todo **recomiendan**: los humanos deciden y ejecutan. Como mucho el 9 % espera que un agente mueva pagos o cambie límites por sí solo ([BoE][boe], [Resultsense][boe-2]) |
| 2 | FSB, 15/10/2026: recomendaciones de adopción responsable de IA | ✅ | Informe final con **12 prácticas** (gobierno, ciclo de vida, supervisión humana), voluntarias, publicado a mediados de octubre tras consulta de junio ([FSB consulta][fsb], [HSF Kramer][fsb-2]) |
| 3 | Santander y Mastercard, marzo 2026: pago ejecutado por un agente de IA | ✅ | 2 de marzo de 2026, con Mastercard Agent Pay, en entorno controlado pero sobre la infraestructura real, dentro de límites que el cliente fijó antes ([Santander][santander]) |
| 4 | Visa, octubre 2026: pilotos de pagos B2B por agentes autorizados | ✅ | Visa, BILL y Billtrust, 20 de octubre: el agente **solo ejecuta** pagos de facturas ya aprobados por el comprador, registrado con el Trusted Agent Protocol de Visa. Sin cifras de volumen ([Visa][visa]) |
| 5 | BIS: dinero tokenizado interoperable conservando las garantías del sistema | ✅, otra fecha | Es el capítulo III del Informe Anual del BIS de **junio** de 2026 («Anchoring trust in money»): libro mayor unificado con dinero de banco central como ancla, depósitos tokenizados, y advertencia sobre stablecoins ([BIS][bis]) |
| 6 | BCG y OpenAI, marzo 2026: de autoservicio a servicio asistido por agentes | ✅ | «How retail banks can put agentic AI to work», 9 de marzo de 2026. La cifra de hasta +30 % de rentabilidad es proyección del propio informe ([BCG][bcg]) |
| 7 | McKinsey, Global Banking Annual Review 2026: redes de plataformas, hiperpersonalización, ecosistemas | ⚠️ parcial | El informe 2026 existe («Precision with speed», mayo 2026), pero **no pudimos confirmar** que plantee redes de plataformas; la hiperpersonalización es del informe **2025** ([Santander sobre McKinsey 2025][mck]) |
| 8 | McKinsey, septiembre 2026: el valor en pagos se desplaza a la capa de confianza e identidad de máquinas | ❌ no encontrado | No hallamos ese informe. La idea sí aparece en el **white paper de Mastercard** del 30 de septiembre de 2026 sobre confianza en comercio agéntico (identidad, intención, controles, ejecución confiable) ([Mastercard][mc]) |
| 9 | J.D. Power, mayo 2026: los clientes piden asesoría práctica pero usan poco las herramientas | ✅ | Estudio de salud financiera publicado el 27 de mayo de 2026: los bancos invirtieron en herramientas y el uso no acompaña ([J.D. Power][jdp]) |
| 10 | SBS Perú, 19/10/2026: segundo bloque normativo de Finanzas Abiertas en consulta | ✅, fecha sin confirmar | El bloque II (infraestructura digital, marco de confianza, estándares, seguridad y calidad) está en consulta; el bloque I salió el 2 de octubre (Res. SBS 02433-2026). No confirmamos el día 19 ([Cuatrecasas][sbs], [El Peruano][sbs-2]) |
| 11 | Pagos instantáneos en euros obligatorios desde octubre de 2025 | ✅ | Reglamento (UE) 2024/886: enviar transferencias instantáneas y verificar el beneficiario desde el 9 de octubre de 2025 en la zona euro |
| 12 | Euro digital: emisión no aprobada; piloto en 2027 | ✅ | Piloto interno de 12 meses en el segundo semestre de 2027, condicionado a aprobar el reglamento a fines de 2026; primera emisión posible en 2029 ([Cointelegraph][eur]) |
| 13 | DORA aplica desde enero de 2025 | ✅ | Desde el 17 de enero de 2025 |

Además, **BBVA «The Frame»** (marco para crear y gobernar agentes) y la ampliación de su
asesor **Blue** en 2027 se confirman en prensa y en la web del banco ([BBVA][bbva]). No
verificamos el día exacto (6 de octubre).

**Lectura:** el documento acierta en las tendencias y en su prudencia («son pilotos, no
adopción masiva»). Donde falla es en atribuir ideas a la fuente equivocada (punto 8) o a
otro año (7). Para nuestro uso no importa: las ideas se sostienen con otras fuentes.

---

## 2. Dónde ya está Aerarium

| Idea del documento | En Aerarium |
|---|---|
| Pagos delegados bajo autorizaciones y límites explícitos (módulo 06, caso 3) | **Modelo B**: mandatos con tope por pago, diario y mensual, vigencia y revocación; el cliente los ve agrupados por plataforma |
| Autonomía proporcional al riesgo (sección 8) | Mandatos = nivel 3 («ejecutar bajo mandato»). **Confirmación reforzada**: destinatario nuevo o ≥ 200 exige firmar esa operación con la cara |
| Identidad del agente | A medias: el mandato está atado a la **plataforma** (Mercatus), no al agente concreto. Es la feature F3-05 |
| Decisiones explicables y trazables (FSB) | Crédito: expediente completo del motor archivado y motivos sin puntaje. **No hay** un registro común para el resto de decisiones automáticas: F3-07 |
| Crédito con evaluación responsable (Smart Credit) | Motor de riesgo con historial propio, empezar bajo y crecer; ciclo con mora y desbloqueo |
| Identidad, privacidad y protección (módulo 04) | Passkeys, teléfono vinculado al chip, Face ID; permisos de pago revocables. Falta **una sola pantalla** para todo lo que tiene acceso: F3-06 |
| Plataforma de agentes con capa de control común (módulos 15 y 20) | Piezas sueltas (mandatos, confirmación, límites en el ledger). Unificarlas es F3-04 |

El documento propone una arquitectura en la que la IA **no sustituye** al core ni a los
motores de riesgo, sino que opera sobre ellos con validaciones deterministas. Es la que ya
seguimos: el límite de la tarjeta, la mora y los topes los hace cumplir el ledger, no una
capa de IA.

---

## 3. Features nuevas

Prioridad: **alta** = se puede construir ya sobre lo hecho y es diferencial; **media** =
necesita una decisión o una pieza previa; **baja** = útil pero no urgente.
El nivel es el de autonomía del documento (1 observar, 2 recomendar, 3 ejecutar bajo
mandato, 4 alto impacto con humano).

| ID | Feature | Nivel | Prioridad | Por qué / de qué depende |
|---|---|---|---|---|
| F3-01 | **Previsión de saldo y aviso de déficit** («tu tarjeta vence el 16 y te dejaría con −40»): proyecta hasta el próximo ingreso con lo observado y propone opciones | 1–2 | Alta | El caso 1 del documento y su ejemplo exacto. Tenemos lo necesario: movimientos, el detector de ingreso recurrente del motor de riesgo y los estados de cuenta de la tarjeta |
| F3-02 | **Bolsillos**: dinero apartado dentro de la cuenta, entre ellos «apartado para el pago de la tarjeta» | 3 | Alta | Módulo 05. El core ya admite varias cuentas por cliente; falta el concepto de cuenta hija y moverlo en la app |
| F3-03 | **Metas con ahorro automático autorizado**: meta con monto y fecha, regla de aporte con tope que el cliente autoriza y revoca | 3 | Media | Módulo 02 (Life Goals). Depende de F3-02 y de F3-04 para que la regla sea un permiso como los demás |
| F3-04 | **Capa de control común**: un registro de qué puede hacer cada actor —el asistente, una regla de ahorro, el agente de una plataforma— con nivel, alcance, monto y vigencia; el nivel 4 siempre con humano | — | Alta | Sección 8 y la «quinta apuesta» del documento. Unifica mandatos y confirmación reforzada antes de que cada feature invente sus propios permisos |
| F3-05 | **Identidad del agente** (*know your agent*): el mandato se ata al agente concreto, no solo a la plataforma, y cada pago guarda la intención que lo originó; compatible con Mastercard Agent Pay y Visa Trusted Agent Protocol | 3 | Media | Visa y Mastercard ya lo exigen en sus pilotos. Hay que acordarlo con Mercatus (pregunta nueva en el canal) |
| F3-06 | **Centro de permisos y privacidad**: una pantalla con todo lo que tiene acceso a la cuenta —agentes, teléfonos, passkeys, consentimientos de datos— y revocar con un toque | — | Alta | Módulo 04. Hoy está repartido en tres pantallas |
| F3-07 | **Registro de decisiones automáticas y revisión humana**: cada decisión (crédito, bloqueo por mora, confirmación exigida, KYC) con su evidencia y motivo, consultable en el backoffice; cola de revisión para rechazos de crédito y bloqueos | — | Media | FSB (trazabilidad, supervisión humana). Generaliza el expediente de crédito que ya existe |
| F3-08 | **Asistente financiero, nivel 1**: responde sobre los propios datos («¿cuánto gasté en agentes este mes?»), solo lectura, con consentimiento y cada respuesta trazada; nivel 2 (preparar sin ejecutar) después | 1→2 | Media | Módulo 01. Necesita decidir proveedor de modelo y qué datos salen del banco. Reemplaza «asistente IA soporte» del backlog anterior |
| F3-09 | **Cuentas en soles (PEN)** y multimoneda | — | Alta | Módulo 05. Para Perú no es opcional. El core ya admite PEN (las subcuentas de Mercatus la usan), pero el producto del cliente —alta, topes, tarjeta— se abre en USD |
| F3-10 | **Finanzas Abiertas Perú**: API de información de cuentas con consentimiento sobre el OAuth que ya tenemos; seguir la consulta de la SBS y comentar | 1 | Media | Módulo 13. La norma está en consulta (bloques I y II); construir sobre la versión final |
| F3-11 | **Operaciones asistidas**: un agente propone cómo resolver cada partida de la cola de conciliación del backoffice y una persona aprueba | 2 | Baja | Módulo 16. El backoffice y la conciliación existen; es la forma de empezar con agentes internos sin riesgo |
| F3-12 | **Métricas de resultados**: déficits evitados, ahorro generado, permisos revocados, intervenciones humanas, reclamos | — | Baja | Sección 10: medir resultados, no cuántos agentes hay |

**Orden sugerido.** F3-04 primero, porque es la base de las demás. Después F3-01, F3-02 y
F3-06, que se ven en la app y aprovechan lo construido. En paralelo F3-09, que es condición
para operar en Perú.

---

## 4. Lo que no tomamos todavía, y por qué

| Idea | Por qué no ahora |
|---|---|
| AI CFO, flujo de caja y tesorería para pymes (módulos 11–12) | Nuestro segmento hoy es persona natural. Se revisa con la fase de pymes del [doc 03](03-servicios-productos.md) |
| Seguros contextuales y microseguros (módulo 09) | Requiere un socio asegurador y su regulación |
| Inversión y patrimonio (módulo 08) | Exige test de idoneidad y otra licencia o un socio |
| Marketplace de soluciones (módulo 14) | Sin base de clientes no hay a quién distribuir |
| Depósitos tokenizados y dinero programable (módulo 10) | El propio BIS lo plantea como infraestructura entre bancos centrales y bancos. Nada que construir antes de que exista ese riel |
| Finanzas personales ampliamente autónomas (2030–2035) | El documento mismo las ubica en exploración. Nuestro límite: nunca nivel 4 sin humano |

---

## Fuentes

[boe]: https://www.bankofengland.co.uk/report/2026/artificial-intelligence-in-uk-financial-services-2026
[boe-2]: https://www.resultsense.com/news/2026-10-27-boe-fca-ai-survey-2026-agents-third-parties/
[fsb]: https://www.fsb.org/2026/06/fsb-consults-on-sound-practices-for-the-responsible-adoption-of-artificial-intelligence-ai/
[fsb-2]: https://www.hsfkramer.com/it/notes/fintech/2026-posts/fintech-roundup-16-oct-2026
[santander]: https://www.santander.com/en/press-room/press-releases/2026/03/santander-and-mastercard-complete-europes-first-live-end-to-end-payment-executed-by-an-ai-agent
[visa]: https://investor.visa.com/news/news-details/2026/Visa-BILL-and-Billtrust-Launch-First-Live-Pilot-for-Trusted-AI-Agent-Payments-Between-Buyer-and-Supplier-Platforms/default.aspx
[bis]: https://www.bis.org/press/p260623.htm
[bcg]: https://web-assets.bcg.com/pdf-src/prod-live/how-retail-banks-can-put-agentic-ai-to-work.pdf
[mck]: https://www.santander.com/en/press-room/insights/new-strategies-for-new-times-how-banks-can-stay-ahead/
[mc]: https://www.mastercard.com/content/dam/mccom/shared/news-and-trends/stories/2026/trust-for-agentic-commerce-white-paper/Trust%20for%20agentic%20commerce%20white%20paper.pdf
[jdp]: https://www.accountsrecovery.net/2026/06/01/banks-are-investing-in-financial-health-tools-customers-arent-showing-up/
[sbs]: https://www.cuatrecasas.com/es/latam/financiero/art/proyecto-reglamento-sbs-finanzas-abiertas
[sbs-2]: https://elperuano.pe/noticia/307111-que-cambios-propone-la-sbs-para-las-finanzas-abiertas-revisa-el-proyecto-normativo
[eur]: https://cointelegraph.com/news/ecb-digital-euro-could-launch-2029
[bbva]: https://www.bbva.com/es/innovacion/bbva-crea-un-modelo-para-escalar-la-ia-a-todo-el-grupo-e-impulsar-su-transformacion-centrada-en-el-cliente/
