import 'dart:convert';

import 'package:aibank_app/api/client.dart';
import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/money.dart';
import 'package:aibank_app/screens/home_screen.dart';
import 'package:aibank_app/screens/pending_payments_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// Lo retenido: dinero que salió del saldo hacia pagos que aún no se cobraron.
///
/// Lo que se prueba es que la persona entienda dónde está su dinero sin que la
/// pantalla afirme nada que no sabe: ni "0 retenido" cuando el banco no pudo
/// calcularlo, ni un cobro pendiente que ya venció.

BffClient _client(Map<String, dynamic> home) => BffClient(
      baseUrl: Uri.parse('http://bff.test'),
      token: () => 'token',
      httpClient: MockClient((_) async => http.Response(jsonEncode(home), 200,
          headers: {'content-type': 'application/json'})),
    );

Map<String, dynamic> _money(int micros) => {'amount_micros': micros, 'currency': 'USD'};

Map<String, dynamic> _group({
  String merchant = 'Mercatus',
  int count = 2,
  int amount = 3500000,
  int expired = 0,
  DateTime? lastExpiresAt,
}) =>
    {
      'merchant': merchant,
      'count': count,
      'amount': _money(amount),
      'expired': _money(expired),
      'last_expires_at':
          (lastExpiresAt ?? DateTime.now().add(const Duration(minutes: 10)))
              .toUtc()
              .toIso8601String(),
    };

Map<String, dynamic> _home({
  Map<String, dynamic>? retained,
  List<Map<String, dynamic>> pending = const [],
}) =>
    {
      'account': {'id': 'acc-1', 'name': 'Ana Pérez', 'currency': 'USD'},
      'balance': _money(96500000),
      if (retained != null) 'retained': retained,
      'pending': pending,
      'movements': [
        {
          'id': 'm1',
          'cursor': 'm1',
          'amount': _money(3500000),
          'sign': -1,
          'kind': 'authorization_hold',
          'description': '',
          'posted_at': '2026-10-25T12:00:00Z',
        },
      ],
    };

Future<void> _pumpHome(WidgetTester tester, Map<String, dynamic> home) async {
  await tester.pumpWidget(MaterialApp(
    home: HomeScreen(client: _client(home), accountId: 'acc-1'),
  ));
  await tester.pumpAndSettle();
}

Future<void> _pumpPending(WidgetTester tester, List<PendingGroup> groups,
    {required DateTime now}) async {
  await tester.pumpWidget(MaterialApp(
    home: PendingPaymentsScreen(
      client: _client(_home()),
      groups: groups,
      now: () => now,
    ),
  ));
}

PendingGroup _pending({
  String merchant = 'Mercatus',
  int count = 2,
  int amount = 3500000,
  int expired = 0,
  required DateTime lastExpiresAt,
}) =>
    PendingGroup(
      merchant: merchant,
      count: count,
      amount: Money(amount, 'USD'),
      expired: Money(expired, 'USD'),
      lastExpiresAt: lastExpiresAt,
    );

