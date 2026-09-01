# 10 — Puntos pendientes y preguntas para el equipo de Mercatus

> **Documento para compartir con Mercatus.** Respuesta de AIBank al contrato
> `aibank_integration_contract.md` v0.3. Contiene lo que confirmamos, lo que
> contraproponemos y las preguntas que necesitamos responder para cerrar el contrato.
>
> El análisis interno que respalda estas posiciones está en
> [09 — Integración Mercatus](09-integracion-mercatus.md).

## Cómo leer este documento

Cada pregunta trae **por qué importa** y **qué asumimos si no hay respuesta**. Ese
supuesto por defecto es lo que vamos a implementar si el punto no se resuelve, para que
ninguna pregunta abierta detenga el desarrollo. Si el supuesto no les sirve, esa es la
pregunta a responder primero.

Prioridades: **P0** bloquea escribir código de esa parte · **P1** bloquea el sandbox ·
**P2** se puede cerrar durante la integración.

---

## 1. Lo que AIBank confirma sin cambios

| Punto del contrato | Respuesta |
|---|---|
| OAuth2 `client_credentials`, dos scopes (§3) | **Aceptado.** Es lo correcto y coincide con lo que teníamos planificado |
| Cuentas mono-moneda por agente, sin conversión en el riel (§4) | **Aceptado.** `currency_mismatch` se rechaza, no se convierte |
| `account_id` lo asigna AIBank, opaco y estable, prefijo `acc_` (§4) | **Aceptado** |
| Montos en enteros de millonésimas, 10⁻⁶ (§5) | **Aceptado**, con una consecuencia grande de nuestro lado — ver §2 |
| Liquidación síncrona, sin webhook (§8) | **Aceptado**, y por la razón que dan: entre dos sub-cuentas nuestras es una sola transacción de ledger. Nuestro core ya garantiza esa atomicidad |
| `Idempotency-Key` derivada del `cart_id` (§6) | **Aceptado**, y es mejor que un UUID por intento |
| Autorizar y después capturar (§2) | **Aceptado.** Ya tenemos ese patrón funcionando para tarjetas; se generaliza |

## 2. Lo que cambia de nuestro lado por el punto de las millonésimas

No es una objeción, es un aviso de alcance: **todo AIBank cuenta hoy en centavos**. Su
argumento es correcto —un servicio a $0.001 por llamada no se puede representar en
centavos, y convertir en el borde perdería dinero justo en el caso de uso principal— así
que vamos a **migrar el ledger completo a escala 6**, no a poner un conversor en el
adaptador.

Eso implica reescribir el esquema de base de datos, los contratos internos y las pruebas
de los cinco lenguajes del sistema. Es la decisión correcta y el momento correcto (todavía
no hay datos de producción), pero es la razón principal por la que la fecha de sandbox
cambia — ver §7.

---

## 3. Bloqueantes de cumplimiento y estructura de cuentas · P0

El contrato marca esto como "lo único genuinamente abierto" (§11) y tiene razón, pero la
respuesta no es una política interna que podamos ajustar: **en ninguna jurisdicción de la
región puede abrirse una cuenta bancaria a nombre de un agente de software.** Las cuentas
pertenecen a personas naturales o jurídicas identificadas bajo estándares GAFI.

La estructura que sí funciona, y que usan las plataformas de pago, es una **cuenta ómnibus**:
Mercatus es el titular legal de una cuenta maestra tras su KYB, y las "cuentas de agente"
son sub-ledgers virtuales dentro de ella. El `acc_...` identifica un sub-ledger, no una
cuenta bancaria. El dinero es legalmente de Mercatus.

Esto no cambia una línea del protocolo de pago, pero traslada obligaciones a Mercatus. De
ahí estas preguntas.

**Q1 · ¿Mercatus acepta ser el titular legal de la cuenta maestra?** · P0
¿En qué país está constituida la sociedad y bajo qué razón social haríamos el KYB?
*Por qué importa:* define la jurisdicción de la integración y qué proveedor licenciado
puede sostenerla.
*Si no hay respuesta:* no podemos abrir ninguna cuenta, ni en sandbox con dinero real.

**Q2 · ¿Pueden identificar, para cada agente, a la persona natural o jurídica que lo
controla?** · P0
Y comprometerse contractualmente a responder por esa identificación.
*Por qué importa:* nosotros monitoreamos AML sobre el flujo agregado y sobre patrones
entre sub-cuentas; ustedes responden por quién está detrás de cada una. Sin ese reparto no
hay estructura viable.
*Si no hay respuesta:* asumimos que el `owner_reference` es trazable a una entidad real en
sus sistemas y lo dejamos como obligación contractual explícita.

