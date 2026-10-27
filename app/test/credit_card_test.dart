import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/money.dart';
import 'package:aibank_app/screens/credit_card_screen.dart';
import 'package:aibank_app/screens/home_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;

import 'support/fake_bank.dart';

Map<String, dynamic> card({int outstanding = 0, int limit = 450000000, String status = 'active'}) => {
      'id': 'card-1',
      'limit': {'amount_micros': limit, 'currency': 'USD'},
      'outstanding': {'amount_micros': outstanding, 'currency': 'USD'},
      'available': {'amount_micros': limit - outstanding, 'currency': 'USD'},
      'annual_rate_bps': 4500,
      'cycle_day': 27,
      'payment_days': 20,
      'status': status,
    };

Map<String, http.Response Function(Map<String, dynamic>)> home({required http.Response Function(Map<String, dynamic>) credit}) => {
      'GET /v1/home': (_) => json({
            'account': {'id': 'acc-1', 'name': 'Cuenta simple', 'currency': 'USD'},
            'balance': {'amount_micros': 0, 'currency': 'USD'},
            'movements': <Object>[],
          }),
      'GET /v1/credit/card': credit,
    };

void main() {
  group('en el inicio', () {
    testWidgets('sin tarjeta invita a pedirla', (tester) async {
      final bank = FakeBank(home(credit: (_) => json({'code': 'no_credit_card', 'message': 'no'}, 404)));
      await tester.pumpWidget(MaterialApp(home: HomeScreen(client: bank.client(token: 't'), accountId: 'acc-1')));
      await tester.pumpAndSettle();
      expect(find.text('Pide tu tarjeta de crédito'), findsOneWidget);
    });

    testWidgets('con tarjeta muestra lo disponible', (tester) async {
      final bank = FakeBank(home(credit: (_) => json(card(outstanding: 100000000))));
      await tester.pumpWidget(MaterialApp(home: HomeScreen(client: bank.client(token: 't'), accountId: 'acc-1')));
      await tester.pumpAndSettle();
      expect(find.text('Disponible 350,00 USD de 450,00 USD'), findsOneWidget);
    });

    testWidgets('si el banco no ofrece tarjetas, no aparece', (tester) async {
      final bank = FakeBank(home(credit: (_) => http.Response('404 page not found', 404)));
      await tester.pumpWidget(MaterialApp(home: HomeScreen(client: bank.client(token: 't'), accountId: 'acc-1')));
      await tester.pumpAndSettle();
      expect(find.byType(CreditCardTile), findsNothing);
    });
  });

  group('pedirla', () {
    Future<FakeBank> pump(WidgetTester tester, Map<String, dynamic> decision) async {
      final bank = FakeBank({'POST /v1/credit/applications': (_) => json(decision)});
      await tester.pumpWidget(MaterialApp(home: CreditApplyScreen(client: bank.client(token: 't'), accountId: 'acc-1')));
      await tester.tap(find.text('Solicitar'));
      await tester.pumpAndSettle();
      return bank;
    }

    testWidgets('aprobada: límite, TEA y condiciones, y en el sandbox lo dice', (tester) async {
      final bank = await pump(tester, {
        'application_id': 'a-1', 'outcome': 'approved', 'reasons': <String>[], 'synthetic_history': true, 'card': card(),
      });
      expect(bank.received['POST /v1/credit/applications'], {'account_id': 'acc-1'});
      expect(find.text('¡Aprobada!'), findsOneWidget);
      expect(find.text('Tu límite es 450,00 USD.'), findsOneWidget);
      expect(find.textContaining('TEA): 45 %'), findsOneWidget);
      expect(find.textContaining('ingresos simulados'), findsOneWidget);
    });

    testWidgets('sin historial: dice qué falta, no el puntaje', (tester) async {
      await pump(tester, {
        'application_id': 'a-2', 'outcome': 'needs_more_history',
        'reasons': ['Poca antigüedad de la cuenta', 'Sin ingresos recurrentes'], 'synthetic_history': false,
      });
      expect(find.text('Todavía no podemos darte una tarjeta'), findsOneWidget);
      expect(find.text('Poca antigüedad de la cuenta'), findsOneWidget);
      expect(find.text('Sin ingresos recurrentes'), findsOneWidget);
      expect(find.textContaining('puntaje'), findsNothing);
    });
  });

  group('usarla y pagarla', () {
    Future<FakeBank> pump(WidgetTester tester, {required http.Response Function(Map<String, dynamic>) onMove, int outstanding = 100000000}) async {
      var current = outstanding;
      final bank = FakeBank({
        'POST /v1/credit/card/draws': (body) {
          final r = onMove(body);
          if (r.statusCode == 201) current += body['amount_micros'] as int;
          return r;
        },
        'POST /v1/credit/card/payments': (body) {
          final r = onMove(body);
          if (r.statusCode == 201) current -= body['amount_micros'] as int;
          return r;
        },
        'GET /v1/credit/card': (_) => json(card(outstanding: current)),
      });
      await tester.pumpWidget(MaterialApp(
        home: CreditCardScreen(
          client: bank.client(token: 't'),
          accountId: 'acc-1',
          card: CreditCard.fromJson(card(outstanding: outstanding)),
        ),
      ));
      return bank;
    }

    http.Response ok(Map<String, dynamic> _) => json({'transaction_id': 'tx', 'duplicate': false}, 201);

    testWidgets('pasar a mi cuenta lleva la clave y actualiza lo disponible', (tester) async {
      final bank = await pump(tester, onMove: ok);
      await tester.tap(find.text('Pasar a mi cuenta'));
      await tester.pumpAndSettle();
      await tester.enterText(find.byKey(const Key('credit-amount')), '50');
      await tester.tap(find.text('Pasar'));
      await tester.pumpAndSettle();

      expect(bank.received['POST /v1/credit/card/draws'], {'account_id': 'acc-1', 'amount_micros': 50000000});
      expect(find.text('300,00 USD'), findsOneWidget, reason: '450 - (100 + 50)');
    });

    testWidgets('"Pagar todo" paga exactamente lo que se debe', (tester) async {
      final bank = await pump(tester, onMove: ok, outstanding: 120000000);
      await tester.tap(find.text('Pagar'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Pagar todo (120,00 USD)'));
      await tester.pumpAndSettle();
      expect(bank.received['POST /v1/credit/card/payments']!['amount_micros'], 120000000);
      expect(find.text('Debes 0,00 USD de un límite de 450,00 USD'), findsOneWidget);
    });

    testWidgets('pagar de más lo explica', (tester) async {
      await pump(tester, onMove: (_) => json({'code': 'overpayment', 'message': 'no'}, 422));
      await tester.tap(find.text('Pagar'));
      await tester.pumpAndSettle();
      await tester.enterText(find.byKey(const Key('credit-amount')), '500');
      await tester.tap(find.widgetWithText(FilledButton, 'Pagar').last);
      await tester.pumpAndSettle();
      expect(find.text('Ese pago es mayor que lo que debes.'), findsOneWidget);
    });

    testWidgets('bloqueada no deja usarla pero sí pagarla', (tester) async {
      await tester.pumpWidget(MaterialApp(
        home: CreditCardScreen(
          client: FakeBank({}).client(),
          accountId: 'acc-1',
          card: CreditCard.fromJson(card(outstanding: 100000000, status: 'blocked')),
        ),
      ));
      final draw = tester.widget<FilledButton>(find.widgetWithText(FilledButton, 'Pasar a mi cuenta'));
      final pay = tester.widget<FilledButton>(find.widgetWithText(FilledButton, 'Pagar'));
      expect(draw.onPressed, isNull);
      expect(pay.onPressed, isNotNull);
    });
  });

  test('la TEA se escribe como en el contrato', () {
    CreditCard rate(int bps) => CreditCard.fromJson({...card(), 'annual_rate_bps': bps});
    expect(rate(4500).annualRate, '45 %');
    expect(rate(4550).annualRate, '45,5 %');
    expect(rate(4525).annualRate, '45,25 %');
    expect(const Money(0, 'USD').format(), '0,00 USD');
  });
}
