import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/auth/device_keys.dart';
import 'package:aibank_app/auth/passkeys.dart';
import 'package:aibank_app/entry.dart';
import 'package:aibank_app/screens/home_screen.dart';
import 'package:aibank_app/screens/login_screen.dart';
import 'package:aibank_app/screens/protect_account_screen.dart';
import 'package:aibank_app/widgets/pin_pad.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;

import 'support/fake_bank.dart';
import 'support/fake_keys.dart';

class NoPasskeys implements PasskeyPlatform {
  @override
  Future<Map<String, dynamic>> create(Map<String, dynamic> options) async =>
      throw const PasskeyException(PasskeyFailure.unavailable);
  @override
  Future<Map<String, dynamic>> get(Map<String, dynamic> options) async =>
      throw const PasskeyException(PasskeyFailure.unavailable);
}

http.Response _challenge(Map<String, dynamic> _) => json({'challenge_id': 'ch-1', 'payload': 'cGF5bG9hZA'});
http.Response _session(Map<String, dynamic> _) =>
    json({'token': 'tok-1', 'customer_id': 'cus-1', 'expires_at': '2026-10-26T12:30:00Z'});

/// Lo que hace falta para llegar a ver la cuenta.
Map<String, http.Response Function(Map<String, dynamic>)> get _account => {
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
    };

Widget _entry(FakeBank bank, FakeKeys keys, {bool autoPrompt = true}) => MaterialApp(
      home: BankEntry(
        baseUrl: Uri.parse('http://bff.test'),
        keys: keys,
        passkeys: NoPasskeys(),
        httpClient: bank.httpClient,
        autoPrompt: autoPrompt,
      ),
    );

