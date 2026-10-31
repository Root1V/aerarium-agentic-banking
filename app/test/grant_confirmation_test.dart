import 'dart:convert';

import 'package:aibank_app/auth/device_keys.dart';
import 'package:aibank_app/screens/consent_screen.dart';
import 'package:aibank_app/screens/mandates_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;

import 'step_up_test.dart' show RecordingKeys;
import 'support/fake_bank.dart';
import 'support/fake_keys.dart';

/// Dar permiso de pago es de alto impacto (nivel 4 de la capa de control): el
/// banco pide confirmarlo con la cara, o con la clave si la cara no puede.

const _handoff = 'csr_prueba';

Map<String, dynamic> _prompt() => {
      'consent_request_id': 'cr-1',
      'requested_by': 'Mercatus',
      'purpose': 'Pagar los servicios que contrata tu agente (prueba)',
      'currency': 'USD',
      'max_per_operation': 20000000,
      'max_total': 100000000,
      'status': 'PENDING',
      'expires_at': '2026-11-01T20:57:00Z',
    };

http.Response _needsConfirmation() => json({
      'code': 'confirmation_required',
      'message': 'confirma la operación con tu teléfono',
      'challenge_id': 'ch-1',
      'payload': 'cGVybWlzbw',
      'reason': 'high_impact',
      'summary': {
        'kind': 'grant_permission',
        'currency': 'USD',
        'to_name': 'Mercatus',
        'max_per_operation': 20000000,
        'max_total': 100000000,
        'expires_in_days': 30,
      },
    }, 428);

/// Un banco que pide confirmar el permiso sin firma y lo concede con ella.
FakeBank _bank() => FakeBank({
      'GET /v1/consent-requests/$_handoff': (_) => json(_prompt()),
      'POST /v1/consent-requests/$_handoff/approve': (body) {
        if (body['confirmation'] == null) return _needsConfirmation();
        return json({
          'mandate_id': 'm-1',
          'account_id': 'acc-1',
          'granted_to': 'Mercatus',
          'currency': 'USD',
          'status': 'active',
          'consumed': 0,
          'expires_at': '2026-12-01T20:57:00Z',
        }, 201);
      },
      'POST /v1/sandbox/consent-request': (_) => json({'handoff_code': _handoff}, 201),
      'GET /v1/mandates': (_) => json({'mandates': <Object>[]}),
    });

/// Abre la pantalla desde otra, para poder ver que se cierra al conceder.
Future<void> _open(WidgetTester tester, FakeBank bank, DeviceKeys keys) async {
  await tester.binding.setSurfaceSize(const Size(800, 1600));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await tester.pumpWidget(MaterialApp(
    home: Builder(
      builder: (context) => TextButton(
        onPressed: () => Navigator.of(context).push(MaterialPageRoute<void>(
          builder: (_) => ConsentScreen(
            client: bank.client(token: 'tok'),
            handoffCode: _handoff,
            accountId: 'acc-1',
            confirmer: TransferConfirmer(keys: keys),
          ),
        )),
        child: const Text('abrir'),
      ),
    ),
  ));
  await tester.tap(find.text('abrir'));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('pide la cara diciendo a quién se da el permiso, y concede con la firma', (tester) async {
    final bank = _bank();
    final keys = RecordingKeys();
    await _open(tester, bank, keys);

    await tester.tap(find.text('Dar permiso'));
    await tester.pumpAndSettle();

    expect(keys.reasons.single, 'Das permiso a Mercatus para pagar desde tu cuenta');
    final approve = bank.received['POST /v1/consent-requests/$_handoff/approve']!;
    expect(approve['confirmation'], {
      'challenge_id': 'ch-1',
      'method': 'biometric',
      'signature': 'firma-biometric-cGVybWlzbw',
    });
    expect(approve['max_per_operation'], 20000000, reason: 'los mismos topes que se firmaron');
    expect(find.text('Dar permiso'), findsNothing, reason: 'concedido, la pantalla se cierra');
  });

  testWidgets('el permiso se le da a un agente, y la cara lo dice', (tester) async {
    final bank = FakeBank({
      'GET /v1/consent-requests/$_handoff': (_) => json({..._prompt(), 'agent_name': 'Agente de compras'}),
      'POST /v1/consent-requests/$_handoff/approve': (body) {
        if (body['confirmation'] != null) return json(<String, Object>{}, 500);
        final need = jsonDecode(_needsConfirmation().body) as Map<String, dynamic>;
        (need['summary'] as Map<String, dynamic>)['agent_name'] = 'Agente de compras';
        return json(need, 428);
      },
    });
    final keys = RecordingKeys();
    await _open(tester, bank, keys);

    expect(find.text('Agente de compras'), findsOneWidget);
    expect(find.text('agente de Mercatus, quiere poder pagar desde tu cuenta'), findsOneWidget);

    await tester.tap(find.text('Dar permiso'));
    await tester.pumpAndSettle();
    expect(keys.reasons.single, 'Das permiso a Agente de compras de Mercatus para pagar desde tu cuenta');
  });

  testWidgets('cerrar el diálogo de la cara no concede nada', (tester) async {
    final bank = _bank();
    await _open(tester, bank, FakeKeys(deviceId: 'dev-1', biometricFailure: DeviceKeyFailure.cancelled));

    await tester.tap(find.text('Dar permiso'));
    await tester.pumpAndSettle();

    expect(bank.calls.where((c) => c.endsWith('/approve')), hasLength(1));
    expect(find.text('Dar permiso'), findsOneWidget, reason: 'sigue en la pantalla, sin error');
  });

  testWidgets('si la cara no puede, se confirma con la clave', (tester) async {
    final bank = _bank();
    await _open(tester, bank, FakeKeys(deviceId: 'dev-1', biometricFailure: DeviceKeyFailure.failed));

    await tester.tap(find.text('Dar permiso'));
    await tester.pumpAndSettle();
    expect(find.text('Escribe tu clave para confirmar'), findsOneWidget);

    await typePin(tester, '482916');

    final approve = bank.received['POST /v1/consent-requests/$_handoff/approve']!;
    expect(approve['confirmation'], {
      'challenge_id': 'ch-1',
      'method': 'pin',
      'signature': 'firma-pin-cGVybWlzbw',
      'pin': '482916',
    });
    expect(find.text('Dar permiso'), findsNothing);
  });

  testWidgets('en el sandbox se puede simular que una plataforma pide permiso', (tester) async {
    final bank = _bank();
    await tester.binding.setSurfaceSize(const Size(800, 1600));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(MaterialApp(
      home: MandatesScreen(
        client: bank.client(token: 'tok'),
        testRequest: TestConsentRequest(
          accountId: 'acc-1',
          currency: 'USD',
          confirmer: TransferConfirmer(keys: FakeKeys(deviceId: 'dev-1')),
        ),
      ),
    ));
    await tester.pumpAndSettle();

    await tester.tap(find.text('Simular una solicitud'));
    await tester.pumpAndSettle();

    expect(bank.received['POST /v1/sandbox/consent-request'], {'currency': 'USD'});
    expect(find.text('Mercatus'), findsOneWidget, reason: 'se abre la solicitud para decidir');
  });

  testWidgets('fuera del sandbox no hay simulación', (tester) async {
    final bank = _bank();
    await tester.pumpWidget(MaterialApp(home: MandatesScreen(client: bank.client(token: 'tok'))));
    await tester.pumpAndSettle();
    expect(find.text('Simular una solicitud'), findsNothing);
  });
}
