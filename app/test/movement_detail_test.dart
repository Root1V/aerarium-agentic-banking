import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/movement_text.dart';
import 'package:aibank_app/money.dart';
import 'package:aibank_app/screens/movement_detail_screen.dart';
import 'package:aibank_app/screens/verify_receipt_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

import 'support/fake_bank.dart';

/// El detalle de un movimiento: cómo se autorizó, la tarea del agente y la
/// constancia que cualquiera puede verificar.

Map<String, dynamic> usd(int micros) => {'amount_micros': micros, 'currency': 'USD'};

Movement transfer() => Movement(
      id: '7f3e9a2b-0000-0000-0000-000000000001',
      cursor: 'c',
      amount: Money(50000000, 'USD'),
      sign: -1,
      kind: 'p2p_transfer',
      description: 'Alquiler',
      counterparty: const Account(id: 'acc-2', name: 'Juan Pérez', currency: 'USD'),
      postedAt: DateTime.utc(2026, 11, 1, 15, 15),
    );

Movement agentPayment() => Movement(
      id: '7f3e9a2b-0000-0000-0000-000000000002',
      cursor: 'c',
      amount: Money(2000000, 'USD'),
      sign: -1,
      kind: 'authorization_hold',
      description: '',
      merchant: 'Mercatus',
      agentName: 'Agente de viajes',
      purchase: 'Comparar precios',
      postedAt: DateTime.utc(2026, 11, 1, 15, 15),
    );

String? copied;

Future<void> open(WidgetTester tester, FakeBank bank, Movement m) async {
  await tester.binding.setSurfaceSize(const Size(800, 2000));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  copied = null;
  tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(SystemChannels.platform, (call) async {
    if (call.method == 'Clipboard.setData') copied = (call.arguments as Map)['text'] as String;
    return null;
  });
  addTearDown(() => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(SystemChannels.platform, null));
  await tester.pumpWidget(MaterialApp(home: MovementDetailScreen(movement: m, client: bank.client(token: 't'), accountId: 'acc-1')));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('una transferencia dice cómo se autorizó y comparte una constancia verificable', (tester) async {
    final m = transfer();
    final bank = FakeBank({
      'GET /v1/accounts/acc-1/movements/${m.id}': (_) => json({
            'authorization': {
              'actor_kind': 'holder',
              'outcome': 'confirmed',
              'reason': 'new_payee',
              'method': 'biometric',
              'device': 'iPhone de Ana',
            },
          }),
      'POST /v1/accounts/acc-1/movements/${m.id}/receipt': (_) => json({
            'code': 'K7Q2M-9XR4T',
            'kind': 'p2p_transfer',
            'amount': usd(50000000),
            'from': 'Ana E.',
            'to': 'Juan P.',
            'posted_at': '2026-11-01T15:15:00Z',
            'url': 'http://bff.test/c/K7Q2M-9XR4T',
          }, 201),
    });
    await open(tester, bank, m);

    expect(find.text('Para'), findsOneWidget);
    expect(find.text('Juan Pérez'), findsOneWidget);
    expect(find.text('Sin comisión'), findsOneWidget);
    expect(find.text('Lo confirmaste con Face ID en iPhone de Ana'), findsOneWidget);
    expect(find.text('Se pidió porque era la primera vez que le enviabas.'), findsOneWidget);

    await tester.tap(find.text('Compartir constancia verificable'));
    await tester.pumpAndSettle();
    expect(copied, contains('Código de verificación: K7Q2M-9XR4T'));
    expect(copied, contains('Comprueba que es real en: http://bff.test/c/K7Q2M-9XR4T'));
    expect(copied, contains('Ana E. → Juan P.'));
  });

  testWidgets('un pago de un agente dice quién, con qué permiso, y la tarea entera', (tester) async {
    final m = agentPayment();
    final bank = FakeBank({
      'GET /v1/accounts/acc-1/movements/${m.id}': (_) => json({
            'payment': {'status': 'captured', 'amount': usd(2000000), 'refunded': usd(0), 'expires_at': '2026-11-01T15:30:00Z'},
            'authorization': {
              'actor_kind': 'agent',
              'outcome': 'allow',
              'agent': 'Agente de viajes',
              'platform': 'Mercatus',
              'signed': true,
              'mandate_max_per_operation': usd(5000000),
              'mandate_expires_at': '2026-12-01T10:00:00Z',
            },
            'task': {
              'reference': 'vuelos-cusco',
              'count': 2,
              'total': usd(3000000),
              'payments': [
                {'description': 'Búsqueda de vuelos', 'amount': usd(1000000), 'at': '2026-11-01T15:14:00Z'},
                {'description': 'Comparar precios', 'amount': usd(2000000), 'at': '2026-11-01T15:15:00Z', 'this': true},
              ],
            },
          }),
    });
    await open(tester, bank, m);

    expect(find.text('Cobrado'), findsOneWidget);
    expect(find.text('Lo pagó Agente de viajes de Mercatus, con su firma'), findsOneWidget);
    expect(find.textContaining('de hasta 5,00 USD por pago, vigente hasta el 1/12'), findsOneWidget);
    expect(find.text('Parte de una tarea del agente'), findsOneWidget);
    expect(find.text('2 pagos · 3,00 USD en total'), findsOneWidget);
    expect(find.text('Búsqueda de vuelos'), findsOneWidget);
  });

  testWidgets('sin el detalle del banco, el movimiento se muestra igual', (tester) async {
    await open(tester, FakeBank({}), transfer());
    expect(find.text('Juan Pérez'), findsOneWidget);
    expect(find.text('Lo confirmaste con Face ID en iPhone de Ana'), findsNothing);
  });

  testWidgets('verificar: una real y una inventada', (tester) async {
    final bank = FakeBank({
      'GET /v1/receipts/K7Q2M-9XR4T': (_) => json({
            'code': 'K7Q2M-9XR4T',
            'amount': usd(50000000),
            'from': 'Ana E.',
            'to': 'Juan P.',
            'posted_at': '2026-11-01T15:15:00Z',
            'url': 'http://bff.test/c/K7Q2M-9XR4T',
            'status': 'completed',
          }),
      'GET /v1/receipts/FALSO-12345': (_) => json({'code': 'receipt_not_found', 'message': 'no'}, 404),
    });
    await tester.pumpWidget(MaterialApp(home: VerifyReceiptScreen(client: bank.client(token: 't'))));
    await tester.pumpAndSettle();

    await tester.enterText(find.byType(TextField), 'K7Q2M-9XR4T');
    await tester.tap(find.text('Verificar'));
    await tester.pumpAndSettle();
    expect(find.text('Este pago es real'), findsOneWidget);
    expect(find.text('50,00 USD · Ana E. → Juan P.'), findsOneWidget);

    await tester.enterText(find.byType(TextField), 'FALSO-12345');
    await tester.tap(find.text('Verificar'));
    await tester.pumpAndSettle();
    expect(find.textContaining('no es de Aerarium'), findsOneWidget);
  });

  test('cómo se autorizó, sin confirmar', () {
    final (title, why) = authorizationText(const AuthorizationInfo(
        actorKind: 'holder', outcome: 'allow', reason: 'known_payee', device: 'iPhone de Ana'));
    expect(title, 'Lo hiciste tú en iPhone de Ana, sin pedir confirmación');
    expect(why, 'No hizo falta: ya le habías enviado antes y el monto es bajo.');
  });
}
