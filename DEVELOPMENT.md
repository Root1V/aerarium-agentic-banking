# Desarrollo

Stack políglota por tarea (justificación en [docs/06](docs/06-stack-tecnologico.md) §2):
Rust (core) · Go (adaptadores, BFF) · Python (riesgo/ML) · Dart/Flutter (app) · TypeScript (backoffice).

## Requisitos

- Rust estable (`curl https://sh.rustup.rs -sSf | sh`)
- Docker Desktop corriendo
- Go 1.27+ (adaptadores, aún no iniciados)

## Core (Rust)

```bash
docker compose -f platform/docker-compose.yml up -d   # Postgres en localhost:5434
cd core && SQLX_OFFLINE=true cargo test               # 10 tests de invariantes del ledger
```

Las migraciones se aplican solas al correr los tests (`sqlx::migrate!`).

> **Si tu base local es anterior a la migración a micras**, `sqlx` va a rechazar
> arrancar porque el checksum de las migraciones cambió. Es la protección
> funcionando: una base con importes en centavos leídos como micras daría saldos
> diez mil veces menores sin que nada falle. Recrea el esquema:
>
> ```bash
> docker exec aibank-postgres psql -U aibank -d aibank \
>   -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public; DROP SCHEMA IF EXISTS cards CASCADE;'
> ```

### Consultas verificadas en compilación

`sqlx` valida cada consulta SQL contra el esquema real **en tiempo de compilación**. Dos modos:

- **Offline (por defecto en CI)**: usa el caché commiteado en `core/.sqlx`. No requiere base de datos.
  ```bash
  SQLX_OFFLINE=true cargo build
  ```
- **En vivo (al cambiar SQL)**: apunta a una base migrada y regenera el caché.
  ```bash
  export DATABASE_URL="postgres://aibank:aibank_dev@localhost:5434/aibank"
  cargo sqlx prepare   # regenera core/.sqlx — commitear el resultado
  ```

Si cambias una consulta o el esquema, **regenera el caché o CI fallará** — que es exactamente el punto.

## Contratos (Protobuf)

`contracts/proto` es la fuente de verdad de la frontera entre el core (Rust) y los
adaptadores (Go). Un cambio incompatible rompe la compilación de ambos lados.

```bash
make -C contracts lint       # valida los .proto
make -C contracts generate   # regenera el cliente Go (el core Rust lo hace en cargo build)
```

## Servidor del core y cliente Go

```bash
cd core && DATABASE_URL="postgres://aibank:aibank_dev@localhost:5434/aibank" \
  cargo run --bin aibank-core-server        # gRPC en 127.0.0.1:50051

cd clients/go && go test ./...              # tests de integración contra el core real
```

Los tests de Go se saltan solos si el core no está escuchando.

## Eventos (outbox → Kafka)

El core NO publica directamente: escribe el evento en la tabla `outbox` dentro de la
misma transacción que los asientos, y el relay lo publica después. Así es imposible
que exista un asiento sin evento o un evento de una transacción revertida.

```bash
docker compose -f platform/docker-compose.yml up -d          # Postgres + Redpanda
docker exec aibank-redpanda rpk topic create aibank.ledger.v1 -p 3

cd core && DATABASE_URL="postgres://aibank:aibank_dev@localhost:5434/aibank" \
  KAFKA_BROKERS=localhost:9092 cargo run --bin aibank-outbox-relay

docker exec aibank-redpanda rpk topic consume aibank.ledger.v1 --offset start
```

Sin `KAFKA_BROKERS` el relay publica al log (modo desarrollo, sin bus).

Entrega **at-least-once**: todo consumidor debe deduplicar por `event_id` (viaja
como header y dentro del payload). Salud del relay: `outbox` con `published_at IS NULL`
creciendo de forma sostenida = bus o relay caídos.

## Adaptadores (Go)

```bash
cd core && cargo run --bin aibank-core-server   # el core debe estar arriba
cd adapters && go test ./...                    # se saltan solos si el core no responde
```

`go.work` liga los módulos locales (`adapters`, `clients/go`) sin publicarlos.

