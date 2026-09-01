# 09 — Integración con Mercatus (riel de pago para agentes de IA)

> Análisis del contrato `aibank_integration_contract.md` v0.3 recibido del equipo de
> Mercatus. Define qué hay que construir, qué bloquea y qué hay que renegociar.

## Resumen

Mercatus es una plataforma de pagos donde **agentes de software** se pagan entre sí. Hoy
funciona sobre USDC y sobre un `MockAIBank` en memoria; quieren que AIBank sea su segundo
riel de liquidación real. El contrato pide cinco endpoints REST, OAuth2 y liquidación
síncrona.

**La buena noticia**: el modelo de pago que piden —autorizar y después capturar— es
exactamente el que ya construimos para tarjetas. La contabilidad es la misma: retener
contra una cuenta de pasivo y liquidar después. No hay que inventar el patrón.

**La mala**: hay **dos bloqueantes** que no se resuelven en la capa de integración, y una
fecha que no es alcanzable.

---

## 1. Bloqueante técnico: la unidad monetaria es incompatible

Mercatus opera en **millonésimas (10⁻⁶)**. Todo AIBank opera en **centavos (10⁻²)**.

No es un detalle de conversión. Su caso de uso central son servicios a **$0.001 por
llamada** — en centavos eso es 0,1 centavos, que **no se puede representar**. Redondear
hacia abajo cobra $0.00 y el pago desaparece; redondear hacia arriba cobra diez veces de
más. Cualquier adaptador que "convierta" en el borde pierde dinero de forma sistemática en
exactamente el caso de uso que justifica la integración.

### Opciones evaluadas

| Opción | Veredicto |
|---|---|
| Convertir en el adaptador | ❌ Pierde plata en cada micropago. Es el caso de uso principal, no un borde |
| Sub-ledger aparte en micras | ❌ Fragmenta la fuente de verdad; conciliar dos ledgers con escalas distintas es una fábrica de descuadres |
| Escala por moneda (USD-2 y USD-6 conviviendo) | ❌ Dos escalas para la misma moneda; el primer error de comparación cuesta dinero real |
| **Migrar el ledger a micras (escala 6)** | ✅ **Recomendado** |

### Por qué migrar es lo correcto, no lo conservador

Un banco que quiere atender pagos máquina-a-máquina **tiene que** contar en una unidad más
fina que el centavo. Esa es una decisión de arquitectura, no un parche para un cliente.

Y el momento es ahora: `i64` en micras cubre hasta **9,2 billones de USD**, de sobra. La
migración toca **42 archivos** y ~150 tests, pero **no hay datos de producción**: hoy es
mecánico (multiplicar por 10.000 y renombrar), y después de lanzar sería un proyecto de
meses con riesgo contable.

**Consecuencias que hay que aceptar:**
- Se renombra `amount_minor` → `amount_micros` en el contrato proto. Rompe la compilación
  en Rust, Go, Dart y TypeScript — que es exactamente lo que se busca: ningún llamador se
  queda con la interpretación vieja en silencio.
- La app sigue mostrando 2 decimales al cliente de consumo. La escala es de
  almacenamiento, no de presentación.
- Los topes y límites del catálogo de productos se reexpresan (×10.000).

---

## 2. Bloqueante regulatorio: no se le puede abrir una cuenta a un software

El contrato lo marca como el único punto abierto y con razón, pero la respuesta no es
"revisemos nuestra política": **en ninguna jurisdicción de la región puede abrirse una
cuenta bancaria a nombre de un agente de software**. Las cuentas pertenecen a personas
—naturales o jurídicas— identificadas bajo estándares GAFI. Un agente no es ninguna de las
dos.

### Estructura viable: cuenta ómnibus con sub-cuentas virtuales

La forma en que esto sí funciona, y que usan las plataformas de pago:

- **Mercatus es el titular** de una cuenta maestra, tras KYB de la empresa. AIBank hace su
  diligencia sobre Mercatus, no sobre cada agente.
- Las "cuentas de agente" son **sub-cuentas virtuales** dentro de esa cuenta: registros del
  ledger, no cuentas bancarias. El `acc_...` que devolvemos identifica un sub-ledger.
- El dinero es **legalmente de Mercatus**, no de los agentes.

**Lo que esto le traslada a Mercatus** y hay que dejar por escrito antes de firmar:
- Debe conocer al controlador humano o jurídico de cada agente y responder por él.
- Nosotros monitoreamos AML sobre el flujo agregado y sobre patrones entre sub-cuentas;
  Mercatus responde por la identidad detrás de cada una.
- El `owner_reference` que ya proponen es el enganche correcto para eso — pero debe ser
  **trazable a una entidad real**, no un identificador opaco de agente.

