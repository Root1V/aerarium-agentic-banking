import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/screens/phone_alias_screen.dart';
import 'package:aibank_app/screens/transfer_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;

import 'support/fake_bank.dart';

http.Response _sent(Map<String, dynamic> _) => json({'transaction_id': 'tx-1', 'duplicate': false}, 201);

void main() {
  group('mi número para recibir', () {
    Future<FakeBank> pump(WidgetTester tester, {String? registered}) async {
      final bank = FakeBank({
        'GET /v1/aliases/phone': (_) => registered == null
            ? json({'code': 'not_found', 'message': 'sin número'}, 404)
            : json({'phone': registered, 'account_id': 'acc-1'}),
        'POST /v1/aliases/phone': (_) =>
            json({'verification_id': 'v-1', 'phone': '+51987654321', 'sandbox_code': '482916'}),
        'POST /v1/aliases/phone/verify': (body) => body['code'] == '482916'
            ? json({'phone': '+51987654321'})
            : json({'code': 'code_incorrect', 'message': 'código', 'remaining_attempts': 4}, 400),
        'DELETE /v1/aliases/phone': (_) => http.Response('', 204),
      });
      await tester.pumpWidget(MaterialApp(home: PhoneAliasScreen(client: bank.client(token: 'tok'), accountId: 'acc-1')));
      await tester.pumpAndSettle();
      return bank;
    }

    testWidgets('se registra con el código, que en el sandbox se ve en pantalla', (tester) async {
      final bank = await pump(tester);
      await tester.enterText(find.byKey(const Key('phone')), '987 654 321');
      await tester.tap(find.text('Enviarme un código'));
      await tester.pumpAndSettle();

      expect(bank.received['POST /v1/aliases/phone'], {'account_id': 'acc-1', 'phone': '987 654 321'});
      expect(find.textContaining('Tu código es 482916'), findsOneWidget);

      await tester.enterText(find.byKey(const Key('code')), '111111');
      await tester.tap(find.text('Verificar'));
      await tester.pumpAndSettle();
      expect(find.text('Código incorrecto. Te quedan 4 intentos.'), findsOneWidget);

      await tester.enterText(find.byKey(const Key('code')), '482916');
      await tester.tap(find.text('Verificar'));
      await tester.pumpAndSettle();
      expect(find.text('Recibes en +51 987 654 321'), findsOneWidget);
    });

    testWidgets('el número registrado se puede quitar', (tester) async {
      final bank = await pump(tester, registered: '+51987654321');
      expect(find.text('Recibes en +51 987 654 321'), findsOneWidget);
      await tester.tap(find.text('Quitar mi número'));
      await tester.pumpAndSettle();
      expect(bank.calls, contains('DELETE /v1/aliases/phone'));
      expect(find.text('Enviarme un código'), findsOneWidget);
    });
  });

  group('enviar a un celular', () {
    Future<FakeBank> pump(WidgetTester tester) async {
      final bank = FakeBank({
        'GET /v1/payees/recent': (_) => json({'payees': <Object>[]}),
        'GET /v1/directory/phone/912345678': (_) => json({
              'phone': '+51912345678',
              'account': {'id': 'acc-juan', 'name': 'Juan P.', 'currency': 'USD'},
            }),
        'GET /v1/directory/phone/987000000': (_) =>
            json({'code': 'phone_not_found', 'message': 'no'}, 404),
        'POST /v1/transfers': _sent,
      });
      await tester.pumpWidget(MaterialApp(
        home: Navigator(
          onGenerateRoute: (_) => MaterialPageRoute<void>(
            builder: (_) => TransferScreen(client: bank.client(token: 'tok'), fromAccountId: 'acc-1', currency: 'USD'),
          ),
        ),
      ));
      await tester.pumpAndSettle();
      return bank;
    }

    testWidgets('al escribir el número muestra a quién va, y envía a esa cuenta', (tester) async {
      final bank = await pump(tester);
      expect(find.text('Celular o cuenta'), findsOneWidget);

      await tester.enterText(find.byKey(const Key('destination')), '912345678');
      await tester.pumpAndSettle();

      expect(find.text('Juan P.'), findsOneWidget);
      expect(find.text('Para · +51 912 345 678'), findsOneWidget);

      await tester.enterText(find.byKey(const Key('amount')), '15');
      await tester.tap(find.byKey(const Key('send')));
      await tester.pumpAndSettle();
      expect(bank.received['POST /v1/transfers']!['to_account_id'], 'acc-juan');
    });

    testWidgets('un número que no es cliente lo dice en el campo', (tester) async {
      await pump(tester);
      await tester.enterText(find.byKey(const Key('destination')), '987000000');
      await tester.pumpAndSettle();
      expect(find.text('Ese número no tiene cuenta en AIBank.'), findsOneWidget);
    });
  });

  group('el historial dice quién', () {
    Movement movement(int sign, {Map<String, dynamic>? counterparty}) => Movement.fromJson({
          'id': 'm1',
          'cursor': 'm1',
          'amount': {'amount_micros': 10000000, 'currency': 'USD'},
          'sign': sign,
          'kind': 'p2p_transfer',
          'description': '',
          'posted_at': '2026-10-27T12:00:00Z',
          if (counterparty != null) 'counterparty': counterparty,
        });

    test('a quién se le envió y de quién se recibió', () {
      final juan = {'id': 'acc-juan', 'name': 'Juan Pérez', 'currency': 'USD'};
      expect(movement(-1, counterparty: juan).label, 'A Juan Pérez');
      expect(movement(1, counterparty: juan).label, 'De Juan Pérez');
      expect(movement(-1).label, 'Transferencia', reason: 'sin contraparte, el tipo');
    });
  });

  test('formato y reconocimiento de celulares', () {
    expect(formatPhone('+51987654321'), '+51 987 654 321');
    expect(formatPhone('987654321'), '987 654 321');
    expect(looksLikePhone('987 654 321'), isTrue);
    expect(looksLikePhone('987-654-321'), isTrue);
    expect(looksLikePhone('87654321'), isFalse);
    expect(looksLikePhone('acc-juan'), isFalse);
  });
}
