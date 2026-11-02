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
    TransferConfirmation? confirmation,
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
        if (confirmation != null) 'confirmation': confirmation.toJson(),
      },
    );
    return TransferResult.fromJson(json);
  }

  // ------------------------------------------------------------------ alta

  /// Envía el alta. Sin sesión: quien se da de alta todavía no es cliente, y
  /// mandar una cabecera de autorización vacía sería ruido.
  Future<OnboardingResult> submitOnboarding(OnboardingRequest request) async {
    final json = await _send(() => _http.post(
          baseUrl.replace(path: '/v1/onboarding'),
          headers: {'Content-Type': 'application/json'},
          body: jsonEncode(request.toJson()),
        ));
    return OnboardingResult.fromJson(json);
  }

  /// Consulta un alta. La referencia va en una cabecera y no en la URL: las URL
  /// acaban en los logs de cualquier proxy, y la referencia funciona como un
  /// token.
  Future<OnboardingResult> onboardingStatus(String reference) async {
    final json = await _send(() => _http.get(
          baseUrl.replace(path: '/v1/onboarding'),
          headers: {'X-Onboarding-Reference': reference},
        ));
    return OnboardingResult.fromJson(json);
  }

  // -------------------------------------------------------------- passkeys

  /// Empieza el registro de la primera passkey, con la referencia del alta.
  Future<PasskeyCeremony> beginEnrollment(String reference) async =>
      PasskeyCeremony.fromJson(await _postPublic('/v1/passkeys/enrollment', {'reference': reference}));

  /// Empieza el registro de una passkey más, en un dispositivo nuevo.
  Future<PasskeyCeremony> beginAddDevice() async =>
      PasskeyCeremony.fromJson(await _post('/v1/passkeys/devices', headers: const {}, body: const {}));

  Future<SessionGrant> finishRegistration(String ceremonyId, Map<String, dynamic> credential) async =>
      SessionGrant.fromJson(await _postPublic('/v1/passkeys/registration', {
        'ceremony_id': ceremonyId,
        'credential': credential,
      }));

  Future<PasskeyCeremony> beginLogin() async =>
      PasskeyCeremony.fromJson(await _postPublic('/v1/passkeys/login/options', const {}));

  Future<SessionGrant> finishLogin(String ceremonyId, Map<String, dynamic> credential) async =>
      SessionGrant.fromJson(await _postPublic('/v1/passkeys/login', {
        'ceremony_id': ceremonyId,
        'credential': credential,
      }));

  /// Las cuentas del titular. Es lo primero que se pide tras entrar.
  Future<List<Account>> listAccounts() async {
    final json = await _get('/v1/accounts', const {});
    return ((json['accounts'] as List<dynamic>?) ?? const [])
        .map((a) => Account.fromJson(a as Map<String, dynamic>))
        .toList();
  }

  /// Cierra la sesión en el banco, no solo en el teléfono: un token olvidado en
  /// memoria tiene que dejar de valer también del otro lado.
  Future<void> logout() => _postNoContent('/v1/session/logout');

  /// A quién se le envió dinero, del más reciente al más antiguo.
  Future<List<Account>> recentPayees() async {
    final json = await _get('/v1/payees/recent', const {});
    return ((json['payees'] as List<dynamic>?) ?? const [])
        .map((a) => Account.fromJson(a as Map<String, dynamic>))
        .toList();
  }

  // ------------------------------------------------------ tarjeta de crédito

  /// La tarjeta del titular, o null si no tiene. Lanza si el banco no ofrece
  /// tarjetas (la ruta no existe).
  /// Lo que el detalle de un movimiento sabe además del historial.
  Future<MovementDetail> movementDetail(String accountId, String transactionId) async =>
      MovementDetail.fromJson(await _get('/v1/accounts/$accountId/movements/$transactionId', const {}));

  /// Emite la constancia verificable de un movimiento (o devuelve la que ya tenía).
  Future<VerifiableReceipt> issueReceipt(String accountId, String transactionId) async =>
      VerifiableReceipt.fromJson(await _post('/v1/accounts/$accountId/movements/$transactionId/receipt',
          headers: const {}, body: const {}));

  /// Comprueba una constancia con su código. Nula si no existe: el comprobante no
  /// es de Aerarium.
  Future<VerifiableReceipt?> verifyReceipt(String code) async {
    try {
      return VerifiableReceipt.fromJson(await _get('/v1/receipts/${Uri.encodeComponent(code.trim())}', const {}));
    } on ApiError catch (e) {
      if (e.code == ApiErrorCode.notFound || e.code == ApiErrorCode.receiptNotFound) return null;
      rethrow;
    }
  }

  /// Todo lo que tiene acceso a la cuenta, para el centro de permisos.
  Future<AccessOverview> access() async => AccessOverview.fromJson(await _get('/v1/access', const {}));

  /// Quita una passkey: el banco deja de aceptarla.
  Future<void> removePasskey(String id) => _deleteNoContent('/v1/passkeys/$id');

  /// Los bolsillos de la cuenta.
  Future<List<Pocket>> pockets(String accountId) async {
    final json = await _get('/v1/accounts/$accountId/pockets', const {});
    return [for (final p in (json['pockets'] as List<dynamic>? ?? const [])) Pocket.fromJson(p as Map<String, dynamic>)];
  }

  Future<Pocket> openPocket(String accountId, String name, {String purpose = 'general'}) async =>
      Pocket.fromJson(await _post('/v1/accounts/$accountId/pockets',
          headers: const {}, body: {'name': name, 'purpose': purpose}));

  /// Aparta dinero de la cuenta en el bolsillo.
  Future<void> fillPocket(String pocketId, int amountMicros, {required String idempotencyKey}) =>
      _post('/v1/pockets/$pocketId/deposit',
          headers: {'Idempotency-Key': idempotencyKey}, body: {'amount_micros': amountMicros});

  /// Devuelve dinero del bolsillo a la cuenta.
  Future<void> emptyPocket(String pocketId, int amountMicros, {required String idempotencyKey}) =>
      _post('/v1/pockets/$pocketId/withdraw',
          headers: {'Idempotency-Key': idempotencyKey}, body: {'amount_micros': amountMicros});

  /// Cierra el bolsillo; lo que quede vuelve a la cuenta.
  Future<void> closePocket(String pocketId) => _deleteNoContent('/v1/pockets/$pocketId');

  /// La previsión de saldo de la cuenta hasta el próximo ingreso.
  Future<Forecast> forecast(String accountId) async =>
      Forecast.fromJson(await _get('/v1/accounts/$accountId/forecast', const {}));

  Future<CreditCard?> creditCard() async {
    try {
      return CreditCard.fromJson(await _get('/v1/credit/card', const {}));
    } on ApiError catch (e) {
      if (e.code == ApiErrorCode.noCreditCard) return null;
      rethrow;
    }
  }

  /// Pide la tarjeta: el banco evalúa los movimientos de esta cuenta.
  Future<CreditDecision> applyForCredit(String accountId) async =>
      CreditDecision.fromJson(await _post('/v1/credit/applications', headers: const {}, body: {'account_id': accountId}));

  /// Pasa dinero de la tarjeta a una cuenta propia.
  Future<void> drawFromCard(String accountId, int amountMicros, {required String idempotencyKey}) =>
      _post('/v1/credit/card/draws',
          headers: {'Idempotency-Key': idempotencyKey}, body: {'account_id': accountId, 'amount_micros': amountMicros});

  /// Paga la tarjeta desde una cuenta propia.
  Future<void> payCard(String accountId, int amountMicros, {required String idempotencyKey}) =>
      _post('/v1/credit/card/payments',
          headers: {'Idempotency-Key': idempotencyKey}, body: {'account_id': accountId, 'amount_micros': amountMicros});

  /// Sandbox: corta el estado de cuenta ahora.
  Future<void> closeCycleNow() => _post('/v1/sandbox/credit/close-cycle', headers: const {}, body: const {});

  /// Sandbox: revisa la mora como si ya hubiera pasado el vencimiento.
  Future<void> passDueDate() => _post('/v1/sandbox/credit/pass-due-date', headers: const {}, body: const {});

  // --------------------------------------------------------------- sandbox

  /// Si el banco ofrece saldo de prueba. Solo el sandbox lo ofrece; en
  /// cualquier otro entorno la ruta no existe y la respuesta es `false`.
  Future<bool> sandboxAvailable() async {
    try {
      final json = await _send(() => _http.get(baseUrl.replace(path: '/v1/sandbox')));
      return json['available'] == true;
    } on ApiError {
      return false;
    }
  }

  /// Acredita saldo de prueba en la cuenta y crea un destinatario de prueba.
  Future<TestFunds> addTestFunds(String accountId) async =>
      TestFunds.fromJson(await _post('/v1/sandbox/funds', headers: const {}, body: {'account_id': accountId}));

  // ---------------------------------------------------- teléfono vinculado

  Future<DeviceChallenge> deviceRegistrationOptions() async =>
      DeviceChallenge.fromJson(await _postPublic('/v1/devices/registration/options', const {}));

  /// Vincula este teléfono. Con `reference`, el primero tras el alta; con
  /// `withSession`, uno más para quien ya entró (con su passkey).
  Future<LinkedDevice> registerDevice({
    required String challengeId,
    String? reference,
    bool withSession = false,
    required DeviceKeyPair keys,
    required String biometricSignature,
    required String possessionSignature,
    required String pin,
    required String name,
  }) async {
    final body = {
      'challenge_id': challengeId,
      if (reference != null) 'reference': reference,
      'biometric_key': keys.biometricKey,
      'possession_key': keys.possessionKey,
      'biometric_signature': biometricSignature,
      'possession_signature': possessionSignature,
      'pin': pin,
      'name': name,
      'hardware_backed': keys.hardwareBacked,
    };
    final json = withSession
        ? await _post('/v1/devices/registration', headers: const {}, body: body)
        : await _postPublic('/v1/devices/registration', body);
    return LinkedDevice(deviceId: json['device_id'] as String, session: SessionGrant.fromJson(json));
  }

  Future<DeviceChallenge> deviceLoginOptions(String deviceId) async =>
      DeviceChallenge.fromJson(await _postPublic('/v1/devices/login/options', {'device_id': deviceId}));

  Future<SessionGrant> deviceLogin({
    required String challengeId,
    required DeviceUnlock method,
    required String signature,
    String? pin,
  }) async =>
      SessionGrant.fromJson(await _postPublic('/v1/devices/login', {
        'challenge_id': challengeId,
        'method': method.name,
        'signature': signature,
        if (pin != null) 'pin': pin,
      }));

  /// Desvincula un teléfono de la cuenta.
  Future<void> revokeDevice(String deviceId) => _deleteNoContent('/v1/devices/$deviceId');

  // --------------------------------------------------------------- celulares

  /// El número propio para recibir, o null si no hay uno registrado.
  Future<String?> myPhone() async {
    try {
      return (await _get('/v1/aliases/phone', const {}))['phone'] as String;
    } on ApiError catch (e) {
      if (e.code == ApiErrorCode.notFound) return null;
      rethrow;
    }
  }

  /// Envía un código por SMS para registrar el número en esta cuenta.
  Future<PhoneVerification> startPhoneAlias(String accountId, String phone) async =>
      PhoneVerification.fromJson(
          await _post('/v1/aliases/phone', headers: const {}, body: {'account_id': accountId, 'phone': phone}));

  /// Comprueba el código. Devuelve el número registrado.
  Future<String> verifyPhoneAlias(String verificationId, String code) async =>
      (await _post('/v1/aliases/phone/verify',
          headers: const {}, body: {'verification_id': verificationId, 'code': code}))['phone'] as String;

  Future<void> removePhoneAlias() => _deleteNoContent('/v1/aliases/phone');

  /// A quién llega lo que se envía a un número.
  Future<PhoneMatch> lookupPhone(String phone) async =>
      PhoneMatch.fromJson(await _get('/v1/directory/phone/${phone.replaceAll(RegExp(r'[\s-]'), '')}', const {}));

  Future<void> _deleteNoContent(String path) async {
    final uri = baseUrl.replace(path: path);
    try {
      final response = await _http.delete(uri, headers: _headers()).timeout(_timeout);
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

  /// POST sin sesión: las ceremonias las hace alguien que todavía no entró.
  Future<Map<String, dynamic>> _postPublic(String path, Map<String, dynamic> body) =>
      _send(() => _http.post(
            baseUrl.replace(path: path),
            headers: {'Content-Type': 'application/json'},
            body: jsonEncode(body),
          ));

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

  /// Códigos con los que falla un intento de entrar. Llegan con 401, pero no
  /// significan que una sesión venció.
  static const _signInCodes = {'passkey_rejected', 'device_rejected', 'pin_incorrect'};

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
    int? remaining;
    var details = const <String, dynamic>{};
    var detail = 'HTTP ${response.statusCode}';
    try {
      final decoded = jsonDecode(response.body);
      if (decoded is Map<String, dynamic>) {
        details = decoded;
        code = decoded['code'] as String?;
        detail = '${decoded['message'] ?? detail}';
        remaining = (decoded['remaining_attempts'] as num?)?.toInt();
      }
    } catch (_) {
      // Cuerpo no interpretable: se mantiene el detalle genérico.
    }

    if (response.statusCode == 401 && !_signInCodes.contains(code)) {
      // Un 401 es una sesión que ya no vale, salvo cuando falla el propio
      // intento de entrar: ahí no había sesión, y decir "tu sesión expiró"
      // confundiría.
      return throw ApiError(ApiErrorCode.unauthenticated, detail);
    }
    throw ApiError.fromResponse(code, detail, remainingAttempts: remaining, details: details);
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
    TransferConfirmation? confirmation,
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
        if (confirmation != null) 'confirmation': confirmation.toJson(),
      },
    );
    return Mandate.fromJson(json);
  }

  /// Hace que la plataforma de prueba pida permiso de pago. Solo en el sandbox.
  /// Devuelve el código con el que se abre la solicitud.
  Future<String> sandboxConsentRequest(String currency) async {
    final json = await _post(
      '/v1/sandbox/consent-request',
      headers: const {},
      body: {'currency': currency},
    );
    return json['handoff_code'] as String;
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
