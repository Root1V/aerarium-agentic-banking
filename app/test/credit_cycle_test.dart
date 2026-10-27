import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/screens/credit_card_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;

import 'support/fake_bank.dart';

Map<String, dynamic> money(int micros) => {'amount_micros': micros, 'currency': 'USD'};

Map<String, dynamic> statement({String status = 'pending', int paid = 0, int interest = 0}) => {
      'period_start': '2026-09-27T12:00:00Z',
      'period_end': '2026-10-27T12:00:00Z',
      'opening': money(0),
      'charges': money(200000000),
      'payments': money(0),
      'interest': money(interest),
      'closing': money(200000000),
      'minimum_payment': money(10000000),
      'due_date': '2026-11-16',
      'paid_since_cut': money(paid),
      'status': status,
    };

Map<String, dynamic> card({Map<String, dynamic>? statement, String status = 'active', int outstanding = 200000000}) => {
      'id': 'card-1',
      'limit': money(450000000),
      'outstanding': money(outstanding),
      'available': money(450000000 - outstanding),
      'annual_rate_bps': 4500,
      'cycle_day': 27,
      'payment_days': 20,
      'status': status,
      if (statement != null) 'statement': statement,
    };

Future<FakeBank> pump(WidgetTester tester, Map<String, dynamic> cardJson,
    {bool sandbox = false, Map<String, http.Response Function(Map<String, dynamic>)> extra = const {}}) async {
  final bank = FakeBank({'GET /v1/credit/card': (_) => json(cardJson), ...extra});
  await tester.pumpWidget(MaterialApp(
    home: CreditCardScreen(
      client: bank.client(token: 't'),
      accountId: 'acc-1',
      card: CreditCard.fromJson(cardJson),
      sandbox: sandbox,
    ),
  ));
  return bank;
}

void main() {
  testWidgets('el estado de cuenta dice cuánto, el mínimo y hasta cuándo', (tester) async {
    await pump(tester, card(statement: statement(interest: 3500000)));
    expect(find.text('Estado de cuenta al 27/10/2026'), findsOneWidget);
    expect(find.text('Total a pagar: 200,00 USD'), findsOneWidget);
    expect(find.text('Pago mínimo: 10,00 USD'), findsOneWidget);
    expect(find.text('Vence el 16/11/2026'), findsOneWidget);
    expect(find.text('Incluye 3,50 USD de intereses'), findsOneWidget);
    expect(find.text('Pendiente'), findsOneWidget);
  });

  testWidgets('vencido sin el mínimo se ve como vencido', (tester) async {
    await pump(tester, card(statement: statement(status: 'past_due'), status: 'blocked'));
    expect(find.text('Vencido'), findsOneWidget);
    expect(find.textContaining('bloqueada'), findsOneWidget);
  });

  testWidgets('"Pagar el mínimo" paga lo que falta del mínimo', (tester) async {
    final bank = await pump(
      tester,
      card(statement: statement(paid: 4000000)),
      extra: {'POST /v1/credit/card/payments': (_) => json({'transaction_id': 'tx', 'duplicate': false}, 201)},
    );
    await tester.tap(find.widgetWithText(FilledButton, 'Pagar'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Pagar el mínimo (6,00 USD)'));
    await tester.pumpAndSettle();
    expect(bank.received['POST /v1/credit/card/payments']!['amount_micros'], 6000000);
  });

  testWidgets('con el mínimo ya pagado no se ofrece pagarlo', (tester) async {
    await pump(tester, card(statement: statement(status: 'minimum_paid', paid: 10000000)));
    await tester.tap(find.widgetWithText(FilledButton, 'Pagar'));
    await tester.pumpAndSettle();
    expect(find.textContaining('Pagar el mínimo'), findsNothing);
    expect(find.textContaining('Pagar todo'), findsOneWidget);
  });

  testWidgets('en el sandbox se puede cortar ahora y simular el vencimiento', (tester) async {
    final bank = await pump(
      tester,
      card(statement: statement()),
      sandbox: true,
      extra: {
        'POST /v1/sandbox/credit/close-cycle': (_) => json(statement()),
        'POST /v1/sandbox/credit/pass-due-date': (_) => json(card(status: 'blocked')),
      },
    );
    await tester.tap(find.text('Cortar el estado de cuenta ahora'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Simular que pasó el vencimiento'));
    await tester.pumpAndSettle();
    expect(bank.calls, containsAll(['POST /v1/sandbox/credit/close-cycle', 'POST /v1/sandbox/credit/pass-due-date']));
  });

  testWidgets('fuera del sandbox no hay atajos', (tester) async {
    await pump(tester, card(statement: statement()));
    expect(find.text('Cortar el estado de cuenta ahora'), findsNothing);
  });
}
