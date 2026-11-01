import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../auth/device_keys.dart';
import '../money.dart';
import 'consent_screen.dart';

/// Los permisos que la persona tiene otorgados, y el sitio donde los retira.
///
/// Sin esta pantalla, revocar sería imposible en la práctica: no se puede
/// retirar un permiso que no se puede ver. Un permiso que solo la plataforma
/// conoce no es un permiso, es un acceso.
///
/// Muestra los vencidos y revocados además de los vivos. Es deliberado: la
/// pregunta que trae aquí a alguien suele ser "¿a quién le di acceso?", y esa
/// respuesta incluye el pasado.
class MandatesScreen extends StatefulWidget {
  const MandatesScreen({super.key, required this.client, this.consent, this.offerSimulation = false});

  final BffClient client;

  /// Lo necesario para conceder un permiso desde aquí: abrir la solicitud de una
  /// plataforma con su código. Nulo si todavía no se sabe desde qué cuenta.
  final ConsentContext? consent;

  /// En el sandbox, ofrecer simular que una plataforma pide permiso.
  final bool offerSimulation;

  @override
  State<MandatesScreen> createState() => _MandatesScreenState();
}

/// Lo necesario para conceder un permiso: desde qué cuenta y con qué se firma
/// la confirmación.
class ConsentContext {
  const ConsentContext({required this.accountId, required this.currency, this.confirmer});

  final String accountId;
  final String currency;
  final TransferConfirmer? confirmer;
}

