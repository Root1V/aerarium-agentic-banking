import 'api/models.dart';
import 'money.dart';

/// Las frases de la previsión de saldo. Aparte de las pantallas para poder
/// probarlas: «tu tarjeta vence el 16 y te dejaría con −40» tiene que decir
/// exactamente eso.

const _months = [
  'enero', 'febrero', 'marzo', 'abril', 'mayo', 'junio',
  'julio', 'agosto', 'septiembre', 'octubre', 'noviembre', 'diciembre',
];

/// «el 16 de noviembre».
String onDay(DateTime d) => 'el ${d.day} de ${_months[d.month - 1]}';

/// Hasta dónde se mira, en palabras: «antes de tu ingreso del 30 de noviembre».
String _beforeIncome(Forecast f) => f.income == null
    ? 'en los próximos 30 días'
    : 'antes de tu ingreso del ${f.income!.expectedOn.day} de ${_months[f.income!.expectedOn.month - 1]}';

/// La frase principal.
String headline(Forecast f) {
  final card = f.card;
  final after = f.afterCard;
  switch (f.status) {
    case 'deficit' when f.cause == 'card' && card != null && after != null:
      final when = card.overdue ? 'venció' : 'vence ${onDay(card.dueOn)}';
      return 'Tu tarjeta $when y te dejaría con ${after.amount.format()} ${_beforeIncome(f)}.';
    case 'deficit':
      return 'A este ritmo de gasto, ${onDay(f.lowest.on)} te quedarías con '
          '${f.lowest.amount.format()}, ${_beforeIncome(f)}.';
    case 'tight':
      final reach = f.income == null ? 'Los próximos 30 días' : 'Llegas a tu ingreso';
      return '$reach, pero justo: ${onDay(f.lowest.on)} te quedarían ${f.lowest.amount.format()}.';
    default:
      return f.income == null
          ? 'En los próximos 30 días no bajarías de ${f.lowest.amount.format()}.'
          : 'Llegas a tu ingreso ${onDay(f.income!.expectedOn)} con unos ${f.lowest.amount.format()}.';
  }
}

/// Cada opción, en palabras. Ninguna se ejecuta sola.
String optionText(Forecast f, ForecastOption o) {
  switch (o.kind) {
    case 'pay_minimum':
      final rest = o.rest ?? Money(0, o.amount.currency);
      final later = f.income == null ? 'más adelante' : 'cuando cobres';
      return 'Paga el mínimo (${o.amount.format()}) ${onDay(f.card!.dueOn)} y el resto '
          '(${rest.format()}) $later. Lo que no pagues genera intereses.';
    case 'spend_cap':
      return 'Si gastas hasta ${o.amount.format()} por día (hoy gastas unos '
          '${f.dailySpend.format()}), llegas sin quedar en negativo.';
    case 'add_funds' when f.card != null:
      return 'Te faltarían ${o.amount.format()} para pagar el mínimo de la tarjeta. '
          'Si no lo pagas, la tarjeta se bloquea por mora.';
    case 'add_funds':
      return 'Te faltarían ${o.amount.format()} para llegar sin quedar en negativo.';
    default:
      return '';
  }
}
