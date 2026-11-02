import 'api/models.dart';

/// Las frases del detalle de un movimiento. Aparte de la pantalla para poder
/// probarlas.

const _months = [
  'enero', 'febrero', 'marzo', 'abril', 'mayo', 'junio',
  'julio', 'agosto', 'septiembre', 'octubre', 'noviembre', 'diciembre',
];

String _two(int n) => n.toString().padLeft(2, '0');

/// «1 de noviembre de 2026, 10:15», en la hora del teléfono.
String longDateTime(DateTime at) {
  final l = at.toLocal();
  return '${l.day} de ${_months[l.month - 1]} de ${l.year}, ${_two(l.hour)}:${_two(l.minute)}';
}

/// En qué quedó el pago de una plataforma, en palabras.
String paymentStatusText(PaymentStatus p) => switch (p.status) {
      'held' => 'Retenido: se cobra o vuelve a tu cuenta antes de las '
          '${_two(p.expiresAt.toLocal().hour)}:${_two(p.expiresAt.toLocal().minute)}',
      'captured' => 'Cobrado',
      'partially_refunded' => 'Devuelto en parte: ${p.refunded.format()}',
      'refunded' => 'Devuelto',
      'released' || 'expired' => 'No se cobró: el dinero volvió a tu cuenta',
      _ => 'En revisión',
    };

/// Si un movimiento cobra comisión. Hoy ninguno: se dice, porque lo que más
/// confianza quita es una comisión que aparece después.
bool chargesNoFee(Movement m) => switch (m.kind) {
      'p2p_transfer' || 'pocket_transfer' || 'authorization_hold' || 'rail_outbound_reserve' => true,
      _ => false,
    };

/// La constancia que la persona copia y manda: lo que pide quien recibe el
/// dinero —quién, cuánto, cuándo y el número de operación—.
String receiptText(Movement m) => [
      'Aerarium · Constancia de operación',
      m.label,
      if (m.isPlatformPayment && m.purchase.isNotEmpty) m.purchase,
      'Monto: ${m.signedAmount.format(withSign: true)}',
      'Fecha: ${longDateTime(m.postedAt)}',
      'N.º de operación: ${m.id}',
    ].join('\n');

/// Cómo se autorizó, en una frase y su porqué. Responde «¿esto lo hice yo?».
(String, String) authorizationText(AuthorizationInfo a) {
  final on = a.device.isEmpty ? '' : ' en ${a.device}';
  if (a.actorKind == 'agent') {
    final who = [a.agent.isEmpty ? 'un agente' : a.agent, if (a.platform.isNotEmpty) 'de ${a.platform}'].join(' ');
    final limit = a.mandateMaxPerOperation == null ? '' : ' de hasta ${a.mandateMaxPerOperation!.format()} por pago';
    final until = a.mandateExpiresAt == null
        ? ''
        : ', vigente hasta el ${a.mandateExpiresAt!.toLocal().day}/${a.mandateExpiresAt!.toLocal().month}';
    return (
      'Lo pagó $who${a.signed ? ', con su firma' : ''}',
      'Dentro del permiso que le diste$limit$until. Si no lo reconoces, retíraselo.',
    );
  }
  if (a.outcome == 'confirmed') {
    final how = a.method == 'pin' ? 'con tu clave' : 'con Face ID';
    final why = switch (a.reason) {
      'new_payee' => 'Se pidió porque era la primera vez que le enviabas.',
      'amount' => 'Se pidió porque el monto es alto.',
      'high_impact' => 'Se pidió porque da poder sobre tu dinero.',
      _ => 'Se pidió para proteger tu cuenta.',
    };
    return ('Lo confirmaste $how$on', why);
  }
  return (
    'Lo hiciste tú$on, sin pedir confirmación',
    a.reason == 'known_payee'
        ? 'No hizo falta: ya le habías enviado antes y el monto es bajo.'
        : 'No hizo falta confirmarlo.',
  );
}

/// La constancia para compartir: lo que pide quien recibe el dinero y, sobre
/// todo, cómo comprobar que es real.
String verifiableReceiptText(VerifiableReceipt r) => [
      'Aerarium · Constancia de operación',
      '${r.from} → ${r.to}',
      'Monto: ${r.amount.format()}',
      'Fecha: ${longDateTime(r.postedAt)}',
      'Código de verificación: ${r.code}',
      'Comprueba que es real en: ${r.url}',
    ].join('\n');