**Q3 · ¿Cuánto tardan en responder un requerimiento de autoridad sobre una sub-cuenta?** · P1
Una orden judicial o un pedido de la unidad de inteligencia financiera llega a AIBank, pero
la identidad está de su lado.
*Por qué importa:* los plazos son legales, no negociables, y el proveedor licenciado va a
exigir un compromiso por escrito.
*Si no hay respuesta:* proponemos 24 horas hábiles para datos de identificación.

**Q4 · ¿De dónde sale el dinero? El contrato no cubre el fondeo.** · P0
Los cinco endpoints mueven saldo entre sub-cuentas, pero ninguno explica cómo entra dinero
al sistema ni cómo sale. En sandbox lo resolvemos con saldos configurables; en producción
un agente no puede pagar con dinero que nunca ingresó.
*Por qué importa:* el fondeo y el retiro son **donde vive todo el riesgo de lavado**. Es
la parte que más escrutinio regulatorio recibe y la que no está especificada.
*Si no hay respuesta:* asumimos que Mercatus fondea la cuenta maestra por transferencia
desde su propia cuenta bancaria y distribuye internamente, sin fondeo directo de terceros
a sub-cuentas — que es la única variante que podemos sostener sin KYC individual.

**Q5 · ¿Los agentes son de Mercatus o de sus clientes?** · P0
Si un tercero opera agentes sobre la plataforma, hay un nivel más de intermediación.
*Por qué importa:* cambia por completo el perfil de riesgo y probablemente la licencia
necesaria.
*Si no hay respuesta:* asumimos agentes operados por clientes de Mercatus, que es el caso
más exigente.

**Q6 · ¿Qué límites esperan?** · P1
Máximo por operación, por sub-cuenta por día, y agregado de la integración.
*Por qué importa:* nuestro catálogo de productos ya tiene topes por producto y los
necesitamos configurados antes de abrir cuentas; además son un control AML, no solo un
parámetro técnico.
*Si no hay respuesta:* sandbox sin límites; producción con topes conservadores que
revisamos con datos reales.

**Q7 · ¿Aceptan que podamos congelar una sub-cuenta sin cortar la integración entera?** · P1
Si un patrón dispara una alerta AML, tenemos que poder bloquear esa sub-cuenta.
*Por qué importa:* el contrato no define qué error devolver en ese caso. Proponemos
`403 account_frozen` como código nuevo.
*Si no hay respuesta:* implementamos `403 account_frozen` y lo documentamos.

> Los puntos Q1, Q2, Q4 y Q5 requieren además confirmación de un abogado regulatorio del
> país de operación y aceptación del proveedor licenciado. Es trabajo nuestro, pero no
> podemos empezarlo sin sus respuestas.

---

## 4. Huecos del contrato · P1

Puntos donde el documento no alcanza para implementar sin adivinar.

**Q8 · ¿Cuánto dura una autorización y qué pasa al vencer?**
La respuesta de `POST /v1/authorizations` trae `expires_at`, pero el contrato no fija el
plazo ni el comportamiento al vencimiento. Además, en el ejemplo de §5 el `expires_at` es
`18:42:00` y el `settled_at` de la captura es `18:41:12` — un margen de 48 segundos. ¿Es
intencional o es solo un ejemplo?
*Por qué importa:* sin liberación automática, una autorización sin capturar inmoviliza
dinero para siempre. Ya tenemos la maquinaria de retenciones vencidas del adaptador de
tarjetas.
*Si no hay respuesta:* **15 minutos**, liberación automática al vencer, y
`409 authorization_expired` al intentar capturar una vencida. Si capturan en el mismo
instante como dicen en §2, 15 minutos les sobra con margen para reintentos.

**Q9 · ¿Cómo distinguen un reintento de captura de una doble captura real?**
El contrato exige `Idempotency-Key` solo en `authorize`. Si un `capture` se corta por
timeout de red y Mercatus reintenta, hoy recibirían `409 already_captured` — indistinguible
de un error de su lógica.
*Por qué importa:* es exactamente el mismo problema de doble cobro que §6 resuelve para
`authorize`, sin resolver para el paso que efectivamente mueve el dinero.
*Si no hay respuesta:* hacemos `capture` idempotente por naturaleza — un segundo `capture`
sobre una autorización ya capturada devuelve **`200` con el mismo `settled_at`**, no `409`.
El `409 already_captured` queda reservado para una captura sobre una autorización anulada o
reembolsada. Nos parece mejor contrato y nos gustaría confirmarlo.

