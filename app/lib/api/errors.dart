/// Errores del BFF traducidos a algo que la persona usuaria pueda entender y
/// sobre lo que pueda actuar.
library;

/// Motivos que el BFF puede devolver. `unknown` cubre códigos que esta versión
/// de la app todavía no conoce: el servidor puede evolucionar más rápido que las
/// apps instaladas.
enum ApiErrorCode {
  unauthenticated,
  notFound,
  insufficientFunds,
  balanceCapExceeded,
  transactionCapExceeded,
  idempotencyConflict,
  invalidRequest,
  currencyMismatch,
  unavailable,
  network,
  // Alta de clientes.
  invalidDocument,
  invalidName,
  invalidDate,
  underage,
  rateLimited,
  referenceConflict,
  // Passkeys.
  passkeyRejected,
  ceremonyExpired,
  notEnrollable,
  alreadyEnrolled,
  // Teléfono vinculado.
  pinIncorrect,
  deviceLocked,
  deviceRejected,
  weakPin,
  // Confirmación de operaciones de riesgo.
  confirmationRequired,
  confirmationRejected,
  confirmationUnavailable,
  unknown,
}

class ApiError implements Exception {
  final ApiErrorCode code;

  /// Mensaje técnico, solo para diagnóstico. NUNCA se muestra en pantalla: puede
  /// contener detalle del servidor que no le sirve a la persona usuaria.
  final String detail;

  const ApiError(this.code, this.detail, {this.remainingAttempts, this.details = const {}});

  /// El cuerpo completo de la respuesta, para los errores que traen datos con
  /// los que seguir —el desafío de una confirmación, por ejemplo—.
  final Map<String, dynamic> details;

  /// Con `pinIncorrect`: cuántos intentos quedan antes del bloqueo.
  final int? remainingAttempts;

  factory ApiError.fromResponse(String? code, String detail,
      {int? remainingAttempts, Map<String, dynamic> details = const {}}) {
    return ApiError(
      switch (code) {
        'unauthenticated' => ApiErrorCode.unauthenticated,
        'not_found' => ApiErrorCode.notFound,
        'insufficient_funds' => ApiErrorCode.insufficientFunds,
        'balance_cap_exceeded' => ApiErrorCode.balanceCapExceeded,
        'transaction_cap_exceeded' => ApiErrorCode.transactionCapExceeded,
        'idempotency_conflict' => ApiErrorCode.idempotencyConflict,
        'invalid_request' || 'missing_idempotency_key' => ApiErrorCode.invalidRequest,
        'currency_mismatch' => ApiErrorCode.currencyMismatch,
        'unavailable' => ApiErrorCode.unavailable,
        'invalid_document' => ApiErrorCode.invalidDocument,
        'invalid_name' => ApiErrorCode.invalidName,
        'invalid_date' => ApiErrorCode.invalidDate,
        'underage' => ApiErrorCode.underage,
        'rate_limited' => ApiErrorCode.rateLimited,
        'reference_conflict' || 'invalid_reference' => ApiErrorCode.referenceConflict,
        'passkey_rejected' => ApiErrorCode.passkeyRejected,
        'ceremony_expired' => ApiErrorCode.ceremonyExpired,
        'not_enrollable' => ApiErrorCode.notEnrollable,
        'already_enrolled' => ApiErrorCode.alreadyEnrolled,
        'pin_incorrect' => ApiErrorCode.pinIncorrect,
        'device_locked' => ApiErrorCode.deviceLocked,
        'device_rejected' => ApiErrorCode.deviceRejected,
        'weak_pin' => ApiErrorCode.weakPin,
        'confirmation_required' => ApiErrorCode.confirmationRequired,
        'confirmation_rejected' => ApiErrorCode.confirmationRejected,
        'confirmation_unavailable' => ApiErrorCode.confirmationUnavailable,
        _ => ApiErrorCode.unknown,
      },
      detail,
      remainingAttempts: remainingAttempts,
      details: details,
    );
  }

  /// Texto para mostrar en pantalla.
  String get userMessage => switch (code) {
        ApiErrorCode.unauthenticated => 'Tu sesión expiró. Vuelve a ingresar.',
        ApiErrorCode.notFound => 'No encontramos esa cuenta.',
        ApiErrorCode.insufficientFunds => 'No tienes saldo suficiente.',
        ApiErrorCode.balanceCapExceeded =>
          'La cuenta de destino llegó a su límite de saldo.',
        ApiErrorCode.transactionCapExceeded =>
          'El monto supera el límite por operación de tu cuenta.',
        ApiErrorCode.idempotencyConflict =>
          'Esta operación ya se registró con otros datos.',
        ApiErrorCode.currencyMismatch => 'Las monedas no coinciden.',
        ApiErrorCode.invalidRequest => 'Revisa los datos e intenta de nuevo.',
        ApiErrorCode.unavailable || ApiErrorCode.network =>
          'No pudimos conectarnos. Intenta de nuevo.',
        ApiErrorCode.invalidDocument => 'El DNI tiene 8 dígitos.',
        ApiErrorCode.invalidName => 'Escribe tus nombres y tus apellidos.',
        ApiErrorCode.invalidDate => 'Revisa la fecha de nacimiento.',
        ApiErrorCode.underage =>
          'Necesitas ser mayor de edad para abrir una cuenta.',
        ApiErrorCode.rateLimited =>
          'Demasiados intentos. Espera un momento y vuelve a intentarlo.',
        ApiErrorCode.referenceConflict => 'Algo salió mal. Intenta de nuevo.',
        ApiErrorCode.passkeyRejected => 'No pudimos verificar tu passkey.',
        ApiErrorCode.ceremonyExpired =>
          'Pasó demasiado tiempo. Vuelve a intentarlo.',
        ApiErrorCode.notEnrollable =>
          'No pudimos proteger tu cuenta desde aquí. Escríbenos para ayudarte.',
        ApiErrorCode.alreadyEnrolled => 'Tu cuenta ya está protegida con una passkey.',
        ApiErrorCode.pinIncorrect => switch (remainingAttempts) {
            1 => 'Clave incorrecta. Te queda 1 intento.',
            final n? => 'Clave incorrecta. Te quedan $n intentos.',
            null => 'Clave incorrecta.',
          },
        ApiErrorCode.deviceLocked =>
          'Bloqueamos este teléfono por seguridad. Entra con tu passkey o escríbenos.',
        ApiErrorCode.deviceRejected => 'Este teléfono ya no está vinculado a tu cuenta.',
        ApiErrorCode.confirmationRequired => 'Confirma la operación con tu teléfono.',
        ApiErrorCode.confirmationRejected => 'No pudimos confirmar la operación. No se envió nada.',
        ApiErrorCode.confirmationUnavailable =>
          'Para esta operación, confirma desde tu teléfono vinculado.',
        ApiErrorCode.weakPin =>
          'Elige una clave que no sea repetida ni consecutiva, como 123456.',
        ApiErrorCode.unknown => 'Algo salió mal. Intenta de nuevo.',
      };

  /// Si reintentar tiene sentido.
  ///
  /// Solo lo es ante fallos de conexión o de servicio. Reintentar por saldo
  /// insuficiente no crea dinero, y ofrecer el botón invita a la persona a
  /// insistir contra una pared.
  bool get isRetryable =>
      code == ApiErrorCode.network || code == ApiErrorCode.unavailable;

  @override
  String toString() => 'ApiError(${code.name}): $detail';
}
