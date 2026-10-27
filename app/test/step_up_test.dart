import 'dart:async';

import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/auth/device_keys.dart';
import 'package:aibank_app/screens/account_gate.dart';
import 'package:aibank_app/screens/transfer_screen.dart';
import 'package:aibank_app/transfer_controller.dart';
import 'package:aibank_app/widgets/pin_pad.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;

import 'support/fake_bank.dart';
import 'support/fake_keys.dart';

/// Lo que responde el banco cuando una transferencia pide confirmarse.
http.Response _needsConfirmation(String id) => json({
      'code': 'confirmation_required',
      'message': 'confirma la operación con tu teléfono',
      'challenge_id': id,
      'payload': 'cGF5bG9hZC0$id',
      'reason': 'new_payee',
      'summary': {'amount_micros': 50000000, 'currency': 'USD', 'to_name': 'Juan Pérez'},
    }, 428);

http.Response _sent(Map<String, dynamic> _) => json({'transaction_id': 'tx-1', 'duplicate': false}, 201);

/// Un banco que pide confirmar las transferencias sin firma y acepta las firmadas.
FakeBank confirmingBank({http.Response Function(Map<String, dynamic> body)? onConfirmed}) {
  var challenges = 0;
  return FakeBank({
    'POST /v1/transfers': (body) {
      if (body['confirmation'] == null) return _needsConfirmation('ch-${++challenges}');
      return (onConfirmed ?? _sent)(body);
    },
  });
}

/// Un teléfono que además anota el motivo que mostró al pedir la cara.
class RecordingKeys extends FakeKeys {
  RecordingKeys({super.biometricFailure}) : super(deviceId: 'dev-1');
  final reasons = <String>[];

  @override
  Future<String> sign(DeviceUnlock key, String payload, {required String reason}) {
    reasons.add(reason);
    return super.sign(key, payload, reason: reason);
  }
}

Future<void> send(TransferController c) => c.send(
      fromAccountId: 'acc-1',
      toAccountId: 'acc-juan',
      amountMicros: 50000000,
      currency: 'USD',
    );

