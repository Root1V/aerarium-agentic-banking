import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../money.dart';

/// La pantalla donde una persona decide si le da a una plataforma permiso para
/// pagar desde una cuenta suya.
///
/// Es la pieza que hace que el mandato pruebe algo. Todo lo demás del modelo B
/// —el permiso, los topes, la revocación— se apoya en que esta decisión la tomó
/// una persona autenticada contra el banco, viendo en la app del banco lo que
/// aprobaba. Si la pantalla la pusiera la plataforma, lo único que tendríamos
/// sería su palabra de que la mostró.
///
/// De ahí tres decisiones de diseño que no son estéticas:
///
/// - **Los topes vienen rellenos con lo que se pidió, pero son editables.** Que
///   se puedan bajar sin fricción es la diferencia entre un consentimiento real
///   y un botón de aceptar.
/// - **No hay botón por defecto.** Aprobar y rechazar pesan lo mismo; poner el
///   foco en aprobar convierte la pantalla en un trámite.
/// - **Lo que se aprueba se dice en dinero, no en micras.** Nadie decide sobre
///   `5000000`.
class ConsentScreen extends StatefulWidget {
  const ConsentScreen({
    super.key,
    required this.client,
    required this.handoffCode,
    required this.accountId,
  });

  final BffClient client;

  /// Lo que la plataforma le entregó a la persona (un enlace, un QR).
  final String handoffCode;

  /// Cuenta desde la que se concedería el permiso.
  final String accountId;

  @override
  State<ConsentScreen> createState() => _ConsentScreenState();
}

class _ConsentScreenState extends State<ConsentScreen> {
  ConsentPrompt? _prompt;
  ApiError? _error;
  bool _loading = true;
  bool _submitting = false;

  final _perOperation = TextEditingController();
  final _total = TextEditingController();
  int _days = 30;
  String? _limitsError;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _perOperation.dispose();
    _total.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final prompt = await widget.client.consentPrompt(widget.handoffCode);
      if (!mounted) return;
      setState(() {
        _prompt = prompt;
        _loading = false;
        // Se precargan los topes pedidos: es el punto de partida razonable, y
        // dejarlos vacíos empujaría a conceder sin límite por comodidad.
        _perOperation.text = _asAmount(prompt.maxPerOperationMicros);
        _total.text = _asAmount(prompt.maxTotalMicros);
      });
    } on ApiError catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e;
        _loading = false;
      });
    }
  }

  static String _asAmount(int? micros) =>
      micros == null ? '' : Money(micros, '').format().trim();

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('Permiso de pago')),
      body: _buildBody(),
    );
  }

  Widget _buildBody() {
    if (_loading) {
      return const Center(child: CircularProgressIndicator());
    }
    if (_error != null) {
      return _Message(
        text: _error!.userMessage,
        onRetry: _error!.isRetryable ? _load : null,
      );
    }

    final prompt = _prompt!;
    if (!prompt.isPending) {
      return _Message(text: _resolvedMessage(prompt.status));
    }

    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        _RequesterCard(prompt: prompt),
        const SizedBox(height: 24),
        Text('Cuánto puede gastar',
            style: Theme.of(context).textTheme.titleMedium),
        const SizedBox(height: 4),
        Text(
          'Puedes bajar estos límites. No puedes subirlos por encima de lo que '
          'te pidieron.',
          style: Theme.of(context).textTheme.bodySmall,
        ),
        const SizedBox(height: 12),
        TextField(
          controller: _perOperation,
          keyboardType: const TextInputType.numberWithOptions(decimal: true),
          decoration: InputDecoration(
            labelText: 'Máximo por pago',
            prefixText: '${prompt.currency} ',
            border: const OutlineInputBorder(),
            helperText: 'Déjalo vacío para no poner un límite por pago',
          ),
        ),
        const SizedBox(height: 12),
        TextField(
          controller: _total,
          keyboardType: const TextInputType.numberWithOptions(decimal: true),
          decoration: InputDecoration(
            labelText: 'Máximo en total',
            prefixText: '${prompt.currency} ',
            border: const OutlineInputBorder(),
            helperText: 'Cuando se agote, tendrán que volver a pedirte permiso',
          ),
        ),
        const SizedBox(height: 20),
        Text('Por cuánto tiempo',
            style: Theme.of(context).textTheme.titleMedium),
        const SizedBox(height: 8),
        SegmentedButton<int>(
          segments: const [
            ButtonSegment(value: 7, label: Text('7 días')),
            ButtonSegment(value: 30, label: Text('30 días')),
            ButtonSegment(value: 90, label: Text('90 días')),
          ],
          selected: {_days},
          onSelectionChanged: (values) => setState(() => _days = values.first),
        ),
        const SizedBox(height: 8),
        Text(
          'Al vencer, el permiso deja de servir solo. Puedes retirarlo antes '
          'cuando quieras.',
          style: Theme.of(context).textTheme.bodySmall,
        ),
        if (_limitsError != null) ...[
          const SizedBox(height: 16),
          Text(
            _limitsError!,
            style: TextStyle(color: Theme.of(context).colorScheme.error),
          ),
        ],
        const SizedBox(height: 28),
        // Los dos botones pesan lo mismo a propósito: destacar el de aprobar
        // convertiría la decisión en un trámite.
        Row(
          children: [
            Expanded(
              child: OutlinedButton(
                onPressed: _submitting ? null : _reject,
                child: const Text('Rechazar'),
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: FilledButton(
                onPressed: _submitting ? null : _approve,
                child: _submitting
                    ? const SizedBox(
                        height: 18,
                        width: 18,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    : const Text('Dar permiso'),
              ),
            ),
          ],
        ),
      ],
    );
  }

  String _resolvedMessage(String status) => switch (status) {
        'APPROVED' => 'Ya diste este permiso.',
        'REJECTED' => 'Rechazaste esta solicitud.',
        'EXPIRED' => 'Esta solicitud venció. Pide una nueva desde la plataforma.',
        _ => 'Esta solicitud ya no está disponible.',
      };

  Future<void> _approve() async {
    final prompt = _prompt!;

    final int? perOperation;
    final int? total;
    try {
      perOperation = _parseLimit(_perOperation.text);
      total = _parseLimit(_total.text);
    } on AmountFormatException catch (e) {
      setState(() => _limitsError = e.message);
      return;
    }

    // Se comprueba también aquí, antes de llamar: el servidor lo rechazaría
    // igual, pero decírselo a la persona en el momento es mucho mejor que
    // devolverle un error del banco por algo que la pantalla ya sabía.
    final excess = _exceedsRequested(prompt, perOperation, total);
    if (excess != null) {
      setState(() => _limitsError = excess);
      return;
    }

    setState(() {
      _submitting = true;
      _limitsError = null;
    });

    try {
      final mandate = await widget.client.approveConsent(
        widget.handoffCode,
        accountId: widget.accountId,
        expiresInDays: _days,
        maxPerOperationMicros: perOperation,
        maxTotalMicros: total,
      );
      if (!mounted) return;
      Navigator.of(context).pop(mandate);
    } on ApiError catch (e) {
      if (!mounted) return;
      setState(() {
        _submitting = false;
        _limitsError = e.userMessage;
      });
    }
  }

  Future<void> _reject() async {
    setState(() => _submitting = true);
    try {
      await widget.client.rejectConsent(widget.handoffCode);
      if (!mounted) return;
      Navigator.of(context).pop(null);
    } on ApiError catch (e) {
      if (!mounted) return;
      setState(() {
        _submitting = false;
        _limitsError = e.userMessage;
      });
    }
  }

  /// Un campo vacío significa "sin tope", no cero.
  static int? _parseLimit(String raw) =>
      raw.trim().isEmpty ? null : parseAmountToMicros(raw);

  /// Devuelve el mensaje si lo concedido supera lo pedido, o `null` si cabe.
  static String? _exceedsRequested(ConsentPrompt prompt, int? perOp, int? total) {
    if (_over(perOp, prompt.maxPerOperationMicros)) {
      return 'No puedes dar más por pago de lo que te pidieron '
          '(${Money(prompt.maxPerOperationMicros!, prompt.currency).format()}).';
    }
    if (_over(total, prompt.maxTotalMicros)) {
      return 'No puedes dar más en total de lo que te pidieron '
          '(${Money(prompt.maxTotalMicros!, prompt.currency).format()}).';
    }
    return null;
  }

  /// Un límite ausente donde se pidió uno acotado también excede: no poner tope
  /// donde te pidieron uno es conceder más, no menos.
  static bool _over(int? granted, int? requested) {
    if (requested == null) return false;
    return granted == null || granted > requested;
  }
}

