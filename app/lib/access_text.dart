import 'api/models.dart';

/// Las frases del centro de permisos. Aparte de la pantalla para poder probarlas.

/// Qué pasó, en palabras: el registro de decisiones dice `mandate_payment` /
/// `deny` / `over_limit`; la persona lee «Pago negado: superaba el tope».
String activityText(ActivityEntry e) => switch ((e.action, e.outcome)) {
      ('transfer', 'allow') => 'Envió dinero',
      ('transfer', 'confirm') => 'Un envío pidió confirmación',
      ('transfer', 'confirmed') => 'Confirmó un envío',
      ('transfer', 'rejected') => 'No se pudo confirmar un envío',
      ('transfer', 'deny') => 'Envío negado',
      ('mandate_payment', 'allow') => 'Pagó con su permiso',
      ('mandate_payment', 'deny') => switch (e.reason) {
          'over_limit' => 'Pago negado: superaba el tope',
          'grant_inactive' => 'Pago negado: el permiso ya no estaba vigente',
          _ => 'Pago negado',
        },
      ('grant_permission', 'confirm') => 'Se pidió confirmar un permiso de pago',
      ('grant_permission', 'confirmed') => 'Dio un permiso de pago',
      ('grant_permission', 'rejected') => 'No se pudo confirmar un permiso de pago',
      _ => '${e.action} · ${e.outcome}',
    };

/// Si la decisión fue un «no»: se marca para que se vea.
bool activityDenied(ActivityEntry e) => e.outcome == 'deny' || e.outcome == 'rejected';

String shortDate(DateTime at) {
  String two(int n) => n.toString().padLeft(2, '0');
  final l = at.toLocal();
  return '${two(l.day)}/${two(l.month)} ${two(l.hour)}:${two(l.minute)}';
}
