import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/auth/device_keys.dart';
import 'package:aibank_app/widgets/pin_pad.dart';
import 'package:flutter_test/flutter_test.dart';

/// Un teléfono de mentira con sus dos llaves.
class FakeKeys implements DeviceKeys {
  FakeKeys({this.deviceId, this.biometricFailure});

  String? deviceId;

  /// Lo que pasa al pedir la cara. `null`: la reconoce.
  DeviceKeyFailure? biometricFailure;


  bool created = false;
  bool wasReset = false;
  final signed = <(DeviceUnlock, String)>[];

  @override
  Future<DeviceKeyStatus> status() async => DeviceKeyStatus(deviceId: deviceId, biometry: 'faceID');

  @override
  Future<DeviceKeyPair> create() async {
    created = true;
    return const DeviceKeyPair(biometricKey: 'pub-bio', possessionKey: 'pub-pos', hardwareBacked: true);
  }

  @override
  Future<String> sign(DeviceUnlock key, String payload, {required String reason}) async {
    if (key == DeviceUnlock.biometric && biometricFailure != null) {
      throw DeviceKeyException(biometricFailure!);
    }
    signed.add((key, payload));
    return 'firma-${key.name}-$payload';
  }

  @override
  Future<void> save(String deviceId) async => this.deviceId = deviceId;

  @override
  Future<void> reset() async {
    wasReset = true;
    deviceId = null;
  }
}

/// Escribe una clave en el teclado de la pantalla.
Future<void> typePin(WidgetTester tester, String pin) async {
  for (final digit in pin.split('')) {
    await tester.tap(find.descendant(of: find.byType(PinPad), matching: find.text(digit)));
    await tester.pump();
  }
  await tester.pumpAndSettle();
}
