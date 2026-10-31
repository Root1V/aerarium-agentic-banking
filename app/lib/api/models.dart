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

  /// Nombre registrado de la plataforma, en los pagos hechos con un permiso.
  /// Vacío en todo lo demás.
  final String merchant;

  /// La otra persona de una transferencia: a quién se le envió o de quién se
  /// recibió. Null en todo lo demás.
  final Account? counterparty;
  final DateTime postedAt;

  const Movement({
    required this.id,
    required this.cursor,
    required this.amount,
    required this.sign,
    required this.kind,
    required this.description,
    required this.postedAt,
    this.merchant = '',
    this.counterparty,
  });

  factory Movement.fromJson(Map<String, dynamic> json) => Movement(
        id: json['id'] as String,
        cursor: json['cursor'] as String,
        amount: Money.fromJson(json['amount'] as Map<String, dynamic>),
        sign: json['sign'] as int,
        kind: json['kind'] as String,
        description: (json['description'] as String?) ?? '',
        merchant: (json['merchant'] as String?) ?? '',
        counterparty: json['counterparty'] == null
            ? null
            : Account.fromJson(json['counterparty'] as Map<String, dynamic>),
        postedAt: DateTime.parse(json['posted_at'] as String),
      );

  /// Importe con su signo, listo para mostrar.
  Money get signedAmount =>
      sign < 0 ? Money(-amount.amountMicros, amount.currency) : amount;

  /// Si es un pago hecho por una plataforma con permiso, con plataforma conocida.
  bool get isPlatformPayment => kind == 'authorization_hold' && merchant.isNotEmpty;

  /// Los movimientos con la tarjeta propia: la otra cuenta es del mismo
  /// titular, y "De Carmen Huaman" (ella misma) no diría nada.
  bool get _withOwnCard => kind == 'credit_drawdown' || kind == 'credit_payment';

  /// Etiqueta legible: a quién o de quién, si se sabe; si no, el tipo.
  String get label => switch (counterparty) {
        _ when isPlatformPayment => 'Pago a $merchant',
        _ when _withOwnCard => _kindLabel,
        final Account who? => sign < 0 ? 'A ${who.name}' : 'De ${who.name}',
        null => _kindLabel,
      };

  String get _kindLabel => switch (kind) {
        'deposit' => 'Depósito',
        'withdrawal' => 'Retiro',
        'p2p_transfer' => 'Transferencia',
        'rail_inbound_credit' => 'Transferencia recibida',
        'rail_outbound_reserve' => 'Transferencia enviada',
        'rail_outbound_reverse' => 'Devolución',
        // Pagos de una plataforma con permiso (modelo B). Al autorizar, el
        // dinero sale del saldo; si nunca se cobra, vuelve. Las dos mitades
        // tienen que leerse como el mismo pago, no como dos movimientos sueltos.
        'authorization_hold' => 'Pago autorizado',
        'authorization_release' => 'Pago no cobrado, devuelto',
        'authorization_refund' => 'Reembolso',
        'authorization_capture' => 'Pago recibido',
        'card_authorization_hold' => 'Compra con tarjeta',
        'card_authorization_reversal' => 'Compra anulada',
        // El cobro de tarjeta solo toca la cuenta por la diferencia con lo
        // retenido: propinas, combustible.
        'card_clearing' => 'Ajuste de compra con tarjeta',
        'sandbox_funding' => 'Saldo de prueba',
        'credit_drawdown' => 'Desde tu tarjeta de crédito',
        'credit_payment' => 'Pago de tu tarjeta de crédito',
        _ => 'Movimiento',
      };
}

/// Lo retenido para una misma plataforma.
class PendingGroup {
  /// Nombre registrado en el banco de quien cobra. Vacío si la retención no se
  /// hizo bajo un permiso de pago.
  final String merchant;
  final int count;
  final Money amount;

  /// Parte que ya venció: sigue fuera del saldo hasta que el banco la libere,
  /// pero ya no se puede cobrar.
  final Money expired;

  /// Antes de esta hora, todo lo retenido o se cobra o vuelve.
  final DateTime lastExpiresAt;

  const PendingGroup({
    required this.merchant,
    required this.count,
    required this.amount,
    required this.expired,
    required this.lastExpiresAt,
  });