class _MandatesScreenState extends State<MandatesScreen> {
  List<Mandate>? _mandates;
  ApiError? _error;
  bool _loading = true;
  String? _revoking;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final mandates = await widget.client.mandates();
      if (!mounted) return;
      setState(() {
        _mandates = mandates;
        _loading = false;
      });
    } on ApiError catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e;
        _loading = false;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final consent = widget.consent;
    return Scaffold(
      appBar: AppBar(
        title: const Text('Permisos de pago'),
        actions: [
          if (consent != null)
            IconButton(
              icon: const Icon(Icons.input),
              tooltip: 'Abrir una solicitud',
              onPressed: _requesting ? null : () => _openByCode(consent),
            ),
        ],
      ),
      body: RefreshIndicator(onRefresh: _load, child: _buildBody()),
      floatingActionButton: consent == null || !widget.offerSimulation
          ? null
          : FloatingActionButton.extended(
              onPressed: _requesting ? null : () => _simulateRequest(consent),
              icon: const Icon(Icons.science_outlined),
              label: const Text('Simular una solicitud'),
            ),
    );
  }

  bool _requesting = false;

  /// La plataforma de prueba pide permiso y se abre la pantalla donde se decide,
  /// como si la persona hubiera llegado desde el enlace de la plataforma.
  Future<void> _simulateRequest(ConsentContext consent) async {
    setState(() => _requesting = true);
    try {
      final handoff = await widget.client.sandboxConsentRequest(consent.currency);
      if (!mounted) return;
      await _decide(consent, handoff);
    } on ApiError catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.userMessage)));
    } finally {
      if (mounted) setState(() => _requesting = false);
    }
  }

  /// Abre la solicitud que una plataforma entregó con su código. Si el código
  /// está en el portapapeles —copiado en otro dispositivo, por ejemplo—, se
  /// propone ya pegado.
  Future<void> _openByCode(ConsentContext consent) async {
    String? pasted;
    try {
      pasted = (await Clipboard.getData(Clipboard.kTextPlain))?.text?.trim();
    } on PlatformException {
      pasted = null;
    }
    if (!mounted) return;
    final code = await showDialog<String>(
      context: context,
      builder: (_) => _HandoffCodeDialog(initial: isHandoffCode(pasted) ? pasted! : ''),
    );
    if (code == null || !mounted) return;
    setState(() => _requesting = true);
    try {
      await _decide(consent, code);
    } finally {
      if (mounted) setState(() => _requesting = false);
    }
  }

  Future<void> _decide(ConsentContext consent, String handoff) async {
    try {
      await Navigator.of(context).push(MaterialPageRoute<Mandate?>(
        builder: (_) => ConsentScreen(
          client: widget.client,
          handoffCode: handoff,
          accountId: consent.accountId,
          confirmer: consent.confirmer,
        ),
      ));
      if (!mounted) return;
      await _load();
    } on ApiError catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.userMessage)));
    } finally {
      if (mounted) setState(() => _requesting = false);
    }
  }

  Widget _buildBody() {
    if (_loading) {
      return const Center(child: CircularProgressIndicator());
    }
    if (_error != null) {
      return _centered(
        Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(_error!.userMessage, textAlign: TextAlign.center),
            if (_error!.isRetryable) ...[
              const SizedBox(height: 16),
              FilledButton(onPressed: _load, child: const Text('Reintentar')),
            ],
          ],
        ),
      );
    }

    final mandates = _mandates ?? const <Mandate>[];
    if (mandates.isEmpty) {
      return _centered(
        const Text(
          'No le diste permiso de pago a ninguna plataforma.',
          textAlign: TextAlign.center,
        ),
      );
    }

    return ListView.separated(
      padding: const EdgeInsets.all(16),
      itemCount: mandates.length,
      separatorBuilder: (_, __) => const SizedBox(height: 12),
      itemBuilder: (context, i) => _MandateCard(
        mandate: mandates[i],
        revoking: _revoking == mandates[i].id,
        onRevoke: () => _confirmRevoke(mandates[i]),
      ),
    );
  }

  Widget _centered(Widget child) => ListView(
        // ListView y no Center para que RefreshIndicator siga funcionando con la
        // lista vacía: si no, no hay nada que arrastrar para recargar.
        physics: const AlwaysScrollableScrollPhysics(),
        children: [
          SizedBox(
            height: MediaQuery.of(context).size.height * 0.6,
            child: Center(
              child: Padding(padding: const EdgeInsets.all(24), child: child),
            ),
          ),
        ],
      );

  Future<void> _confirmRevoke(Mandate mandate) async {
    // Se confirma porque es irreversible: un permiso retirado no se puede
    // reactivar, hay que volver a otorgarlo desde la plataforma.
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('¿Retirar el permiso?'),
        content: Text(
          '${mandate.holderName} no podrá iniciar pagos nuevos desde tu cuenta.\n\n'
          'Los pagos que ya estén en curso pueden completarse; para volver a '
          'darle permiso tendrán que pedírtelo otra vez.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: const Text('Cancelar'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(true),
            child: const Text('Retirar'),
          ),
        ],
      ),
    );
    if (confirmed != true) return;

    setState(() => _revoking = mandate.id);
    try {
      await widget.client.revokeMandate(mandate.id);
      if (!mounted) return;
      setState(() => _revoking = null);
      await _load();
    } on ApiError catch (e) {
      if (!mounted) return;
      setState(() => _revoking = null);
      ScaffoldMessenger.of(context)
          .showSnackBar(SnackBar(content: Text(e.userMessage)));
    }
  }
}

class _MandateCard extends StatelessWidget {
  const _MandateCard({
    required this.mandate,
    required this.revoking,
    required this.onRevoke,
  });

