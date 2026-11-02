import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../movement_text.dart';
import '../theme.dart';

/// El detalle de un movimiento: qué fue, con quién, cuándo, en qué quedó y su
/// número de operación, más lo que se puede hacer con él.
///
/// Es lo que muestran los neobancos al tocar un movimiento, y lo que se espera
/// en el Perú: la constancia con el número de operación para mandársela a quien
/// pagaste o te pagó.
class MovementDetailScreen extends StatefulWidget {
  const MovementDetailScreen({
    super.key,
    required this.movement,
    required this.client,
    required this.accountId,
    this.onSendAgain,
    this.onOpenMandates,
    this.onOpenPockets,
  });

  final Movement movement;
  final BffClient client;
  final String accountId;

  /// Enviar otra vez a la misma persona. Nulo si no aplica.
  final void Function(Account payee)? onSendAgain;

  /// Ver el permiso de la plataforma que cobró.
  final VoidCallback? onOpenMandates;
  final VoidCallback? onOpenPockets;

  @override
  State<MovementDetailScreen> createState() => _MovementDetailScreenState();
}

class _MovementDetailScreenState extends State<MovementDetailScreen> {
  MovementDetail? _detail;
  bool _loaded = false;
  bool _issuing = false;

  Movement get m => widget.movement;

  /// Los movimientos que llevan constancia verificable: lo que se paga a alguien.
  bool get _receiptable => m.kind == 'p2p_transfer' || m.kind == 'authorization_hold';

  @override
  void initState() {
    super.initState();
    _loadDetail();
  }

  /// Lo que el banco sabe además del historial. Si no se puede saber, el detalle
  /// se muestra igual, sin eso: callar es mejor que afirmar algo que no se sabe.
  Future<void> _loadDetail() async {
    try {
      final d = await widget.client.movementDetail(widget.accountId, m.id);
      if (mounted) {
        setState(() {
          _detail = d;
          _loaded = true;
        });
      }
    } catch (_) {
      if (mounted) setState(() => _loaded = true);
    }
  }

  /// Emite la constancia verificable y la copia, lista para pegar en un chat.
  Future<void> _shareReceipt() async {
    setState(() => _issuing = true);
    try {
      final receipt = await widget.client.issueReceipt(widget.accountId, m.id);
      await _copy(verifiableReceiptText(receipt),
          'Constancia copiada con su código: quien la reciba puede comprobar que es real');
    } on ApiError catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.userMessage)));
    } finally {
      if (mounted) setState(() => _issuing = false);
    }
  }

  Future<void> _copy(String text, String done) async {
    await Clipboard.setData(ClipboardData(text: text));
    if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(done)));
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final incoming = m.sign > 0;
    final payment = _detail?.payment;
    final status = switch (m.kind) {
      'authorization_hold' => payment == null ? (_loaded ? null : '…') : paymentStatusText(payment),
      'authorization_release' => 'Devuelto a tu cuenta',
      _ => 'Completado',
    };
    final counterparty = m.counterparty;

    return Scaffold(
      appBar: AppBar(title: const Text('Detalle')),
      body: ListView(
        padding: const EdgeInsets.fromLTRB(16, 8, 16, 32),
        children: [
          Center(
            child: Column(
              children: [
                CircleAvatar(
                  radius: 28,
                  child: Icon(
                    m.isPocketMove
                        ? Icons.savings_outlined
                        : m.isPlatformPayment
                            ? Icons.smart_toy_outlined
                            : incoming
                                ? Icons.south_west
                                : Icons.north_east,
                    size: 28,
                  ),
                ),
                const SizedBox(height: AppSpacing.md),
                Text(m.label, style: theme.textTheme.titleMedium, textAlign: TextAlign.center),
                const SizedBox(height: AppSpacing.xs),
                Text(
                  m.signedAmount.format(withSign: true),
                  key: const Key('detail-amount'),
                  style: theme.textTheme.headlineMedium?.copyWith(
                    color: incoming ? context.moneyColors.positive : null,
                    fontFeatures: const [FontFeature.tabularFigures()],
                  ),
                ),
                if (status != null) ...[
                  const SizedBox(height: AppSpacing.sm),
                  Chip(label: Text(status), visualDensity: VisualDensity.compact),
                ],
              ],
            ),
          ),
          const SizedBox(height: AppSpacing.lg),
          Card(
            child: Column(
              children: [
                _Line('Tipo', m.isPlatformPayment ? 'Pago con permiso' : m.kindName),
                if (counterparty != null) _Line(incoming ? 'De' : 'Para', counterparty.name),
                if (m.isPlatformPayment) ...[
                  _Line('Plataforma', m.merchant),
                  if (m.agentName.isNotEmpty) _Line('Agente', m.agentName),
                  if (m.purchase.isNotEmpty) _Line('Qué compró', m.purchase),
                ],
                if (m.isPocketMove) _Line('Bolsillo', m.description),
                if (!m.isPocketMove &&
                    m.description.isNotEmpty &&
                    m.description.toLowerCase() != m.label.toLowerCase())
                  _Line('Concepto', m.description),
                _Line('Fecha', longDateTime(m.postedAt)),
                if (chargesNoFee(m)) const _Line('Comisión', 'Sin comisión'),
                _Line(
                  'N.º de operación',
                  m.id,
                  trailing: IconButton(
                    tooltip: 'Copiar el número de operación',
                    icon: const Icon(Icons.copy, size: 18),
                    onPressed: () => _copy(m.id, 'Número de operación copiado'),
                  ),
                ),
              ],
            ),
          ),
          if (_detail?.authorization case final a?) _AuthorizationCard(info: a),
          if (_detail?.task case final task?) _TaskCard(task: task),
          const SizedBox(height: AppSpacing.lg),
          if (_receiptable)
            FilledButton.tonalIcon(
              onPressed: _issuing ? null : _shareReceipt,
              icon: const Icon(Icons.verified_outlined),
              label: const Text('Compartir constancia verificable'),
            )
          else
            FilledButton.tonalIcon(
              onPressed: () => _copy(receiptText(m), 'Constancia copiada: pégala donde quieras enviarla'),
              icon: const Icon(Icons.receipt_long_outlined),
              label: const Text('Copiar constancia'),
            ),
          if (widget.onSendAgain != null && counterparty != null && !incoming && m.kind == 'p2p_transfer') ...[
            const SizedBox(height: AppSpacing.sm),
            OutlinedButton.icon(
              onPressed: () => widget.onSendAgain!(counterparty),
              icon: const Icon(Icons.replay),
              label: Text('Enviar de nuevo a ${counterparty.name}'),
            ),
          ],
          if (m.isPlatformPayment && widget.onOpenMandates != null) ...[
            const SizedBox(height: AppSpacing.sm),
            OutlinedButton.icon(
              onPressed: widget.onOpenMandates,
              icon: const Icon(Icons.verified_user_outlined),
              label: Text('Ver el permiso de ${m.merchant}'),
            ),
          ],
          if (m.isPocketMove && widget.onOpenPockets != null) ...[
            const SizedBox(height: AppSpacing.sm),
            OutlinedButton.icon(
              onPressed: widget.onOpenPockets,
              icon: const Icon(Icons.savings_outlined),
              label: const Text('Ver mis bolsillos'),
            ),
          ],
        ],
      ),
    );
  }
}

