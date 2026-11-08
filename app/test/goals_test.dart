import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/auth/device_keys.dart';
import 'package:aibank_app/goal_text.dart';
import 'package:aibank_app/screens/goal_screen.dart';
import 'package:aibank_app/screens/pockets_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'step_up_test.dart' show RecordingKeys;
import 'support/fake_bank.dart';
import 'support/fake_keys.dart';

/// Las metas (F3-03) en la app: el avance, la regla que se confirma con la cara,
/// y lo que la regla hizo, también cuando no apartó y por qué.

Map<String, dynamic> pen(int micros) => {'amount_micros': micros, 'currency': 'PEN'};

Map<String, dynamic> goalJson({
  int saved = 0,
  Map<String, dynamic>? rule,
  Map<String, dynamic> pace = const {'suggested_cadence': 'monthly'},
  List<Map<String, dynamic>> contributions = const [],
}) =>
    {
      'id': 'g-1',
      'name': 'Viaje a Cusco',
      'pocket_id': 'pk-g',
      'target': pen(100000000),
      'saved': pen(saved),
      'reached': saved >= 100000000,
      if (rule != null) 'rule': rule,
      'pace': pace,
      'contributions': contributions,
    };

GoalContribution contribution(String outcome, {String reason = '', int amount = 0}) => GoalContribution.fromJson({
      'run_on': '2026-11-09',
      'outcome': outcome,
      'asked': pen(30000000),
      'amount': pen(amount),
      'reason': reason,
      'lowest': pen(40000000),
      'income_on': '2026-11-30',
    });

