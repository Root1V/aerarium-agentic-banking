import 'dart:convert';

import 'package:aibank_app/auth/passkeys.dart';
import 'package:aibank_app/main.dart';
import 'package:aibank_app/screens/account_gate.dart';
import 'package:aibank_app/screens/home_screen.dart';
import 'package:aibank_app/screens/login_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'support/fake_bank.dart';

/// Un teléfono de mentira: devuelve lo que se le diga y anota qué se le pidió.
class FakePasskeys implements PasskeyPlatform {
  FakePasskeys({this.failure});

  final PasskeyFailure? failure;
  Map<String, dynamic>? createdWith;
  Map<String, dynamic>? signedWith;

  @override
  Future<Map<String, dynamic>> create(Map<String, dynamic> options) async {
    createdWith = options;
    if (failure != null) throw PasskeyException(failure!);
    return {'id': 'cred-1', 'type': 'public-key'};
  }

  @override
  Future<Map<String, dynamic>> get(Map<String, dynamic> options) async {
    signedWith = options;
    if (failure != null) throw PasskeyException(failure!);
    return {'id': 'cred-1', 'type': 'public-key'};
  }
}

const _options = {
  'publicKey': {'challenge': 'desafio', 'rpId': 'aerarium.test'},
};

http.Response _ceremony(Map<String, dynamic> _) => json({'ceremony_id': 'cer-1', 'options': _options});

http.Response _session(Map<String, dynamic> _) =>
    json({'token': 'tok-1', 'customer_id': 'cus-1', 'expires_at': '2026-10-25T12:30:00Z'});

Widget _app(Widget home) => MaterialApp(home: home);