/// Cómo se autorizó: lo que responde «¿esto lo hice yo?».
class _AuthorizationCard extends StatelessWidget {
  const _AuthorizationCard({required this.info});

  final AuthorizationInfo info;

  @override
  Widget build(BuildContext context) {
    final (title, why) = authorizationText(info);
    final icon = info.actorKind == 'agent'
        ? Icons.smart_toy_outlined
        : info.outcome == 'confirmed'
            ? (info.method == 'pin' ? Icons.pin_outlined : Icons.face_outlined)
            : Icons.check_circle_outline;
    return Padding(
      padding: const EdgeInsets.only(top: AppSpacing.lg),
      child: Card(
        child: ListTile(
          contentPadding: const EdgeInsets.fromLTRB(16, 8, 16, 8),
          leading: Icon(icon, color: Theme.of(context).colorScheme.primary),
          title: Text(title),
          subtitle: Text(why),
        ),
      ),
    );
  }
}

/// Los pagos de la misma tarea del agente: un micropago suelto no dice nada; la
/// tarea entera, sí.
class _TaskCard extends StatelessWidget {
  const _TaskCard({required this.task});

  final AgentTask task;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(top: AppSpacing.lg),
      child: Card(
        child: Padding(
          padding: const EdgeInsets.fromLTRB(16, 12, 16, 8),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text('Parte de una tarea del agente', style: theme.textTheme.titleSmall),
              const SizedBox(height: 2),
              Text('${task.count} pagos · ${task.total.format()} en total', style: theme.textTheme.bodySmall),
              const SizedBox(height: 8),
              for (final p in task.payments)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 4),
                  child: Row(
                    children: [
                      Icon(p.isThis ? Icons.arrow_right : Icons.circle, size: p.isThis ? 18 : 6),
                      const SizedBox(width: 8),
                      Expanded(
                        child: Text(p.description.isEmpty ? 'Pago' : p.description,
                            style: p.isThis ? const TextStyle(fontWeight: FontWeight.w600) : null),
                      ),
                      Text(p.amount.format()),
                    ],
                  ),
                ),
            ],
          ),
        ),
      ),
    );
  }
}

class _Line extends StatelessWidget {
  const _Line(this.label, this.value, {this.trailing});

  final String label;
  final String value;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: EdgeInsets.fromLTRB(16, 12, trailing == null ? 16 : 4, 12),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 120,
            child: Text(label, style: theme.textTheme.bodyMedium?.copyWith(color: theme.colorScheme.onSurfaceVariant)),
          ),
          Expanded(child: SelectableText(value, style: theme.textTheme.bodyMedium)),
          if (trailing != null) trailing!,
        ],
      ),
    );
  }
}
