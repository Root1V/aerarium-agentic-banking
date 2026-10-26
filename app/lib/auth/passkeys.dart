import 'dart:convert';

import 'package:flutter/services.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';

/// Por qué no se pudo usar una passkey, en términos que la pantalla pueda
/// explicar.
enum PasskeyFailure {
  /// La persona cerró el diálogo del sistema. No es un error: no se muestra nada.
  cancelled,

  /// Este dispositivo o esta compilación no puede usar passkeys con el banco:
  /// Android todavía sin módulo nativo, o la app sin asociar a su dominio.
  unavailable,

  /// El sistema operativo falló por otra razón.
  failed,
}

class PasskeyException implements Exception {
  const PasskeyException(this.failure, [this.detail = '']);
  final PasskeyFailure failure;

  /// Solo para diagnóstico. Nunca se muestra.
  final String detail;

  @override
  String toString() => 'PasskeyException(${failure.name}): $detail';
}

/// El autenticador de la plataforma: el chip seguro del teléfono y su biometría.
///
/// Es una interfaz y no una llamada directa al canal nativo para que el flujo
/// —qué se pide, qué se hace con la respuesta, qué se le dice a la persona— se
/// pueda probar sin un teléfono. Las opciones y las respuestas van en la
/// serialización JSON de WebAuthn, tal como las da y las espera el banco.
abstract interface class PasskeyPlatform {
  /// Crea una passkey nueva. `options` es lo que devolvió el banco al empezar el
  /// registro; la respuesta se le reenvía sin tocar.
  Future<Map<String, dynamic>> create(Map<String, dynamic> options);

  /// Firma un desafío de login con una passkey existente.
  Future<Map<String, dynamic>> get(Map<String, dynamic> options);
}

/// Passkeys del sistema operativo, por el canal nativo.
///
/// En iOS lo atiende AuthenticationServices (ios/Runner/PasskeysPlugin.swift).
/// En Android todavía no hay módulo: el canal no responde y la pantalla lo
/// explica en vez de fallar a ciegas.
class NativePasskeys implements PasskeyPlatform {
  const NativePasskeys();

  static const _channel = MethodChannel('dev.aerarium/passkeys');

  @override
  Future<Map<String, dynamic>> create(Map<String, dynamic> options) =>
      _invoke('create', options);

  @override
  Future<Map<String, dynamic>> get(Map<String, dynamic> options) =>
      _invoke('get', options);

  Future<Map<String, dynamic>> _invoke(String method, Map<String, dynamic> options) async {
    try {
      final raw = await _channel.invokeMethod<String>(method, jsonEncode(options));
      if (raw == null) throw const PasskeyException(PasskeyFailure.failed, 'respuesta vacía');
      return jsonDecode(raw) as Map<String, dynamic>;
    } on MissingPluginException {
      throw const PasskeyException(PasskeyFailure.unavailable, 'sin módulo nativo');
    } on PlatformException catch (e) {
      throw PasskeyException(
        switch (e.code) {
          'cancelled' => PasskeyFailure.cancelled,
          'unavailable' => PasskeyFailure.unavailable,
          _ => PasskeyFailure.failed,
        },
        '${e.code}: ${e.message}',
      );
    }
  }
}

/// Las ceremonias completas: pedir el desafío al banco, firmarlo en el
/// teléfono y entregarle la firma.
class PasskeyFlow {
  const PasskeyFlow({required this.client, required this.platform});

  final BffClient client;
  final PasskeyPlatform platform;

  /// Registra la primera passkey de una cuenta recién abierta.
  Future<SessionGrant> enroll(String onboardingReference) async {
    final ceremony = await client.beginEnrollment(onboardingReference);
    final credential = await platform.create(ceremony.options);
    return client.finishRegistration(ceremony.id, credential);
  }

  /// Entra con una passkey ya registrada. No se pide quién es: la passkey lo dice.
  Future<SessionGrant> login() async {
    final ceremony = await client.beginLogin();
    final credential = await platform.get(ceremony.options);
    return client.finishLogin(ceremony.id, credential);
  }
}

/// Qué decirle a la persona cuando una ceremonia no termina. `null` es no decir
/// nada: cerrar el diálogo del sistema fue decisión suya, no un fallo.
String? explainPasskeyError(Object error) => switch (error) {
      PasskeyException(failure: PasskeyFailure.cancelled) => null,
      PasskeyException(failure: PasskeyFailure.unavailable) =>
        'Este teléfono todavía no puede usar passkeys con AIBank.',
      PasskeyException() => 'No se pudo usar la passkey. Inténtalo otra vez.',
      ApiError() => error.userMessage,
      _ => 'Algo salió mal. Inténtalo otra vez.',
    };
