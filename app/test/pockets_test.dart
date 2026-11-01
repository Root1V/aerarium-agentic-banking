import 'package:aibank_app/screens/home_screen.dart';
import 'package:aibank_app/screens/pockets_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;

import 'support/fake_bank.dart';

/// Los bolsillos (F3-02) en la app: verlos, abrir uno, apartar y cerrar.

Map<String, dynamic> usd(int micros) => {'amount_micros': micros, 'currency': 'USD'};

Map<String, dynamic> pocket(String id, String name, int micros, {String purpose = 'general'}) =>
    {'id': id, 'name': name, 'purpose': purpose, 'balance': usd(micros)};

/// Un banco con bolsillos que cambian según lo que se hace.
FakeBank bank(List<Map<String, dynamic>> pockets) => FakeBank({
      'GET /v1/home': (_) => json({
            'account': {'id': 'acc-1', 'name': 'Ana', 'currency': 'USD'},
            'balance': usd(100000000),
            'movements': <Object>[],
          }),
      'GET /v1/accounts/acc-1/pockets': (_) => json({'pockets': pockets}),
      'POST /v1/accounts/acc-1/pockets': (body) {
        final created = pocket('pk-new', body['name'] as String, 0, purpose: body['purpose'] as String);
        pockets.add(created);
        return json(created, 201);
      },
      'POST /v1/pockets/pk-1/deposit': (body) {
        final p = pockets.firstWhere((p) => p['id'] == 'pk-1');
        p['balance'] = usd((p['balance']['amount_micros'] as int) + (body['amount_micros'] as int));
        return json({'transaction_id': 'tx-1'}, 201);
      },
      'DELETE /v1/pockets/pk-1': (_) {
        pockets.removeWhere((p) => p['id'] == 'pk-1');
        return http.Response('', 204);
      },
    });

void main() {
  testWidgets('el inicio dice cuánto hay apartado, sin sumarlo al disponible', (tester) async {
    final b = bank([pocket('pk-1', 'Vacaciones', 40000000), pocket('pk-2', 'Para la tarjeta', 50000000, purpose: 'card')]);
    await tester.pumpWidget(MaterialApp(home: HomeScreen(client: b.client(token: 't'), accountId: 'acc-1')));
    await tester.pumpAndSettle();

    // Dentro de la tarjeta del saldo, en una fila.
    expect(find.text('Apartado en bolsillos'), findsOneWidget);
    expect(find.text('90,00 USD'), findsOneWidget);
    expect(find.text('100,00 USD'), findsOneWidget, reason: 'el disponible es el de la cuenta, sin los bolsillos');

    await tester.tap(find.byKey(const Key('pockets')));
    await tester.pumpAndSettle();
    expect(find.byType(PocketsScreen), findsOneWidget);
    expect(find.text('Y 100,00 USD disponibles en tu cuenta'), findsOneWidget);
    expect(find.text('Para pagar la tarjeta'), findsOneWidget);
  });

  testWidgets('abrir un bolsillo para la tarjeta', (tester) async {
    final b = bank([]);
    await tester.pumpWidget(MaterialApp(
        home: PocketsScreen(client: b.client(token: 't'), accountId: 'acc-1', currency: 'USD')));
    await tester.pumpAndSettle();
    expect(find.text('Aparta dinero para algo'), findsOneWidget);

    await tester.tap(find.text('Nuevo bolsillo'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Es para pagar la tarjeta'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Abrir'));
    await tester.pumpAndSettle();

    expect(b.received['POST /v1/accounts/acc-1/pockets'], {'name': 'Para la tarjeta', 'purpose': 'card'});
    expect(find.text('Para la tarjeta'), findsOneWidget);
  });

  testWidgets('apartar y cerrar, con lo que vuelve a la cuenta dicho antes', (tester) async {
    final b = bank([pocket('pk-1', 'Vacaciones', 40000000)]);
    await tester.pumpWidget(MaterialApp(
        home: PocketsScreen(client: b.client(token: 't'), accountId: 'acc-1', currency: 'USD')));
    await tester.pumpAndSettle();

    // Las acciones se abren al tocar el bolsillo.
    await tester.tap(find.text('Vacaciones'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Apartar'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), '25,50');
    await tester.tap(find.widgetWithText(FilledButton, 'Apartar'));
    await tester.pumpAndSettle();
    expect(b.received['POST /v1/pockets/pk-1/deposit'], {'amount_micros': 25500000});
    // En el resumen y en su fila: es el único bolsillo.
    expect(find.text('65,50 USD'), findsNWidgets(2));

    await tester.tap(find.text('Vacaciones'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Cerrar el bolsillo'));
    await tester.pumpAndSettle();
    expect(find.text('Los 65,50 USD que tiene vuelven a tu cuenta.'), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, 'Cerrar'));
    await tester.pumpAndSettle();
    expect(b.calls, contains('DELETE /v1/pockets/pk-1'));
    expect(find.text('Vacaciones'), findsNothing);
  });
}