void main() {
  group('abrir la app en un teléfono vinculado', () {
    testWidgets('pide la cara sola y abre la cuenta, sin escribir nada', (tester) async {
      final bank = FakeBank({
        'POST /v1/devices/login/options': _challenge,
        'POST /v1/devices/login': _session,
        ..._account,
      });
      final keys = FakeKeys(deviceId: 'dev-1');

      await tester.pumpWidget(_entry(bank, keys));
      await tester.pumpAndSettle();

      expect(find.byType(HomeScreen), findsOneWidget);
      expect(bank.received['POST /v1/devices/login/options'], {'device_id': 'dev-1'});
      expect(bank.received['POST /v1/devices/login'], {
        'challenge_id': 'ch-1',
        'method': 'biometric',
        'signature': 'firma-biometric-cGF5bG9hZA',
      });
      expect(bank.authorization['GET /v1/home'], 'Bearer tok-1');
    });

    testWidgets('tras salir no pide la cara al instante', (tester) async {
      final bank = FakeBank({});
      await tester.pumpWidget(_entry(bank, FakeKeys(deviceId: 'dev-1'), autoPrompt: false));
      await tester.pumpAndSettle();

      expect(bank.calls, isEmpty);
      expect(find.text('Entrar con Face ID'), findsOneWidget);
    });

    testWidgets('cerrar el diálogo de Face ID no es un error', (tester) async {
      final bank = FakeBank({'POST /v1/devices/login/options': _challenge});
      await tester.pumpWidget(_entry(bank, FakeKeys(deviceId: 'dev-1', biometricFailure: DeviceKeyFailure.cancelled)));
      await tester.pumpAndSettle();

      expect(find.text('Entrar con Face ID'), findsOneWidget);
      expect(find.byType(PinPad), findsNothing);
      expect(find.textContaining('salió mal'), findsNothing);
    });

    testWidgets('"Usar mi clave" en el diálogo lleva a la clave, que firma con la otra llave', (tester) async {
      final bank = FakeBank({
        'POST /v1/devices/login/options': _challenge,
        'POST /v1/devices/login': _session,
        ..._account,
      });
      final keys = FakeKeys(deviceId: 'dev-1', biometricFailure: DeviceKeyFailure.fallback);

      await tester.pumpWidget(_entry(bank, keys));
      await tester.pumpAndSettle();
      expect(find.byType(PinPad), findsOneWidget);

      await typePin(tester, '482916');

      expect(bank.received['POST /v1/devices/login'], {
        'challenge_id': 'ch-1',
        'method': 'pin',
        'signature': 'firma-pin-cGF5bG9hZA',
        'pin': '482916',
      });
      expect(find.byType(HomeScreen), findsOneWidget);
    });

    testWidgets('una clave mala dice cuántos intentos quedan', (tester) async {
      final bank = FakeBank({
        'POST /v1/devices/login/options': _challenge,
        'POST /v1/devices/login': (_) =>
            json({'code': 'pin_incorrect', 'message': 'clave incorrecta', 'remaining_attempts': 2}, 401),
      });
      await tester.pumpWidget(_entry(bank, FakeKeys(deviceId: 'dev-1', biometricFailure: DeviceKeyFailure.fallback)));
      await tester.pumpAndSettle();
      await typePin(tester, '135790');

      expect(find.text('Clave incorrecta. Te quedan 2 intentos.'), findsOneWidget);
      expect(find.textContaining('sesión expiró'), findsNothing);
    });

    testWidgets('un teléfono bloqueado borra sus llaves y lleva a entrar de otra forma', (tester) async {
      final bank = FakeBank({
        'POST /v1/devices/login/options': (_) => json({'code': 'device_locked', 'message': 'bloqueado'}, 403),
      });
      final keys = FakeKeys(deviceId: 'dev-1');
      await tester.pumpWidget(_entry(bank, keys));
      await tester.pumpAndSettle();

      expect(keys.wasReset, isTrue);
      expect(find.byType(LoginScreen), findsOneWidget);
      expect(find.text('Bloqueamos este teléfono por seguridad. Entra con tu passkey o escríbenos.'), findsOneWidget);
    });

    testWidgets('si Face ID cambió, se explica y se pasa a la clave', (tester) async {
      final bank = FakeBank({'POST /v1/devices/login/options': _challenge});
      await tester.pumpWidget(_entry(bank, FakeKeys(deviceId: 'dev-1', biometricFailure: DeviceKeyFailure.invalidated)));
      await tester.pumpAndSettle();

      expect(find.text('Face ID cambió en este teléfono. Por seguridad, entra con tu clave.'), findsOneWidget);
      expect(find.byType(PinPad), findsOneWidget);
    });
  });

  group('proteger la cuenta', () {
    Future<FakeBank> pumpProtect(WidgetTester tester, FakeKeys keys, {String? reference = 'ref-alta', String token = ''}) async {
      final bank = FakeBank({
        'POST /v1/devices/registration/options': _challenge,
        'POST /v1/devices/registration': (_) => json({
              'device_id': 'dev-nuevo',
              'token': 'tok-1',
              'customer_id': 'cus-1',
              'expires_at': '2026-10-26T12:30:00Z',
            }, 201),
      });
      await tester.pumpWidget(MaterialApp(
        home: ProtectAccountScreen(
          flow: DeviceFlow(client: bank.client(token: token), keys: keys),
          biometryName: 'Face ID',
          onboardingReference: reference,
          onLinked: (s) => linkedToken = s.token,
        ),
      ));
      return bank;
    }

    setUp(() => linkedToken = null);

    testWidgets('clave, confirmación y Face ID vinculan el teléfono con la referencia del alta', (tester) async {
      final keys = FakeKeys();
      final bank = await pumpProtect(tester, keys);

      await typePin(tester, '482916');
      expect(find.text('Confirma tu clave'), findsOneWidget);
      await typePin(tester, '482916');
      await tester.tap(find.text('Activar Face ID'));
      await tester.pumpAndSettle();

      expect(keys.created, isTrue);
      expect(keys.signed.map((s) => s.$1), [DeviceUnlock.biometric, DeviceUnlock.pin],
          reason: 'prueba las dos llaves antes de entregarlas');
      expect(bank.received['POST /v1/devices/registration'], {
        'challenge_id': 'ch-1',
        'reference': 'ref-alta',
        'biometric_key': 'pub-bio',
        'possession_key': 'pub-pos',
        'biometric_signature': 'firma-biometric-cGF5bG9hZA',
        'possession_signature': 'firma-pin-cGF5bG9hZA',
        'pin': '482916',
        'name': 'iPhone',
        'hardware_backed': true,
      });
      expect(bank.authorization['POST /v1/devices/registration'], isNull, reason: 'el alta no tiene sesión');
      expect(keys.deviceId, 'dev-nuevo', reason: 'el teléfono recuerda su id');
      expect(linkedToken, 'tok-1');
    });

    testWidgets('una clave fácil de adivinar se rechaza antes de pedir la cara', (tester) async {
      final keys = FakeKeys();
      final bank = await pumpProtect(tester, keys);
      await typePin(tester, '123456');

      expect(find.text('Elige una clave que no sea repetida ni consecutiva, como 123456.'), findsOneWidget);
      expect(find.text('Crea tu clave'), findsOneWidget);
      expect(bank.calls, isEmpty);
    });

    testWidgets('si la confirmación no coincide, se elige otra vez', (tester) async {
      await pumpProtect(tester, FakeKeys());
      await typePin(tester, '482916');
      await typePin(tester, '482917');

      expect(find.text('Las claves no coinciden. Elige tu clave otra vez.'), findsOneWidget);
      expect(find.text('Crea tu clave'), findsOneWidget);
    });

    testWidgets('en un teléfono nuevo, tras la passkey, se vincula con la sesión', (tester) async {
      final bank = await pumpProtect(tester, FakeKeys(), reference: null, token: 'tok-passkey');
      await typePin(tester, '482916');
      await typePin(tester, '482916');
      await tester.tap(find.text('Activar Face ID'));
      await tester.pumpAndSettle();

      expect(bank.authorization['POST /v1/devices/registration'], 'Bearer tok-passkey');
      expect(bank.received['POST /v1/devices/registration']!.containsKey('reference'), isFalse);
    });

    testWidgets('sin Face ID configurado lo explica', (tester) async {
      await pumpProtect(tester, FakeKeys(biometricFailure: DeviceKeyFailure.unavailable));
      await typePin(tester, '482916');
      await typePin(tester, '482916');
      await tester.tap(find.text('Activar Face ID'));
      await tester.pumpAndSettle();

      expect(find.text('Activa Face ID en los ajustes del teléfono para proteger tu cuenta.'), findsOneWidget);
    });
  });

  testWidgets('un teléfono sin vincular ofrece entrar o abrir una cuenta', (tester) async {
    await tester.pumpWidget(_entry(FakeBank({}), FakeKeys()));
    await tester.pumpAndSettle();
    expect(find.byType(LoginScreen), findsOneWidget);
    expect(find.text('Abrir una cuenta'), findsOneWidget);
  });

  testWidgets('desvincular borra las llaves y avisa', (tester) async {
    final bank = FakeBank({
      'POST /v1/devices/login/options': _challenge,
      'POST /v1/devices/login': _session,
      'DELETE /v1/devices/dev-1': (_) => http.Response('', 204),
      ..._account,
    });
    final keys = FakeKeys(deviceId: 'dev-1');
    await tester.pumpWidget(_entry(bank, keys));
    await tester.pumpAndSettle();

    await tester.tap(find.byTooltip('Más'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Desvincular este teléfono'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Desvincular'));
    await tester.pumpAndSettle();

    expect(bank.authorization['DELETE /v1/devices/dev-1'], 'Bearer tok-1');
    expect(keys.wasReset, isTrue);
    expect(find.byType(LoginScreen), findsOneWidget);
    expect(find.text('Desvinculaste este teléfono.'), findsOneWidget);
  });

  test('las claves que se adivinan', () {
    for (final pin in ['000000', '999999', '123456', '012345', '987654', '543210', '12345', '1234567', 'abcdef']) {
      expect(isWeakPin(pin), isTrue, reason: pin);
    }
    for (final pin in ['482916', '135790', '112233', '907251']) {
      expect(isWeakPin(pin), isFalse, reason: pin);
    }
  });

  group('canal nativo', () {
    const channel = MethodChannel('dev.aerarium/device_keys');
    tearDown(() =>
        TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(channel, null));

    test('sin módulo nativo, el teléfono no está vinculado', () async {
      TestWidgetsFlutterBinding.ensureInitialized();
      final status = await const NativeDeviceKeys().status();
      expect(status.linked, isFalse);
    });

    test('traduce los códigos del sistema', () async {
      TestWidgetsFlutterBinding.ensureInitialized();
      for (final failure in DeviceKeyFailure.values) {
        TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
            .setMockMethodCallHandler(channel, (_) async => throw PlatformException(code: failure.name));
        await expectLater(
          const NativeDeviceKeys().sign(DeviceUnlock.biometric, 'x', reason: ''),
          throwsA(isA<DeviceKeyException>().having((e) => e.failure, 'failure', failure)),
        );
      }
    });
  });
}

String? linkedToken;