  factory PendingGroup.fromJson(Map<String, dynamic> json) => PendingGroup(
        merchant: (json['merchant'] as String?) ?? '',
        count: json['count'] as int,
        amount: Money.fromJson(json['amount'] as Map<String, dynamic>),
        expired: Money.fromJson(json['expired'] as Map<String, dynamic>),
        lastExpiresAt: DateTime.parse(json['last_expires_at'] as String),
      );

  bool get allExpired => expired.amountMicros >= amount.amountMicros;
}

/// Contenido de la pantalla principal, resuelto en una sola llamada.
class Home {
  final Account account;

  /// Lo disponible: lo retenido ya salió de aquí.
  final Money balance;

  /// Lo que salió del saldo hacia pagos que aún no se cobraron ni volvieron.
  ///
  /// `null` significa que el banco no pudo calcularlo, que NO es lo mismo que
  /// cero: en ese caso la pantalla no dice nada en vez de afirmar que no hay
  /// nada retenido.
  final Money? retained;

  final List<PendingGroup> pending;
  final List<Movement> movements;

  /// Si hay movimientos más antiguos que los de esta pantalla.
  final bool hasMore;

  const Home({
    required this.account,
    required this.balance,
    required this.movements,
    this.retained,
    this.pending = const [],
    this.hasMore = false,
  });

  factory Home.fromJson(Map<String, dynamic> json) => Home(
        account: Account.fromJson(json['account'] as Map<String, dynamic>),
        balance: Money.fromJson(json['balance'] as Map<String, dynamic>),
        retained: json['retained'] == null
            ? null
            : Money.fromJson(json['retained'] as Map<String, dynamic>),
        pending: ((json['pending'] as List<dynamic>?) ?? const [])
            .map((g) => PendingGroup.fromJson(g as Map<String, dynamic>))
            .toList(),
        movements: ((json['movements'] as List<dynamic>?) ?? const [])
            .map((m) => Movement.fromJson(m as Map<String, dynamic>))
            .toList(),
        hasMore: (json['has_more'] as bool?) ?? false,
      );

  /// Si hay algo retenido que contar. Falso tanto si es cero como si se
  /// desconoce: en los dos casos lo correcto es no mostrar la fila.
  bool get hasRetained => (retained?.amountMicros ?? 0) > 0;
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

  /// Lo gastado hoy y este mes. `null` si el banco no pudo calcularlo: la
  /// pantalla sigue siendo útil sin él, y lo esencial es poder retirar.
  final MandateSpend? spend;

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
    this.spend,
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
        spend: json['spend'] == null
            ? null
            : MandateSpend.fromJson(json['spend'] as Map<String, dynamic>),
      );
}

/// Lo gastado bajo un permiso, con "hoy" contado en la hora de la persona.
class MandateSpend {
  final Money today;
  final int todayCount;
  final Money month;
  final int monthCount;

  /// Retenido sin cobrar. Ya va incluido en `today` y `month`.
  final Money inFlight;

  const MandateSpend({
    required this.today,
    required this.todayCount,
    required this.month,
    required this.monthCount,
    required this.inFlight,
  });

  factory MandateSpend.fromJson(Map<String, dynamic> json) => MandateSpend(
        today: Money.fromJson(json['today'] as Map<String, dynamic>),
        todayCount: json['today_count'] as int,
        month: Money.fromJson(json['month'] as Map<String, dynamic>),
        monthCount: json['month_count'] as int,
        inFlight: Money.fromJson(json['in_flight'] as Map<String, dynamic>),
      );
}

/// Datos del alta de nivel 1: lo justo para verificar a la persona con la fuente
/// oficial. Cada campo de más es gente que abandona el formulario.
class OnboardingRequest {
  /// La genera el dispositivo y se CONSERVA en cada reintento del mismo alta.
  final String reference;
  final String documentNumber;
  final String firstNames;
  final String lastNames;
  final DateTime dateOfBirth;

  const OnboardingRequest({
    required this.reference,
    required this.documentNumber,
    required this.firstNames,
    required this.lastNames,
    required this.dateOfBirth,
  });

