# 14 — UX de la app y decisión Flutter frente a nativo

> Revisión de dos decisiones que se tomaron pronto y con poca evidencia: **cómo debe ser la
> app** de un banco cuyo diferencial son los agentes, y **si Flutter sigue siendo la
> tecnología correcta**. Octubre 2026. Corrige una afirmación del [doc 06](06-stack-tecnologico.md).

## Resumen

**Se mantiene Flutter, pero no por la razón original**, y con dos reglas nuevas:

1. **El núcleo de seguridad es nativo** (Swift y Kotlin), invocado desde Flutter.
2. **Flutter web solo sirve para el portal de clientes autenticados**, nunca para la web pública.

En UX, tres restricciones pesan más que cualquier benchmark genérico: la **SBS exige dos
factores** en toda operación digital, **en Perú el listón lo pone Yape**, y **un banco de
agentes tiene que resumir cientos de micropagos** en vez de notificarlos uno a uno.

---

## 1. Flutter o nativo

### Lo que decía el doc 06, y por qué no aguanta

El doc 06 justificaba Flutter como «la elección dominante de los neobancos grandes». La
evidencia muestra una división, no una mayoría:

| Banco | Stack móvil |
|---|---|
| Nubank | Flutter, con un intérprete de UI dirigida por servidor encima ([VGV][vgv], [InfoQ][infoq]) |
| Revolut | **Nativo**: Swift en iOS, Kotlin en Android ([iOS][rev-ios], [Android][rev-and]) |

El argumento que sí aguanta es otro: **Revolut puede mantener dos equipos nativos y
nosotros no.**

### Por qué Flutter encaja en este banco

- **El mercado es Android y de gama baja.** En Perú, Android tiene el 82% del tráfico móvil
  ([StatCounter][statcounter]) y la gama baja fue el 47% de los teléfonos importados en 2024
  ([Forbes Perú][forbes]). El 18% de iOS es el segmento con más poder adquisitivo: no se
  puede abandonar. Un solo código para los dos es lo que hace viable cubrirlos con un
  equipo pequeño.
- **El sobrecoste de tamaño es tolerable**: unos 4–5 MB fijos del motor por arquitectura
  ([javathinking][size]), y publicando con *app bundles* cada teléfono solo descarga lo suyo.
- **Las alternativas no mejoran la situación:**
  - **Compose Multiplatform** es estable en iOS desde mayo de 2025 ([Kotlin][cmp]), pero no
    hay ningún banco con nombre que comparta UI con él en producción; los bancos lo usan para
    compartir lógica con UI nativa ([Batteries Included][kmp-banks]). Eso son dos interfaces.
  - **React Native** tiene más oferta de desarrolladores y su nueva arquitectura cerró la
    brecha de rendimiento, pero el código JavaScript es más fácil de descompilar
    ([Acquaint][rn-sec]) y no resuelve nada que hoy nos falte.

### Los tres riesgos, y cómo se contienen

**1. La seguridad pasa por plugins de terceros.** Las passkeys en Flutter dependen de
paquetes de la comunidad que envuelven las APIs nativas ([pub.dev][passkey-pkg]). En un
banco es el último sitio donde conviene un intermediario sin auditar.

> **Regla 1.** Passkeys, vinculación del dispositivo, atestación de la app (App Attest, Play
> Integrity) y detección de root o jailbreak se escriben en **Swift y Kotlin**, y Flutter
> los invoca por *platform channels*. Flutter no impide lo nativo: solo hay que decidir qué
> parte lo es, y esta es la que se decide así.

*Aplicada en `feat/app-auth-passkeys`:* el puente de passkeys en iOS son ~200 líneas de
Swift sobre AuthenticationServices, sin paquete intermedio. Solo traduce JSON de WebAuthn;
desafíos y verificación viven en el banco.

**2. El compromiso de Google.** En 2024 un exmiembro del equipo hizo un fork (Flock)
alegando que tres de seis plataformas estaban en mantenimiento ([DevClass][flock]). Eran
las de escritorio, no las móviles. Riesgo medio, y la regla 1 lo acota: lo más delicado no
depende de Flutter.

**3. La web.** Flutter web pinta en un canvas, pesa 1,5–2 MB y tiene huecos de
accesibilidad con lectores de pantalla ([Softaims][flutter-web]).

> **Regla 2.** Flutter web sirve como **portal para clientes autenticados**. La web pública
> —marketing, precios, ayuda— es HTML normal: necesita indexarse, cargar rápido y ser
> accesible, y Flutter no da ninguna de las tres.

### Cuándo revisar esta decisión

- Si el equipo crece hasta poder sostener equipos separados de iOS y Android.
- Si aparecen integraciones profundas del sistema operativo como producto central
  (widgets, reloj, Wallet), que en Flutter exigen extensiones nativas igualmente.
- Si Google anuncia que reduce Flutter móvil.

**Cambiar es más barato ahora que nunca**: hoy la app son cuatro pantallas. Con lo anterior,
no hay motivo para hacerlo.

---

## 2. UX: principios para este banco

### Tres restricciones propias

**A. La regulación define el login.** La [Resolución SBS 2286-2024][sbs] exige desde julio
de 2025 **dos factores independientes** en toda operación digital, y **el banco responde por
las pérdidas** si no los aplicó. Una passkey con biometría cubre los dos en un solo gesto:
posesión (la clave atada al dispositivo) e inherencia (huella o cara). Las passkeys son la
forma de cumplir **sin** OTP por SMS, no un extra.