El riel simulado (`adapters/rails/sim`) reproduce de forma determinista lo que un
riel real hace a diario: timeouts, rechazos, notificaciones duplicadas y entregas
fuera de orden. No es andamiaje temporal — se queda como herramienta de pruebas,
porque esos escenarios casi no se pueden provocar contra el sandbox de un proveedor.

## Servicios (Go)

```bash
cd services && go test ./...    # onboarding: requiere core + Postgres arriba
```

El alta de clientes es una máquina de estados persistida y reanudable: cada paso se
registra al completarse, así una caída se retoma donde quedó en vez de volver a
pedir documentos al cliente y a pagar verificaciones ya hechas. Los pasos y sus
resultados son además el rastro de auditoría que el supervisor puede exigir.

## BFF (API del canal móvil)

```bash
cd services && go test ./bff/     # requiere core + Postgres arriba
```

La autenticación es un PUERTO (`bff.Authenticator`), no una implementación. En
producción la resuelven las passkeys (`services/passkeys`): el banco es el Relying
Party WebAuthn, emite los desafíos, verifica las firmas y abre sesiones de 30
minutos. El sustituto de desarrollo vive en `bff/sim` para que no pueda cablearse a
producción por descuido.

Reglas de las passkeys, cada una con su prueba:

- **Verificación del usuario obligatoria** (cara, huella o código). Es lo que hace
  que una passkey cubra los dos factores que exige la SBS (Res. 2286-2024).
- **La primera passkey se registra con la referencia del alta**: aprobada, sin
  passkey previa y dentro de las 24 horas. Una más, solo con sesión.
- **Cada desafío sirve una vez** y para un solo tipo de ceremonia; se consume en la
  misma sentencia que lo lee.
- **Una passkey desconocida, una firma desde otro dominio y una passkey clonada
  reciben la misma respuesta** (`passkey_rejected`). La diferencia va al log.
- Los tokens de sesión se guardan como SHA-256; cerrar sesión los revoca en el banco.

```bash
# Arrancar el canal con passkeys (requiere Postgres para ceremonias y sesiones):
cd services && PASSKEY_RP_ID=<dominio> DATABASE_URL=... CORE_ADDR=... go run ./bff/cmd/bff
```

`PASSKEY_RP_ORIGINS` (por defecto `https://<dominio>`) y `PASSKEY_RP_NAME` son
opcionales. Sin `ALLOW_DEV_AUTH`, el alta no se monta: su KYC es simulado.

Dos reglas del canal: el dinero viaja como entero en micras (nunca decimal en
JSON) y toda operación que mueve dinero exige `Idempotency-Key`.

## App (Flutter)

```bash
export PATH="/opt/homebrew/Caskroom/flutter/3.47.2/flutter/bin:$PATH"
cd app && flutter test && flutter analyze

# Contra el BFF real:
flutter run --dart-define=AIBANK_BFF_URL=http://localhost:8080 \
            --dart-define=AIBANK_TOKEN=... --dart-define=AIBANK_ACCOUNT_ID=...
```

Sin sesión fija ni sandbox, la app entra por `BankEntry` (`lib/entry.dart`):

- **Teléfono vinculado:** Face ID al abrir, sin pantalla de login. Si falla o se
  prefiere, la clave de 6 dígitos.
- **Teléfono sin vincular:** entrar con passkey o abrir una cuenta. Las dos terminan
  en **Protege tu cuenta**: crear la clave y activar Face ID, que vincula el teléfono.

El teléfono guarda dos llaves P-256 en el Secure Enclave (`services/devices`): una
que solo firma tras Face ID —cubre los dos factores— y otra sin biometría que
acompaña a la clave, para que solo se puedan probar claves desde ese teléfono. La
clave se verifica en el banco (Argon2id) y cinco errores seguidos bloquean el
teléfono. Las llaves no se copian a iCloud, a diferencia de las passkeys.

El puente con el sistema está escrito a mano en Swift (`ios/Runner/AppDelegate.swift`,
CryptoKit, LocalAuthentication y AuthenticationServices), sin paquetes de terceros
(doc 14, regla 1). En el simulador las llaves son de software, porque el Secure
Enclave simulado no admite llaves atadas a Face ID; para simular la cara:

```bash
xcrun simctl spawn booted notifyutil -s com.apple.BiometricKit.enrollmentChanged 1
xcrun simctl spawn booted notifyutil -p com.apple.BiometricKit.enrollmentChanged
xcrun simctl spawn booted notifyutil -p com.apple.BiometricKit_Sim.pearl.match   # cara reconocida
```

**En un iPhone de verdad**, conectado por cable y en la misma Wi-Fi que el Mac:

```bash
app/tool/iphone.sh
```

Levanta el sandbox con el canal del titular abierto a la red local (con
`DEV_API_KEY`, para que nadie más en la Wi-Fi use el simulador de titulares) e
instala la app en modo release apuntando a la IP del Mac.

El token de sesión vive solo en memoria: cerrar la app es cerrar la sesión.

**Operaciones de riesgo** (`services/stepup`): una transferencia a un destinatario
nuevo, o de 200 o más, no se ejecuta con la sesión sola. El banco responde `428
confirmation_required` con un desafío que lleva dentro el monto, el destino y la
clave de idempotencia; el teléfono lo firma con Face ID («Envías 50,00 USD a Juan
Pérez») —o con la clave si la cara no puede— y la app repite la misma petición con
la firma. Si lo firmado no es exactamente lo que se pide ejecutar, se rechaza: la
confirmación de 50 no sirve para mover 900. Tras una transferencia confirmada, el
destinatario queda conocido y los envíos chicos ya no la piden.

Los **destinatarios recientes** (`GET /v1/payees/recent`) salen de la misma tabla:
a quien ya se le envió con una sesión real, del más reciente al más antiguo. La
pantalla de envío los muestra arriba y nadie escribe un identificador de cuenta.

En el sandbox, «Añadir saldo de prueba» acredita 250 (bajo el tope por operación de
la cuenta de nivel 1) y crea un destinatario de prueba para enviarle.

**Para probarla de punta a punta, un comando:**

```bash
app/tool/sandbox.sh ios    # simulador de iPhone
app/tool/sandbox.sh web    # portal web en Chrome
```

Levanta el sandbox, arranca la app contra él y entra por la pantalla de titular de
prueba: «Crear titular de prueba» crea una persona con 100 USD y entra con su sesión.
Hay hot reload (`r` en la terminal).

Usa los puertos 38080/38081/5050 porque 8080/8081 casi siempre están ocupados en una
máquina de desarrollo; se cambian con `HOLDER_PORT`, `PARTNER_PORT` y `WEB_PORT`. El
script hace casar los cinco valores que tienen que coincidir —puerto, CORS del portal,
clave del simulador, URL compilada y modo sandbox—, que es donde falla hacerlo a mano.

**La entrada de prueba no existe en producción**: solo se compila con
`--dart-define=AIBANK_SANDBOX=true`. Verificado sobre el binario: la compilación web sin
ese valor no contiene ni el texto de la pantalla ni la ruta `/dev/holders`.

Android está configurado pero no se ha compilado en esta máquina (falta el SDK).

Dos reglas del cliente: el dinero nunca es `double` (entero en micras, se formatea
solo para mostrar) y un reintento de envío CONSERVA la clave de idempotencia — si
generara una nueva, un timeout que el servidor sí procesó cobraría dos veces.

## Tarjetas

Una compra con tarjeta son DOS momentos, no uno: la autorización retiene el dinero
(sale del saldo disponible pero sigue en el banco) y el cobro posterior lo saca de
verdad — por un monto que puede diferir del autorizado (propinas, combustible).
Modelarlo así es lo que hace que el saldo disponible del cliente sea correcto.

```bash
cd adapters && go test ./cards/...
```

## Conciliación

Es la contrapartida de haber construido un ledger propio: sin registro propio no
hay nada contra qué comparar. Cubre tres frentes — el ledger contra sí mismo, el
ledger contra el extracto del proveedor (cruzando por clave de idempotencia) y el
dinero detenido en cuentas de tránsito o retención.

```bash
cd core && SQLX_OFFLINE=true cargo test --test reconciliation
```

## Backoffice (TypeScript)

```bash
cd backoffice && npm install
node --test test/*.test.ts && npx tsc --noEmit

ALLOW_DEV_AUTH=true npm start   # consola en :8090
```

Node ejecuta los `.ts` eliminando tipos, sin empaquetador ni paso de compilación.
Eso descarta sintaxis que genere código: nada de *parameter properties*, enums ni
decoradores.

