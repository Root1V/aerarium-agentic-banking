import 'dart:convert';

import 'package:http/http.dart' as http;

/// Sesión de un titular de prueba.
class SandboxSession {
  const SandboxSession({
    required this.customerId,
    required this.accountId,
    required this.token,
  });

  final String customerId;
  final String accountId;
  final String token;
}

/// Por qué no se pudo crear el titular, en términos que la pantalla pueda
/// explicar.
enum SandboxFailure {
  /// No hay servidor en esa dirección, o no responde.
  unreachable,

  /// El entorno exige `X-Dev-Key` y no coincide.
  wrongDevKey,

  /// El servidor existe pero no tiene el simulador: no arrancó en modo sandbox.
  notASandbox,

  unknown,
}

class SandboxException implements Exception {
  const SandboxException(this.failure);
  final SandboxFailure failure;
}

/// Cliente del simulador de titulares (`POST /dev/holders`).
///
/// Vive aparte del `BffClient` a propósito: estas rutas solo existen en el
/// sandbox. Mezclarlas con el cliente de producción dejaría en la app
/// publicada código que sabe pedir sesiones sin autenticarse.
class SandboxClient {
  SandboxClient({
    required this.baseUrl,
    this.devKey = '',
    http.Client? httpClient,
  }) : _http = httpClient ?? http.Client();

  final Uri baseUrl;

  /// Clave del simulador en los entornos alojados. Vacía en local.
  final String devKey;

  final http.Client _http;

  static const _timeout = Duration(seconds: 15);

  /// Crea una persona con cuenta y saldo, y devuelve su sesión.
  Future<SandboxSession> createHolder({
    required String name,
    required int initialBalanceMicros,
    String currency = 'USD',
  }) async {
    final http.Response response;
    try {
      response = await _http
          .post(
            baseUrl.replace(path: '/dev/holders'),
            headers: {
              'Content-Type': 'application/json',
              if (devKey.isNotEmpty) 'X-Dev-Key': devKey,
            },
            body: jsonEncode({
              'name': name,
              'currency': currency,
              'initial_balance': initialBalanceMicros,
            }),
          )
          .timeout(_timeout);
    } on Exception {
      throw const SandboxException(SandboxFailure.unreachable);
    }

    switch (response.statusCode) {
      case 201:
        final json = jsonDecode(response.body) as Map<String, dynamic>;
        return SandboxSession(
          customerId: json['customer_id'] as String,
          accountId: json['account_id'] as String,
          token: json['token'] as String,
        );
      case 401:
        throw const SandboxException(SandboxFailure.wrongDevKey);
      // Sin las rutas de /dev el servidor responde 404 o 405: está vivo, pero
      // no es un sandbox. Distinguirlo de "no responde" ahorra media hora
      // buscando un problema de red que no existe.
      case 404 || 405:
        throw const SandboxException(SandboxFailure.notASandbox);
      default:
        throw const SandboxException(SandboxFailure.unknown);
    }
  }

  void close() => _http.close();
}
