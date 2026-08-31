import 'package:flutter/material.dart';

import 'api/client.dart';
import 'screens/home_screen.dart';

/// Punto de entrada de la app.
///
/// La sesión se recibe por configuración de compilación mientras la
/// autenticación real (passkeys vinculadas al dispositivo) no está integrada:
/// así no hay ningún login provisional en el código que pueda quedarse.
void main() {
  const baseUrl = String.fromEnvironment(
    'AIBANK_BFF_URL',
    defaultValue: 'http://localhost:8080',
  );
  const token = String.fromEnvironment('AIBANK_TOKEN');
  const accountId = String.fromEnvironment('AIBANK_ACCOUNT_ID');

  runApp(const AiBankApp(
    baseUrl: baseUrl,
    token: token,
    accountId: accountId,
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
    final client = BffClient(
      baseUrl: Uri.parse(baseUrl),
      token: () => token,
    );

    return MaterialApp(
      title: 'AIBank',
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xFF6750A4)),
        useMaterial3: true,
      ),
      home: accountId.isEmpty
          ? const _MissingSession()
          : HomeScreen(client: client, accountId: accountId),
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
          padding: EdgeInsets.all(24),
          child: Text(
            'Falta la sesión. Ejecuta con:\n'
            '--dart-define=AIBANK_TOKEN=... --dart-define=AIBANK_ACCOUNT_ID=...',
            textAlign: TextAlign.center,
          ),
        ),
      ),
    );
  }
}
