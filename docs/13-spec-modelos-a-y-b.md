# 13 — Especificación técnica: modelos A y B

> **Documento para compartir con Mercatus.** Cómo integrarse con AIBank en los
> dos modelos, cómo probarlos en el sandbox y qué cambia al pasar a cuentas
> reales.
>
> Sustituye a [doc 11](11-spec-integracion-sandbox.md), que cubría solo el
> modelo A. El análisis de por qué el modelo B es como es está en
> [doc 12](12-modelo-b-iniciacion-de-pagos.md).

## Los dos modelos en una página

|  | **Modelo A — ómnibus** | **Modelo B — cuenta propia** |
|---|---|---|
| Titular de la cuenta | Mercatus | **El cliente final**, que es cliente de AIBank |
| KYC | AIBank sobre Mercatus (KYB) | AIBank sobre **cada persona** |
| De quién es el dinero | Legalmente de Mercatus | **De la persona**, siempre |
| Quién autoriza el pago | Mercatus, con su credencial | **El titular**, con un mandato que otorgó |
| Cuándo sirve | Clientes que no bancarizan con AIBank | Clientes que **ya** son de AIBank |
| Estado | ✅ En sandbox | ✅ En sandbox |

**No compiten: se complementan.** El A es el camino para el grueso de los
clientes de Mercatus. El B es el camino para quien ya tiene cuenta en AIBank, y
es el que evita que Mercatus tenga que custodiar dinero ajeno.

> **Una pregunta para su abogado, y es urgente.** En el modelo A los agentes son
> de sus clientes (Q5) y ustedes fondean la maestra desde su propia cuenta (Q4).
> Eso significa que Mercatus recibe y mantiene dinero de terceros, lo que en Perú
> se parece a emitir dinero electrónico —figura que exige una EEDE. No lo
> afirmamos: lo tiene que responder un abogado regulatorio peruano. Pero conviene
> preguntarlo **antes** del KYB, porque puede cambiar cuál de los dos modelos es
> el principal. El modelo B no tiene este problema.

---

## 1. Lo que comparten

**El contrato de pago es idéntico en los dos modelos.** `authorize`, `capture`,
`refund` y el `GET` son los mismos endpoints, con los mismos códigos de error,
la misma idempotencia y la misma vigencia de 15 minutos.

Lo único que cambia es **de dónde sale la autoridad para mover el dinero**, y eso
se expresa con un solo campo:

```jsonc
POST /v1/authorizations
{
  "payer_account_id": "acc_...",
  "payee_account_id": "acc_...",
  "amount": 1000,
  "currency": "USD",
  "mandate_id": "..."   // ← presente = modelo B · ausente = modelo A
}
```

Es un campo y no dos endpoints a propósito: su cliente HTTP es el mismo, y pasar
de un modelo al otro no le obliga a reescribir el camino de pago.

También son comunes:

- **Millonésimas (10⁻⁶)** en todo importe. `1000` = $0.001.
- **OAuth2 `client_credentials`**, un `client_id` por integración.
- **`Idempotency-Key` obligatoria** en `authorize`, conservada indefinidamente.
- **Identificadores opacos** con prefijo: `acc_`, `auth_`.
- **Límites**: 50 req/s por integración, 10 req/s por cuenta pagadora.

Contrato completo en [`contracts/openapi/mercatus-v1.yaml`](../contracts/openapi/mercatus-v1.yaml).

---

## 2. Modelo A — cuenta ómnibus

Sin cambios respecto de lo ya entregado. El flujo, resumido:

