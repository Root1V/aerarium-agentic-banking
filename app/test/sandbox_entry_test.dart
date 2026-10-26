import 'dart:convert';

import 'package:aibank_app/sandbox/sandbox_client.dart';
import 'package:aibank_app/sandbox/sandbox_entry_screen.dart';
import 'package:aibank_app/theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// La entrada del sandbox crea una persona de prueba y entra con ella.
///
/// Lo que se prueba es sobre todo que falle bien: quien la ve es alguien
/// integrando o enseñando la app, y un "algo salió mal" sin pista le cuesta
/// media hora. Cada causa distinta tiene su propia explicación.

class _Recorder {
  final List<http.Request> requests = [];
}

(SandboxClient, _Recorder) _client({
  int status = 201,
  String devKey = '',
  bool throwNetwork = false,
}) {
  final recorder = _Recorder();
  final mock = MockClient((request) async {
    recorder.requests.add(request);
    if (throwNetwork) throw http.ClientException('connection refused');
    if (status != 201) return http.Response('', status);
    return http.Response(
      jsonEncode({
        'customer_id': 'c-1',
        'account_id': 'acc-1',
        'token': 'dev-token',
        'currency': 'USD',
        'balance': 100000000,
      }),
      201,
      headers: {'content-type': 'application/json'},
    );
  });
  return (
    SandboxClient(
      baseUrl: Uri.parse('http://sandbox.test'),
      devKey: devKey,
      httpClient: mock,
    ),
    recorder,
  );
}

Future<SandboxSession?> _pumpAndCreate(
  WidgetTester tester,
  SandboxClient client,
) async {
  SandboxSession? received;
  await tester.pumpWidget(MaterialApp(
    theme: appLightTheme,
    home: SandboxEntryScreen(client: client, onSession: (s) => received = s),
  ));
  await tester.tap(find.text('Crear titular de prueba'));
  await tester.pumpAndSettle();
  return received;
}

void main() {
  testWidgets('dice que es un entorno de pruebas', (tester) async {
    final (client, _) = _client();
    await tester.pumpWidget(MaterialApp(
      home: SandboxEntryScreen(client: client, onSession: (_) {}),
    ));

    // Quien vea una captura tiene que saber que no es dinero real.
    expect(find.textContaining('sin dinero real'), findsOneWidget);
  });

  testWidgets('crea el titular y entrega la sesión', (tester) async {
    final (client, recorder) = _client();
    final session = await _pumpAndCreate(tester, client);

    expect(session?.accountId, 'acc-1');
    expect(session?.token, 'dev-token');

    final request = recorder.requests.single;
    expect(request.url.path, '/dev/holders');
    final body = jsonDecode(request.body) as Map<String, dynamic>;
    expect(body['name'], 'Ana Pérez');
    // Entero en micras, como todo importe que cruza la red.
    expect(body['initial_balance'], 100000000);
  });

  testWidgets('manda la clave del entorno solo si la tiene', (tester) async {
    final (conClave, r1) = _client(devKey: 'secreta');
    await _pumpAndCreate(tester, conClave);
    expect(r1.requests.single.headers['X-Dev-Key'], 'secreta');

    final (sinClave, r2) = _client();
    await _pumpAndCreate(tester, sinClave);
    // En local no hay clave, y mandar una vacía sería ruido en los logs.
    expect(r2.requests.single.headers.containsKey('X-Dev-Key'), isFalse);
  });

  testWidgets('sin servidor lo dice, con la dirección', (tester) async {
    final (client, _) = _client(throwNetwork: true);
    final session = await _pumpAndCreate(tester, client);

    expect(session, isNull);
    expect(find.textContaining('No hay respuesta en http://sandbox.test'),
        findsOneWidget);
  });

  testWidgets('una clave equivocada no se confunde con un fallo de red',
      (tester) async {
    final (client, _) = _client(status: 401);
    await _pumpAndCreate(tester, client);

    expect(find.textContaining('AIBANK_DEV_KEY'), findsOneWidget);
  });

  testWidgets('un servidor que no es sandbox se reconoce como tal',
      (tester) async {
    final (client, _) = _client(status: 404);
    await _pumpAndCreate(tester, client);

    expect(find.textContaining('no es un sandbox'), findsOneWidget);
  });

  testWidgets('un nombre vacío no llega al servidor', (tester) async {
    final (client, recorder) = _client();
    await tester.pumpWidget(MaterialApp(
      home: SandboxEntryScreen(client: client, onSession: (_) {}),
    ));
    await tester.enterText(find.byType(TextField), '   ');
    await tester.tap(find.text('Crear titular de prueba'));
    await tester.pumpAndSettle();

    expect(recorder.requests, isEmpty);
    expect(find.textContaining('Escribe un nombre'), findsOneWidget);
  });
}
