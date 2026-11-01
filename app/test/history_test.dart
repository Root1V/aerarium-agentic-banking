import 'dart:convert';

import 'package:aibank_app/api/client.dart';
import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/history.dart';
import 'package:aibank_app/money.dart';
import 'package:aibank_app/screens/home_screen.dart';
import 'package:aibank_app/screens/mandates_screen.dart';
import 'package:aibank_app/screens/payment_group_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// El historial de alguien cuyo agente paga por llamada.
///
/// Sin agrupar, 120 micropagos son 120 filas de "−0,001 USD" que entierran todo
/// lo demás. Lo que se prueba es que se agrupen sin mentir: solo lo consecutivo,
/// con la suma exacta, y sin sacar de su sitio lo que pasó en medio.

var _seq = 0;

Movement _pay(String merchant, int micros) => Movement(
      id: 'm${_seq++}',
      cursor: 'c$_seq',
      amount: Money(micros, 'USD'),
      sign: -1,
      kind: 'authorization_hold',
      description: '',
      merchant: merchant,
      postedAt: DateTime(2026, 10, 25),
    );

Movement _other(String kind, int micros, {int sign = 1}) => Movement(
      id: 'm${_seq++}',
      cursor: 'c$_seq',
      amount: Money(micros, 'USD'),
      sign: sign,
      kind: kind,
      description: '',
      postedAt: DateTime(2026, 10, 25),
    );

