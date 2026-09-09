# 12 — Modelo B: iniciación de pagos sobre la cuenta del propio cliente

> Respuesta a **Q25** de Mercatus (§8 de su segunda respuesta). Analiza el modelo
> alternativo y qué haría falta para sostenerlo.
>
> Modelo A (ómnibus) en [doc 09](09-integracion-mercatus.md) y
> [doc 10 §3](10-preguntas-mercatus.md). Lo ya construido, en
> [doc 11](11-spec-integracion-sandbox.md).

## Resumen

**La respuesta a Q25 es sí**, y el modelo B es mejor de lo que Mercatus plantea —
para ellos y para nosotros. Pero **no bajo la figura que proponen**: entregarle a
Mercatus un `client_credentials` sobre la cuenta de un cliente sería un error de
diseño con consecuencias legales, no un atajo.

Tres cosas que conviene separar:

1. **El modelo B arregla un problema que el modelo A tiene y que todavía no
   habíamos nombrado**: en el ómnibus, Mercatus custodia dinero de terceros.
2. **El contrato de pago no cambia en absoluto.** `authorize`, `capture`,
   vigencia, idempotencia y códigos de error son idénticos. Lo que cambia es
   quién autoriza el movimiento y cómo se prueba.
3. **No desbloquea tanto como esperan.** Elimina el obstáculo más difícil, pero
   ni ellos ni nosotros quedamos libres de trámite.

---

## 1. Lo que el modelo B arregla (y conviene mirar de frente)

El modelo A que ya acordamos tiene una consecuencia que hasta ahora quedó
implícita. Juntando dos cosas que Mercatus confirmó por escrito:

- **Q5**: los agentes son de **clientes de Mercatus**, no de Mercatus.
- **Q4**: Mercatus fondea la cuenta maestra **desde su propia cuenta bancaria** y
  distribuye internamente entre sub-cuentas.

Es decir: el dinero de los clientes de Mercatus entra a una cuenta de Mercatus,
y desde ahí a un sub-ledger a nombre de ese cliente dentro de la cuenta ómnibus.
Legalmente ese dinero es de Mercatus; el cliente tiene un **derecho contractual
contra Mercatus**, no dinero en un banco.

Eso es exactamente la forma de **captar fondos de terceros**. Dos implicaciones:

**Regulatoria.** En Perú, la Ley 29985 define dinero electrónico como valor
monetario almacenado, aceptado como medio de pago por terceros y emitido contra
la recepción de fondos. Recibir dinero de clientes y permitir que sus agentes se
paguen entre sí con ese saldo se parece mucho a eso, y emitirlo exige una **EEDE**
(~USD 0,8M de capital, 12–24 meses de trámite — [doc 02](02-regulacion-licencias.md)).
**No estoy afirmando que Mercatus necesite una EEDE**: eso lo dice un abogado
regulatorio peruano, no nosotros. Pero es la primera pregunta que hay que
hacerle, y es más urgente que la constitución de la S.A.C.

**De riesgo.** Si Mercatus quiebra, sus clientes son acreedores no garantizados
de Mercatus. Es la misma forma de fallo que Synapse, documentada en
[doc 02](02-regulacion-licencias.md), donde miles de personas perdieron acceso a
su dinero porque el saldo que veían en una app no era una cuenta bancaria a su
nombre.

**En el modelo B nada de esto ocurre.** El cliente es cliente de AIBank, con su
KYC hecho directamente con nosotros. El dinero nunca sale de nuestros libros
hacia un pool controlado por un tercero. Mercatus no custodia nada: es software.

> Esto no invalida el modelo A. Lo hace **condicional** a que el abogado de
> Mercatus confirme bajo qué figura puede sostener esos saldos. Vale la pena que
> esa consulta se haga ya, porque puede cambiar el plazo de todo.

---

## 2. Q25.1 — ¿Bajo qué figura? No con `client_credentials`

Mercatus pregunta si alcanza con que el cliente le otorgue un
`client_credentials` con scope `payments:write` acotado a su cuenta.

**No alcanza, y el motivo es de fondo, no de configuración.**

`client_credentials` es autenticación de máquina a máquina: prueba que *Mercatus
es Mercatus*. **No prueba que el cliente consintió.** Un secreto entregado a
Mercatus sobre la cuenta de un tercero es una credencial al portador que:

- mueve el dinero de esa persona **indefinidamente**, sin caducidad ligada al
  consentimiento;
- no deja **ningún rastro** de que el titular haya autorizado nada;
- no se puede revocar por el titular, solo por nosotros o por Mercatus;
- no distingue **qué** se autorizó: cualquier monto, cualquier destinatario.

En una disputa —"yo nunca autoricé ese pago"— AIBank no tendría nada que
mostrar salvo la palabra de Mercatus. Ese riesgo es nuestro, no de ellos.

### La figura correcta: mandato de pago con consentimiento verificable