```bash
# 1. Abrir la sub-cuenta del agente (idempotente por owner_reference + currency)
curl -X POST "$BASE/v1/accounts" -H "Authorization: Bearer $TOKEN" \
  -d '{"owner_reference":"agent_7c1e9a","currency":"USD","initial_balance":100000}'
# → {"account_id":"acc_0f8289...","currency":"USD","status":"active"}

# 2. Autorizar — sin mandate_id
curl -X POST "$BASE/v1/authorizations" -H "Authorization: Bearer $TOKEN" \
  -H 'Idempotency-Key: cart_demo_001' \
  -d '{"payer_account_id":"acc_0f8289...","payee_account_id":"acc_7e7ef7...",
       "amount":1000,"currency":"USD"}'

# 3. Capturar
curl -X POST "$BASE/v1/authorizations/$AUTH/capture" -H "Authorization: Bearer $TOKEN"
```

Detalle completo y respuestas reales en [doc 11 §3](11-spec-integracion-sandbox.md).

---

## 3. Modelo B — pago sobre la cuenta del cliente

Cuatro pasos. Los dos primeros ocurren **una vez por permiso**; los dos últimos,
en cada pago.

### 3.1 Mercatus pide permiso

```bash
curl -X POST "$BASE/v1/consent-requests" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "currency": "USD",
    "max_per_operation": 5000000,
    "max_total": 50000000,
    "purpose": "Pagos del agente de investigación de Acme S.A."
  }'
```

```json
{
  "consent_request_id": "9c2f1e88-...",
  "handoff_code": "csr_kQ8vN2...",
  "status": "pending",
  "expires_at": "2026-09-01T20:57:00Z"
}
```

**Nótese lo que la petición NO lleva: la cuenta.** Mercatus dice cuánto necesita
y para qué; **el titular elige** sobre qué cuenta lo concede. Así una plataforma
no aprende identificadores de cuenta antes de tener permiso, ni puede sondear qué
cuentas existen probando peticiones.

`purpose` es obligatorio y se muestra literalmente en la pantalla del banco. Un
texto vago convierte la decisión del titular en una que no puede tomar bien.

La solicitud vive **una hora**.

### 3.2 El titular concede, en la app de AIBank

Mercatus le entrega el `handoff_code` al cliente —un enlace, un QR— y este lo
abre en su app de AIBank. Ahí:

1. Se autentica **contra el banco**, con su passkey.
2. Ve quién pide (el nombre registrado de la integración, no uno que ella
   elija), para qué, y cuánto.
3. Elige la cuenta, y puede **bajar** los topes y acortar la vigencia. Nunca
   subirlos.

Al aprobar nace el **mandato**. Mercatus lo descubre consultando:

```bash
curl "$BASE/v1/consent-requests/$REQUEST_ID" -H "Authorization: Bearer $TOKEN"
```

```json
{
  "consent_request_id": "9c2f1e88-...",
  "status": "approved",
  "mandate_id": "4a7b1c2d-...",
  "account_id": "acc_3f9e8d7c..."
}
```

> Se consulta, no se notifica. Un webhook obligaría a Mercatus a exponer un
> endpoint y a AIBank a firmar y reintentar. Para una decisión que tarda lo que
> tarda una persona en mirar su teléfono, consultar es más simple para las dos
> partes y deja un endpoint público menos que defender.

### 3.3 Pagar

Idéntico al modelo A, más el `mandate_id`:

```bash
curl -X POST "$BASE/v1/authorizations" -H "Authorization: Bearer $TOKEN" \
  -H 'Idempotency-Key: cart_demo_002' \
  -d '{
    "mandate_id": "4a7b1c2d-...",
    "payer_account_id": "acc_3f9e8d7c...",
    "payee_account_id": "acc_7e7ef7...",
    "amount": 1000,
    "currency": "USD"
  }'
```

Dos reglas que conviene conocer:

- **El `payer_account_id` tiene que coincidir con el del mandato.** No lo
  corregimos en silencio: si su cliente cree estar pagando desde otra cuenta, es
  un error suyo que conviene que vea.
- **El receptor tiene que ser una cuenta de Mercatus.** Un mandato autoriza a
  *sacar* dinero de la cuenta del titular, nunca a elegir libremente dónde
  termina. Sin esta regla, un permiso otorgado para pagarle a un agente serviría
  para mandar el dinero a cualquier parte.