  Map<String, dynamic> toJson() => {
        'reference': reference,
        'document_number': documentNumber,
        'first_names': firstNames,
        'last_names': lastNames,
        'date_of_birth': '${dateOfBirth.year.toString().padLeft(4, '0')}-'
            '${dateOfBirth.month.toString().padLeft(2, '0')}-'
            '${dateOfBirth.day.toString().padLeft(2, '0')}',
      };
}

enum OnboardingStatus { approved, inReview, rejected, inProgress }

class OnboardingResult {
  final String reference;
  final OnboardingStatus status;

  /// Solo cuando la cuenta está abierta.
  final String? customerId;
  final String? accountId;

  const OnboardingResult({
    required this.reference,
    required this.status,
    this.customerId,
    this.accountId,
  });

  factory OnboardingResult.fromJson(Map<String, dynamic> json) => OnboardingResult(
        reference: json['reference'] as String,
        status: switch (json['status']) {
          'approved' => OnboardingStatus.approved,
          'in_review' => OnboardingStatus.inReview,
          'rejected' => OnboardingStatus.rejected,
          _ => OnboardingStatus.inProgress,
        },
        customerId: json['customer_id'] as String?,
        accountId: json['account_id'] as String?,
      );
}


/// Una ceremonia de passkey en curso: el desafío que emitió el banco.
class PasskeyCeremony {
  final String id;

  /// Lo que se le pasa tal cual al sistema operativo.
  final Map<String, dynamic> options;

  const PasskeyCeremony({required this.id, required this.options});

  factory PasskeyCeremony.fromJson(Map<String, dynamic> json) => PasskeyCeremony(
        id: json['ceremony_id'] as String,
        options: json['options'] as Map<String, dynamic>,
      );
}

/// La sesión que entrega el banco tras una passkey verificada.
class SessionGrant {
  final String token;
  final String customerId;
  final DateTime expiresAt;

  const SessionGrant({required this.token, required this.customerId, required this.expiresAt});

  factory SessionGrant.fromJson(Map<String, dynamic> json) => SessionGrant(
        token: json['token'] as String,
        customerId: json['customer_id'] as String,
        expiresAt: DateTime.parse(json['expires_at'] as String),
      );
}


/// Lo que el teléfono tiene que firmar para el banco, en base64url.
class DeviceChallenge {
  final String id;
  final String payload;

  const DeviceChallenge({required this.id, required this.payload});

  factory DeviceChallenge.fromJson(Map<String, dynamic> json) => DeviceChallenge(
        id: json['challenge_id'] as String,
        payload: json['payload'] as String,
      );
}

/// Las claves públicas de las dos llaves del teléfono, en base64url (SPKI DER).
class DeviceKeyPair {
  final String biometricKey;
  final String possessionKey;

  /// En el chip seguro. `false` solo en el simulador.
  final bool hardwareBacked;

  const DeviceKeyPair({
    required this.biometricKey,
    required this.possessionKey,
    required this.hardwareBacked,
  });
}

/// Cómo se desbloquea la llave del teléfono para entrar.
enum DeviceUnlock {
  /// Cara o huella: la llave que solo firma tras la biometría.
  biometric,

  /// La clave de 6 dígitos, junto con la llave de posesión.
  pin,
}

/// Un teléfono recién vinculado: su id y la sesión ya abierta.
class LinkedDevice {
  final String deviceId;
  final SessionGrant session;

  const LinkedDevice({required this.deviceId, required this.session});
}


/// Lo que el banco pide confirmar antes de mover el dinero.
class ConfirmationChallenge {
  final String id;

  /// Los bytes exactos que firma el teléfono, en base64url. Llevan dentro el
  /// monto y el destino: la firma vale para esta operación y ninguna otra.
  final String payload;
  final int amountMicros;
  final String currency;

  /// A quién se le envía, o a quién se le da el permiso.
  final String toName;

  /// Qué se confirma: `transfer` o `grant_permission`.
  final String kind;

  /// Los topes y la vigencia del permiso que se da. Nulos: sin tope.
  final int? maxPerOperationMicros;
  final int? maxTotalMicros;
  final int? expiresInDays;

