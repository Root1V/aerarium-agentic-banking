import 'api/models.dart';
import 'forecast_text.dart';

/// Las frases de las metas. Aparte de las pantallas para poder probarlas: «no
/// apartó para que no te quedes corto antes de tu ingreso del 30» tiene que
/// decir exactamente eso.

/// «cada semana», «cada mes».
String cadenceText(String cadence) => cadence == 'weekly' ? 'cada semana' : 'cada mes';

/// «por semana», «al mes».
String perCadence(String cadence) => cadence == 'weekly' ? 'por semana' : 'al mes';

/// La regla, en una línea: «S/ 30,00 cada semana».
String ruleText(GoalRule r) => '${r.amount.format()} ${cadenceText(r.cadence)}';

/// Si la meta llega, y qué haría falta. Nulo si no hay nada que decir.
String? paceText(Goal g) {
  if (g.reached) return '¡Lo lograste! Juntaste ${g.saved.format()}.';
  final pace = g.pace;
  final suggested = pace.suggested;
  final date = g.targetDate;
  if (g.rule != null && pace.reachOn != null) {
    final reach = 'A este ritmo la completas ${onDay(pace.reachOn!)}.';
    if (pace.onTrack == false && date != null && suggested != null) {
      return '$reach No llegas para ${onDay(date)}: con ${suggested.format()} ${perCadence(pace.suggestedCadence)}, sí.';
    }
    return reach;
  }
  if (date != null && suggested != null) {
    return 'Para llegar ${onDay(date)}, aparta ${suggested.format()} ${perCadence(pace.suggestedCadence)}.';
  }
  return null;
}

/// Lo que hizo la regla un día, y por qué: (título, detalle).
(String, String) contributionText(GoalContribution c) {
  final day = onDay(c.runOn);
  // «antes de tu ingreso del 30 de noviembre», como en la previsión.
  final income =
      c.incomeOn == null ? 'en los próximos días' : 'antes de tu ingreso del ${onDay(c.incomeOn!).substring(3)}';
  final lowest = c.lowest == null ? '' : ' Tu saldo iba a bajar hasta ${c.lowest!.format()}.';
  return switch ((c.outcome, c.reason)) {
    ('saved', _) => ('Apartó ${c.amount.format()}', day),
    ('reduced', _) => (
        'Apartó ${c.amount.format()} de ${c.asked.format()}',
        '$day. Apartó menos para que no te quedes corto $income.$lowest',
      ),
    ('skipped', 'keeps_cushion') => (
        'No apartó',
        '$day. Te habría dejado corto $income.$lowest',
      ),
    ('skipped', 'insufficient_funds') => ('No apartó', '$day. No tenías saldo.'),
    ('skipped', _) => ('No apartó', '$day. La regla no tenía permiso.'),
    ('reached', _) => ('Meta cumplida', '$day. Ya no aparta más.'),
    _ => (c.outcome, day),
  };
}
