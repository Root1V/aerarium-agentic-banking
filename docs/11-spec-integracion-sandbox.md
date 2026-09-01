# 11 — Datos de integración del sandbox

> **Documento para compartir con Mercatus.** Todo lo que hace falta para conectar
> el cliente HTTP contra el sandbox de AIBank y reemplazar `MockAIBank`.
>
> Contrato de referencia: [`contracts/openapi/mercatus-v1.yaml`](../contracts/openapi/mercatus-v1.yaml).
> Los defaults acordados están en [10 — Preguntas](10-preguntas-mercatus.md).

## Estado: E0 y E1 entregados

Lo prometido para E0 era el OpenAPI congelado y un servidor de respuestas fijas.
**Está el servidor real**, no un stub: los siete endpoints funcionan contra el
ledger, con la contabilidad de verdad detrás.

| Endpoint | Estado |
|---|---|
| `POST /oauth/token` | ✅ |
| `POST /v1/accounts` | ✅ con saldo inicial en sandbox |
| `POST /v1/authorizations` | ✅ |
| `POST /v1/authorizations/{id}/capture` | ✅ |
| `GET /v1/authorizations/{id}` | ✅ con verificación opcional del vendedor |
| `POST /v1/authorizations/{id}/refund` | ✅ total; el parcial existe en el modelo, falta exponerlo |
| `GET /v1/accounts/{id}/transactions` | ✅ paginado |

**Lo que el sandbox NO tiene todavía**: dinero real. Hasta que cierre la
constitución de Mercatus Technologies S.A.C. y el KYB de la cuenta maestra, todo
el saldo es de prueba. La integración técnica no depende de eso; el paso a
producción sí.

---

## 1. Entorno

| | |
|---|---|
| **Base URL del sandbox** | la que se les comunique al entregar credenciales |
| **Emisor de tokens** | `https://sandbox.aibank.local` (claim `iss`) |
| **Moneda habilitada** | USD. PEN y EUR en la segunda entrega, según lo acordado |
| **Salud del servicio** | `GET /health` → `{"status":"ok","sandbox":true}` |

Los tokens del sandbox **no sirven en producción**: el `iss` es distinto y se
verifica. Un token de un entorno rechazado en el otro es deliberado.

## 2. Credenciales

Se entregan por canal seguro, fuera de este documento:

```
client_id:     mercatus_sandbox
client_secret: <se entrega aparte>
scopes:        payments:read payments:write accounts:write
```

**El secreto se muestra una sola vez.** La base guarda su SHA-256; no hay forma
de recuperarlo, solo de rotarlo. Si lo pierden, avisen y lo rotamos.

**Rotación**: al rotar, el secreto anterior sigue sirviendo **7 días**. Pueden
desplegar el nuevo sin coordinar un corte simultáneo. En rotaciones programadas
avisamos con 30 días.

---

## 3. Autenticación

```bash
curl -X POST "$BASE/oauth/token" \
  -d grant_type=client_credentials \
  -d client_id=mercatus_sandbox \
  -d client_secret="$SECRET"
```

```json
{
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "expires_in": 3600,
  "scope": "payments:read payments:write accounts:write"
}
```

Cachéenlo y renuévenlo antes de que venza. Las credenciales también se aceptan
por `Basic` auth si su cliente lo prefiere.

Un token vencido devuelve `401 invalid_credential` con el mensaje "token
expirado" — es la señal de pedir uno nuevo, no de revisar credenciales. Un token
válido sin el scope necesario devuelve **`403 insufficient_scope`**: pedir otro
token con el mismo `client_id` no lo resuelve.

---

## 4. El camino de pago, con respuestas reales

Todo lo que sigue está copiado de una ejecución real contra el servicio.

### Abrir las dos cuentas

El saldo inicial es lo que permite probar `insufficient_funds` de verdad. Aquí,
$0.10 = `100000` micras.

```bash
curl -X POST "$BASE/v1/accounts" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"owner_reference":"agent_buyer_demo","currency":"USD",
       "display_name":"Mercatus · buyer","initial_balance":100000}'
```

```json
{"account_id":"acc_0f828914a6ad4fd785dabe4cfdd6d43a","currency":"USD","status":"active"}
```

Repetir la llamada con el mismo `owner_reference` devuelve **`200` y la misma
cuenta**, no una segunda.

### Autorizar $0.001

```bash
curl -X POST "$BASE/v1/authorizations" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: cart_demo_001' \
  -d '{"payer_account_id":"acc_0f8289...","payee_account_id":"acc_7e7ef7...",
       "amount":1000,"currency":"USD"}'
```

```
HTTP/1.1 201 Created
```
```json
{
  "authorization_id": "auth_90471849223245cb96f14d06767b489b",
  "status": "authorized",
  "expires_at": "2026-09-01T16:52:07.47762Z"
}
```

El dinero ya salió del saldo disponible del pagador, pero **no está en el
receptor**: está retenido. Vence en 15 minutos.

### El reintento con la misma clave