  const ConfirmationChallenge({
    required this.id,
    required this.payload,
    required this.amountMicros,
    required this.currency,
    required this.toName,
    this.kind = 'transfer',
    this.maxPerOperationMicros,
    this.maxTotalMicros,
    this.expiresInDays,
  });

  factory ConfirmationChallenge.fromJson(Map<String, dynamic> json) {
    final summary = json['summary'] as Map<String, dynamic>? ?? const {};
    return ConfirmationChallenge(
      id: json['challenge_id'] as String,
      payload: json['payload'] as String,
      amountMicros: (summary['amount_micros'] as num?)?.toInt() ?? 0,
      currency: summary['currency'] as String? ?? '',
      toName: summary['to_name'] as String? ?? '',
      kind: summary['kind'] as String? ?? 'transfer',
      maxPerOperationMicros: (summary['max_per_operation'] as num?)?.toInt(),
      maxTotalMicros: (summary['max_total'] as num?)?.toInt(),
      expiresInDays: (summary['expires_in_days'] as num?)?.toInt(),
    );
  }
}

/// La firma de una confirmación, para repetir la operación con ella.
class TransferConfirmation {
  final String challengeId;
  final DeviceUnlock method;
  final String signature;
  final String? pin;

  const TransferConfirmation({
    required this.challengeId,
    required this.method,
    required this.signature,
    this.pin,
  });

  Map<String, dynamic> toJson() => {
        'challenge_id': challengeId,
        'method': method.name,
        'signature': signature,
        if (pin != null) 'pin': pin,
      };
}

/// Saldo de prueba recién acreditado, con alguien a quien enviarle.
class TestFunds {
  final int amountMicros;
  final String currency;
  final Account payee;

  /// El celular de prueba del destinatario, si el banco tiene directorio.
  final String? payeePhone;

  const TestFunds({required this.amountMicros, required this.currency, required this.payee, this.payeePhone});

  factory TestFunds.fromJson(Map<String, dynamic> json) => TestFunds(
        amountMicros: (json['amount_micros'] as num).toInt(),
        currency: json['currency'] as String,
        payee: Account.fromJson(json['payee'] as Map<String, dynamic>),
        payeePhone: json['payee_phone'] as String?,
      );
}


/// Un código enviado por SMS para registrar un número.
class PhoneVerification {
  final String id;
  final String phone;

  /// Solo en el sandbox, donde no hay SMS: el código que se habría enviado.
  final String? sandboxCode;

  const PhoneVerification({required this.id, required this.phone, this.sandboxCode});

  factory PhoneVerification.fromJson(Map<String, dynamic> json) => PhoneVerification(
        id: json['verification_id'] as String,
        phone: json['phone'] as String,
        sandboxCode: json['sandbox_code'] as String?,
      );
}

/// A quién llega lo que se envía a un número: su cuenta y el nombre a medias.
class PhoneMatch {
  final String phone;
  final Account account;

  const PhoneMatch({required this.phone, required this.account});

  factory PhoneMatch.fromJson(Map<String, dynamic> json) => PhoneMatch(
        phone: json['phone'] as String,
        account: Account.fromJson(json['account'] as Map<String, dynamic>),
      );
}

/// "+51987654321" → "+51 987 654 321"; "987654321" → "987 654 321".
String formatPhone(String phone) {
  final digits = phone.replaceAll(RegExp(r'\D'), '');
  final local = digits.length == 11 && digits.startsWith('51') ? digits.substring(2) : digits;
  if (local.length != 9) return phone;
  final grouped = '${local.substring(0, 3)} ${local.substring(3, 6)} ${local.substring(6)}';
  return phone.startsWith('+') ? '+51 $grouped' : grouped;
}

/// Si lo escrito es un celular peruano: nueve dígitos que empiezan por 9.
bool looksLikePhone(String input) => RegExp(r'^9\d{8}$').hasMatch(input.replaceAll(RegExp(r'[\s-]'), ''));


/// La tarjeta de crédito del titular.
class CreditCard {
  final String id;
  final Money limit;

  /// Lo que se debe hoy.
  final Money outstanding;

  /// Lo que todavía se puede usar.
  final Money available;

  /// Tasa efectiva anual del contrato, en puntos básicos (4500 = 45 %).
  final int annualRateBps;
  final int cycleDay;
  final int paymentDays;

