import 'dart:convert';

import 'package:http/http.dart' as http;

import 'errors.dart';
import 'models.dart';

/// Cliente del BFF.
class BffClient {
  final Uri baseUrl;
  final http.Client _http;
  final String Function() _token;

  BffClient({
    required this.baseUrl,
    required String Function() token,
    http.Client? httpClient,
  })  : _http = httpClient ?? http.Client(),
        _token = token;

  static const _timeout = Duration(seconds: 15);

  Future<Home> home(String accountId) async {
    final json = await _get('/v1/home', {'account_id': accountId});
    return Home.fromJson(json);
  }

  Future<Statement> movements(String accountId,
      {int limit = 25, String cursor = ''}) async {
    final json = await _get('/v1/accounts/$accountId/movements', {
      'limit': '$limit',
      if (cursor.isNotEmpty) 'cursor': cursor,
    });
    return Statement.fromJson(json);
  }

  /// Envía dinero a otra cuenta del banco.
  ///
  /// `idempotencyKey` la decide quien llama y DEBE ser la misma en cada reintento
  /// de la misma operación. Es lo que impide que un reintento tras un timeout
  /// —donde no se sabe si el servidor procesó o no— envíe el dinero dos veces.
  Future<TransferResult> transfer({
    required String idempotencyKey,
    required String fromAccountId,
    required String toAccountId,
    required int amountMicros,
    required String currency,
    String description = '',
  }) async {
    final json = await _post(
      '/v1/transfers',
      headers: {'Idempotency-Key': idempotencyKey},
      body: {
        'from_account_id': fromAccountId,
        'to_account_id': toAccountId,
        'amount_micros': amountMicros,
        'currency': currency,
        'description': description,
      },
    );
    return TransferResult.fromJson(json);
  }

  void close() => _http.close();

  // ------------------------------------------------------------ transporte

  Future<Map<String, dynamic>> _get(
      String path, Map<String, String> query) async {
    final uri = baseUrl.replace(
      path: path,
      queryParameters: query.isEmpty ? null : query,
    );
    return _send(() => _http.get(uri, headers: _headers()));
  }

  Future<Map<String, dynamic>> _post(
    String path, {
    required Map<String, String> headers,
    required Map<String, dynamic> body,
  }) async {
    final uri = baseUrl.replace(path: path);
    return _send(() => _http.post(
          uri,
          headers: {..._headers(), ...headers, 'Content-Type': 'application/json'},
          body: jsonEncode(body),
        ));
  }

  /// Para respuestas 204, donde no hay cuerpo que interpretar.
  Future<void> _postNoContent(String path) async {
    final uri = baseUrl.replace(path: path);
    try {
      final response = await _http
          .post(uri, headers: _headers())
          .timeout(_timeout);
      if (response.statusCode >= 200 && response.statusCode < 300) return;
      if (response.statusCode == 401) {
        throw ApiError(ApiErrorCode.unauthenticated, 'HTTP 401');
      }
      throw ApiError.fromResponse(_codeOf(response.body), 'HTTP ${response.statusCode}');
    } on ApiError {
      rethrow;
    } catch (e) {
      throw ApiError(ApiErrorCode.network, e.toString());
    }
  }

  static String? _codeOf(String body) {
    try {
      final decoded = jsonDecode(body);
      return decoded is Map<String, dynamic> ? decoded['code'] as String? : null;
    } catch (_) {
      return null;
    }
  }

  Map<String, String> _headers() => {'Authorization': 'Bearer ${_token()}'};

  Future<Map<String, dynamic>> _send(
      Future<http.Response> Function() request) async {
    final http.Response response;
    try {
      response = await request().timeout(_timeout);
    } catch (e) {
      // Un fallo de red deja el resultado en duda: pudo haberse procesado. Por eso
      // se marca como reintentable y el reintento conserva la clave original.
      throw ApiError(ApiErrorCode.network, e.toString());
    }

    if (response.statusCode >= 200 && response.statusCode < 300) {
      final decoded = jsonDecode(response.body);
      if (decoded is! Map<String, dynamic>) {
        throw const ApiError(ApiErrorCode.unknown, 'respuesta inesperada');
      }
      return decoded;
    }

    String? code;
    var detail = 'HTTP ${response.statusCode}';
    try {
      final decoded = jsonDecode(response.body);
      if (decoded is Map<String, dynamic>) {
        code = decoded['code'] as String?;
        detail = '${decoded['message'] ?? detail}';
      }
    } catch (_) {
      // Cuerpo no interpretable: se mantiene el detalle genérico.
    }

    if (response.statusCode == 401) {
      return throw ApiError(ApiErrorCode.unauthenticated, detail);
    }
    throw ApiError.fromResponse(code, detail);
  }
}

/// Permisos delegados: lo que el titular puede ver, conceder y retirar.
extension MandateApi on BffClient {
  /// Lee lo que una plataforma está pidiendo.
  Future<ConsentPrompt> consentPrompt(String handoffCode) async {
    final json = await _get('/v1/consent-requests/$handoffCode', const {});
    return ConsentPrompt.fromJson(json);
  }

  /// Concede el permiso.
  ///
  /// Los topes son los que fija LA PERSONA, no los que pidió la plataforma:
  /// puede conceder menos. El servidor rechaza conceder más.
  Future<Mandate> approveConsent(
    String handoffCode, {
    required String accountId,
    required int expiresInDays,
    int? maxPerOperationMicros,
    int? maxTotalMicros,
  }) async {
    final json = await _post(
      '/v1/consent-requests/$handoffCode/approve',
      headers: const {},
      body: {
        'account_id': accountId,
        'expires_in_days': expiresInDays,
        if (maxPerOperationMicros != null)
          'max_per_operation': maxPerOperationMicros,
        if (maxTotalMicros != null) 'max_total': maxTotalMicros,
      },
    );
    return Mandate.fromJson(json);
  }

  /// Rechaza la solicitud sin conceder nada.
  Future<void> rejectConsent(String handoffCode) async {
    await _postNoContent('/v1/consent-requests/$handoffCode/reject');
  }

  /// Permisos que el titular tiene otorgados.
  Future<List<Mandate>> mandates() async {
    final json = await _get('/v1/mandates', const {});
    final list = json['mandates'] as List<dynamic>? ?? const [];
    return list
        .map((m) => Mandate.fromJson(m as Map<String, dynamic>))
        .toList(growable: false);
  }

  /// Retira un permiso.
  Future<Mandate> revokeMandate(String mandateId) async {
    final json = await _post('/v1/mandates/$mandateId/revoke',
        headers: const {}, body: const {});
    return Mandate.fromJson(json);
  }
}