`capture`, `refund` y el `GET` funcionan exactamente igual que en el modelo A.

### 3.4 Consultar el permiso

```bash
curl "$BASE/v1/mandates/$MANDATE_ID" -H "Authorization: Bearer $TOKEN"
```

```json
{
  "mandate_id": "4a7b1c2d-...",
  "account_id": "acc_3f9e8d7c...",
  "currency": "USD",
  "status": "active",
  "max_per_operation": 5000000,
  "max_total": 20000000,
  "consumed": 8000000,
  "remaining": 12000000,
  "expires_at": "2026-10-01T20:57:00Z"
}
```

Conviene consultarlo: saber cuánto queda permite pedir una ampliación **antes**
de que un pago falle delante de un usuario.

`consumed` no es un contador que llevemos aparte — se **deriva** de las
autorizaciones vivas del mandato. Una retención sin capturar ya consume el tope
(el dinero está comprometido); liberarla o reembolsarla lo devuelve solo.

---

## 4. Lo que el titular puede hacer y ustedes no

Estas operaciones ocurren en el canal del cliente, no en la API de socio. Se
listan aquí para que sepan qué esperar:

| El titular puede | Efecto sobre ustedes |
|---|---|
| **Rechazar** la solicitud | Nunca nace el mandato; `status: rejected` |
| **Conceder menos** de lo pedido | El mandato tiene los topes de él, no los suyos |
| **Revocar** en cualquier momento | Los pagos nuevos fallan con `403 mandate_revoked` |
| Ver el consumo de cada permiso | — |

**Revocar no cancela las retenciones vivas.** Del otro lado puede haber un
vendedor que ya entregó lo que se le pagó, y dejarlo sin cobro sería trasladarle
un problema que no es suyo. La exposición queda acotada por los 15 minutos de
vigencia de la retención.

---

## 5. Errores del modelo B

A la tabla del modelo A ([doc 11 §7](11-spec-integracion-sandbox.md)) se agregan
estos. Ninguno reemplaza a los existentes.

| HTTP | `error` | Cuándo | Qué hacer |
|---|---|---|---|
| 404 | `consent_request_not_found` | La solicitud no existe, venció, o es de otra integración | Pedir una nueva |
| 404 | `mandate_not_found` | El mandato no existe o es de otra integración | Pedir consentimiento |
| 403 | `mandate_revoked` | El titular retiró el permiso | Pedir consentimiento de nuevo |
| 403 | `mandate_expired` | La vigencia terminó | Pedir consentimiento de nuevo |
| 403 | `mandate_account_not_covered` | El pagador declarado no es el del mandato | Corregir el `payer_account_id` |
| 422 | `mandate_limit_exceeded` | El pago supera lo autorizado | Pedir una ampliación, o cobrar menos |

**`422 mandate_limit_exceeded` no es `402 insufficient_funds`**, y la distinción
importa: uno significa que hay dinero pero falta permiso —lo arregla el titular
ampliando el mandato—, el otro que falta dinero —lo arregla fondeando. Colapsarlos
dejaría a su cliente sin saber a quién recurrir.

---

## 6. Escenarios de prueba del modelo B

A los 20 del modelo A se suman estos. Todos están verificados de nuestro lado.

| # | Escenario | Esperado |
|---|---|---|
| 21 | Flujo completo: pedir → aprobar → pagar → capturar | `201` → `201` → `201` → `200` |
| 22 | Pedir sin `purpose` | `400 malformed_request` |
| 23 | Consultar una solicitud aún no resuelta | `200 status: pending` |
| 24 | Consultar una solicitud de otra integración | `404 consent_request_not_found` |
| 25 | Pagar por encima del tope, con saldo de sobra | `422 mandate_limit_exceeded` |
| 26 | Pagar tras la revocación del titular | `403 mandate_revoked` |
| 27 | Pagar declarando otra cuenta pagadora | `403 mandate_account_not_covered` |
| 28 | Usar un mandato de otra integración | `404 mandate_not_found` |
| 29 | Consultar el consumo tras un pago | `consumed` y `remaining` coherentes |
| 30 | Aprobar dos veces la misma solicitud | La segunda, `409` |