/// Quién pide y para qué. Va primero porque es lo que decide la respuesta.
class _RequesterCard extends StatelessWidget {
  const _RequesterCard({required this.prompt});

  final ConsentPrompt prompt;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(prompt.requestedBy, style: theme.textTheme.titleLarge),
            const SizedBox(height: 4),
            Text('quiere poder pagar desde tu cuenta',
                style: theme.textTheme.bodyMedium),
            if (prompt.purpose.isNotEmpty) ...[
              const SizedBox(height: 16),
              Text('Para qué', style: theme.textTheme.labelLarge),
              const SizedBox(height: 4),
              Text(prompt.purpose, style: theme.textTheme.bodyMedium),
            ],
            const SizedBox(height: 16),
            Text('Lo que te piden', style: theme.textTheme.labelLarge),
            const SizedBox(height: 4),
            Text(
              _requestedSummary(prompt),
              style: theme.textTheme.bodyMedium,
            ),
          ],
        ),
      ),
    );
  }

  static String _requestedSummary(ConsentPrompt prompt) {
    final parts = <String>[];
    if (prompt.maxPerOperationMicros != null) {
      parts.add(
          'hasta ${Money(prompt.maxPerOperationMicros!, prompt.currency).format()} por pago');
    }
    if (prompt.maxTotalMicros != null) {
      parts.add(
          'hasta ${Money(prompt.maxTotalMicros!, prompt.currency).format()} en total');
    }
    // Sin ningún tope pedido, decirlo con todas las letras. Es la petición más
    // amplia posible y quien decide tiene que verla como tal.
    if (parts.isEmpty) return 'Sin límite de monto';
    return parts.join(', ');
  }
}

class _Message extends StatelessWidget {
  const _Message({required this.text, this.onRetry});

  final String text;
  final VoidCallback? onRetry;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(text, textAlign: TextAlign.center),
            if (onRetry != null) ...[
              const SizedBox(height: 16),
              FilledButton(onPressed: onRetry, child: const Text('Reintentar')),
            ],
          ],
        ),
      ),
    );
  }
}
