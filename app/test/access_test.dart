import 'package:aibank_app/access_text.dart';
import 'package:aibank_app/api/errors.dart';
import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/auth/device_keys.dart';
import 'package:aibank_app/screens/access_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;

import 'support/fake_bank.dart';
import 'support/fake_keys.dart';

/// El centro de permisos y privacidad (F3-06) en la app.

Map<String, dynamic> overview() => {
      'platforms': [
        {
          'mandate_id': 'm-1',
          'account_id': 'acc-1',
          'granted_to': 'Mercatus',
          'agent_name': 'Agente de viajes',
          'currency': 'USD',
          'status': 'active',
          'max_per_operation': 20000000,
          'consumed': 0,
          'expires_at': '2026-12-01T10:00:00Z',
        },
      ],
      'phones': [
        {'id': 'd-1', 'name': 'iPhone de Ana', 'current': true, 'created_at': '2026-10-01T10:00:00Z'},
        {'id': 'd-2', 'name': 'iPad', 'created_at': '2026-10-02T10:00:00Z', 'last_used_at': '2026-10-30T09:05:00Z'},
      ],
      'passkeys': [
        {'id': 'pk-1', 'created_at': '2026-09-01T10:00:00Z', 'synced': true},
      ],
      'phone_directory': '+51 987 654 321',
      'activity': [
        {
          'at': '2026-11-01T15:00:00Z',
          'actor_kind': 'agent',
          'actor_name': 'Agente de viajes de Mercatus',
          'action': 'mandate_payment',
          'outcome': 'deny',
          'reason': 'over_limit',
        },
      ],
    };

FakeBank bank({http.Response Function(Map<String, dynamic>)? removePasskey}) => FakeBank({
      'GET /v1/access': (_) => json(overview()),
      'POST /v1/mandates/m-1/revoke': (_) => json({...overview()['platforms'][0], 'status': 'revoked'}),
      'DELETE /v1/devices/d-2': (_) => http.Response('', 204),
      'DELETE /v1/passkeys/pk-1': removePasskey ?? (_) => http.Response('', 204),
    });

Future<void> open(WidgetTester tester, FakeBank b, {Future<void> Function()? unlink}) async {
  await tester.binding.setSurfaceSize(const Size(800, 2400));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await tester.pumpWidget(MaterialApp(home: AccessScreen(client: b.client(token: 't'), onUnlinkThisPhone: unlink)));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('todo lo que tiene acceso, en una pantalla', (tester) async {
    await open(tester, bank());

    expect(find.text('Agente de viajes de Mercatus'), findsWidgets);
    expect(find.text('Hasta 20,00 USD por pago · hasta el 01/12'), findsOneWidget);
    expect(find.text('Este teléfono'), findsOneWidget);
    expect(find.text('Usado el 30/10 ${DateTime.parse('2026-10-30T09:05:00Z').toLocal().hour.toString().padLeft(2, '0')}:05'),
        findsOneWidget);
    expect(find.text('Passkey en tu llavero'), findsOneWidget);
    expect(find.text('+51 987 654 321'), findsOneWidget);
    expect(find.text('Pago negado: superaba el tope'), findsOneWidget);
  });

  testWidgets('retirar un permiso de pago, confirmándolo antes', (tester) async {
    final b = bank();
    await open(tester, b);
    await tester.tap(find.widgetWithText(TextButton, 'Retirar'));
    await tester.pumpAndSettle();
    expect(find.text('¿Retirar el permiso a Agente de viajes de Mercatus?'), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, 'Retirar'));
    await tester.pumpAndSettle();
    expect(b.calls, contains('POST /v1/mandates/m-1/revoke'));
  });

  testWidgets('desvincular otro teléfono va al banco; este, por el camino que borra sus llaves', (tester) async {
    final b = bank();
    var unlinkedThis = false;
    await open(tester, b, unlink: () async => unlinkedThis = true);

    await tester.tap(find.widgetWithText(TextButton, 'Desvincular').last);
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, 'Desvincular'));
    await tester.pumpAndSettle();
    expect(b.calls, contains('DELETE /v1/devices/d-2'));

    await tester.tap(find.widgetWithText(TextButton, 'Desvincular').first);
    await tester.pumpAndSettle();
    expect(find.textContaining('saldrás de la app'), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, 'Desvincular'));
    await tester.pumpAndSettle();
    expect(unlinkedThis, isTrue);
  });

  testWidgets('quitar la única forma de entrar se explica, no se esconde', (tester) async {
    final b = bank(
        removePasskey: (_) => json({'code': 'last_way_in', 'message': 'es tu única forma de entrar'}, 409));
    await open(tester, b);
    await tester.tap(find.widgetWithText(TextButton, 'Quitar').first);
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, 'Quitar'));
    await tester.pumpAndSettle();
    expect(find.textContaining('Es tu única forma de entrar'), findsOneWidget);
  });

  test('si el banco no desvincula la única forma de entrar, el teléfono no borra sus llaves', () async {
    final keys = FakeKeys(deviceId: 'd-1');
    final b = FakeBank({
      'DELETE /v1/devices/d-1': (_) => json({'code': 'last_way_in', 'message': 'única'}, 409),
    });
    final flow = DeviceFlow(client: b.client(token: 't'), keys: keys);
    await expectLater(flow.unlink('d-1'), throwsA(isA<ApiError>().having((e) => e.code, 'code', ApiErrorCode.lastWayIn)));
    expect(keys.wasReset, isFalse);
    expect(keys.deviceId, 'd-1');

    // Cualquier otro fallo —sin red, ya desvinculado— borra igual: quien pidió
    // desvincular no quiere que este teléfono siga pudiendo entrar.
    final gone = FakeBank({'DELETE /v1/devices/d-1': (_) => json({'code': 'not_found', 'message': 'no'}, 404)});
    await DeviceFlow(client: gone.client(token: 't'), keys: keys).unlink('d-1');
    expect(keys.wasReset, isTrue);
  });

  test('cada decisión del registro se lee en palabras', () {
    ActivityEntry e(String action, String outcome, [String reason = '']) => ActivityEntry(
        at: DateTime(2026), actorKind: 'holder', actorName: 'Tú', action: action, outcome: outcome, reason: reason);
    expect(activityText(e('grant_permission', 'confirmed')), 'Dio un permiso de pago');
    expect(activityText(e('mandate_payment', 'deny', 'grant_inactive')), 'Pago negado: el permiso ya no estaba vigente');
    expect(activityText(e('transfer', 'confirm', 'new_payee')), 'Un envío pidió confirmación');
    expect(activityDenied(e('transfer', 'rejected')), isTrue);
  });
}