**Q10 · `recipient_mismatch` / `amount_mismatch`: ¿quién compara?**
`GET /v1/authorizations/{id}` no recibe lo que el vendedor esperaba, así que AIBank no
tiene contra qué comparar. O el endpoint acepta parámetros de verificación, o esos códigos
le corresponden al cliente de Mercatus, no a nosotros.
*Por qué importa:* son dos diseños distintos y hay que elegir uno antes de que escriban el
cliente.
*Si no hay respuesta:* aceptamos query params **opcionales** `expected_amount` y
`expected_payee_account_id`; si vienen y no coinciden, devolvemos `422`. Si no vienen, el
`GET` responde normal y la comparación queda del lado del vendedor. Así funcionan las dos
variantes con un solo endpoint.

**Q11 · Ventana de idempotencia: proponemos indefinida, no 24 horas.**
Nuestro ledger conserva las claves de operación **para siempre** por diseño. Es más
estricto que su propuesta, no menos: una clave reusada a las 25 horas sería rechazada en
vez de crear un pago nuevo.
*Por qué importa:* como derivan la clave del `cart_id`, que ya es de un solo uso, la
reutilización legítima nunca debería ocurrir — y si ocurre, queremos que falle ruidosamente.
*Si no hay respuesta:* retención indefinida.

**Q12 · ¿Qué código HTTP esperan en el replay idempotente?**
§6 dice que la respuesta debe ser "la autorización que ya existe", pero no dice si es `201`
(como la original) o `200`.
*Si no hay respuesta:* devolvemos **`200`** con el cuerpo idéntico al original, más un
header `Idempotent-Replay: true` para que sea distinguible en sus logs.

**Q13 · ¿`POST /v1/accounts` es idempotente?**
Si se corta la red al crear una cuenta y reintentan, hoy crearían dos sub-cuentas para el
mismo agente y el saldo quedaría partido.
*Si no hay respuesta:* el par `(owner_reference, currency)` es único; un segundo POST con
el mismo par devuelve la cuenta existente con `200` en vez de crear otra.

**Q14 · Faltan códigos de error para el camino de `authorize`.**
§7 define `404` solo para `authorization_id` inexistente. ¿Qué esperan si la cuenta
pagadora o receptora no existe, o si pagador y receptor son la misma cuenta?
*Si no hay respuesta:* `404 account_not_found` y `422 same_account`.

**Q15 · ¿Reembolso parcial, sí o no?**
El contrato lo define solo total (§5, fase 2). La operación de soporte casi siempre termina
necesitando parcial.
*Por qué importa:* diseñarlo ahora cuesta poco; agregarlo después de que existan datos
cuesta mucho más.
*Si no hay respuesta:* diseñamos el modelo para soportar parcial y exponemos primero solo
el total.

---

## 5. Dimensionamiento y operación · P1

Preguntas que no cambian el contrato pero sí lo que construimos detrás.

**Q16 · ¿Qué volumen esperan?**
Pagos por segundo en régimen y en pico, y cuántas sub-cuentas activas en los primeros
meses.
*Por qué importa:* sin esto no podemos fijar el rate limiting que ustedes piden
documentado (§9) ni dimensionar el core. Un riel para micropagos de agentes puede tener un
perfil de carga muy distinto al de un banco de personas.
*Si no hay respuesta:* dimensionamos para 50 req/s sostenidos y publicamos ese límite.

**Q17 · ¿Qué latencia y disponibilidad necesitan?**
Un agente que paga por llamada de API no puede esperar segundos.
*Si no hay respuesta:* apuntamos a p99 por debajo de 300 ms en el camino
`authorize`+`capture` y lo medimos desde el primer día.

**Q18 · Un solo `client_id` para toda la integración: ¿cómo rotamos el secreto?**
Estamos de acuerdo con el modelo de §3, pero implica que un token comprometido afecta a
**todos** los agentes.
*Por qué importa:* necesitamos poder rotar sin ventana de caída, y ustedes necesitan saber
con cuánto preaviso.
*Si no hay respuesta:* soportamos **dos secretos válidos simultáneamente** durante 7 días y
avisamos con 30 días de anticipación en rotaciones programadas.

**Q19 · ¿Necesitan consultar movimientos de una cuenta?**
No hay endpoint de extracto en el contrato, pero cualquier operación de soporte —de
ustedes o nuestra— lo va a pedir el primer día que un agente reclame un cobro.
*Si no hay respuesta:* agregamos `GET /v1/accounts/{id}/transactions` paginado en la misma
entrega que el resto.

