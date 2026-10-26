import 'package:flutter/material.dart';

import 'api/client.dart';
import 'api/models.dart';
import 'auth/passkeys.dart';
import 'sandbox/sandbox_client.dart';
import 'sandbox/sandbox_entry_screen.dart';
import 'screens/account_gate.dart';
import 'screens/enroll_passkey_screen.dart';
import 'screens/home_screen.dart';
import 'screens/login_screen.dart';
import 'screens/onboarding_screen.dart';
import 'theme.dart';

/// Configuración de compilación.
///
/// Sin sesión fija ni sandbox, la app entra con passkeys. `AIBANK_TOKEN` y
/// `AIBANK_ACCOUNT_ID` quedan para apuntar la app a una sesión ya abierta
/// mientras se desarrolla.
const _baseUrl = String.fromEnvironment(
  'AIBANK_BFF_URL',
  defaultValue: 'http://localhost:8080',
);
const _token = String.fromEnvironment('AIBANK_TOKEN');
const _accountId = String.fromEnvironment('AIBANK_ACCOUNT_ID');

/// `true` monta la entrada del sandbox, que crea un titular de prueba.
///
/// Es una constante de compilación y no una opción en tiempo de ejecución a
/// propósito: con `false`, la rama que construye la entrada es código muerto y
/// el compilador la elimina junto con todo lo que solo ella usa. En una app
/// compilada sin este valor no existe ninguna forma —ni oculta ni desactivada—
/// de pedir una sesión sin autenticarse.
const _sandbox = bool.fromEnvironment('AIBANK_SANDBOX');

/// Clave del simulador de titulares, para los sandbox alojados.
const _devKey = String.fromEnvironment('AIBANK_DEV_KEY');

void main() {
  runApp(const AiBankApp(
    baseUrl: _baseUrl,
    token: _token,
    accountId: _accountId,
  ));
}

class AiBankApp extends StatelessWidget {
  const AiBankApp({
    super.key,
    required this.baseUrl,
    required this.token,
    required this.accountId,
  });

  final String baseUrl;
  final String token;
  final String accountId;

  @override
  Widget build(BuildContext context) {
    final Widget home;
    if (accountId.isNotEmpty) {
      home = HomeScreen(
        client: BffClient(baseUrl: Uri.parse(baseUrl), token: () => token),
        accountId: accountId,
      );
    } else if (_sandbox) {
      home = _sandboxEntry();
    } else {
      home = Builder(builder: _login);
    }

    return MaterialApp(
      title: 'AIBank',
      theme: appLightTheme,
      darkTheme: appDarkTheme,
      themeMode: ThemeMode.system,
      home: home,
    );
  }

  // ------------------------------------------------------------- passkeys

  PasskeyFlow _flow() => PasskeyFlow(
        // Las ceremonias no llevan sesión: el cliente no manda token.
        client: BffClient(baseUrl: Uri.parse(baseUrl), token: () => ''),
        platform: const NativePasskeys(),
      );

  Widget _login(BuildContext context) => LoginScreen(
        flow: _flow(),
        onSignedIn: (session) => _enter(context, session),
        onOpenAccount: () => Navigator.of(context).push(MaterialPageRoute<void>(
          builder: (context) => OnboardingScreen(
            client: BffClient(baseUrl: Uri.parse(baseUrl), token: () => ''),
            // El alta aprobada lleva directo a proteger la cuenta, sin pila
            // detrás: volver al formulario de un alta cerrada no tiene sentido.
            onApproved: (result) => Navigator.of(context).pushAndRemoveUntil(
              MaterialPageRoute<void>(
                // Cada pantalla navega con el contexto de su propia ruta: el de
                // la anterior deja de existir al vaciarse la pila.
                builder: (context) => EnrollPasskeyScreen(
                  flow: _flow(),
                  onboardingReference: result.reference,
                  onEnrolled: (session) => _enter(context, session),
                  onAlreadyEnrolled: () => _signOut(context),
                ),
              ),
              (_) => false,
            ),
          ),
        )),
      );

  /// Abre la cuenta con la sesión recién emitida.
  ///
  /// El token vive solo en memoria: cerrar la app es cerrar la sesión, y volver
  /// a entrar es una passkey, que cuesta un segundo. Guardarlo en disco sería
  /// dejar una llave del dinero de alguien en un archivo.
  void _enter(BuildContext context, SessionGrant session) {
    Navigator.of(context).pushAndRemoveUntil(
      MaterialPageRoute<void>(
        builder: (context) => AccountGate(
          client: BffClient(baseUrl: Uri.parse(baseUrl), token: () => session.token),
          onSignOut: () => _signOut(context),
        ),
      ),
      (_) => false,
    );
  }

  void _signOut(BuildContext context) {
    Navigator.of(context).pushAndRemoveUntil(
      MaterialPageRoute<void>(builder: _login),
      (_) => false,
    );
  }

  // -------------------------------------------------------------- sandbox

  Widget _sandboxEntry() {
    final sandbox = SandboxClient(baseUrl: Uri.parse(baseUrl), devKey: _devKey);

    return Builder(
      builder: (context) => SandboxEntryScreen(
        client: sandbox,
        onOpenAccount: () => Navigator.of(context).push(MaterialPageRoute<void>(
          builder: (_) => OnboardingScreen(
            // El alta no lleva sesión: el token del cliente queda vacío y el
            // cliente no lo manda en estas llamadas.
            client: BffClient(baseUrl: Uri.parse(baseUrl), token: () => ''),
            onApproved: (result) async {
              // En producción, aquí se registra la passkey del dispositivo. En
              // el sandbox no hay otra forma de autenticar a nadie.
              final token = await sandbox.createSession(result.customerId!);
              if (!context.mounted) return;
              // Se vacía la pila: volver atrás desde la cuenta recién abierta no
              // debe llevar al formulario del alta.
              Navigator.of(context).pushAndRemoveUntil(
                MaterialPageRoute<void>(
                  builder: (_) => HomeScreen(
                    client: BffClient(baseUrl: Uri.parse(baseUrl), token: () => token),
                    accountId: result.accountId!,
                  ),
                ),
                (_) => false,
              );
            },
          ),
        )),
        // Se reemplaza la ruta en vez de apilarla: volver atrás desde el
        // inicio no debe llevar a crear otro titular.
        onSession: (session) => Navigator.of(context).pushReplacement(
          MaterialPageRoute<void>(
            builder: (_) => HomeScreen(
              client: BffClient(
                baseUrl: Uri.parse(baseUrl),
                token: () => session.token,
              ),
              accountId: session.accountId,
            ),
          ),
        ),
      ),
    );
  }
}