> Esto requiere confirmación de un abogado regulatorio del país donde se opere. El análisis
> de licencias del [doc 02](02-regulacion-licencias.md) aplica: bajo BaaS, además, el
> proveedor licenciado tiene que aceptar esta estructura.

---

## 3. La fecha de sandbox no es alcanzable

El contrato registra **2026-09-02 como "Confirmado por AIBank"** — dos días desde hoy. El
alcance real es:

| Trabajo | Estimación |
|---|---|
| Migración del ledger a micras (42 archivos, ~150 tests) | 3–5 días |
| Servidor OAuth2 con scopes (hoy no existe autenticación real) | 5–8 días |
| Cinco endpoints REST + idempotencia + estados | 5–8 días |
| Apertura de cuentas con saldo configurable en sandbox | 2–3 días |
| Rate limiting documentado | 2 días |
| Endurecimiento y pruebas de integración | 3–5 días |

**Realista: 4–6 semanas** para un sandbox utilizable. Prometer dos días y entregar algo a
medias es peor que renegociar hoy: Mercatus va a escribir su cliente contra lo que
publiquemos, y cambiarlo después cuesta el doble.

**Contrapropuesta sugerida**: sandbox con los tres endpoints del camino de pago
(`authorize`, `capture`, `GET`) en **3 semanas**, apertura de cuentas y reembolso en la
cuarta. Eso les desbloquea escribir el cliente casi de inmediato.

---

## 4. Observaciones sobre el contrato

Puntos donde el documento tiene huecos o donde conviene responder distinto:

| Punto | Observación |
|---|---|
| **Retención de idempotencia 24 h** (§6) | Nuestro ledger conserva las claves **para siempre** por diseño: es más estricto, no menos. Una clave reusada a las 25 h sería rechazada. Proponemos mantenerlo así y que Mercatus derive la clave del `cart_id`, que ya es de un solo uso — con eso la reutilización nunca ocurre |
| **`recipient_mismatch` / `amount_mismatch`** (§7) | Ambiguo. `GET /v1/authorizations/{id}` no recibe lo que el vendedor esperaba, así que no puede compararlo. O el endpoint acepta parámetros de verificación, o esos códigos no le corresponden a AIBank sino al cliente de Mercatus |
| **Expiración de autorizaciones** (§5) | La respuesta trae `expires_at` pero el contrato no define el plazo ni qué pasa al vencer. Hay que fijarlo: sin liberación automática, el dinero de una autorización sin capturar queda inmovilizado. Ya tenemos la maquinaria de retenciones vencidas del adaptador de tarjetas |
| **Reembolso solo total** (§5) | Aceptable para la fase 1, pero operaciones de soporte casi siempre termina necesitando parcial. Conviene diseñarlo desde ya aunque se implemente después |
| **Liquidación síncrona** (§8) | De acuerdo, y por la razón que dan: entre dos sub-cuentas nuestras es una sola transacción de ledger. Nuestro core ya garantiza esa atomicidad |
| **Un `client_id` por integración** (§3) | De acuerdo, pero implica que un token comprometido afecta a **todos** los agentes. Debe ir acompañado de rate limiting por sub-cuenta y de detección de anomalías, no solo del scope |

---

## 5. Plan de implementación

Orden derivado de las dependencias, no de la comodidad:

| # | Feature | Por qué va aquí |
|---|---|---|
| 1 | **Migración del ledger a micras** | Todo lo demás depende de poder representar $0.001. Hacerlo primero y sin datos de producción |
| 2 | **Autenticación OAuth2 con scopes** | Hoy la autenticación es un puerto con sustituto de desarrollo. Es la pieza que falta para exponer cualquier API a un tercero |
| 3 | **Primitiva de autorización en el core** | Generalizar el patrón retención→captura del adaptador de tarjetas a un servicio propio, con expiración y liberación |
| 4 | **API REST de Mercatus** | Los cinco endpoints sobre las piezas anteriores |
| 5 | **Sandbox** | Entorno separado, cuentas con saldo configurable, credenciales autoservicio, rate limiting documentado |
| 6 | **Reembolsos** | Fase 2 del propio contrato |

## 6. Qué hay que resolver antes de escribir código

1. **Confirmar la estructura de cuenta ómnibus** con asesoría regulatoria del país de
   operación, y que el proveedor BaaS la acepte. Bloquea la apertura de cuentas.
2. **Renegociar la fecha del sandbox** con Mercatus.
3. **Aclarar los tres huecos del contrato**: expiración, `*_mismatch` y ventana de
   idempotencia.

Las features 1 a 3 del plan **no dependen de nada de esto** y aportan valor por sí solas —
la migración a micras y la autenticación real son deuda propia que había que pagar igual.