**B. En Perú, el listón es Yape.** Unos 17 millones de usuarios activos y 34 operaciones al
mes cada uno ([Peru Retail][yape-ia]), con QR y alias de celular, sin formularios
([dLocal][yape-dlocal]). Pagar a un celular o con QR interoperable es lo mínimo esperado.

**C. Un banco de agentes tiene un problema de notificaciones que nadie ha resuelto.** Monzo
notifica cada movimiento ([LeanConvert][monzo]): funciona con cinco compras al día. Un agente
que paga $0,001 por llamada hace cientos, y notificarlas una a una es spam. Hay que
**agregar**: «Tu agente de investigación gastó 0,42 USD hoy en 380 llamadas».

### Principios

| Principio | De dónde viene | Cómo se aplica aquí |
|---|---|---|
| **El saldo primero, y el verdadero** | Revolut: saldo arriba. Monzo: transparencia siempre | **Disponible y retenido por separado.** El ledger ya separa las retenciones; la app las esconde. «0,50 USD retenidos por pagos en curso» evita la llamada a soporte de «me falta dinero» |
| **Onboarding por niveles** | El abandono del KYC está entre el 40% y el 68%, y se dispara pasados 3 minutos y tras un primer intento fallido con el documento ([CheckFile][kyc]) | Empezar con DNI y una cuenta de topes bajos, y subir con el KYC completo. **Los niveles de KYC son productos**: el core ya tiene topes por producto. Validar con el asesor qué permite la SBS |
| **Fricción positiva** | Monzo: pasos extra deliberados donde protegen ([Econsultancy][friction]) | Ya está en el consentimiento: los dos botones pesan lo mismo y retirar pide confirmación. Extenderlo a los agentes |
| **Centro de control de agentes** | Visa y Mastercard: topes programables, aprobación para comercios nuevos o montos altos, revocación en tiempo real ([Elogic][agentic]) | Los mandatos ya tienen topes, vigencia y revocación. Faltan el **gasto agregado por agente**, los **umbrales de aprobación** y el «qué compró» |
| **Una frase clara por estado** | Lenguaje llano; un estado vacío dice qué hacer | Ya es la regla de la app: «Sin límite de monto», «No pudimos conectarnos» |
| **UI dirigida por servidor, más adelante** | Nubank: el 70% de sus pantallas nuevas, publicadas en 20 minutos sin la tienda ([Stac][sdui]) | No ahora. Encaja cuando haya un equipo de producto que necesite publicar sin esperar a la tienda |

---

## 3. Plan resultante

| # | Rama | Qué | Por qué en este orden |
|---|---|---|---|
| 1 | `feat/app-runnable` | iOS, Android y portal web; entrada de sandbox; tokens de diseño | Sin esto la app solo existe dentro de los tests |
| 2 | `feat/app-saldo-retenido` | Disponible frente a retenido, con los pagos en curso | Pequeño y el backend ya lo tiene |
| 3 | `feat/app-onboarding` | Alta por niveles | Sin esto nadie puede hacerse cliente |
| 4 | `feat/app-agentes` | Centro de control de agentes | Es lo que diferencia a este banco |
| 5 | `feat/app-auth-passkeys` | Núcleo de seguridad nativo | Obligatorio por la SBS antes de mover dinero real |
| 6 | — | QR y alias de celular interoperables | Depende del proveedor de rieles |
| 7 | — | Tarjetas | El backend existe; falta exponerlo |

---

[vgv]: https://verygood.ventures/blog/building-for-100m-users-nubanks-flutter-journey/
[infoq]: https://www.infoq.com/presentations/mobile-server-driven-ui-scale
[rev-ios]: https://medium.com/revolut/the-fundamentals-of-ios-at-revolut-f75acb765ac8
[rev-and]: https://medium.com/revolut/the-fundamentals-of-android-at-revolut-3537101a2c7c
[statcounter]: https://gs.statcounter.com/os-market-share/mobile/peru
[forbes]: https://forbes.pe/economia-y-finanzas/2025-02-19/importacion-de-smartphones-crecio-166-en-2024-estas-fueron-las-marcas-mas-vendidas/
[size]: https://www.javathinking.com/blog/flutter-apps-are-too-big-in-size/
[cmp]: https://kotlinlang.org/compose-multiplatform/
[kmp-banks]: https://batteriesincluded.io/insights/kotlin-multiplatform-and-compose-multiplatform
[rn-sec]: https://acquaintsoft.com/blog/react-native-vs-flutter-for-fintech-apps
[passkey-pkg]: https://pub.dev/packages/flutter_passkey_service
[flock]: https://devclass.com/2024/10/30/flutter-forked-as-flock-developer-cites-company-wide-issues-at-google/
[flutter-web]: https://softaims.com/blog/flutter-web-desktop-production-ready-2026
[sbs]: https://lpderecho.pe/sbs-reglamentos-reforzar-validacion-identidad-usuarios-consentimiento-operaciones-resolucion-sbs-02286-2024/
[yape-ia]: https://www.peru-retail.com/yape-va-mas-alla-de-los-pagos-asi-busca-transformar-la-experiencia-de-sus-usuarios-con-ia
[yape-dlocal]: https://www.dlocal.com/blog/markets-and-consumers/yape-perus-superapp-is-now-on-file-and-redefining-mobile-conversion/
[monzo]: https://leanconvert.com/insights/how-to-get-as-good-at-ux-as-monzo/
[kyc]: https://www.checkfile.ai/en-US/blog/digital-onboarding-kyc-reduce-dropoffs-compliance
[friction]: https://econsultancy.com/prudent-ux-for-banking-monzo-designs-positive-friction/
[agentic]: https://elogic.co/blog/agentic-payments/
[sdui]: https://stac.dev/blogs/tech-companies-sdui