void main() {
  _agentTests();

  group('agrupación', () {
    test('cien pagos seguidos a la misma plataforma son una fila', () {
      final items = groupHistory([for (var i = 0; i < 120; i++) _pay('Mercatus', 1000)]);

      expect(items, hasLength(1));
      final group = items.single as PaymentGroup;
      expect(group.merchant, 'Mercatus');
      expect(group.count, 120);
      // Suma exacta en micras: 120 × $0,001.
      expect(group.total.amountMicros, -120000);
    });

    test('lo que pasó en medio se queda en su sitio', () {
      final items = groupHistory([
        _pay('Mercatus', 1000),
        _pay('Mercatus', 1000),
        _other('rail_inbound_credit', 50000000),
        _pay('Mercatus', 1000),
        _pay('Mercatus', 1000),
      ]);

      // Fundir los cuatro pagos sacaría la transferencia de su sitio, y el
      // historial dejaría de contar lo que pasó en el orden en que pasó.
      expect(items.map((i) => i.runtimeType), [PaymentGroup, SingleMovement, PaymentGroup]);
    });

    test('plataformas distintas no se mezclan', () {
      final items = groupHistory([
        _pay('Mercatus', 1000),
        _pay('Mercatus', 1000),
        _pay('Otra', 1000),
        _pay('Otra', 1000),
      ]);

      expect(items.map((i) => (i as PaymentGroup).merchant), ['Mercatus', 'Otra']);
    });

    test('un pago suelto no es un grupo, y dice a quién', () {
      final items = groupHistory([_pay('Mercatus', 1000)]);

      final single = items.single as SingleMovement;
      expect(single.movement.label, 'Pago a Mercatus');
    });

    test('una racha cortada por la paginación no presume de total', () {
      final pagos = [for (var i = 0; i < 3; i++) _pay('Mercatus', 1000)];

      // Hay más movimientos atrás y la racha toca el final: puede seguir.
      final cortada = groupHistory(pagos, hasMore: true).single as PaymentGroup;
      expect(cortada.continuesBeyond, isTrue);

      // Si no hay nada más atrás, el conteo es el total.
      final completa = groupHistory(pagos).single as PaymentGroup;
      expect(completa.continuesBeyond, isFalse);
    });

    test('solo la última racha puede seguir más atrás', () {
      final items = groupHistory([
        _pay('Mercatus', 1000),
        _pay('Mercatus', 1000),
        _other('deposit', 1000000),
        _pay('Otra', 1000),
        _pay('Otra', 1000),
      ], hasMore: true);

      // La primera racha tiene un depósito detrás: está completa.
      expect((items.first as PaymentGroup).continuesBeyond, isFalse);
      expect((items.last as PaymentGroup).continuesBeyond, isTrue);
    });

    test('un pago sin plataforma conocida no se agrupa con nadie', () {
      final items = groupHistory([_pay('', 1000), _pay('', 1000)]);

      // Sin saber de quién son, agruparlos sería afirmar que son de lo mismo.
      expect(items.whereType<SingleMovement>(), hasLength(2));
      expect((items.first as SingleMovement).movement.label, 'Pago autorizado');
    });
  });

  group('pantallas', () {
    Map<String, dynamic> movementJson(String kind, int micros, int sign,
            {String merchant = '', String agent = '', String purchase = ''}) =>
        {
          'id': 'm${_seq++}',
          'cursor': 'c$_seq',
          'amount': {'amount_micros': micros, 'currency': 'USD'},
          'sign': sign,
          'kind': kind,
          'description': '',
          if (merchant.isNotEmpty) 'merchant': merchant,
          if (agent.isNotEmpty) 'agent_name': agent,
          if (purchase.isNotEmpty) 'purchase': purchase,
          'posted_at': '2026-10-25T12:00:00Z',
        };

    Map<String, dynamic> mandateJson({Map<String, dynamic>? spend}) => {
          'mandate_id': 'man-1',
          'account_id': 'acc-1',
          'granted_to': 'Mercatus',
          'currency': 'USD',
          'status': 'active',
          'consumed': 3100000,
          'expires_at': '2026-11-25T00:00:00Z',
          if (spend != null) 'spend': spend,
        };

    Map<String, dynamic> money(int micros) => {'amount_micros': micros, 'currency': 'USD'};

    BffClient client({List<Map<String, dynamic>> movements = const [], Map<String, dynamic>? spend}) =>
        BffClient(
          baseUrl: Uri.parse('http://bff.test'),
          token: () => 't',
          httpClient: MockClient((request) async {
            final body = request.url.path == '/v1/mandates'
                ? {'mandates': [mandateJson(spend: spend)]}
                : {
                    'account': {'id': 'acc-1', 'name': 'Ana', 'currency': 'USD'},
                    'balance': {'amount_micros': 1000000, 'currency': 'USD'},
                    'retained': {'amount_micros': 0, 'currency': 'USD'},
                    'movements': movements,
                  };
            return http.Response(jsonEncode(body), 200,
                headers: {'content-type': 'application/json'});
          }),
        );

    testWidgets('el inicio muestra los pagos de un agente como una fila', (tester) async {
      await tester.pumpWidget(MaterialApp(
        home: HomeScreen(
          client: client(movements: [
            movementJson('authorization_hold', 1000, -1, merchant: 'Mercatus'),
            movementJson('authorization_hold', 2000, -1, merchant: 'Mercatus'),
            movementJson('authorization_hold', 3000, -1, merchant: 'Mercatus'),
            movementJson('deposit', 50000000, 1),
          ]),
          accountId: 'acc-1',
        ),
      ));
      await tester.pumpAndSettle();

      expect(find.text('Pagos a Mercatus'), findsOneWidget);
      expect(find.text('3 pagos'), findsOneWidget);
      expect(find.text('-0,006 USD'), findsOneWidget);
      // El depósito sigue en su fila.
      expect(find.text('Depósito'), findsOneWidget);
    });

    testWidgets('si la racha sigue más atrás, dice "últimos"', (tester) async {
      final c = BffClient(
        baseUrl: Uri.parse('http://bff.test'),
        token: () => 't',
        httpClient: MockClient((_) async => http.Response(
              jsonEncode({
                'account': {'id': 'acc-1', 'name': 'Ana', 'currency': 'USD'},
                'balance': {'amount_micros': 1000000, 'currency': 'USD'},
                'movements': [
                  movementJson('authorization_hold', 1000, -1, merchant: 'Mercatus'),
                  movementJson('authorization_hold', 1000, -1, merchant: 'Mercatus'),
                  movementJson('authorization_hold', 1000, -1, merchant: 'Mercatus'),
                ],
                'has_more': true,
              }),
              200,
              headers: {'content-type': 'application/json'},
            )),
      );
      await tester.pumpWidget(MaterialApp(home: HomeScreen(client: c, accountId: 'acc-1')));
      await tester.pumpAndSettle();

      // Hoy pudo haber 121; en pantalla hay 3. "3 pagos" sería falso.
      expect(find.text('Últimos 3 pagos'), findsOneWidget);
      expect(find.text('3 pagos'), findsNothing);
    });

    testWidgets('tocar el grupo muestra qué compró el agente en cada pago, y de ahí el permiso', (tester) async {
      await tester.pumpWidget(MaterialApp(
        home: HomeScreen(
          client: client(movements: [
            movementJson('authorization_hold', 250000, -1,
                merchant: 'Mercatus', agent: 'Agente de investigación', purchase: 'Text Summarizer: resumen'),
            movementJson('authorization_hold', 1200000, -1,
                merchant: 'Mercatus', agent: 'Agente de investigación', purchase: 'Búsqueda de vuelos Lima–Cusco'),
          ]),
          accountId: 'acc-1',
        ),
      ));
      await tester.pumpAndSettle();

      await tester.tap(find.text('Pagos a Mercatus'));
      await tester.pumpAndSettle();

      // Primero en qué se fue el dinero, con las palabras del agente.
      expect(find.byType(PaymentGroupScreen), findsOneWidget);
      expect(find.text('Text Summarizer: resumen'), findsOneWidget);
      expect(find.text('Búsqueda de vuelos Lima–Cusco'), findsOneWidget);
      expect(find.text('-1,45 USD'), findsOneWidget);
      expect(find.text('2 pagos · Agente de investigación'), findsOneWidget);

      // Y si no cuadra, el permiso, para cortarlo.
      await tester.tap(find.text('Ver el permiso de Mercatus'));
      await tester.pumpAndSettle();
      expect(find.byType(MandatesScreen), findsOneWidget);
    });

    testWidgets('los pagos de antes no dicen qué se compró, y la pantalla lo explica', (tester) async {
      await tester.pumpWidget(MaterialApp(
        home: HomeScreen(
          client: client(movements: [
            movementJson('authorization_hold', 1000, -1, merchant: 'Mercatus'),
            movementJson('authorization_hold', 1000, -1, merchant: 'Mercatus'),
          ]),
          accountId: 'acc-1',
        ),
      ));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Pagos a Mercatus'));
      await tester.pumpAndSettle();

      expect(find.text('Pago autorizado'), findsNWidgets(2));
      expect(find.textContaining('de antes de que los agentes dijeran'), findsOneWidget);
    });

    testWidgets('cada permiso dice cuánto gastó hoy y este mes', (tester) async {
      await tester.binding.setSurfaceSize(const Size(800, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(MaterialApp(
        home: MandatesScreen(
          client: client(spend: {
            'today': money(420000),
            'today_count': 380,
            'month': money(3100000),
            'month_count': 1204,
            'in_flight': money(120000),
          }),
        ),
      ));
      await tester.pumpAndSettle();

      expect(find.text('0,42 USD · 380 pagos'), findsOneWidget);
      expect(find.text('3,10 USD · 1204 pagos'), findsOneWidget);
      expect(find.text('0,12 USD'), findsOneWidget);
      expect(find.text('Gastado en total'), findsOneWidget);
    });

    testWidgets('sin nada en curso no hay fila de en curso', (tester) async {
      await tester.binding.setSurfaceSize(const Size(800, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(MaterialApp(
        home: MandatesScreen(
          client: client(spend: {
            'today': money(0),
            'today_count': 0,
            'month': money(0),
            'month_count': 0,
            'in_flight': money(0),
          }),
        ),
      ));
      await tester.pumpAndSettle();

      expect(find.text('En curso'), findsNothing);
      expect(find.text('Hoy'), findsOneWidget);
    });

    testWidgets('si el gasto no se pudo calcular, el permiso se ve igual', (tester) async {
      await tester.binding.setSurfaceSize(const Size(800, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(MaterialApp(home: MandatesScreen(client: client())));
      await tester.pumpAndSettle();

      // Sin cifras inventadas, y lo esencial —retirarlo— sigue ahí.
      expect(find.text('Hoy'), findsNothing);
      expect(find.text('Retirar permiso'), findsOneWidget);
    });
  });
}

/// Qué agente pagó y qué compró (AE-10): el grupo dice el agente si fue uno
/// solo, y cada pago, lo que el agente declaró al pagar.
void _agentTests() {
  Movement byAgent(String agent, String purchase) => Movement(
        id: 'm${_seq++}',
        cursor: 'c$_seq',
        amount: Money(1000, 'USD'),
        sign: -1,
        kind: 'authorization_hold',
        description: '',
        merchant: 'Mercatus',
        agentName: agent,
        purchase: purchase,
        postedAt: DateTime(2026, 10, 25),
      );

  test('el grupo nombra al agente si todos los pagos son suyos', () {
    final one = groupHistory([byAgent('Agente de viajes', 'a'), byAgent('Agente de viajes', 'b')]);
    expect((one.single as PaymentGroup).agentName, 'Agente de viajes');
    final two = groupHistory([byAgent('Agente de viajes', 'a'), byAgent('Agente de compras', 'b')]);
    expect((two.single as PaymentGroup).agentName, '', reason: 'varios agentes: no se nombra a uno');
  });

  test('el pago lee lo que declaró el agente', () {
    final m = Movement.fromJson({
      'id': 'x',
      'cursor': 'c',
      'amount': {'amount_micros': 1000, 'currency': 'USD'},
      'sign': -1,
      'kind': 'authorization_hold',
      'description': '',
      'merchant': 'Mercatus',
      'agent_name': 'Agente de viajes',
      'purchase': 'Búsqueda de vuelos Lima–Cusco',
      'posted_at': '2026-10-31T10:00:00Z',
    });
    expect(m.agentName, 'Agente de viajes');
    expect(m.purchase, 'Búsqueda de vuelos Lima–Cusco');
  });
}