  final Mandate mandate;
  final bool revoking;
  final VoidCallback onRevoke;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(mandate.holderName, style: theme.textTheme.titleMedium),
                ),
                _StatusChip(status: mandate.status),
              ],
            ),
            const SizedBox(height: 12),
            // Lo gastado va antes que los topes: es la pregunta que trae aquí a
            // alguien que sospecha de un cobro. Y primero lo de hoy, que es lo
            // que la persona puede contrastar con lo que hizo su agente.
            if (mandate.spend case final spend?) ...[
              _Row(label: 'Hoy', value: _spent(spend.today, spend.todayCount)),
              _Row(label: 'Este mes', value: _spent(spend.month, spend.monthCount)),
              if (spend.inFlight.amountMicros > 0)
                _Row(label: 'En curso', value: spend.inFlight.format()),
            ],
            _Row(
              label: 'Gastado en total',
              value: Money(mandate.consumedMicros, mandate.currency).format(),
            ),
            if (mandate.remainingMicros != null)
              _Row(
                label: 'Disponible',
                value: Money(mandate.remainingMicros!, mandate.currency).format(),
              ),
            if (mandate.maxPerOperationMicros != null)
              _Row(
                label: 'Máximo por pago',
                value:
                    Money(mandate.maxPerOperationMicros!, mandate.currency).format(),
              ),
            _Row(
              label: mandate.isActive ? 'Vence' : 'Venció',
              value: _formatDate(mandate.expiresAt),
            ),
            if (mandate.revokedAt != null)
              _Row(label: 'Retirado el', value: _formatDate(mandate.revokedAt!)),
            if (mandate.canRevoke) ...[
              const SizedBox(height: 12),
              Align(
                alignment: Alignment.centerRight,
                child: TextButton(
                  onPressed: revoking ? null : onRevoke,
                  child: revoking
                      ? const SizedBox(
                          height: 16,
                          width: 16,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Text('Retirar permiso'),
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }

  /// "0,42 USD · 380 pagos". El número de pagos importa tanto como el importe:
  /// 0,42 USD en un pago y en 380 son dos historias distintas.
  static String _spent(Money amount, int count) {
    if (count == 0) return amount.format();
    return '${amount.format()} · ${count == 1 ? '1 pago' : '$count pagos'}';
  }

  /// Fecha en formato local sin depender de `intl`: la app no tiene esa
  /// dependencia y una fecha corta no la justifica.
  static String _formatDate(DateTime date) {
    final local = date.toLocal();
    final d = local.day.toString().padLeft(2, '0');
    final m = local.month.toString().padLeft(2, '0');
    return '$d/$m/${local.year}';
  }
}

class _StatusChip extends StatelessWidget {
  const _StatusChip({required this.status});

  final String status;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final (label, background, foreground) = switch (status) {
      'active' => ('Activo', scheme.primaryContainer, scheme.onPrimaryContainer),
      'revoked' => ('Retirado', scheme.surfaceContainerHighest, scheme.onSurfaceVariant),
      'expired' => ('Vencido', scheme.surfaceContainerHighest, scheme.onSurfaceVariant),
      _ => (status, scheme.surfaceContainerHighest, scheme.onSurfaceVariant),
    };
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Text(label, style: TextStyle(color: foreground, fontSize: 12)),
    );
  }
}

class _Row extends StatelessWidget {
  const _Row({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          Text(label, style: theme.textTheme.bodySmall),
          Text(value, style: theme.textTheme.bodyMedium),
        ],
      ),
    );
  }
}

/// Si un texto parece el código de una solicitud de permiso.
bool isHandoffCode(String? text) =>
    text != null && RegExp(r'^csr_[A-Za-z0-9_-]{20,}$').hasMatch(text.trim());

class _HandoffCodeDialog extends StatefulWidget {
  const _HandoffCodeDialog({required this.initial});

  final String initial;

  @override
  State<_HandoffCodeDialog> createState() => _HandoffCodeDialogState();
}

class _HandoffCodeDialogState extends State<_HandoffCodeDialog> {
  late final _controller = TextEditingController(text: widget.initial);

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: const Text('Abrir una solicitud'),
      content: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Text('Pega el código que te dio la plataforma. Empieza con «csr_».'),
          const SizedBox(height: 12),
          TextField(
            controller: _controller,
            autofocus: widget.initial.isEmpty,
            autocorrect: false,
            decoration: const InputDecoration(labelText: 'Código de la solicitud'),
            onChanged: (_) => setState(() {}),
          ),
        ],
      ),
      actions: [
        TextButton(onPressed: () => Navigator.of(context).pop(), child: const Text('Cancelar')),
        FilledButton(
          onPressed: isHandoffCode(_controller.text)
              ? () => Navigator.of(context).pop(_controller.text.trim())
              : null,
          child: const Text('Abrir'),
        ),
      ],
    );
  }
}