```
HTTP/1.1 200 OK
Idempotent-Replay: true
```

Mismo cuerpo, mismo `authorization_id`. **`200` y no `201`** porque no se creó
nada, y el encabezado lo hace distinguible en sus logs sin comparar cuerpos.

La misma clave con otro monto o con otras cuentas es `409 idempotency_key_reused`.

### El vendedor verifica antes de entregar

```bash
curl "$BASE/v1/authorizations/$AUTH?expected_amount=1000&expected_payee_account_id=$PAYEE" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{
  "authorization_id": "auth_90471849223245cb96f14d06767b489b",
  "status": "authorized",
  "payer_account_id": "acc_0f828914a6ad4fd785dabe4cfdd6d43a",
  "payee_account_id": "acc_7e7ef74930224d5780c4b7726c84ce70",
  "amount": 1000,
  "currency": "USD",
  "expires_at": "2026-09-01T16:52:07.47762Z"
}
```

Si el monto no coincide:

```
HTTP/1.1 422
{"error":"amount_mismatch","message":"el monto autorizado no coincide con el esperado"}
```

**Los dos parámetros `expected_*` son opcionales.** Sin ellos el `GET` responde
el estado y la comparación queda de su lado, como estaba en el contrato
original. Con ellos, la hacemos nosotros. Esto resuelve el hueco de
`recipient_mismatch`/`amount_mismatch` sin obligarles a cambiar nada.

### Capturar

```bash
curl -X POST "$BASE/v1/authorizations/$AUTH/capture" -H "Authorization: Bearer $TOKEN"
```

```json
{
  "authorization_id": "auth_90471849223245cb96f14d06767b489b",
  "status": "captured",
  "settled_at": "2026-09-01T16:37:20.869083Z"
}
```

**Capturar otra vez devuelve exactamente lo mismo**, con el mismo `settled_at`.
No hace falta `Idempotency-Key`: la autorización ya es única.

### Extracto del receptor

```json
{
  "account_id": "acc_7e7ef74930224d5780c4b7726c84ce70",
  "transactions": [
    {
      "cursor": "1717",
      "transaction_id": "ca825028-5b08-44d9-971d-bf24124dcf00",
      "amount": 1000,
      "currency": "USD",
      "direction": "credit",
      "kind": "authorization_capture",
      "posted_at": "2026-09-01T16:37:20.866696Z"
    }
  ]
}
```

`kind` puede ser `authorization_hold`, `authorization_capture`,
`authorization_release`, `authorization_refund` o `sandbox_funding`.

---

## 5. Escenarios de prueba

Sugerimos cubrir estos en su cliente. Todos están verificados de nuestro lado.

| # | Escenario | Cómo provocarlo | Esperado |
|---|---|---|---|
| 1 | Camino feliz | Autorizar → consultar → capturar | `201` → `200` → `200 captured` |
| 2 | Reintento de autorización | Repetir con la misma `Idempotency-Key` | `200` + `Idempotent-Replay: true`, mismo id |
| 3 | Clave reusada con otro monto | Misma clave, `amount` distinto | `409 idempotency_key_reused` |
| 4 | Sin clave | Omitir `Idempotency-Key` | `400 malformed_request` |
| 5 | Saldo insuficiente | Cuenta con `initial_balance: 100000`, autorizar `999000000` | `402 insufficient_funds` |
| 6 | Reintento de captura | Capturar dos veces | `200` ambas, mismo `settled_at` |
| 7 | Vencimiento | Autorizar y esperar 15 min sin capturar | `GET` → `expired`; capturar → `409 authorization_expired` |
| 8 | Verificación correcta | `expected_amount` y `expected_payee_account_id` correctos | `200` |
| 9 | Monto distinto | `expected_amount` incorrecto | `422 amount_mismatch` |
| 10 | Receptor distinto | `expected_payee_account_id` incorrecto | `422 recipient_mismatch` |
| 11 | Pagarse a sí mismo | Mismo id en pagador y receptor | `422 same_account` |
| 12 | Cuenta inexistente | Un `acc_` que no existe | `404 account_not_found` |
| 13 | Cuenta ajena | Un `acc_` de otra integración | `404 account_not_found` (no `403`) |
| 14 | Token de solo lectura | Pedir `scope=payments:read` y autorizar | `403 insufficient_scope` |
| 15 | Token vencido | Esperar más de una hora | `401 invalid_credential` |
| 16 | Reembolso | Capturar y reembolsar | `200 refunded` |
| 17 | Reembolso sin captura | Reembolsar una autorización viva | `409 nothing_to_refund` |
| 18 | Alta idempotente | Abrir la misma cuenta dos veces | `201` y luego `200`, mismo `account_id` |
| 19 | Moneda no habilitada | Abrir en `EUR` | `422 currency_mismatch` |
| 20 | Límite de tasa | Superar 50 req/s | `429` + `Retry-After` |

Los escenarios 12 y 13 responden **igual a propósito**: confirmar que una cuenta
existe pero es de otra integración permitiría descubrir qué cuentas hay en el
banco probando identificadores.

