import '../money.dart';

class Account {
  final String id;
  final String name;
  final String currency;

  const Account({required this.id, required this.name, required this.currency});

  factory Account.fromJson(Map<String, dynamic> json) => Account(
        id: json['id'] as String,
        name: json['name'] as String,
        currency: json['currency'] as String,
      );
}

/// Movimiento tal como lo muestra el extracto.
class Movement {
  final String id;
  final String cursor;

  /// Importe siempre positivo; el signo va aparte para no perderlo al formatear.
  final Money amount;

  /// +1 si suma al saldo, -1 si resta. Lo calcula el BFF para que la app no
  /// tenga que razonar en términos contables.
  final int sign;

  final String kind;
  final String description;
  final DateTime postedAt;

  const Movement({
    required this.id,
    required this.cursor,
    required this.amount,
    required this.sign,
    required this.kind,
    required this.description,
    required this.postedAt,
  });

  factory Movement.fromJson(Map<String, dynamic> json) => Movement(
        id: json['id'] as String,
        cursor: json['cursor'] as String,
        amount: Money.fromJson(json['amount'] as Map<String, dynamic>),
        sign: json['sign'] as int,
        kind: json['kind'] as String,
        description: (json['description'] as String?) ?? '',
        postedAt: DateTime.parse(json['posted_at'] as String),
      );

  /// Importe con su signo, listo para mostrar.
  Money get signedAmount =>
      sign < 0 ? Money(-amount.amountMicros, amount.currency) : amount;

  /// Etiqueta legible del tipo de movimiento.
  String get label => switch (kind) {
        'deposit' => 'Depósito',
        'withdrawal' => 'Retiro',
        'p2p_transfer' => 'Transferencia',
        'rail_inbound_credit' => 'Transferencia recibida',
        'rail_outbound_reserve' => 'Transferencia enviada',
        'rail_outbound_reverse' => 'Devolución',
        _ => 'Movimiento',
      };
}

/// Contenido de la pantalla principal, resuelto en una sola llamada.
class Home {
  final Account account;
  final Money balance;
  final List<Movement> movements;

  const Home({
    required this.account,
    required this.balance,
    required this.movements,
  });

  factory Home.fromJson(Map<String, dynamic> json) => Home(
        account: Account.fromJson(json['account'] as Map<String, dynamic>),
        balance: Money.fromJson(json['balance'] as Map<String, dynamic>),
        movements: ((json['movements'] as List<dynamic>?) ?? const [])
            .map((m) => Movement.fromJson(m as Map<String, dynamic>))
            .toList(),
      );
}

/// Página del extracto.
class Statement {
  final List<Movement> movements;

  /// Vacío cuando no hay más páginas.
  final String nextCursor;

  const Statement({required this.movements, required this.nextCursor});

  factory Statement.fromJson(Map<String, dynamic> json) => Statement(
        movements: ((json['movements'] as List<dynamic>?) ?? const [])
            .map((m) => Movement.fromJson(m as Map<String, dynamic>))
            .toList(),
        nextCursor: (json['next_cursor'] as String?) ?? '',
      );
}

class TransferResult {
  final String transactionId;

  /// El servidor reconoció la operación como ya registrada: el reintento no
  /// duplicó el envío.
  final bool duplicate;

  const TransferResult({required this.transactionId, required this.duplicate});

  factory TransferResult.fromJson(Map<String, dynamic> json) => TransferResult(
        transactionId: json['transaction_id'] as String,
        duplicate: (json['duplicate'] as bool?) ?? false,
      );
}

/// Lo que una plataforma le está pidiendo al titular de una cuenta.
///
/// Es lo que se muestra en la pantalla de consentimiento, y por eso sus campos
/// están pensados para leerse, no para procesarse: quién pide, para qué y
/// cuánto, en ese orden.
class ConsentPrompt {
  final String consentRequestId;

  /// Nombre REGISTRADO de la plataforma. No lo elige ella al pedir: si pudiera,
  /// podría hacerse pasar por otra en la pantalla del banco.
  final String requestedBy;

  /// Para qué, en palabras de la plataforma. Se muestra literalmente.
  final String purpose;
  final String currency;

  /// Topes solicitados. `null` significa "sin tope pedido" en esa dimensión.
  final int? maxPerOperationMicros;
  final int? maxTotalMicros;

  /// `pending` · `approved` · `rejected` · `expired`.
  final String status;
  final DateTime expiresAt;

  const ConsentPrompt({
    required this.consentRequestId,
    required this.requestedBy,
    required this.purpose,
    required this.currency,
    required this.maxPerOperationMicros,
    required this.maxTotalMicros,
    required this.status,
    required this.expiresAt,
  });

  bool get isPending => status == 'PENDING';

  factory ConsentPrompt.fromJson(Map<String, dynamic> json) => ConsentPrompt(
        consentRequestId: json['consent_request_id'] as String,
        requestedBy: json['requested_by'] as String? ?? '',
        purpose: json['purpose'] as String? ?? '',
        currency: json['currency'] as String? ?? '',
        maxPerOperationMicros: json['max_per_operation'] as int?,
        maxTotalMicros: json['max_total'] as int?,
        status: json['status'] as String? ?? 'unknown',
        expiresAt: DateTime.parse(json['expires_at'] as String),
      );
}

/// Un permiso vigente que el titular otorgó a una plataforma.
class Mandate {
  final String id;
  final String accountId;

  /// A quién se le concedió.
  final String grantedTo;
  final String currency;

  /// `active` · `revoked` · `expired`.
  final String status;
  final int? maxPerOperationMicros;
  final int? maxTotalMicros;

  /// Cuánto lleva comprometido, incluidas las retenciones sin capturar.
  final int consumedMicros;

  /// Cuánto queda. `null` si el permiso no tiene tope total.
  final int? remainingMicros;

  final DateTime expiresAt;
  final DateTime? revokedAt;

  const Mandate({
    required this.id,
    required this.accountId,
    required this.grantedTo,
    required this.currency,
    required this.status,
    required this.maxPerOperationMicros,
    required this.maxTotalMicros,
    required this.consumedMicros,
    required this.remainingMicros,
    required this.expiresAt,
    required this.revokedAt,
  });

  /// Si el permiso habilita pagos ahora mismo.
  bool get isActive => status == 'active';

  /// Solo un permiso vivo se puede retirar: revocar uno vencido no cambia nada
  /// y ofrecer el botón invita a una acción sin efecto.
  bool get canRevoke => isActive;

  factory Mandate.fromJson(Map<String, dynamic> json) => Mandate(
        id: json['mandate_id'] as String,
        accountId: json['account_id'] as String? ?? '',
        grantedTo: json['granted_to'] as String? ?? '',
        currency: json['currency'] as String? ?? '',
        status: json['status'] as String? ?? 'unknown',
        maxPerOperationMicros: json['max_per_operation'] as int?,
        maxTotalMicros: json['max_total'] as int?,
        consumedMicros: json['consumed'] as int? ?? 0,
        remainingMicros: json['remaining'] as int?,
        expiresAt: DateTime.parse(json['expires_at'] as String),
        revokedAt: json['revoked_at'] == null
            ? null
            : DateTime.parse(json['revoked_at'] as String),
      );
}