**Q20 · ¿PEN y EUR desde el día 1?**
El contrato lista tres monedas (§1). Si el sandbox puede arrancar solo con USD, la primera
entrega llega antes.
*Si no hay respuesta:* sandbox con USD; PEN y EUR en la segunda entrega.

**Q21 · ¿Salen desde IPs fijas?**
Para restringir por lista blanca además del token.
*Si no hay respuesta:* no aplicamos restricción por IP en sandbox y lo evaluamos para
producción.

---

## 6. Comercial y legal · P2 (pero antes de producción)

**Q22 · El contrato no menciona precio.**
¿Asumen que el riel es gratuito? Un pago intra-banco nos cuesta poco, pero no cero, y el
volumen esperado de micropagos cambia la aritmética por completo.
*Por qué importa:* es una conversación mejor tenerla ahora que después de integrar.
*Si no hay respuesta:* sandbox sin costo; el modelo de producción queda por definir.

**Q23 · ¿Quién retiene el rendimiento sobre los saldos en reposo?**
Si hay saldos parados en la cuenta maestra, generan rendimiento. Es un punto estándar en
cualquier acuerdo de cuenta ómnibus y conviene cerrarlo por escrito.

**Q24 · ¿Hay acuerdo marco de servicios y de tratamiento de datos?**
El `owner_reference` y el `display_name` son datos personales si identifican a un
controlador humano.

---

## 7. Calendario: contrapropuesta

El contrato registra **2026-09-02 como "Confirmado por AIBank"** (§11). Esa fecha no es
alcanzable y preferimos decirlo ahora: ustedes van a escribir el cliente contra lo que
publiquemos, y cambiarlo después cuesta el doble.

El alcance real es la migración del ledger a escala 6, un servidor OAuth2 completo (hoy la
autenticación de AIBank es un puerto con sustituto de desarrollo), la primitiva de
autorización, los endpoints, el sandbox y el rate limiting: **4 a 6 semanas** para algo
utilizable de verdad.

**Lo que proponemos en su lugar:**

| Entrega | Contenido | Plazo |
|---|---|---|
| **E0** | Especificación OpenAPI congelada + servidor de respuestas fijas en la URL de sandbox | **3 días hábiles** |
| **E1** | Camino de pago real: `authorize`, `capture`, `GET`, OAuth2, idempotencia | **3 semanas** |
| **E2** | `POST /v1/accounts` con saldo configurable, extracto, rate limiting documentado | **4 semanas** |
| **E3** | Reembolso, PEN y EUR | por acordar |

**E0 es el punto importante**: con el OpenAPI congelado y un servidor que responda las
formas correctas con datos fijos, Mercatus puede escribir y probar el cliente completo
—incluidos los caminos de error— sin esperar a que el banco esté detrás. Es lo mismo que
ustedes hicieron con `MockAIBank`, pero del lado nuestro y contra la API real.

E2 depende de las respuestas de §3: sin la estructura de cuentas resuelta no podemos abrir
cuentas ni siquiera de prueba, si van a llevar dinero real.

---

## 8. Qué avanza sin esperar respuestas

Para que quede claro que nada de esto detiene el trabajo. Estas tres piezas ya están en
nuestro backlog y no dependen de ninguna pregunta de este documento:

1. **Migración del ledger a millonésimas** — decidida, empieza ya.
2. **Servidor OAuth2 con scopes** — deuda propia que teníamos que pagar igual para exponer
   cualquier API a un tercero.
3. **Primitiva de autorización en el core** — generalizar el patrón retención→captura que
   ya funciona en tarjetas.

Los endpoints, el sandbox y la apertura de cuentas se apoyan sobre esas tres.

---

## 9. Resumen de lo que necesitamos de Mercatus

Ordenado por urgencia, para no leer 24 preguntas antes de saber qué contestar primero:

1. **Q1, Q2, Q4, Q5** — estructura de cuentas y fondeo. Sin esto no abrimos ninguna cuenta.
2. **Aceptación del calendario de §7** — o una contrapropuesta suya.
3. **Q8, Q9, Q10** — los tres huecos que cambian la forma de la API. Conviene cerrarlos
   antes de que escriban el cliente.
4. **Q16** — volumen esperado, para publicar un rate limiting real y no inventado.
5. El resto puede resolverse durante la integración con los supuestos por defecto.
