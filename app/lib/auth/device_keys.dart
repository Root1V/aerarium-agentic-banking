import 'package:flutter/services.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';

/// Por qué el teléfono no firmó.
enum DeviceKeyFailure {
  /// Cerró el diálogo de Face ID. No es un error: no se muestra nada.
  cancelled,

  /// Pulsó "Usar mi clave" en el diálogo del sistema.
  fallback,

  /// El sistema bloqueó la biometría tras varios intentos: toca la clave.
  lockout,

  /// La biometría del teléfono cambió desde que se vinculó (alguien añadió una
  /// cara o una huella): la llave biométrica dejó de servir.
  invalidated,

  /// No hay Face ID ni Touch ID configurados, o falta el módulo nativo.
  unavailable,

  failed,
}

class DeviceKeyException implements Exception {
  const DeviceKeyException(this.failure, [this.detail = '']);
  final DeviceKeyFailure failure;

  /// Solo para diagnóstico. Nunca se muestra.
  final String detail;

  @override
  String toString() => 'DeviceKeyException(${failure.name}): $detail';
}

/// Qué sabe el teléfono de sí mismo antes de hablar con el banco.
class DeviceKeyStatus {
  const DeviceKeyStatus({this.deviceId, this.biometry = 'none'});

  /// El id con el que el banco conoce a este teléfono, si está vinculado.
  final String? deviceId;

  /// `faceID`, `touchID`, `other` o `none`.
  final String biometry;

  bool get linked => deviceId != null;
  String get biometryName => biometry == 'touchID' ? 'Touch ID' : 'Face ID';
}

/// Las llaves del teléfono, en su chip seguro.
///
/// Interfaz y no llamada directa al canal para que el flujo se pueda probar sin
/// teléfono, igual que las passkeys.
abstract interface class DeviceKeys {
  Future<DeviceKeyStatus> status();

  /// Crea las dos llaves (reemplaza las que hubiera) y devuelve las públicas.
  Future<DeviceKeyPair> create();

  /// Firma `payload` (base64url) con una de las llaves. La biométrica pide la
  /// cara o la huella con `reason`.
  Future<String> sign(DeviceUnlock key, String payload, {required String reason});

  /// Recuerda el id que le dio el banco.
  Future<void> save(String deviceId);

  /// Borra las llaves y el id: el teléfono deja de estar vinculado.
  Future<void> reset();
}

class NativeDeviceKeys implements DeviceKeys {
  const NativeDeviceKeys();

  static const _channel = MethodChannel('dev.aerarium/device_keys');

  @override
  Future<DeviceKeyStatus> status() async {
    try {
      final raw = await _channel.invokeMapMethod<String, dynamic>('status');
      return DeviceKeyStatus(
        deviceId: raw?['deviceId'] as String?,
        biometry: raw?['biometry'] as String? ?? 'none',
      );
    } on MissingPluginException {
      // Android, todavía: sin módulo nativo no hay llaves que recordar.
      return const DeviceKeyStatus();
    }
  }

  @override
  Future<DeviceKeyPair> create() async {
    final raw = await _call<Map<Object?, Object?>>('create');
    return DeviceKeyPair(
      biometricKey: raw['biometricKey']! as String,
      possessionKey: raw['possessionKey']! as String,
      hardwareBacked: raw['hardwareBacked']! as bool,
    );
  }

  @override
  Future<String> sign(DeviceUnlock key, String payload, {required String reason}) =>
      _call<String>('sign', {
        'key': key == DeviceUnlock.biometric ? 'biometric' : 'possession',
        'payload': payload,
        'reason': reason,
      });

  @override
  Future<void> save(String deviceId) => _call<void>('save', {'deviceId': deviceId});

  @override
  Future<void> reset() => _call<void>('reset');

  Future<T> _call<T>(String method, [Object? args]) async {
    try {
      return await _channel.invokeMethod<T>(method, args) as T;
    } on MissingPluginException {
      throw const DeviceKeyException(DeviceKeyFailure.unavailable, 'sin módulo nativo');
    } on PlatformException catch (e) {
      throw DeviceKeyException(
        DeviceKeyFailure.values.firstWhere((f) => f.name == e.code, orElse: () => DeviceKeyFailure.failed),
        '${e.code}: ${e.message}',
      );
    }
  }
}

/// Vincular el teléfono y entrar con él.
class DeviceFlow {
  const DeviceFlow({required this.client, required this.keys});

  final BffClient client;
  final DeviceKeys keys;

