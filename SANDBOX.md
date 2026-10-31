# Sandbox de AIBank — cómo levantarlo e integrarse

> Documento para el equipo que integra. La especificación funcional de los dos
> modelos está en [docs/13](docs/13-spec-modelos-a-y-b.md); esto es cómo
> conseguir un entorno donde ejercitarla.

## Lo que hay que saber antes

**El sandbox no lleva dinero real y nunca lo llevará.** El dinero de prueba lo
crea una cuenta de caja del propio entorno. Los tokens que emite tampoco sirven
en producción: el emisor (`iss`) es distinto y se verifica, así que un token de
un entorno es rechazado en el otro. Es deliberado.

**Hay una pieza que solo existe aquí**: el simulador de titulares
(`POST /dev/holders`). El modelo B necesita una persona con cuenta en AIBank que
conceda el permiso, y en producción esa persona llega por el onboarding con KYC.
Sin un atajo en el sandbox, el modelo B no se podría probar en absoluto: se
pediría un consentimiento que nadie podría aprobar nunca. Está detrás de una
variable de entorno y del binario del canal, que **se niega a arrancar** sin
ella.

---

## 1. Levantarlo

Hay dos formas, y las dos sirven:

- **Uno propio, en su máquina — disponible hoy.** El código es público, así que
  no hace falta esperar a nadie: se clona y se levanta. Aislado, reiniciable de
  cero y sin compartir límites de tasa con nadie; para desarrollar suele ser más
  cómodo que el alojado. Es lo que describe el resto de esta sección.
- **El entorno alojado**, con una URL pública, cuando esté desplegado. La
  dirección, las credenciales y la clave del simulador de titulares se entregan
  **por canal seguro, aparte de este documento**. Con eso se puede saltar directo
  a la sección 3.

Requisitos para levantarlo uno mismo: git y Docker con Compose v2. Nada más — ni
Rust, ni Go, ni Postgres.

```bash
git clone https://github.com/Root1V/aerarium-agentic-banking.git && cd aerarium-agentic-banking
echo "OAUTH_SIGNING_KEY=$(head -c 32 /dev/urandom | base64)" > platform/.env
```

No hay clave por defecto a propósito: un entorno que arranca con una clave
escrita en un archivo público es un entorno cuyos tokens puede fabricar
cualquiera. Guárdala — si cambia, los tokens ya emitidos dejan de valer.

```bash
docker compose -f platform/docker-compose.sandbox.yml up --build -d
```

La primera vez tarda: compila el core en Rust. Después arranca en segundos.

**Si 8080 u 8081 ya están ocupados** —pasa a menudo: son los puertos por defecto de
media docena de herramientas— se mueven sin tocar nada más:

```bash
PARTNER_PUBLISH=28081 HOLDER_PUBLISH=28080 \
  docker compose -f platform/docker-compose.sandbox.yml up --build -d
```

La demo no se entera: habla con los servicios por la red interna de Docker.

| | |
|---|---|
| **API de socio** | `http://localhost:8081` |
| **Canal del titular** (modelo B) | `http://localhost:8080` |
| **Salud** | `curl localhost:8081/health` → `{"status":"ok","sandbox":true}` |

El entorno levanta cuatro piezas, y las cuatro hacen falta: el core (ledger y
autorizaciones), el **barrendero** (libera las retenciones vencidas — sin él los
15 minutos de vigencia serían una promesa que nada cumple), la API de socio y el
canal del titular.

## 2. Las credenciales

El alta corre sola al levantar el entorno e imprime el secreto **una vez**:

```bash
docker compose -f platform/docker-compose.sandbox.yml logs bootstrap
```

```
MERCATUS_PRODUCTS=USD=AGENT-USD

Credenciales de la integración — el secreto NO se puede recuperar después:
  client_id:     mercatus_sandbox
  client_secret: ...
Scopes: payments:read payments:write accounts:write
```

La base guarda su SHA-256; no hay forma de recuperarlo, solo de rotarlo. Volver a
levantar el entorno **no** cambia el secreto: el alta detecta que la integración
ya existe y no toca nada — pero entonces tampoco lo vuelve a imprimir, así que
esa línea del log solo existe tras el primer arranque. Si se pierde, se emite uno
nuevo y el anterior sigue sirviendo 7 días:

```bash
docker compose -f platform/docker-compose.sandbox.yml run --rm \
  bootstrap -client-id mercatus_sandbox -rotate
```

## 3. El recorrido completo, en un comando

Antes de escribir nada, conviene ver el camino entero funcionando:

```bash
docker compose -f platform/docker-compose.sandbox.yml \
  run --rm -e CLIENT_SECRET="<el secreto>" demo -model b
```

