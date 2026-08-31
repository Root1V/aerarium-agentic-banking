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
      sign < 0 ? Money(-amount.amountMinor, amount.currency) : amount;

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