void main() {
  group('lo que hizo la regla, en palabras', () {
    test('apartar menos dice por qué, con lo que preveía el saldo', () {
      final (title, detail) = contributionText(contribution('reduced', reason: 'keeps_cushion', amount: 15000000));
      expect(title, 'Apartó S/ 15,00 de S/ 30,00');
      expect(
          detail,
          'el 9 de noviembre. Apartó menos para que no te quedes corto antes de tu ingreso del 30 de noviembre. '
          'Tu saldo iba a bajar hasta S/ 40,00.');
    });

    test('no apartar también se dice', () {
      expect(contributionText(contribution('skipped', reason: 'keeps_cushion')).$2,
          'el 9 de noviembre. Te habría dejado corto antes de tu ingreso del 30 de noviembre. Tu saldo iba a bajar hasta S/ 40,00.');
      expect(contributionText(contribution('skipped', reason: 'insufficient_funds')).$2,
          'el 9 de noviembre. No tenías saldo.');
    });

    test('el ritmo: cuándo se llega, y cuánto haría falta si no', () {
      final late = Goal.fromJson({
        ...goalJson(rule: {'amount': pen(10000000), 'cadence': 'weekly', 'next_run_on': '2026-11-14'}),
        'target_date': '2026-12-31',
        'pace': {
          'reach_on': '2027-01-16',
          'on_track': false,
          'suggested': pen(13000000),
          'suggested_cadence': 'weekly',
        },
      });
      expect(paceText(late),
          'A este ritmo la completas el 16 de enero. No llegas para el 31 de diciembre: con S/ 13,00 por semana, sí.');

      final noRule = Goal.fromJson({
        ...goalJson(),
        'target_date': '2026-12-31',
        'pace': {'suggested': pen(45000000), 'suggested_cadence': 'monthly'},
      });
      expect(paceText(noRule), 'Para llegar el 31 de diciembre, aparta S/ 45,00 al mes.');
    });
  });

  testWidgets('las metas van arriba, con su avance, y su bolsillo no se repite abajo', (tester) async {
    final bank = FakeBank({
      'GET /v1/accounts/acc-1/pockets': (_) => json({
            'pockets': [
              {'id': 'pk-g', 'name': 'Viaje a Cusco', 'purpose': 'goal', 'balance': pen(30000000)},
              {'id': 'pk-1', 'name': 'Para la tarjeta', 'purpose': 'card', 'balance': pen(50000000)},
            ]
          }),
      'GET /v1/accounts/acc-1/goals': (_) => json({
            'goals': [
              goalJson(
                  saved: 30000000, rule: {'amount': pen(30000000), 'cadence': 'weekly', 'next_run_on': '2026-11-14'})
            ]
          }),
    });
    await tester.pumpWidget(
        MaterialApp(home: PocketsScreen(client: bank.client(token: 't'), accountId: 'acc-1', currency: 'PEN')));
    await tester.pumpAndSettle();

    expect(find.text('Bolsillos y metas'), findsOneWidget);
    expect(find.text('Viaje a Cusco'), findsOneWidget, reason: 'una vez, como meta');
    expect(find.text('S/ 30,00 de S/ 100,00'), findsOneWidget);
    expect(find.text('Aparta S/ 30,00 cada semana'), findsOneWidget);
    expect(find.text('Para la tarjeta'), findsOneWidget);
    expect(find.text('S/ 80,00'), findsOneWidget, reason: 'lo apartado suma bolsillos y metas');
  });

  testWidgets('programar aportes pide la cara diciendo cuánto, cada cuánto y para qué', (tester) async {
    var goal = goalJson(pace: {'suggested': pen(25000000), 'suggested_cadence': 'weekly'});
    final bank = FakeBank({
      'GET /v1/goals/g-1': (_) => json(goal),
      'PUT /v1/goals/g-1/rule': (body) {
        if (body['confirmation'] == null) {
          return json({
            'code': 'confirmation_required',
            'challenge_id': 'ch-1',
            'payload': 'cmVnbGE',
            'reason': 'high_impact',
            'summary': {
              'kind': 'savings_rule',
              'amount_micros': body['amount_micros'],
              'currency': 'PEN',
              'to_name': 'Viaje a Cusco',
              'cadence': body['cadence']
            },
          }, 428);
        }
        goal = goalJson(
          saved: 25000000,
          rule: {'amount': pen(25000000), 'cadence': 'weekly', 'next_run_on': '2026-11-14'},
          contributions: [
            {'run_on': '2026-11-07', 'outcome': 'saved', 'asked': pen(25000000), 'amount': pen(25000000)}
          ],
        );
        return json(goal);
      },
    });
    final keys = RecordingKeys();
    await tester.binding.setSurfaceSize(const Size(800, 1600));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(MaterialApp(
        home: GoalScreen(client: bank.client(token: 't'), goalId: 'g-1', confirmer: TransferConfirmer(keys: keys))));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('edit-rule')));
    await tester.pumpAndSettle();
    // Propone lo que hace falta, cada semana.
    expect(find.widgetWithText(TextField, '25'), findsOneWidget);
    await tester.tap(find.text('Continuar'));
    await tester.pumpAndSettle();

    expect(keys.reasons.single, 'Apartar S/ 25,00 cada semana para Viaje a Cusco');
    expect(bank.received['PUT /v1/goals/g-1/rule'], {
      'amount_micros': 25000000,
      'cadence': 'weekly',
      'confirmation': {'challenge_id': 'ch-1', 'method': 'biometric', 'signature': 'firma-biometric-cmVnbGE'},
    });
    expect(find.text('Aparta S/ 25,00 cada semana'), findsOneWidget);
    expect(find.text('Apartó S/ 25,00'), findsOneWidget, reason: 'el primer aporte es hoy');
  });

  testWidgets('sin teléfono vinculado no se ofrece programar aportes', (tester) async {
    final bank = FakeBank({'GET /v1/goals/g-1': (_) => json(goalJson())});
    await tester.pumpWidget(MaterialApp(home: GoalScreen(client: bank.client(token: 't'), goalId: 'g-1')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('edit-rule')), findsNothing);
    expect(find.text('Para programar aportes, entra desde tu teléfono vinculado.'), findsOneWidget);
  });

  testWidgets('cerrar el diálogo de la cara no programa nada', (tester) async {
    final bank = FakeBank({
      'GET /v1/goals/g-1': (_) => json(goalJson()),
      'PUT /v1/goals/g-1/rule': (_) => json({
            'code': 'confirmation_required',
            'challenge_id': 'ch-1',
            'payload': 'cmVnbGE',
            'summary': {'kind': 'savings_rule'},
          }, 428),
    });
    await tester.binding.setSurfaceSize(const Size(800, 1600));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(MaterialApp(
        home: GoalScreen(
            client: bank.client(token: 't'),
            goalId: 'g-1',
            confirmer:
                TransferConfirmer(keys: FakeKeys(deviceId: 'dev-1', biometricFailure: DeviceKeyFailure.cancelled)))));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('edit-rule')));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), '20');
    await tester.tap(find.text('Continuar'));
    await tester.pumpAndSettle();
    expect(bank.calls.where((c) => c == 'PUT /v1/goals/g-1/rule'), hasLength(1));
    expect(find.text('Ahorro automático'), findsOneWidget, reason: 'sigue sin regla, y sin error');
  });
}