void main() {
  group('confirmar una operación de riesgo', () {
    test('pide la cara mostrando monto y destinatario, y repite la misma operación firmada', () async {
      final bank = confirmingBank();
      final keys = RecordingKeys();
      final c = TransferController(bank.client(token: 'tok'), confirmer: TransferConfirmer(keys: keys));

      await send(c);

      expect(c.status, TransferStatus.success);
      expect(keys.reasons.single, 'Envías 50,00 USD a Juan Pérez');
      final confirmed = bank.received['POST /v1/transfers']!;
      expect(confirmed['confirmation'], {
        'challenge_id': 'ch-1',
        'method': 'biometric',
        'signature': 'firma-biometric-cGF5bG9hZC0ch-1',
      });
      expect(confirmed['amount_micros'], 50000000, reason: 'la misma operación, no otra');
    });

    test('cerrar el diálogo de la cara no envía nada', () async {
      final bank = confirmingBank();
      final c = TransferController(bank.client(),
          confirmer: TransferConfirmer(keys: RecordingKeys(biometricFailure: DeviceKeyFailure.cancelled)));

      await send(c);

      expect(c.status, TransferStatus.idle);
      expect(c.error, isNull);
      expect(bank.calls, ['POST /v1/transfers'], reason: 'solo la petición que pidió confirmar');
    });

    test('si la cara no puede, se confirma con la clave', () async {
      final bank = confirmingBank();
      final c = TransferController(bank.client(),
          confirmer: TransferConfirmer(keys: RecordingKeys(biometricFailure: DeviceKeyFailure.fallback)));

      await send(c);
      expect(c.status, TransferStatus.needsPin);
      expect(c.challenge?.toName, 'Juan Pérez');

      await c.confirmWithPin('482916');

      expect(c.status, TransferStatus.success);
      final confirmed = bank.received['POST /v1/transfers']!['confirmation'] as Map<String, dynamic>;
      expect(confirmed['method'], 'pin');
      expect(confirmed['pin'], '482916');
      expect(confirmed['challenge_id'], 'ch-2', reason: 'cada intento con la clave usa un desafío nuevo');
    });

    test('una clave mala deja seguir intentando y dice cuántos intentos quedan', () async {
      final bank = confirmingBank(
        onConfirmed: (_) => json({'code': 'pin_incorrect', 'message': 'clave', 'remaining_attempts': 3}, 401),
      );
      final c = TransferController(bank.client(),
          confirmer: TransferConfirmer(keys: RecordingKeys(biometricFailure: DeviceKeyFailure.lockout)));

      await send(c);
      await c.confirmWithPin('135790');

      expect(c.status, TransferStatus.needsPin);
      expect(c.error?.userMessage, 'Clave incorrecta. Te quedan 3 intentos.');
    });

    test('sin teléfono vinculado no se puede confirmar, y se dice por qué', () async {
      final c = TransferController(confirmingBank().client());
      await send(c);
      expect(c.status, TransferStatus.failed);
      expect(c.error?.userMessage, 'Para esta operación, confirma desde tu teléfono vinculado.');
    });

    test('una operación que no pide confirmación sale sin pedir la cara', () async {
      final bank = FakeBank({'POST /v1/transfers': _sent});
      final keys = RecordingKeys();
      final c = TransferController(bank.client(), confirmer: TransferConfirmer(keys: keys));
      await send(c);
      expect(c.status, TransferStatus.success);
      expect(keys.reasons, isEmpty);
    });
  });

  group('pantalla de envío', () {
    testWidgets('el destinatario de prueba se elige con un toque', (tester) async {
      await tester.pumpWidget(MaterialApp(
        home: TransferScreen(
          client: FakeBank({}).client(),
          fromAccountId: 'acc-1',
          currency: 'USD',
          suggestedPayee: const Account(id: 'acc-juan', name: 'Juan Pérez (prueba)', currency: 'USD'),
        ),
      ));
      await tester.tap(find.text('Juan Pérez (prueba)'));
      await tester.pump();
      expect(tester.widget<TextField>(find.byKey(const Key('destination'))).controller!.text, 'acc-juan');
    });

    testWidgets('mientras se pide la cara no muestra el teclado de la clave', (tester) async {
      final keys = _SlowKeys();
      await tester.pumpWidget(MaterialApp(
        home: TransferScreen(
          client: confirmingBank().client(),
          fromAccountId: 'acc-1',
          currency: 'USD',
          confirmer: TransferConfirmer(keys: keys),
        ),
      ));
      await tester.enterText(find.byKey(const Key('destination')), 'acc-juan');
      await tester.enterText(find.byKey(const Key('amount')), '50');
      await tester.tap(find.byKey(const Key('send')));
      await tester.pump();
      await tester.pump();

      expect(find.text('Confirma con tu cara'), findsOneWidget);
      expect(find.byType(PinPad), findsNothing);
      keys.release();
      await tester.pumpAndSettle();
    });

    testWidgets('si la cara no puede, muestra monto, destinatario y la clave', (tester) async {
      final bank = confirmingBank();
      await tester.pumpWidget(MaterialApp(
        home: TransferScreen(
          client: bank.client(),
          fromAccountId: 'acc-1',
          currency: 'USD',
          confirmer: TransferConfirmer(keys: RecordingKeys(biometricFailure: DeviceKeyFailure.fallback)),
        ),
      ));
      await tester.enterText(find.byKey(const Key('destination')), 'acc-juan');
      await tester.enterText(find.byKey(const Key('amount')), '50');
      await tester.tap(find.byKey(const Key('send')));
      await tester.pumpAndSettle();

      expect(find.text('Confirma el envío'), findsOneWidget);
      expect(find.text('50,00 USD'), findsOneWidget);
      expect(find.text('para Juan Pérez'), findsOneWidget);
      expect(find.byType(PinPad), findsOneWidget);

      await typePin(tester, '482916');
      expect(bank.received['POST /v1/transfers']!['confirmation']['method'], 'pin');
    });
  });

  group('saldo de prueba', () {
    Map<String, http.Response Function(Map<String, dynamic>)> account({required bool sandbox}) => {
          'GET /v1/accounts': (_) => json({
                'accounts': [
                  {'id': 'acc-1', 'name': 'Cuenta simple', 'currency': 'USD'},
                ],
              }),
          'GET /v1/home': (_) => json({
                'account': {'id': 'acc-1', 'name': 'Cuenta simple', 'currency': 'USD'},
                'balance': {'amount_micros': 0, 'currency': 'USD'},
                'movements': <Object>[],
              }),
          if (sandbox) 'GET /v1/sandbox': (_) => json({'available': true}),
          'POST /v1/sandbox/funds': (_) => json({
                'amount_micros': 1000000000,
                'currency': 'USD',
                'payee': {'id': 'acc-juan', 'name': 'Juan Pérez (prueba)', 'currency': 'USD'},
              }),
        };

    testWidgets('en el sandbox se ofrece y acredita', (tester) async {
      final bank = FakeBank(account(sandbox: true));
      await tester.pumpWidget(MaterialApp(home: AccountGate(client: bank.client(token: 'tok'), onSignOut: () {})));
      await tester.pumpAndSettle();

      await tester.tap(find.text('Añadir saldo de prueba'));
      await tester.pumpAndSettle();

      expect(bank.received['POST /v1/sandbox/funds'], {'account_id': 'acc-1'});
      expect(find.textContaining('Añadimos 1.000,00 USD de prueba'), findsOneWidget);
    });

    testWidgets('fuera del sandbox no aparece', (tester) async {
      final bank = FakeBank(account(sandbox: false));
      await tester.pumpWidget(MaterialApp(home: AccountGate(client: bank.client(token: 'tok'), onSignOut: () {})));
      await tester.pumpAndSettle();
      expect(find.text('Añadir saldo de prueba'), findsNothing);
    });
  });
}

/// Un teléfono que tarda en reconocer la cara hasta que se le dice.
class _SlowKeys extends RecordingKeys {
  final _face = Completer<void>();
  void release() => _face.complete();

  @override
  Future<String> sign(DeviceUnlock key, String payload, {required String reason}) async {
    await _face.future;
    return super.sign(key, payload, reason: reason);
  }
}