Narra el camino entero del modelo B: token, sub-cuenta del receptor, titular de
prueba, solicitud de permiso, decisión de la persona, consulta del resultado,
autorización, captura, reembolso parcial y estado del mandato. `-model a` hace el
mismo recorrido en el modelo ómnibus, que no necesita el canal del titular.

El código es un cliente HTTP normal y corriente, sin nada de nuestras librerías:
[`services/mercatus/cmd/demo`](services/mercatus/cmd/demo/main.go). Es el mejor
punto de partida para el cliente de ustedes.

---

## 4. Modelo A, a mano

```bash
BASE=http://localhost:8081
TOKEN=$(curl -s -X POST "$BASE/oauth/token" \
  -d grant_type=client_credentials \
  -d client_id=mercatus_sandbox \
  -d client_secret="$SECRET" | jq -r .access_token)

# Abrir la sub-cuenta de un agente. Idempotente por owner_reference + moneda.
curl -s -X POST "$BASE/v1/accounts" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"owner_reference":"agente-42","currency":"USD","initial_balance":50000000}'

# Autorizar $0,001 — mil micras. Sin mandate_id: modelo A.
curl -s -X POST "$BASE/v1/authorizations" -H "Authorization: Bearer $TOKEN" \
  -H 'Idempotency-Key: carrito-001' -H 'Content-Type: application/json' \
  -d '{"payer_account_id":"acc_...","payee_account_id":"acc_...",
       "amount":1000,"currency":"USD"}'

curl -s -X POST "$BASE/v1/authorizations/auth_.../capture" \
  -H "Authorization: Bearer $TOKEN"
```

Los importes son **enteros en micras** (10⁻⁶): 1 USD = `1000000`, un centavo =
`10000`, $0,001 = `1000`. Nunca decimales — JSON no distingue enteros de
flotantes y el saldo acabaría en aritmética binaria.

## 5. Modelo B, a mano

El único cambio en el pago es un campo. Lo que cambia de verdad es lo que ocurre
**antes**: alguien tiene que dar permiso.

```bash
# 1. Un titular de prueba. Solo en el sandbox.
#    En el entorno alojado esta ruta pide además: -H "X-Dev-Key: $DEV_KEY"
HOLDER=$(curl -s -X POST http://localhost:8080/dev/holders \
  -H 'Content-Type: application/json' \
  -d '{"currency":"USD","name":"Ana Pérez","initial_balance":50000000}')
HOLDER_TOKEN=$(echo "$HOLDER" | jq -r .token)
HOLDER_ACCOUNT=$(echo "$HOLDER" | jq -r .account_id)

# 2. Pedir permiso. Nótese que la petición NO nombra ninguna cuenta.
REQUEST=$(curl -s -X POST "$BASE/v1/consent-requests" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"currency":"USD","max_per_operation":5000000,"max_total":50000000,
       "purpose":"Pagos del agente de investigación de Acme S.A."}')
HANDOFF=$(echo "$REQUEST" | jq -r .handoff_code)

# 3. El titular ve la solicitud en su app y decide. Puede conceder MENOS.
curl -s -X POST "http://localhost:8080/v1/consent-requests/$HANDOFF/approve" \
  -H "Authorization: Bearer $HOLDER_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"account_id\":\"$HOLDER_ACCOUNT\",\"max_per_operation\":2500000,
       \"expires_in_days\":30}"

# 4. La plataforma consulta —no hay webhook— y aquí aprende la cuenta.
curl -s "$BASE/v1/consent-requests/$(echo "$REQUEST" | jq -r .consent_request_id)" \
  -H "Authorization: Bearer $TOKEN"

# 5. Pagar: idéntico al modelo A más el mandate_id.
```

`purpose` es obligatorio y se muestra literalmente en la pantalla del banco. Un
texto vago convierte la decisión del titular en una que no puede tomar bien.

**En su dashboard, el paso 4 es un sondeo.** Se consulta cada pocos segundos
hasta que `status` deja de ser `pending`. Se consulta y no se notifica porque un
webhook obligaría a ustedes a exponer un endpoint y a nosotros a firmarlo y
reintentarlo, para una decisión que tarda lo que tarda una persona en mirar su
teléfono. La solicitud vive **una hora**; pasada esa hora queda `expired` y hay
que pedir una nueva.

---

## 5b. Alta de clientes

El canal del titular trae el alta de nivel 1: DNI, nombres, apellidos y fecha de
nacimiento contra el proveedor de identidad, y una cuenta básica de topes bajos. La
máquina de estados es la real; el proveedor está simulado, con **DNI de prueba fijos**
para ensayar cada desenlace, como las tarjetas de prueba de una pasarela:

