import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/screens/transfer_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;

import 'support/fake_bank.dart';

const _juan = {'id': 'acc-juan', 'name': 'Juan Pérez (prueba)', 'currency': 'USD'};
const _ana = {'id': 'acc-ana', 'name': 'Ana María Torres', 'currency': 'USD'};

http.Response _sent(Map<String, dynamic> _) => json({'transaction_id': 'tx-1', 'duplicate': false}, 201);

Future<FakeBank> pumpTransfer(WidgetTester tester, {List<Object> recent = const [], Account? suggested}) async {
  final bank = FakeBank({
    'GET /v1/payees/recent': (_) => json({'payees': recent}),
    'POST /v1/transfers': _sent,
  });
  await tester.pumpWidget(MaterialApp(
    home: Navigator(
      onGenerateRoute: (_) => MaterialPageRoute<void>(
        builder: (_) => TransferScreen(
          client: bank.client(token: 'tok'),
          fromAccountId: 'acc-1',
          currency: 'USD',
          suggestedPayee: suggested,
        ),
      ),
    ),
  ));
  await tester.pumpAndSettle();
  return bank;
}

void main() {
  testWidgets('se envía a un reciente sin escribir ninguna cuenta', (tester) async {
    final bank = await pumpTransfer(tester, recent: [_ana, _juan]);

    expect(find.text('Recientes'), findsOneWidget);
    expect(find.text('AT'), findsOneWidget, reason: 'iniciales de Ana María Torres');

    await tester.tap(find.text('Ana María Torres'));
    await tester.pump();

    expect(find.text('Para'), findsOneWidget);
    expect(find.byKey(const Key('destination')), findsNothing, reason: 'no se ve ningún código de cuenta');

    await tester.enterText(find.byKey(const Key('amount')), '20');
    await tester.tap(find.byKey(const Key('send')));
    await tester.pumpAndSettle();

    expect(bank.received['POST /v1/transfers']!['to_account_id'], 'acc-ana');
  });

  testWidgets('"Cambiar" vuelve a la lista', (tester) async {
    await pumpTransfer(tester, recent: [_ana]);
    await tester.tap(find.text('Ana María Torres'));
    await tester.pump();
    await tester.tap(find.text('Cambiar'));
    await tester.pump();
    expect(find.text('Recientes'), findsOneWidget);
    expect(find.byKey(const Key('destination')), findsOneWidget);
  });

  testWidgets('el destinatario de prueba aparece primero y sin repetirse', (tester) async {
    await pumpTransfer(tester,
        recent: [_ana, _juan], suggested: Account.fromJson(_juan));
    final names = tester.widgetList<Text>(find.descendant(of: find.byType(ListView), matching: find.byType(Text)))
        .map((t) => t.data)
        .where((d) => d != null && d.length > 2)
        .toList();
    expect(names, ['Juan Pérez (prueba)', 'Ana María Torres']);
  });

  testWidgets('sin recientes se escribe la cuenta, como antes', (tester) async {
    final bank = await pumpTransfer(tester);
    expect(find.text('Recientes'), findsNothing);
    expect(find.text('Celular o cuenta'), findsOneWidget);

    await tester.enterText(find.byKey(const Key('destination')), 'acc-otra');
    await tester.enterText(find.byKey(const Key('amount')), '5');
    await tester.tap(find.byKey(const Key('send')));
    await tester.pumpAndSettle();
    expect(bank.received['POST /v1/transfers']!['to_account_id'], 'acc-otra');
  });

  testWidgets('si la lista no carga, igual se puede enviar', (tester) async {
    final bank = FakeBank({'POST /v1/transfers': _sent}); // sin ruta de recientes: 404
    await tester.pumpWidget(MaterialApp(
      home: TransferScreen(client: bank.client(), fromAccountId: 'acc-1', currency: 'USD'),
    ));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('destination')), findsOneWidget);
  });

  test('iniciales', () {
    expect(initials('Juan Pérez (prueba)'), 'JP');
    expect(initials('Ana María Torres'), 'AT');
    expect(initials('carmen'), 'C');
    expect(initials(''), '?');
  });
}
