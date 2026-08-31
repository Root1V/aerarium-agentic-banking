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
  unknown,
}

class ApiError implements Exception {
  final ApiErrorCode code;

  /// Mensaje técnico, solo para diagnóstico. NUNCA se muestra en pantalla: puede
  /// contener detalle del servidor que no le sirve a la persona usuaria.
  final String detail;

  const ApiError(this.code, this.detail);

  factory ApiError.fromResponse(String? code, String detail) {
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
        _ => ApiErrorCode.unknown,
      },
      detail,
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
