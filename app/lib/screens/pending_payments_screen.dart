import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/models.dart';
import '../theme.dart';
import 'mandates_screen.dart';

/// Los pagos que salieron del saldo pero todavía no se cobraron.
///
/// Responde a la pregunta que trae aquí a alguien que ve su saldo más bajo de lo
/// que esperaba: "¿dónde está mi dinero?". Por eso lo primero es la explicación,
/// y después el desglose por plataforma — agrupado, no una fila por pago: un
/// agente que paga por llamada deja cientos, y nadie las lee una a una.
class PendingPaymentsScreen extends StatelessWidget {
  const PendingPaymentsScreen({
    super.key,
    required this.client,
    required this.groups,
    DateTime Function()? now,
  }) : _now = now ?? DateTime.now;

  final BffClient client;
  final List<PendingGroup> groups;

  /// Reloj inyectable, para que las pruebas no dependan de la hora real.
  final DateTime Function() _now;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final now = _now();

    return Scaffold(
      appBar: AppBar(title: const Text('Pagos en curso')),
      body: ListView(
        padding: const EdgeInsets.all(AppSpacing.lg),
        children: [
          Text(
            'Este dinero ya no está en tu saldo disponible, pero todavía no se '
            'ha cobrado. Cada pago se cobra o vuelve a tu cuenta antes de vencer.',
            style: theme.textTheme.bodyMedium?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: AppSpacing.xl),
          for (final group in groups) ...[
            _GroupCard(group: group, now: now),
            const SizedBox(height: AppSpacing.md),
          ],
          const SizedBox(height: AppSpacing.md),
          // Si lo que la persona ve no le cuadra, lo siguiente que quiere es
          // cortarlo. Mandarla a buscar los permisos por otro lado sería añadir
          // un paso justo cuando hay prisa.
          OutlinedButton.icon(
            icon: const Icon(Icons.key_outlined),
            label: const Text('Gestionar permisos de pago'),
            onPressed: () => Navigator.of(context).push(MaterialPageRoute<void>(
              builder: (_) => MandatesScreen(client: client),
            )),
          ),
        ],
      ),
    );
  }
}

class _GroupCard extends StatelessWidget {
  const _GroupCard({required this.group, required this.now});

  final PendingGroup group;
  final DateTime now;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodyMedium?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Card(
      child: Padding(
        padding: const EdgeInsets.all(AppSpacing.lg),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Expanded(
                  child: Text(
                    group.merchant.isEmpty ? 'Otros pagos en curso' : group.merchant,
                    style: theme.textTheme.titleMedium,
                  ),
                ),
                const SizedBox(width: AppSpacing.md),
                Text(group.amount.format(), style: theme.textTheme.titleMedium?.copyWith(
                  fontFeatures: const [FontFeature.tabularFigures()],
                )),
              ],
            ),
            const SizedBox(height: AppSpacing.xs),
            Text(_paymentCount(group.count), style: muted),
            const SizedBox(height: AppSpacing.sm),
            if (!group.allExpired)
              Text('Se cobran o vuelven ${_deadline(group.lastExpiresAt, now)}.',
                  style: muted),
            if (group.expired.amountMicros > 0)
              Padding(
                padding: const EdgeInsets.only(top: AppSpacing.xs),
                // Lo vencido ya no se puede cobrar. Decirlo evita que la
                // persona crea que todavía puede salir ese dinero.
                child: Text(
                  group.allExpired
                      ? 'Ya vencieron: vuelven a tu saldo en breve.'
                      : '${group.expired.format()} ya vencieron y vuelven a tu '
                          'saldo en breve.',
                  style: muted,
                ),
              ),
          ],
        ),
      ),
    );
  }
}

String _paymentCount(int count) => count == 1 ? '1 pago' : '$count pagos';

/// "antes de las 14:05" si es hoy; con la fecha si no lo es.
///
/// La hora se muestra en la zona del teléfono: es la que la persona lee en su
/// reloj, y una hora en UTC sería técnicamente correcta e inútil.
String _deadline(DateTime at, DateTime now) {
  final local = at.toLocal();
  final today = now.toLocal();
  String two(int n) => n.toString().padLeft(2, '0');
  final hour = '${two(local.hour)}:${two(local.minute)}';
  final sameDay = local.year == today.year &&
      local.month == today.month &&
      local.day == today.day;
  return sameDay
      ? 'antes de las $hour'
      : 'antes del ${two(local.day)}/${two(local.month)} a las $hour';
}
