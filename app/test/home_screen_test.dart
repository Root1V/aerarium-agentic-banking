import 'dart:convert';

import 'package:aibank_app/api/client.dart';
import 'package:aibank_app/screens/home_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

BffClient clientReturning(http.Response response) => BffClient(
      baseUrl: Uri.parse('http://bff.test'),
      token: () => 'token',
      httpClient: MockClient((_) async => response),
    );

http.Response homeResponse({required int balanceMicros, required List<Map<String, dynamic>> movements}) =>
    http.Response(
      jsonEncode({
        'account': {'id': 'acc-1', 'name': 'Cuenta simple', 'currency': 'USD'},
        'balance': {'amount_micros': balanceMicros, 'currency': 'USD'},
        'movements': movements,
      }),
      200,
    );

Map<String, dynamic> movement({
  required String id,
  required int amountMicros,
  required int sign,
  String kind = 'p2p_transfer',
  String description = '',
}) =>
    {
      'id': id,
      'cursor': id,
      'amount': {'amount_micros': amountMicros, 'currency': 'USD'},
      'sign': sign,
      'kind': kind,
      'description': description,
      'posted_at': '2026-08-31T12:00:00Z',
    };

void main() {
  testWidgets('muestra el saldo formateado', (tester) async {
    final client = clientReturning(homeResponse(balanceMicros: 1525750000, movements: []));

    await tester.pumpWidget(
      MaterialApp(home: HomeScreen(client: client, accountId: 'acc-1')),
    );
    await tester.pumpAndSettle();

    expect(find.text('1.525,75 USD'), findsOneWidget);
  });

  testWidgets('distingue lo que entra de lo que sale', (tester) async {
    final client = clientReturning(homeResponse(balanceMicros: 100000000, movements: [
      movement(id: 'm1', amountMicros: 50000000, sign: 1, kind: 'deposit'),
      movement(id: 'm2', amountMicros: 25000000, sign: -1),
    ]));

    await tester.pumpWidget(
      MaterialApp(home: HomeScreen(client: client, accountId: 'acc-1')),
    );
    await tester.pumpAndSettle();

    // El signo debe ser inequívoco: confundir un cobro con un abono es de los
    // errores que más rápido hacen desconfiar de una app bancaria.
    expect(find.text('+50,00 USD'), findsOneWidget);
    expect(find.text('-25,00 USD'), findsOneWidget);
    expect(find.text('Depósito'), findsOneWidget);
    expect(find.text('Transferencia'), findsOneWidget);
  });

  testWidgets('sin movimientos muestra un mensaje, no una lista vacía', (tester) async {
    final client = clientReturning(homeResponse(balanceMicros: 0, movements: []));

    await tester.pumpWidget(
      MaterialApp(home: HomeScreen(client: client, accountId: 'acc-1')),
    );
    await tester.pumpAndSettle();

    expect(find.text('Todavía no tienes movimientos'), findsOneWidget);
  });

  testWidgets('ante un fallo muestra un mensaje y permite reintentar', (tester) async {
    final client = clientReturning(
      http.Response(jsonEncode({'code': 'unavailable', 'message': 'x'}), 503),
    );

    await tester.pumpWidget(
      MaterialApp(home: HomeScreen(client: client, accountId: 'acc-1')),
    );
    await tester.pumpAndSettle();

    expect(find.text('No pudimos conectarnos. Intenta de nuevo.'), findsOneWidget);
    expect(find.widgetWithText(FilledButton, 'Reintentar'), findsOneWidget);
  });

  testWidgets('una sesión expirada se explica en términos entendibles', (tester) async {
    final client = clientReturning(
      http.Response(jsonEncode({'code': 'unauthenticated', 'message': 'x'}), 401),
    );

    await tester.pumpWidget(
      MaterialApp(home: HomeScreen(client: client, accountId: 'acc-1')),
    );
    await tester.pumpAndSettle();

    expect(find.text('Tu sesión expiró. Vuelve a ingresar.'), findsOneWidget);
  });
}