void main() {
  testWidgets('sin sesión fija ni sandbox, la app arranca en entrar con passkey', (tester) async {
    // El teléfono dice que no está vinculado.
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        const MethodChannel('dev.aerarium/device_keys'), (_) async => {'deviceId': null, 'biometry': 'faceID'});
    addTearDown(() => tester.binding.defaultBinaryMessenger
        .setMockMethodCallHandler(const MethodChannel('dev.aerarium/device_keys'), null));

    await tester.pumpWidget(const AiBankApp(baseUrl: 'http://bff.test', token: '', accountId: ''));
    await tester.pumpAndSettle();
    expect(find.byType(LoginScreen), findsOneWidget);
    expect(find.text('Abrir una cuenta'), findsOneWidget);
  });

  group('entrar', () {
    testWidgets('firma el desafío del banco y entrega la firma con su ceremonia', (tester) async {
      final bank = FakeBank({
        'POST /v1/passkeys/login/options': _ceremony,
        'POST /v1/passkeys/login': _session,
      });
      final phone = FakePasskeys();
      String? token;

      await tester.pumpWidget(_app(LoginScreen(
        flow: PasskeyFlow(client: bank.client(), platform: phone),
        onSignedIn: (s) => token = s.token,
      )));
      await tester.tap(find.text('Entrar'));
      await tester.pumpAndSettle();

      expect(phone.signedWith, _options, reason: 'las opciones van al sistema sin tocar');
      expect(bank.received['POST /v1/passkeys/login'], {
        'ceremony_id': 'cer-1',
        'credential': {'id': 'cred-1', 'type': 'public-key'},
      });
      expect(token, 'tok-1');
    });

    testWidgets('cerrar el diálogo del sistema no es un error', (tester) async {
      final bank = FakeBank({'POST /v1/passkeys/login/options': _ceremony});
      await tester.pumpWidget(_app(LoginScreen(
        flow: PasskeyFlow(client: bank.client(), platform: FakePasskeys(failure: PasskeyFailure.cancelled)),
        onSignedIn: (_) => fail('no debía entrar'),
      )));
      await tester.tap(find.text('Entrar'));
      await tester.pumpAndSettle();

      expect(bank.received.containsKey('POST /v1/passkeys/login'), isFalse);
      expect(find.byType(Text).evaluate().map((e) => (e.widget as Text).data),
          isNot(contains(contains('salió mal'))));
      final button = tester.widget<ButtonStyleButton>(find.byType(FilledButton));
      expect(button.onPressed, isNotNull, reason: 'se puede volver a intentar');
    });

    testWidgets('un teléfono sin passkeys lo dice en vez de fallar a ciegas', (tester) async {
      final bank = FakeBank({'POST /v1/passkeys/login/options': _ceremony});
      await tester.pumpWidget(_app(LoginScreen(
        flow: PasskeyFlow(client: bank.client(), platform: FakePasskeys(failure: PasskeyFailure.unavailable)),
        onSignedIn: (_) => fail('no debía entrar'),
      )));
      await tester.tap(find.text('Entrar'));
      await tester.pumpAndSettle();

      expect(find.text('Este teléfono todavía no puede usar passkeys con AIBank.'), findsOneWidget);
    });

    testWidgets('una passkey rechazada no se confunde con una sesión vencida', (tester) async {
      final bank = FakeBank({
        'POST /v1/passkeys/login/options': _ceremony,
        'POST /v1/passkeys/login': (_) =>
            json({'code': 'passkey_rejected', 'message': 'no pudimos verificar tu passkey'}, 401),
      });
      await tester.pumpWidget(_app(LoginScreen(
        flow: PasskeyFlow(client: bank.client(), platform: FakePasskeys()),
        onSignedIn: (_) => fail('no debía entrar'),
      )));
      await tester.tap(find.text('Entrar'));
      await tester.pumpAndSettle();

      expect(find.text('No pudimos verificar tu passkey.'), findsOneWidget);
      expect(find.textContaining('sesión expiró'), findsNothing);
    });

    testWidgets('las ceremonias no mandan token', (tester) async {
      final bank = FakeBank({
        'POST /v1/passkeys/login/options': _ceremony,
        'POST /v1/passkeys/login': _session,
      });
      await tester.pumpWidget(_app(LoginScreen(
        flow: PasskeyFlow(client: bank.client(token: 'viejo'), platform: FakePasskeys()),
        onSignedIn: (_) {},
      )));
      await tester.tap(find.text('Entrar'));
      await tester.pumpAndSettle();

      expect(bank.authorization['POST /v1/passkeys/login/options'], isNull);
      expect(bank.authorization['POST /v1/passkeys/login'], isNull);
    });
  });

  group('tras entrar', () {
    final homeOf = {
      'account': {'id': 'acc-2', 'name': 'Cuenta simple', 'currency': 'USD'},
      'balance': {'amount_micros': 0, 'currency': 'USD'},
      'movements': <Object>[],
    };

    testWidgets('abre la cuenta del titular sin que la escriba nadie', (tester) async {
      final bank = FakeBank({
        'GET /v1/accounts': (_) => json({
              'accounts': [
                {'id': 'acc-2', 'name': 'Cuenta simple', 'currency': 'USD'},
              ],
            }),
        'GET /v1/home': (_) => json(homeOf),
      });

      await tester.pumpWidget(_app(AccountGate(client: bank.client(token: 'tok-1'), onSignOut: () {})));
      await tester.pumpAndSettle();

      expect(find.byType(HomeScreen), findsOneWidget);
      expect(tester.widget<HomeScreen>(find.byType(HomeScreen)).accountId, 'acc-2');
      expect(bank.authorization['GET /v1/home'], 'Bearer tok-1');
    });

    testWidgets('salir cierra la sesión también en el banco', (tester) async {
      final bank = FakeBank({
        'GET /v1/home': (_) => json(homeOf),
        'POST /v1/session/logout': (_) => http.Response('', 204),
      });
      var out = false;

      await tester.pumpWidget(_app(HomeScreen(
        client: bank.client(token: 'tok-1'),
        accountId: 'acc-2',
        onSignOut: () => out = true,
      )));
      await tester.pumpAndSettle();
      await tester.tap(find.byTooltip('Salir'));
      await tester.pumpAndSettle();

      expect(bank.authorization['POST /v1/session/logout'], 'Bearer tok-1');
      expect(out, isTrue);
    });

    testWidgets('una sesión vencida lleva a volver a entrar, no a reintentar', (tester) async {
      final bank = FakeBank({
        'GET /v1/home': (_) => json({'code': 'unauthenticated', 'message': 'sesión inválida'}, 401),
      });
      var out = false;

      await tester.pumpWidget(_app(HomeScreen(
        client: bank.client(token: 'vencido'),
        accountId: 'acc-2',
        onSignOut: () => out = true,
      )));
      await tester.pumpAndSettle();

      expect(find.text('Reintentar'), findsNothing);
      await tester.tap(find.text('Volver a entrar'));
      expect(out, isTrue);
    });

    testWidgets('sin a dónde salir, no se ofrece salir', (tester) async {
      final bank = FakeBank({'GET /v1/home': (_) => json(homeOf)});
      await tester.pumpWidget(_app(HomeScreen(client: bank.client(), accountId: 'acc-2')));
      await tester.pumpAndSettle();
      expect(find.byTooltip('Salir'), findsNothing);
    });
  });

  group('canal nativo', () {
    const channel = MethodChannel('dev.aerarium/passkeys');

    void answer(Future<Object?> Function(MethodCall call) handler) {
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(channel, handler);
    }

    tearDown(() =>
        TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(channel, null));

    test('manda y recibe el JSON de WebAuthn', () async {
      TestWidgetsFlutterBinding.ensureInitialized();
      MethodCall? seen;
      answer((call) async {
        seen = call;
        return jsonEncode({'id': 'cred-1'});
      });

      final out = await const NativePasskeys().get({'publicKey': {'challenge': 'x'}});

      expect(seen?.method, 'get');
      expect(jsonDecode(seen?.arguments as String), {'publicKey': {'challenge': 'x'}});
      expect(out, {'id': 'cred-1'});
    });

    test('sin módulo nativo —Android hoy— es "no disponible"', () async {
      TestWidgetsFlutterBinding.ensureInitialized();
      await expectLater(
        const NativePasskeys().create(const {}),
        throwsA(isA<PasskeyException>().having((e) => e.failure, 'failure', PasskeyFailure.unavailable)),
      );
    });

    test('traduce los códigos del sistema', () async {
      TestWidgetsFlutterBinding.ensureInitialized();
      for (final (code, failure) in [
        ('cancelled', PasskeyFailure.cancelled),
        ('unavailable', PasskeyFailure.unavailable),
        ('failed', PasskeyFailure.failed),
      ]) {
        answer((_) async => throw PlatformException(code: code));
        await expectLater(
          const NativePasskeys().get(const {}),
          throwsA(isA<PasskeyException>().having((e) => e.failure, 'failure', failure)),
        );
      }
    });
  });
}