void main() {
  group('inicio', () {
    testWidgets('lo que se ve es lo disponible', (tester) async {
      await _pumpHome(tester, _home(retained: _money(0)));

      // "Disponible", no "Saldo": lo retenido ya salió de aquí.
      expect(find.text('Disponible'), findsOneWidget);
      expect(find.text('96,50 USD'), findsOneWidget);
    });

    testWidgets('lo retenido se explica junto al saldo', (tester) async {
      await _pumpHome(tester, _home(retained: _money(3500000), pending: [_group()]));

      expect(find.text('3,50 USD retenidos en pagos en curso'), findsOneWidget);
    });

    testWidgets('sin nada retenido no hay fila', (tester) async {
      await _pumpHome(tester, _home(retained: _money(0)));

      // "0,00 retenidos" sería ruido que hace dudar.
      expect(find.byKey(const Key('retained')), findsNothing);
    });

    testWidgets('si el banco no pudo calcularlo, no se afirma nada',
        (tester) async {
      await _pumpHome(tester, _home());

      // Desconocido no es cero: callar es mejor que decir que no hay nada.
      expect(find.byKey(const Key('retained')), findsNothing);
      expect(find.textContaining('retenidos'), findsNothing);
    });

    testWidgets('el pago de una plataforma se rotula como tal', (tester) async {
      await _pumpHome(tester, _home(retained: _money(0)));

      // Antes salía "Movimiento": la persona no tenía cómo relacionarlo con
      // el permiso que concedió.
      expect(find.text('Pago autorizado'), findsOneWidget);
    });

    testWidgets('tocar lo retenido lleva al desglose', (tester) async {
      await _pumpHome(tester, _home(retained: _money(3500000), pending: [_group()]));

      await tester.tap(find.byKey(const Key('retained')));
      await tester.pumpAndSettle();

      expect(find.text('Pagos en curso'), findsOneWidget);
      expect(find.text('Mercatus'), findsOneWidget);
      expect(find.text('2 pagos'), findsOneWidget);
    });
  });

  group('desglose', () {
    final now = DateTime(2026, 10, 25, 13, 50);

    testWidgets('dice hasta cuándo, en la hora del teléfono', (tester) async {
      await _pumpPending(tester, [
        _pending(lastExpiresAt: DateTime(2026, 10, 25, 14, 5)),
      ], now: now);

      expect(find.text('Se cobran o vuelven antes de las 14:05.'), findsOneWidget);
    });

    testWidgets('si vence otro día, da la fecha', (tester) async {
      await _pumpPending(tester, [
        _pending(lastExpiresAt: DateTime(2026, 10, 26, 0, 3)),
      ], now: DateTime(2026, 10, 25, 23, 55));

      // Pasada la medianoche, "antes de las 00:03" se leería como hoy.
      expect(find.text('Se cobran o vuelven antes del 26/10 a las 00:03.'),
          findsOneWidget);
    });

    testWidgets('lo vencido se dice aparte', (tester) async {
      await _pumpPending(tester, [
        _pending(amount: 3500000, expired: 1000000,
            lastExpiresAt: DateTime(2026, 10, 25, 14, 5)),
      ], now: now);

      expect(find.text('1,00 USD ya vencieron y vuelven a tu saldo en breve.'),
          findsOneWidget);
      // Lo que no venció sigue con su hora.
      expect(find.textContaining('antes de las 14:05'), findsOneWidget);
    });

    testWidgets('si todo venció, no se promete un cobro', (tester) async {
      await _pumpPending(tester, [
        _pending(amount: 2000000, expired: 2000000,
            lastExpiresAt: DateTime(2026, 10, 25, 13, 40)),
      ], now: now);

      expect(find.text('Ya vencieron: vuelven a tu saldo en breve.'), findsOneWidget);
      expect(find.textContaining('Se cobran'), findsNothing);
    });

    testWidgets('un solo pago se dice en singular', (tester) async {
      await _pumpPending(tester, [
        _pending(count: 1, lastExpiresAt: DateTime(2026, 10, 25, 14, 5)),
      ], now: now);

      expect(find.text('1 pago'), findsOneWidget);
    });

    testWidgets('sin plataforma conocida no se inventa un nombre', (tester) async {
      await _pumpPending(tester, [
        _pending(merchant: '', lastExpiresAt: DateTime(2026, 10, 25, 14, 5)),
      ], now: now);

      expect(find.text('Otros pagos en curso'), findsOneWidget);
    });

    testWidgets('desde aquí se llega a los permisos', (tester) async {
      await _pumpPending(tester, [
        _pending(lastExpiresAt: DateTime(2026, 10, 25, 14, 5)),
      ], now: now);

      // Si algo no cuadra, lo siguiente que la persona quiere es cortarlo.
      expect(find.text('Gestionar permisos de pago'), findsOneWidget);
    });
  });

  test('retained ausente se lee como desconocido, no como cero', () {
    final home = Home.fromJson(_home());
    expect(home.retained, isNull);
    expect(home.hasRetained, isFalse);
  });
}