  /// Vincula este teléfono: crea las llaves, prueba que firman (con la cara) y
  /// se las presenta al banco con la clave elegida.
  ///
  /// Con `reference`, el primer teléfono tras el alta. Sin ella, `client` tiene
  /// que llevar la sesión de quien ya entró.
  Future<SessionGrant> link({String? reference, required String pin, required String biometryName}) async {
    final pair = await keys.create();
    final challenge = await client.deviceRegistrationOptions();
    final bio = await keys.sign(DeviceUnlock.biometric, challenge.payload,
        reason: 'Activa $biometryName para entrar a AIBank');
    final possession = await keys.sign(DeviceUnlock.pin, challenge.payload, reason: '');
    final linked = await client.registerDevice(
      challengeId: challenge.id,
      reference: reference,
      withSession: reference == null,
      keys: pair,
      biometricSignature: bio,
      possessionSignature: possession,
      pin: pin,
      name: 'iPhone',
    );
    await keys.save(linked.deviceId);
    return linked.session;
  }

  Future<SessionGrant> unlock(String deviceId, DeviceUnlock method, {String? pin}) async {
    final challenge = await client.deviceLoginOptions(deviceId);
    final signature = await keys.sign(method, challenge.payload, reason: 'Entrar a AIBank');
    return client.deviceLogin(challengeId: challenge.id, method: method, signature: signature, pin: pin);
  }

  /// Desvincula el teléfono en el banco y borra sus llaves.
  ///
  /// Las llaves se borran aunque el banco no responda: quien pidió desvincular
  /// no quiere que este teléfono siga pudiendo entrar, y sin llaves no puede.
  Future<void> unlink(String deviceId) async {
    try {
      await client.revokeDevice(deviceId);
    } on ApiError catch (e) {
      // Si es la única forma de entrar, el banco no lo desvincula, y aquí
      // tampoco se borra: sin llaves y sin passkey, la persona quedaría fuera.
      if (e.code == ApiErrorCode.lastWayIn) rethrow;
      // El banco ya no lo reconoce o no hay red: igual se borra aquí.
    }
    await keys.reset();
  }
}

/// Qué decirle a la persona cuando el teléfono no pudo entrar o vincularse.
/// `null` es no decir nada: cerrar el diálogo fue decisión suya.
String? explainDeviceError(Object error, {String biometryName = 'Face ID'}) => switch (error) {
      DeviceKeyException(failure: DeviceKeyFailure.cancelled || DeviceKeyFailure.fallback) => null,
      DeviceKeyException(failure: DeviceKeyFailure.lockout) =>
        '$biometryName está bloqueado por demasiados intentos. Entra con tu clave.',
      DeviceKeyException(failure: DeviceKeyFailure.invalidated) =>
        '$biometryName cambió en este teléfono. Por seguridad, entra con tu clave.',
      DeviceKeyException(failure: DeviceKeyFailure.unavailable) =>
        'Activa $biometryName en los ajustes del teléfono para proteger tu cuenta.',
      DeviceKeyException() => 'No se pudo usar $biometryName. Inténtalo otra vez.',
      ApiError() => error.userMessage,
      _ => 'Algo salió mal. Inténtalo otra vez.',
    };

/// La misma regla que el banco: seis dígitos, ni repetidos ni consecutivos. Se
/// comprueba aquí para avisar antes de pedir la cara; la verdad está en el banco.
bool isWeakPin(String pin) {
  if (!RegExp(r'^\d{6}$').hasMatch(pin)) return true;
  final d = pin.codeUnits;
  bool all(bool Function(int i) f) => [for (var i = 1; i < d.length; i++) f(i)].every((x) => x);
  return all((i) => d[i] == d[0]) || all((i) => d[i] == d[i - 1] + 1) || all((i) => d[i] == d[i - 1] - 1);
}


/// Firma con el teléfono la confirmación de una operación de riesgo.
class TransferConfirmer {
  const TransferConfirmer({required this.keys});

  final DeviceKeys keys;

  /// Con la cara (`DeviceUnlock.biometric`) o con la clave (`DeviceUnlock.pin`,
  /// que firma con la llave de posesión y lleva la clave al banco).
  Future<TransferConfirmation> confirm(
    ConfirmationChallenge challenge,
    DeviceUnlock method, {
    String? pin,
    required String reason,
  }) async {
    final signature = await keys.sign(method, challenge.payload, reason: reason);
    return TransferConfirmation(challengeId: challenge.id, method: method, signature: signature, pin: pin);
  }
}
