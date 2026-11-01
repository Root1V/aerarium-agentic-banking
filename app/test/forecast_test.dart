import 'dart:convert';

import 'package:aibank_app/api/client.dart';
import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/forecast_text.dart';
import 'package:aibank_app/screens/forecast_screen.dart';
import 'package:aibank_app/screens/home_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// La previsión de saldo (F3-01): las frases, y el aviso en el inicio.

Map<String, dynamic> usd(int micros) => {'amount_micros': micros, 'currency': 'USD'};

/// El caso 1 del documento: la tarjeta vence el 16 y deja en −40 antes del 30.
Map<String, dynamic> deficitJson() => {
      'today': '2026-11-01',
      'until': '2026-11-30',
      'balance': usd(300000000),
      'daily_spend': usd(10000000),
      'income': {'amount': usd(1500000000), 'expected_on': '2026-11-30', 'cadence': 'monthly'},
      'card': {'due_on': '2026-11-16', 'minimum': usd(20000000), 'total': usd(190000000)},
      'lowest': {'on': '2026-11-29', 'amount': usd(-170000000)},
      'after_card': {'on': '2026-11-16', 'amount': usd(-40000000)},
      'status': 'deficit',
      'cause': 'card',
      'options': [
        {'kind': 'pay_minimum', 'amount': usd(20000000), 'rest': usd(170000000)},
        {'kind': 'spend_cap', 'amount': usd(3928571)},
      ],
    };

void main() {
  group('frases', () {
    test('la del documento: la tarjeta vence el 16 y deja en −40', () {
      final f = Forecast.fromJson(deficitJson());
      expect(headline(f),
          'Tu tarjeta vence el 16 de noviembre y te dejaría con -40,00 USD antes de tu ingreso del 30 de noviembre.');
    });

    test('pagar el mínimo dice cuánto, cuándo y lo que queda', () {
      final f = Forecast.fromJson(deficitJson());
      expect(optionText(f, f.options.first),
          'Paga el mínimo (20,00 USD) el 16 de noviembre y el resto (170,00 USD) cuando cobres. '
          'Lo que no pagues genera intereses.');
    });

    test('con holgura dice con cuánto se llega', () {
      final f = Forecast.fromJson({
        ...deficitJson(),
        'status': 'ok',
        'cause': '',
        'card': null,
        'after_card': null,
        'lowest': {'on': '2026-11-29', 'amount': usd(220000000)},
        'options': <Object>[],
      });
      expect(headline(f), 'Llegas a tu ingreso el 30 de noviembre con unos 220,00 USD.');
    });

    test('sin un ingreso que fechar mira 30 días', () {
      final f = Forecast.fromJson({
        ...deficitJson(),
        'income': null,
        'card': null,
        'after_card': null,
        'cause': 'spending',
        'lowest': {'on': '2026-11-24', 'amount': usd(-40000000)},
      });
      expect(headline(f),
          'A este ritmo de gasto, el 24 de noviembre te quedarías con -40,00 USD, en los próximos 30 días.');
    });
  });

  group('pantallas', () {
    BffClient client(Map<String, dynamic>? forecast) => BffClient(
          baseUrl: Uri.parse('http://bff.test'),
          token: () => 't',
          httpClient: MockClient((request) async {
            if (request.url.path.endsWith('/forecast')) {
              return forecast == null
                  ? http.Response(jsonEncode({'code': 'unavailable', 'message': 'no'}), 503)
                  : http.Response(jsonEncode(forecast), 200, headers: {'content-type': 'application/json'});
            }
            return http.Response(
                jsonEncode({
                  'account': {'id': 'acc-1', 'name': 'Ana', 'currency': 'USD'},
                  'balance': usd(300000000),
                  'movements': <Object>[],
                }),
                200,
                headers: {'content-type': 'application/json'});
          }),
        );

    testWidgets('el inicio avisa del déficit y lleva a qué hacer', (tester) async {
      await tester.binding.setSurfaceSize(const Size(800, 1600));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(MaterialApp(home: HomeScreen(client: client(deficitJson()), accountId: 'acc-1')));
      await tester.pumpAndSettle();

      // En el inicio, corto: título y una línea con el porqué.
      expect(find.text('No llegas a tu ingreso del 30'), findsOneWidget);
      expect(find.text('Al pagar la tarjeta el 16 de noviembre quedarías en -40,00 USD'), findsOneWidget);
      await tester.tap(find.byKey(const Key('forecast')));
      await tester.pumpAndSettle();

      expect(find.byType(ForecastScreen), findsOneWidget);
      // En el detalle, la frase entera.
      expect(find.textContaining('Tu tarjeta vence el 16 de noviembre y te dejaría con -40,00 USD'), findsOneWidget);
      expect(find.text('Qué puedes hacer'), findsOneWidget);
      expect(find.textContaining('Paga el mínimo (20,00 USD)'), findsOneWidget);
      expect(find.textContaining('Si gastas hasta'), findsOneWidget);
      expect(find.textContaining('No movemos nada por ti'), findsOneWidget);
    });

    testWidgets('si la previsión falla, el inicio se ve igual', (tester) async {
      await tester.pumpWidget(MaterialApp(home: HomeScreen(client: client(null), accountId: 'acc-1')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('balance')), findsOneWidget);
      expect(find.byKey(const Key('forecast')), findsNothing);
    });
  });
}