  /// `active`, `blocked` (por un pago pendiente) o `closed`.
  final String status;

  /// El último estado de cuenta, si ya hubo un corte.
  final CreditStatement? statement;

  const CreditCard({
    this.statement,
    required this.id,
    required this.limit,
    required this.outstanding,
    required this.available,
    required this.annualRateBps,
    required this.cycleDay,
    required this.paymentDays,
    required this.status,
  });

  bool get blocked => status == 'blocked';

  /// "45 %", "45,5 %".
  String get annualRate {
    final whole = annualRateBps ~/ 100;
    final decimals = annualRateBps % 100;
    if (decimals == 0) return '$whole %';
    return '$whole,${decimals.toString().padLeft(2, '0').replaceAll(RegExp(r'0$'), '')} %';
  }

  factory CreditCard.fromJson(Map<String, dynamic> json) => CreditCard(
        id: json['id'] as String,
        limit: Money.fromJson(json['limit'] as Map<String, dynamic>),
        outstanding: Money.fromJson(json['outstanding'] as Map<String, dynamic>),
        available: Money.fromJson(json['available'] as Map<String, dynamic>),
        annualRateBps: (json['annual_rate_bps'] as num).toInt(),
        cycleDay: (json['cycle_day'] as num).toInt(),
        paymentDays: (json['payment_days'] as num).toInt(),
        status: json['status'] as String,
        statement: json['statement'] == null
            ? null
            : CreditStatement.fromJson(json['statement'] as Map<String, dynamic>),
      );
}

/// Un estado de cuenta de la tarjeta.
class CreditStatement {
  final DateTime periodEnd;
  final Money charges;
  final Money payments;
  final Money interest;

  /// Lo que se debía al corte.
  final Money closing;
  final Money minimumPayment;
  final DateTime dueDate;

  /// Lo pagado desde el corte.
  final Money paidSinceCut;

  /// `paid`, `minimum_paid`, `pending` o `past_due`.
  final String status;

  const CreditStatement({
    required this.periodEnd,
    required this.charges,
    required this.payments,
    required this.interest,
    required this.closing,
    required this.minimumPayment,
    required this.dueDate,
    required this.paidSinceCut,
    required this.status,
  });

  /// Lo que falta para cubrir el mínimo; cero si ya se cubrió.
  Money get minimumLeft => Money(
        (minimumPayment.amountMicros - paidSinceCut.amountMicros).clamp(0, minimumPayment.amountMicros),
        minimumPayment.currency,
      );

  factory CreditStatement.fromJson(Map<String, dynamic> json) => CreditStatement(
        periodEnd: DateTime.parse(json['period_end'] as String),
        charges: Money.fromJson(json['charges'] as Map<String, dynamic>),
        payments: Money.fromJson(json['payments'] as Map<String, dynamic>),
        interest: Money.fromJson(json['interest'] as Map<String, dynamic>),
        closing: Money.fromJson(json['closing'] as Map<String, dynamic>),
        minimumPayment: Money.fromJson(json['minimum_payment'] as Map<String, dynamic>),
        dueDate: DateTime.parse(json['due_date'] as String),
        paidSinceCut: Money.fromJson(json['paid_since_cut'] as Map<String, dynamic>),
        status: json['status'] as String,
      );
}

/// Lo que respondió el banco a una solicitud de tarjeta.
class CreditDecision {
  /// `approved`, `needs_more_history` o `declined`.
  final String outcome;

  /// Qué faltó, en palabras de la persona. Vacío si se aprobó.
  final List<String> reasons;

  /// En el sandbox, la evaluación usó un historial simulado.
  final bool syntheticHistory;

  final CreditCard? card;

  const CreditDecision({required this.outcome, required this.reasons, required this.syntheticHistory, this.card});

  bool get approved => outcome == 'approved';

  factory CreditDecision.fromJson(Map<String, dynamic> json) => CreditDecision(
        outcome: json['outcome'] as String,
        reasons: ((json['reasons'] as List<dynamic>?) ?? const []).cast<String>(),
        syntheticHistory: json['synthetic_history'] as bool? ?? false,
        card: json['card'] == null ? null : CreditCard.fromJson(json['card'] as Map<String, dynamic>),
      );
}
