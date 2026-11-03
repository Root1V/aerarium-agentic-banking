import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/screens/fx_screen.dart';
import 'package:aibank_app/screens/home_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'support/fake_bank.dart';

/// Cuentas en soles y dólares, y cambiar entre ellas con el costo dicho (F3-09).

Map<String, dynamic> money(int micros, String currency) => {'amount_micros': micros, 'currency': currency};

const soles = Account(id: 'acc-pen', name: 'Ana', currency: 'PEN');
const dolares = Account(id: 'acc-usd', name: 'Ana', currency: 'USD');

void main() {
  testWidgets('cotiza, dice cuánto cuesta el cambio y lo ejecuta', (tester) async {
    final bank = FakeBank({
      'POST /v1/fx/quotes': (_) => json({
            'quote_id': 'q-1',
            'from': money(100000000, 'USD'),
            'to': money(336310000, 'PEN'),
            'reference_rate': 3380000,
            'applied_rate': 3363100,
            'cost': money(1690000, 'PEN'),
            'expires_at': DateTime.now().add(const Duration(seconds: 30)).toUtc().toIso8601String(),
          }, 201),
      'POST /v1/fx/conversions': (_) => json({'transaction_id': 'tx-1'}, 201),
    });
    await tester.pumpWidget(MaterialApp(
        home: Builder(
            builder: (context) => TextButton(
                onPressed: () => Navigator.of(context).push(MaterialPageRoute<bool>(
                    builder: (_) => FxScreen(client: bank.client(token: 't'), from: dolares, to: soles))),
                child: const Text('abrir')))));
    await tester.tap(find.text('abrir'));
    await tester.pumpAndSettle();

    expect(find.text('Cambiar a soles'), findsOneWidget);
    await tester.enterText(find.byType(TextField), '100');
    await tester.tap(find.text('Ver cuánto recibo'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    expect(bank.received['POST /v1/fx/quotes'],
        {'from_account_id': 'acc-usd', 'to_account_id': 'acc-pen', 'amount_micros': 100000000});
    expect(find.text('S/ 336,31'), findsOneWidget);
    expect(find.text('S/ 3,3631 por dólar'), findsOneWidget);
    expect(find.text('S/ 3,3800 por dólar'), findsOneWidget);
    expect(find.text('S/ 1,69'), findsOneWidget);

    await tester.tap(find.textContaining('Cambiar · vale'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));
    expect(bank.received['POST /v1/fx/conversions'], {'quote_id': 'q-1'});
    await tester.pumpAndSettle();
    expect(find.text('abrir'), findsOneWidget, reason: 'hecho el cambio, la pantalla se cierra');
  });

  testWidgets('el inicio dice de qué cuenta es y ofrece cambiar a la otra moneda', (tester) async {
    final bank = FakeBank({
      'GET /v1/home': (_) => json({
            'account': {'id': 'acc-pen', 'name': 'Ana', 'currency': 'PEN'},
            'balance': money(500000000, 'PEN'),
            'movements': <Object>[],
          }),
    });
    String? switched;
    await tester.pumpWidget(MaterialApp(
      home: HomeScreen(
        client: bank.client(token: 't'),
        accountId: 'acc-pen',
        accounts: const [soles, dolares],
        onSwitchAccount: (id) => switched = id,
      ),
    ));
    await tester.pumpAndSettle();

    expect(find.text('Cuenta en soles'), findsOneWidget);
    expect(find.text('S/ 500,00'), findsOneWidget);
    expect(find.text('Cambiar a dólares'), findsOneWidget);

    await tester.tap(find.byKey(const Key('account-switcher')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Cuenta en dólares'));
    await tester.pumpAndSettle();
    expect(switched, 'acc-usd');
  });

  testWidgets('con una sola cuenta se ofrece abrir la otra', (tester) async {
    final bank = FakeBank({
      'GET /v1/home': (_) => json({
            'account': {'id': 'acc-pen', 'name': 'Ana', 'currency': 'PEN'},
            'balance': money(500000000, 'PEN'),
            'movements': <Object>[],
          }),
      'POST /v1/accounts': (_) => json({'id': 'acc-usd', 'name': 'Ana', 'currency': 'USD'}, 201),
    });
    String? selected;
    await tester.pumpWidget(MaterialApp(
      home: HomeScreen(
        client: bank.client(token: 't'),
        accountId: 'acc-pen',
        accounts: const [soles],
        onSwitchAccount: (_) {},
        onAccountsChanged: ({select}) => selected = select,
      ),
    ));
    await tester.pumpAndSettle();
    expect(find.text('Cambiar a dólares'), findsNothing, reason: 'sin cuenta en dólares no hay a dónde cambiar');

    await tester.tap(find.byKey(const Key('account-switcher')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Abrir cuenta en dólares'));
    await tester.pumpAndSettle();
    expect(bank.received['POST /v1/accounts'], {'currency': 'USD'});
    expect(selected, 'acc-usd', reason: 'se abre y se pasa a verla');
  });

  test('el tipo de cambio se lee con cuatro decimales', () {
    expect(rateText(3363100), '3,3631');
    expect(rateText(3380000), '3,3800');
  });
}