Lo que hace falta es **autorización delegada**, el mismo patrón que usa el open
banking: el cliente autoriza a Mercatus **a través de AIBank**, y AIBank emite a
Mercatus un token que lleva esa delegación dentro.

```
1. El agente quiere pagar → Mercatus redirige al cliente a AIBank.
2. El cliente se autentica CON AIBANK (passkey, en nuestra app).
3. Ve exactamente qué autoriza: qué cuenta, qué tope, hasta cuándo.
4. AIBank registra el mandato y emite a Mercatus un token ligado a él.
5. Mercatus opera dentro del mandato. Nada más.
6. El cliente ve y revoca sus mandatos en la app, cuando quiera.
```

Diferencias que importan, frente a lo que proponen:

| | `client_credentials` del cliente | Mandato delegado |
|---|---|---|
| Quién se autentica | Mercatus, con un secreto del cliente | **El cliente, contra AIBank** |
| Prueba del consentimiento | Ninguna | Registro firmado por nosotros |
| Alcance | La cuenta entera, sin tope | Cuenta, tope y vigencia explícitos |
| Revocación | Solo AIBank o Mercatus | **El titular, desde su app** |
| En una disputa | La palabra de Mercatus | Evidencia auditable |

El `client_id` de Mercatus **sigue siendo uno solo**, como hoy. Lo que se
multiplica no son las credenciales sino los mandatos, y esos los otorga cada
cliente.

### Sobre la figura de iniciador (PISP)

Mercatus pregunta si exigimos que estén registrados como iniciador de pagos.
La respuesta honesta es que **esa figura hoy no existe en Perú**: no hay mandato
integral de open finance, a diferencia de Brasil, que sí tiene iniciación de
pagos regulada ([doc 02 §6](02-regulacion-licencias.md)).

Eso corta en dos direcciones:

- **A favor**: no hay registro ante el supervisor que esperar. Nada que tramitar.
- **En contra**: **no hay marco que reparta la responsabilidad.** Sin un régimen
  de iniciación, todo se apoya en el mandato contractual y en cómo lo diseñemos.
  Si el mecanismo de consentimiento es débil, el fraude y las disputas los
  absorbe AIBank.

Por eso el mandato no es burocracia: en ausencia de regulación, **es lo único que
nos protege**. Un régimen como el brasileño nos habría dado reglas hechas; aquí
hay que construirlas.

---

## 3. Q25.2 — Sí, hace falta una sub-cuenta para el agente

Mercatus pregunta si el agente necesita un espacio propio dentro de la cuenta del
cliente o si puede operar sobre el `account_id` del titular.

**Sobre el `account_id` del titular, no.** Un agente con un error de programación
—o un token de Mercatus comprometido— podría vaciar la cuenta principal de una
persona. La diferencia práctica es la que hay entre darle a un agente una tarjeta
prepago con S/ 200 y darle la tarjeta de débito.

La estructura correcta es una **sub-cuenta de propósito** bajo el mismo titular:

- **Mismo KYC**: es la misma persona, no hay diligencia adicional.
- **Saldo propio**, que el cliente fondea deliberadamente desde su cuenta.
- **Tope propio**, independiente del de la cuenta principal.
- **Revocable por separado**: cerrar el acceso del agente no toca la cuenta.
- **Visible en la app** como lo que es: el dinero que esa persona le confió a un
  agente.

