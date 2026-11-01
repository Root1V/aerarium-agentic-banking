import 'package:flutter/material.dart';

import '../history.dart';

/// Los pagos de un grupo del historial, uno por uno: qué compró el agente en
/// cada uno, cuándo y por cuánto.
///
/// El grupo responde «cuánto lleva»; esta pantalla responde «¿en qué?». Lo que
/// se lee es lo que el agente declaró al pagar (AE-10), tal cual. Y desde aquí
/// se llega al permiso, por si lo que se ve no cuadra.
class PaymentGroupScreen extends StatelessWidget {
  const PaymentGroupScreen({super.key, required this.group, required this.onOpenMandates});

  final PaymentGroup group;
  final VoidCallback onOpenMandates;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final agent = group.agentName;
    // Si los pagos son de varios agentes, cada fila dice el suyo.
    final mixedAgents = agent.isEmpty && group.movements.any((m) => m.agentName.isNotEmpty);
    final nothingDeclared = group.movements.every((m) => m.purchase.isEmpty);

    return Scaffold(
      appBar: AppBar(title: Text('Pagos a ${group.merchant}')),
      body: ListView(
        padding: const EdgeInsets.fromLTRB(16, 8, 16, 24),
        children: [
          Text(group.total.format(withSign: true),
              style: theme.textTheme.headlineMedium?.copyWith(
                fontWeight: FontWeight.w600,
                fontFeatures: const [FontFeature.tabularFigures()],
              )),
          const SizedBox(height: 4),
          Text(
            [
              group.continuesBeyond ? 'Últimos ${group.count} pagos' : '${group.count} pagos',
              if (agent.isNotEmpty) agent,
            ].join(' · '),
            style: theme.textTheme.bodyMedium,
          ),
          if (group.continuesBeyond) ...[
            const SizedBox(height: 4),
            Text('Hay pagos más antiguos que todavía no se cargaron.', style: theme.textTheme.bodySmall),
          ],
          if (nothingDeclared) ...[
            const SizedBox(height: 16),
            Text(
              'Estos pagos son de antes de que los agentes dijeran qué compraban en cada uno.',
              style: theme.textTheme.bodyMedium,
            ),
          ],
          const SizedBox(height: 8),
          for (final m in group.movements)
            ListTile(
              contentPadding: EdgeInsets.zero,
              title: Text(m.purchase.isEmpty ? 'Pago autorizado' : m.purchase),
              subtitle: Text([
                if (mixedAgents && m.agentName.isNotEmpty) m.agentName,
                _when(m.postedAt),
              ].join(' · ')),
              trailing: Text(
                m.signedAmount.format(withSign: true),
                style: const TextStyle(
                  fontWeight: FontWeight.w600,
                  fontFeatures: [FontFeature.tabularFigures()],
                ),
              ),
            ),
          const SizedBox(height: 16),
          OutlinedButton.icon(
            onPressed: onOpenMandates,
            icon: const Icon(Icons.verified_user_outlined),
            label: Text('Ver el permiso de ${group.merchant}'),
          ),
        ],
      ),
    );
  }

  static String _when(DateTime at) {
    String two(int n) => n.toString().padLeft(2, '0');
    final local = at.toLocal();
    return '${two(local.day)}/${two(local.month)} a las ${two(local.hour)}:${two(local.minute)}';
  }
}