| DNI | Desenlace |
|---|---|
| `00000000` | Identidad no superada → `rejected` |
| `11111111` | Verificación no concluyente → `in_review` |
| `22222222` | Coincidencia en listas de sanciones → `in_review` |
| Cualquier otro de 8 dígitos | `approved`, con cuenta abierta |

```bash
curl -X POST http://localhost:8080/v1/onboarding -H 'Content-Type: application/json' \
  -d '{"reference":"<uuid generado por el cliente>","document_number":"45678912",
       "first_names":"Ana María","last_names":"Pérez Quispe","date_of_birth":"1995-03-14"}'
```

Tres cosas del contrato que no son obvias:

- **`11111111` y `22222222` responden exactamente igual.** La normativa contra el lavado
  prohíbe avisarle a alguien de que coincide con una lista; la respuesta no distingue una
  foto borrosa de una coincidencia en la OFAC. El motivo real queda en el expediente, para
  cumplimiento.
- **Un mismo DNI no puede tener dos altas vivas.** La segunda va a revisión, sin abrir
  cuenta y sin decir por qué: responder «ese DNI ya es cliente» le diría a cualquiera
  quién tiene cuenta en el banco.
- **La `reference` la genera el cliente y se conserva en cada reintento.** Es lo que
  impide abrir dos cuentas tras un corte de red, y lo único que permite consultar el
  alta después (`GET /v1/onboarding` con la cabecera `X-Onboarding-Reference`).

## 5c. La app en un iPhone

Con el iPhone conectado por cable al Mac donde corre el sandbox, y en la misma Wi-Fi:

```bash
app/tool/iphone.sh
```

La app arranca como la real: abres una cuenta con un DNI de 8 dígitos (KYC simulado:
los de prueba de arriba siguen valiendo), creas tu clave, activas Face ID, y desde ahí
entras con la cara. Un DNI que ya abrió cuenta en este sandbox queda en revisión: la
regla de una persona, un alta.

Con **Añadir saldo de prueba** recibes 250 USD y un destinatario, Juan Pérez (prueba).
Enviarle por primera vez pide confirmar con la cara mostrando monto y nombre; los
envíos siguientes de menos de 200, ya no, y Juan aparece en **Recientes** al enviar.
Juan trae un celular de prueba (sale en el aviso del saldo): escríbelo en **Celular o
cuenta** y verás «Juan P.» antes de enviar. Para recibir con tu número: **Más › Mi
número para recibir**; el código del «SMS» aparece en pantalla. La primera vez el iPhone pide activar el Modo desarrollador y
confiar en el Mac.

## 5d. Tarjeta de crédito

En el inicio, **Pide tu tarjeta de crédito**. El motor de riesgo de verdad evalúa tu
cuenta; como en el sandbox es nueva, se le suman seis meses de un sueldo simulado (la
decisión lo dice). Con la tarjeta aprobada puedes **pasar dinero a tu cuenta** y
**pagarla**. El límite lo hace cumplir el core: no se puede usar más ni pagar de más.

El ciclo dura un mes; en la tarjeta hay dos atajos de prueba: **Cortar el estado de
cuenta ahora** (total, pago mínimo y vencimiento) y **Simular que pasó el
vencimiento**: sin el mínimo pagado, la tarjeta se bloquea; con «Pagar el mínimo» se
desbloquea al instante.

## 6. Reiniciar de cero

```bash
docker compose -f platform/docker-compose.sandbox.yml down -v
```

Borra el volumen y con él todas las cuentas, permisos y movimientos. El siguiente
`up` emite **un secreto nuevo**.

## 7. Qué cambia al pasar a cuentas reales

| | Sandbox | Producción |
|---|---|---|
| Saldos | los crea la caja del entorno | entran por fondeo real |
| `initial_balance` al abrir cuenta | permitido | **rechazado** |
| `POST /dev/holders` | existe (con clave, si está alojado) | **no existe**: onboarding con KYC |
| Titulares | de mentira | personas verificadas |
| Emisor de tokens | `https://sandbox.aibank.local` | otro, y se verifica |

El contrato de pago —`authorize`, `capture`, `refund`, el `GET`, los códigos de
error, la idempotencia y los 15 minutos— es **el mismo**. Un cliente que funciona
aquí funciona allá.

## 8. Lo que todavía no está

- **PEN y EUR**: solo USD, según lo acordado para la segunda entrega.
- **Fondeo y retiro**: el contrato no los cubre. Es la parte con más escrutinio
  regulatorio y necesita diseño antes de mover dinero real.
- **Producción**: depende de la licencia o el proveedor BaaS en Perú, que es
  nuestro bloqueante y no depende de esta integración.