Nos cuesta poco: es el modelo de cuenta que ya tenemos —una cuenta de cliente
bajo un producto del catálogo— con un propósito y un mandato asociados. Y es
mejor producto: convierte una decisión difícil ("¿le doy acceso a mi banco a un
agente?") en una fácil ("le pongo S/ 200 y veo en qué los gasta").

---

## 4. Q25.3 — El contrato de pago no cambia

Esta es la mejor noticia y conviene decirla sin matices: **`authorize`,
`capture`, `refund` y el `GET` son exactamente los mismos.** Misma vigencia de 15
minutos, misma idempotencia, mismos códigos de error, mismos importes en micras.

Lo verifiqué contra el código, no lo supongo:

| Capa | ¿Cambia en el modelo B? |
|---|---|
| Primitiva de autorización del core (Rust) | **No.** No conoce integraciones ni titulares: mueve dinero entre dos cuentas |
| Contrato REST de pago | **No** |
| Códigos de error existentes | **No**; se agregan tres |
| Comprobación de acceso a la cuenta | **Sí.** Hoy pregunta "¿esta cuenta es de esta integración?"; pasaría a "¿hay un mandato vivo del titular para esta integración?" |
| Flujo OAuth | **Sí.** Hoy solo `client_credentials`; haría falta `authorization_code` |

Es decir: dos puntos del sistema, no una reescritura. El resto —la máquina de
estados, la contabilidad, la idempotencia, el barrendero de vencidas— sirve igual
para los dos modelos.

Los tres códigos nuevos:

| HTTP | `error` | Cuándo |
|---|---|---|
| 403 | `mandate_revoked` | El titular revocó el permiso |
| 403 | `mandate_expired` | El mandato venció |
| 422 | `mandate_limit_exceeded` | El pago supera el tope autorizado |

---

## 5. Lo que el modelo B NO desbloquea

Mercatus espera que esto les permita arrancar sin esperar la constitución de la
S.A.C. y el KYB. **Es cierto a medias, y conviene ser preciso.**

**Sí desaparece** el obstáculo más difícil: no hay cuenta ómnibus, así que no hay
KYB de custodia ni la pregunta sobre captación de fondos de terceros.

**Pero sigue haciendo falta:**

1. **Que Mercatus sea una persona jurídica.** Necesitamos contratar con alguien
   —responsabilidad, protección de datos, el compromiso sobre `owner_reference`—
   y eso exige la entidad constituida igual.
2. **Diligencia sobre Mercatus como proveedor de servicios.** Más liviana que un
   KYB de custodia, pero no cero: van a mover dinero de nuestros clientes.

**Y hay un obstáculo mayor que ninguno de los dos modelos resuelve, y es
nuestro**: AIBank todavía no tiene licencia ni proveedor BaaS contratado. Hasta
que eso se cierre, **ningún modelo mueve dinero real**. El sandbox funciona
completo hoy, pero el paso a producción está bloqueado de nuestro lado, no del de
ellos. Conviene decírselo con esas palabras.

---

## 6. Por qué el modelo B nos conviene a nosotros

Más allá de lo regulatorio, cambia quién es dueño de la relación:

| | Modelo A | Modelo B |
|---|---|---|
| Cliente de AIBank | Mercatus, una empresa | **Cada usuario final** |
| Nuestro rol | Tubería de liquidación | Banco de esas personas |
| Depósitos | De Mercatus | **De cada cliente** |
| Venta cruzada | Ninguna | Crédito, que es el motor del plan |
| Si Mercatus se va | Perdemos todo el volumen | Los clientes se quedan |

El plan de negocio ([doc 04](04-plan-negocio.md)) dice que el crédito es el motor
de ingresos y que se construye sobre datos transaccionales propios. En el modelo
A no vemos a ningún usuario final: vemos a Mercatus. En el modelo B cada persona
es cliente nuestro, con su historial, que es exactamente el insumo del
[motor de scoring](../risk/src/aibank_risk/) que ya está construido.

**La contrapartida honesta**: el modelo B solo sirve para gente que *ya* es
cliente de AIBank, y hoy no hay ninguno. No es un canal de captación — es una
forma de monetizar mejor a los clientes que tengamos. El modelo A sigue siendo el
camino para el resto de los clientes de Mercatus, como ellos mismos plantean.

---

## 7. Qué habría que construir

No es un cambio de configuración. En esfuerzo se parece a lo que ya se construyó
para el modelo A.

| # | Pieza | Dónde |
|---|---|---|
| 1 | **Mandato de pago**: cuenta, integración, topes, vigencia, estado y revocación | core (Rust) |
| 2 | **Flujo de consentimiento** `authorization_code` con autenticación del cliente | oauth + app |
| 3 | **Pantalla de consentimiento**: qué cuenta, qué tope, hasta cuándo | app (Flutter) |
| 4 | **Sub-cuenta de agente** con propósito y tope propio | core + BFF |
| 5 | **Gestión de mandatos** para el titular: verlos y revocarlos | app + BFF |
| 6 | Comprobación por mandato en la API de socio y tres códigos nuevos | services/mercatus |

Las piezas 3 y 5 son de la app de consumo, que hoy tiene saldo, movimientos y
transferencia. Son pantallas nuevas, no ajustes.

---

## 8. Recomendación

**Responder que sí a Q25, con la corrección de la figura**, y no construirlo
todavía.

1. **Confirmar el modelo B como arquitectura objetivo**, con mandato delegado y
   sub-cuenta de agente — no con `client_credentials` sobre cuenta ajena.
2. **Pedirles que le lleven a su abogado la pregunta del §1** antes de seguir con
   el KYB del ómnibus. Puede cambiar cuál de los dos modelos es el principal.
3. **Seguir con el modelo A** para el sandbox y para el resto de sus clientes:
   está construido, probado y no depende de esto.
4. **Cerrar el país ancla y el proveedor BaaS de nuestro lado**, que es el
   bloqueante real de los dos modelos.
5. **Construir el modelo B cuando haya clientes propios que lo usen**, no antes.
   Hoy sería construir para un universo de cero usuarios.

Un punto que conviene no dejar pasar: Mercatus dice que **no lo asume ni lo
construye sin confirmación explícita**. Es la actitud correcta y merece una
respuesta igual de explícita — un sí con condiciones escritas, no un "lo vemos".