### Nota sobre el escenario 7

Esperar 15 minutos en una prueba automatizada es incómodo. Si les sirve,
habilitamos un parámetro `ttl_minutes` en el sandbox para poder crear
autorizaciones de vida corta. Pídanlo y lo exponemos.

---

## 6. Límites y cuotas

| | Sandbox | Producción (previsto) |
|---|---|---|
| Por integración | 50 req/s sostenidos, ráfaga 100 | igual, revisable con datos reales |
| Por sub-cuenta pagadora | 10 req/s, ráfaga 20 | igual |
| Monto por operación | sin tope | por acordar con datos reales |

Al superar el límite: `429` con `Retry-After` en segundos. **Reintenten con la
misma `Idempotency-Key`** — no duplica nada.

El límite por sub-cuenta es más bajo a propósito: la integración comparte una
credencial, así que un agente en bucle podría consumir la cuota de todos los
demás. Acotarlo por cuenta convierte ese incidente en un problema de un agente.

## 7. Errores: la tabla completa

| HTTP | `error` | Cuándo |
|---|---|---|
| 400 | `malformed_request` | Campo faltante o inválido, falta `Idempotency-Key`, campo desconocido en el cuerpo |
| 401 | `invalid_credential` | Token ausente, vencido o inválido |
| 402 | `insufficient_funds` | El pagador no llega al monto |
| 403 | `insufficient_scope` | Token válido sin el scope necesario |
| 403 | `account_frozen` | La cuenta está bloqueada por un control |
| 404 | `account_not_found` | Cuenta inexistente, ajena o mal formada |
| 404 | `authorization_not_found` | Ídem para autorizaciones |
| 409 | `idempotency_key_reused` | Misma clave, payload distinto |
| 409 | `authorization_expired` | Se intentó capturar una retención vencida |
| 409 | `nothing_to_refund` | Reembolso sobre algo que no se cobró |
| 422 | `amount_mismatch` / `recipient_mismatch` | La verificación del vendedor no coincide |
| 422 | `currency_mismatch` | Monedas distintas, o moneda no habilitada |
| 422 | `same_account` | Pagador y receptor coinciden |
| 429 | `rate_limited` | Cuota superada |
| 503 | `service_unavailable` | Transitorio. **Reintentable** con la misma clave |

**El campo `error` es estable**: compárenlo por igualdad. El campo `message` es
para humanos y puede cambiar sin aviso — no lo parseen.

Solo `503` y `429` son reintentables. Todo lo demás es definitivo: reintentar no
cambia el resultado.

---

## 8. Levantar el entorno en local

Por si quieren correrlo del lado de ustedes mientras integran.

```bash
docker compose -f platform/docker-compose.yml up -d
```

```bash
cd core && DATABASE_URL="postgres://aibank:aibank_dev@localhost:5434/aibank" \
  cargo run --bin aibank-core-server
```

Alta de la integración y de las cuentas internas. Imprime el secreto una vez:

```bash
go run ./services/mercatus/cmd/bootstrap -client-id mercatus_sandbox -currencies USD
```

Con lo que imprime el comando anterior:

```bash
DATABASE_URL="postgres://aibank:aibank_dev@localhost:5434/aibank?sslmode=disable" \
CORE_ADDR=127.0.0.1:50051 BIND_ADDR=127.0.0.1:8081 \
OAUTH_ISSUER="https://sandbox.aibank.local" \
OAUTH_SIGNING_KEY="$(head -c 32 /dev/urandom | base64)" \
MERCATUS_SANDBOX=true MERCATUS_PRODUCTS="USD=AGENT-USD" \
MERCATUS_SANDBOX_CASH="<el que imprimió bootstrap>" \
go run ./services/mercatus/cmd/mercatus
```

El barrendero de retenciones vencidas, que es lo que hace real el vencimiento de
15 minutos:

```bash
cd core && DATABASE_URL="postgres://aibank:aibank_dev@localhost:5434/aibank" \
  cargo run --bin aibank-authorization-sweeper
```

---

## 9. Lo que sigue pendiente

**De su lado**, y es lo que más tiempo va a tomar:

1. Constitución de Mercatus Technologies S.A.C. y KYB de la cuenta maestra. Sin
   eso el sandbox no puede llevar dinero real.
2. El compromiso contractual sobre `owner_reference`: trazable a una persona
   natural o jurídica real, con respuesta en 24 horas hábiles a un requerimiento
   de autoridad.

**Del nuestro:**

1. Fijar el país ancla de operación. Ustedes asumen Perú; nuestro plan de
   licencias todavía no lo cierra, y condiciona la licencia y el proveedor.
   Conviene confirmarlo antes de avanzar con el KYB.
2. Reembolso parcial en la API. El modelo del core ya lo soporta; falta el campo.
3. PEN y EUR, según lo acordado para la segunda entrega.
4. Fondeo y retiro. El contrato no los cubre y son la parte con más escrutinio
   regulatorio: hay que diseñarlos antes de mover dinero real.