Y uno que **debe fallar**, y que probamos nosotros: **una plataforma no puede
conceder su propio permiso**. El token de socio no vale en el canal del titular;
la aprobación exige que una persona se autentique con su dispositivo. Si eso
funcionara, el mandato no probaría que nadie autorizó nada.

---

## 7. Del sandbox a cuentas reales

Qué cambia y qué no, cuando dejemos de simular.

**No cambia:** ningún endpoint, ningún código de error, ningún formato. El
cliente que escriban contra el sandbox es el que corre en producción.

**Cambia:**

| | Sandbox | Producción |
|---|---|---|
| `initial_balance` al abrir cuenta | Disponible | **Rechazado.** El dinero entra por fondeo real |
| Cuentas del modelo B | Las creamos nosotros para probar | Personas con su KYC hecho |
| Dinero | De prueba | Real |
| Base URL y credenciales | De sandbox | Distintas; el `iss` del token cambia y se verifica |

Un token de sandbox **no sirve en producción** y viceversa. Es deliberado.

**Lo que falta para producción, y no depende de ustedes:** AIBank todavía no
tiene licencia ni proveedor BaaS contratado en Perú. Hasta que eso se cierre,
ningún modelo mueve dinero real. Es el bloqueante principal y es nuestro.

---

## 8. Qué garantiza cada modelo

Vale la pena que esto quede explícito, porque es lo que se defiende en una
disputa.

**Modelo A.** El dinero es de Mercatus. AIBank hace diligencia sobre Mercatus;
Mercatus responde por la identidad detrás de cada sub-cuenta (Q2), con respuesta
en 24 horas hábiles a un requerimiento de autoridad (Q3). Si un cliente de
Mercatus reclama, el interlocutor es Mercatus.

**Modelo B.** El dinero es del cliente y AIBank lo conoce directamente. Ante "yo
nunca autoricé ese pago" mostramos el mandato: qué persona se autenticó, contra
qué dispositivo, qué vio en pantalla, qué topes fijó y cuándo. Un `scope` no
probaría nada de eso.

Perú **no tiene marco de iniciación de pagos** —no hay open finance obligatorio,
a diferencia de Brasil—, así que no existe un régimen que reparta la
responsabilidad entre banco e iniciador. El mandato es lo único que la reparte, y
por eso su diseño no es burocracia: es la protección de las dos partes.

---

## 9. Pendientes

**Nuestro:**

1. **Pantallas de consentimiento en la app.** El backend está completo y probado;
   falta la interfaz que ve el titular. Hasta entonces el modelo B se puede
   ejercitar por API pero no con un usuario real.
2. **Licencia o proveedor BaaS en Perú.** Bloquea producción de los dos modelos.
3. **Reembolso parcial** en la API; el modelo del core ya lo soporta.
4. **PEN y EUR**, según lo acordado para la segunda entrega.
5. **Fondeo y retiro**: el contrato no los cubre y es donde vive el escrutinio
   regulatorio. Hay que diseñarlos antes de mover dinero real.

**De ustedes:**

1. **La consulta al abogado sobre custodia de fondos de terceros** en el modelo A
   (ver el recuadro del principio). Es lo que puede cambiar el plan.
2. Constitución de Mercatus Technologies S.A.C. y KYB.
3. El compromiso contractual sobre `owner_reference`.
4. Decidir si el modelo B les sirve como camino de entrada para los clientes que
   ya bancaricen con nosotros, una vez que existan.