Regla del módulo: ninguna acción ocurre sin quedar atribuida a una persona, y los
intentos DENEGADOS se registran igual que los permitidos — un intento rechazado es
justo lo que interesa detectar. El rastro de auditoría es de solo inserción, como
el ledger.

## Observabilidad

Una misma traza une app → BFF → core (Rust) → evento del outbox → relay. Dos
propagaciones distintas hacen falta y ninguna es automática:

- **Entre procesos**: contexto W3C por metadata gRPC y cabeceras HTTP.
- **En el tiempo**: el evento del outbox guarda el `traceparent` de la transacción
  que lo originó, porque se publica después y en otro proceso. Sin eso la traza se
  corta en el COMMIT, justo donde hace falta para seguir un pago asíncrono.

Regla dura: en una traza NUNCA entran datos personales ni secretos. Hay una guarda
(`is_safe_attribute` / `IsSafeAttribute`) que filtra por subcadena y es
deliberadamente estricta — las trazas salen a herramientas de terceros.

## Riesgo crediticio (Python)

```bash
cd risk && python3 -m pytest
```

Es una tarjeta de puntuación transparente, no un modelo entrenado: sin cartera
colocada no hay incumplimientos que aprender, y un modelo ajustado sobre datos que
no se tienen es teatro. Tres propiedades no negociables: **explicable** (toda
negativa trae motivos comunicables), **reproducible** (mismas entradas y versión →
mismo resultado, años después) y **no discriminatoria** (los atributos protegidos
no pueden intervenir, con la barrera en el camino de ejecución).

Sin dependencias: elimina toda fuente de no determinismo en una decisión auditable.

## API de socio (riel de pago para agentes)

Es el canal de SOCIO, hermano del BFF y separado de él: el BFF sirve a una persona con
sesión de dispositivo, este a una plataforma con credencial de máquina que actúa por
muchas cuentas. Juntarlos filtraría las reglas de autorización de uno en el otro.

```bash
cd services && go test ./oauth/ ./mercatus/     # requieren core + Postgres arriba

# Alta de una integración: imprime el secreto UNA vez.
go run ./services/mercatus/cmd/bootstrap -client-id mercatus_sandbox -currencies USD
```

Contrato congelado en [`contracts/openapi/mercatus-v1.yaml`](contracts/openapi/mercatus-v1.yaml);
datos para integrar en [docs/11](docs/11-spec-integracion-sandbox.md).

El JWT está escrito a mano y el algoritmo NUNCA se lee del token: es lo que cierra la
confusión de algoritmo, incluido `alg: none`. Los secretos se guardan como SHA-256 porque
los genera el banco con 256 bits de entropía — para una contraseña humana eso sería un
error, y está anotado en el esquema para que nadie lo copie al lugar equivocado.

## Sandbox completo, en un comando

Para levantar el entorno como lo levanta quien integra —sin toolchains, todo en
contenedores— hay un compose aparte:

```bash
echo "OAUTH_SIGNING_KEY=$(head -c 32 /dev/urandom | base64)" > platform/.env
docker compose -f platform/docker-compose.sandbox.yml up --build -d
```

Levanta las cuatro piezas que hacen falta para ejercitar los dos modelos: core,
barrendero de retenciones, API de socio (`:8081`) y canal del titular (`:8080`).
El alta de la integración corre sola e imprime el secreto una vez
(`logs bootstrap`).

El canal del titular es la pieza que faltaba para que el modelo B se pudiera
probar fuera de una prueba de Go: la mitad del flujo la protagoniza una persona.
Su binario **se niega a arrancar** sin una forma de autenticar: `ALLOW_DEV_AUTH=true`
(el sustituto de desarrollo, que es lo que usa el sandbox) o `PASSKEY_RP_ID`.

El recorrido completo por HTTP, que es también el ejemplo de cliente:

```bash
docker compose -f platform/docker-compose.sandbox.yml \
  run --rm -e CLIENT_SECRET="<el secreto>" demo -model b
```

Guía para compartir en [SANDBOX.md](SANDBOX.md).

### Publicarlo en internet

Un añadido pone Caddy delante y saca los servicios de las interfaces públicas:

```bash
docker compose -f platform/docker-compose.sandbox.yml \
               -f platform/docker-compose.sandbox.public.yml up -d
```

En `platform/.env`, además de `OAUTH_SIGNING_KEY`:

```
PARTNER_DOMAIN=sandbox-api.<dominio>
HOLDER_DOMAIN=sandbox-app.<dominio>
DEV_API_KEY=<aleatorio>
PARTNER_PUBLISH=127.0.0.1:8081
HOLDER_PUBLISH=127.0.0.1:8080
```

Los dos nombres tienen que resolver a la máquina **antes** de levantarlo: Caddy
pide los certificados al arrancar, los renueva solo y guarda su estado en un
volumen — sin él, cada redespliegue volvería a pedirlos y Let's Encrypt limita
cuántas veces se puede.

`DEV_API_KEY` deja de ser opcional en cuanto el entorno es alcanzable desde
fuera. Sin ella el simulador de titulares queda abierto, y `POST /dev/sessions`
emite una sesión para el `customer_id` que se le pida: cualquiera podría hablar
por el titular de prueba de otro socio. El binario avisa en el arranque cuando
está sin clave.

Una integración por socio, cada una con su secreto:

```bash
docker compose -f platform/docker-compose.sandbox.yml run --rm \
  bootstrap -client-id otro_socio -name "Otro Socio"
```

Una cosa a tener en cuenta antes de abrirlo a más de un socio: el límite de tasa
es **por instancia**. Con una réplica es correcto; con dos, el límite efectivo se
duplica y habría que moverlo a un almacén compartido.

La caja del sandbox NO se agota —es una cuenta interna y esas admiten sobregiro—
así que su saldo negativo no es un problema a vigilar: es la cuenta de cuánto
dinero de prueba se ha emitido.

## Mandatos de pago (modelo B)

El permiso que un titular le da a una plataforma para iniciar pagos desde una cuenta suya.
Es lo que hace posible que el dinero nunca salga a un pool de un tercero.

```bash
cd core && SQLX_OFFLINE=true cargo test --test mandates
cd services && go test ./mercatus/ -run TestElModeloB
```

Un mandato NO es un scope. Un scope dice qué puede hacer una integración; un mandato dice
qué autorizó una persona, sobre qué cuenta, hasta qué monto y contra qué evidencia. Ante
"yo nunca autoricé ese pago", un scope no prueba nada.

Lo consumido se DERIVA de las autorizaciones, no de un contador: uno que hay que subir al
autorizar y bajar al liberar y al reembolsar termina desincronizado, y aquí eso significa
o bloquear pagos legítimos o permitir los que superan el tope.

El reparto entre los dos servicios es la garantía: quien pide el permiso (API de socio) no
puede concederlo (canal del titular). Hay un test que lo intenta.

## Autorizaciones y su barrendero

Retener ahora, mover el dinero después. Vive en el core y no en un adaptador porque
pagador y receptor son dos cuentas del banco: capturar es una sola transacción de ledger
y tiene que confirmarse junto con el cambio de estado.

```bash
cd core && SQLX_OFFLINE=true cargo test --test authorizations

# Sin este proceso, una retención que nadie captura inmoviliza el dinero para siempre.
DATABASE_URL="postgres://aibank:aibank_dev@localhost:5434/aibank" \
  cargo run --bin aibank-authorization-sweeper
```

"Vencida" es un estado DERIVADO, no almacenado: guardarlo obligaría a escribir en la base
cada vez que el reloj avanza. Una retención pasada de fecha se reporta vencida y rechaza
la captura aunque el barrendero no haya pasado todavía.

## Convenciones

- Flujo git: cada feature en rama `feat/<nombre>`; revisión → merge a `main`. Estados en [roadmap.md](roadmap.md).
- Regla del core: `core/` no depende de SDKs de proveedores; todo lo externo entra por adaptadores.
- Montos: siempre enteros en **micras** — millonésimas (10⁻⁶) de la unidad mayor.
  1 USD = `1_000_000`; 1 centavo = `10_000`; $0,001 = `1_000`. Nunca coma flotante.
  El porqué de la escala 6, en [`core/src/money.rs`](core/src/money.rs).
- El ledger es append-only: las correcciones son asientos de reversa, jamás UPDATE.
