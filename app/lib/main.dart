import 'package:flutter/material.dart';

import 'api/client.dart';
import 'sandbox/sandbox_client.dart';
import 'sandbox/sandbox_entry_screen.dart';
import 'screens/home_screen.dart';
import 'theme.dart';

/// Configuración de compilación.
///
/// La sesión se recibe por configuración mientras la autenticación real
/// (passkeys vinculadas al dispositivo) no está integrada: así no hay ningún
/// login provisional en el código que pueda quedarse.
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
      home = const _MissingSession();
    }

    return MaterialApp(
      title: 'AIBank',
      theme: appLightTheme,
      darkTheme: appDarkTheme,
      themeMode: ThemeMode.system,
      home: home,
    );
  }

  Widget _sandboxEntry() {
    return Builder(
      builder: (context) => SandboxEntryScreen(
        client: SandboxClient(baseUrl: Uri.parse(baseUrl), devKey: _devKey),
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

class _MissingSession extends StatelessWidget {
  const _MissingSession();

  @override
  Widget build(BuildContext context) {
    return const Scaffold(
      body: Center(
        child: Padding(
          padding: EdgeInsets.all(AppSpacing.xl),
          child: Text(
            'Falta la sesión. Ejecuta con:\n'
            '--dart-define=AIBANK_TOKEN=... --dart-define=AIBANK_ACCOUNT_ID=...\n\n'
            'o, contra el sandbox:\n'
            '--dart-define=AIBANK_SANDBOX=true',
            textAlign: TextAlign.center,
          ),
        ),
      ),
    );
  }
}
