import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../auth/device_keys.dart';
import '../money.dart';
import '../theme.dart';
import '../transfer_controller.dart';
import '../widgets/pin_pad.dart';

/// Pantalla de envío de dinero.
///
/// El botón de reintentar solo aparece cuando reintentar puede cambiar algo (un
/// fallo de red o de servicio). Ofrecerlo ante saldo insuficiente invitaría a
/// insistir contra una pared.
class TransferScreen extends StatefulWidget {
  const TransferScreen({
    super.key,
    required this.client,
    required this.fromAccountId,
    required this.currency,
    this.confirmer,
    this.suggestedPayee,
  });

  final BffClient client;
  final String fromAccountId;
  final String currency;

  /// Firma con el teléfono las operaciones de riesgo.
  final TransferConfirmer? confirmer;

  /// Un destinatario más para la lista —en el sandbox, el de prueba recién
  /// creado— aunque todavía no se le haya enviado nada.
  final Account? suggestedPayee;

  @override
  State<TransferScreen> createState() => _TransferScreenState();
}

class _TransferScreenState extends State<TransferScreen> {
  late final TransferController _controller =
      TransferController(widget.client, confirmer: widget.confirmer);

  final _destinationField = TextEditingController();
  final _amountField = TextEditingController();
  final _descriptionField = TextEditingController();

  String? _amountError;

  /// A quién se le envía, elegido de los recientes. Mientras sea null, se
  /// escribe la cuenta a mano.
  Account? _payee;
  List<Account> _recent = const [];

  @override
  void initState() {
    super.initState();
    _controller.addListener(_onControllerChanged);
    if (widget.suggestedPayee case final payee?) _recent = [payee];
    _loadRecent();
  }

  /// Si la lista no carga, se escribe la cuenta a mano: no es motivo para no
  /// poder enviar.
  Future<void> _loadRecent() async {
    try {
      final recent = await widget.client.recentPayees();
      if (!mounted) return;
      final suggested = widget.suggestedPayee;
      setState(() => _recent = [
            if (suggested != null) suggested,
            ...recent.where((a) => a.id != suggested?.id),
          ]);
    } on ApiError {
      // Se queda con lo que había.
    }
  }

  @override
  void dispose() {
    _controller.removeListener(_onControllerChanged);
    _controller.dispose();
    _destinationField.dispose();
    _amountField.dispose();
    _descriptionField.dispose();
    super.dispose();
  }

  void _onControllerChanged() {
    if (!mounted) return;
    setState(() {});
    if (_controller.status == TransferStatus.success) {
      Navigator.of(context).pop(true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final sending = _controller.status == TransferStatus.sending;
    final error = _controller.error;

    if (_controller.status == TransferStatus.needsPin ||
        (sending && _controller.challenge != null)) {
      return _ConfirmWithPin(controller: _controller);
    }

    return Scaffold(
      appBar: AppBar(title: const Text('Enviar dinero')),
      body: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (_payee case final payee?)
              _ChosenPayee(
                payee: payee,
                onChange: sending ? null : () => setState(() => _payee = null),
              )
            else ...[
              if (_recent.isNotEmpty) ...[
                Text('Recientes', style: Theme.of(context).textTheme.titleSmall),
                const SizedBox(height: AppSpacing.sm),
                SizedBox(
                  height: 92,
                  child: ListView.separated(
                    scrollDirection: Axis.horizontal,
                    itemCount: _recent.length,
                    separatorBuilder: (_, __) => const SizedBox(width: AppSpacing.md),
                    itemBuilder: (_, i) => _RecentPayee(
                      payee: _recent[i],
                      onTap: sending ? null : () => setState(() => _payee = _recent[i]),
                    ),
                  ),
                ),
                const SizedBox(height: AppSpacing.md),
              ],
              TextField(
                key: const Key('destination'),
                controller: _destinationField,
                enabled: !sending,
                decoration: InputDecoration(
                  labelText: _recent.isEmpty ? 'Cuenta de destino' : 'U otra cuenta',
                  border: const OutlineInputBorder(),
                ),
              ),
            ],
            const SizedBox(height: 16),
            TextField(
              key: const Key('amount'),
              controller: _amountField,
              enabled: !sending,
              keyboardType: const TextInputType.numberWithOptions(decimal: true),
              decoration: InputDecoration(
                labelText: 'Monto',
                suffixText: widget.currency,
                errorText: _amountError,
                border: const OutlineInputBorder(),
              ),
            ),
            const SizedBox(height: 16),
            TextField(
              key: const Key('description'),
              controller: _descriptionField,
              enabled: !sending,
              decoration: const InputDecoration(
                labelText: 'Concepto (opcional)',
                border: OutlineInputBorder(),
              ),
            ),
            const SizedBox(height: 24),
            if (error != null)
              Padding(
                padding: const EdgeInsets.only(bottom: 16),
                child: Text(
                  error.userMessage,
                  key: const Key('error'),
                  style: TextStyle(color: Theme.of(context).colorScheme.error),
                ),
              ),
            if (_controller.canRetry)
              FilledButton(
                key: const Key('retry'),
                onPressed: sending ? null : _controller.retry,
                child: const Text('Reintentar'),
              )
            else
              FilledButton(
                key: const Key('send'),
                onPressed: sending ? null : _send,
                child: sending
                    ? const SizedBox(
                        height: 20,
                        width: 20,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    : const Text('Enviar'),
              ),
          ],
        ),
      ),
    );
  }

  Future<void> _send() async {
    final int amountMicros;
    try {
      amountMicros = parseAmountToMicros(_amountField.text);
    } on AmountFormatException catch (e) {
      setState(() => _amountError = e.message);
      return;
    }
    setState(() => _amountError = null);

    await _controller.send(
      fromAccountId: widget.fromAccountId,
      toAccountId: _payee?.id ?? _destinationField.text.trim(),
      amountMicros: amountMicros,
      currency: widget.currency,
      description: _descriptionField.text.trim(),
    );
  }
}

/// La operación pidió confirmarse: qué se aprueba, y la cara o la clave.
///
/// Mientras se pide la cara solo se muestra qué se está aprobando; el teclado
/// aparece si la cara no pudo.
class _ConfirmWithPin extends StatelessWidget {
  const _ConfirmWithPin({required this.controller});

  final TransferController controller;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final challenge = controller.challenge;
    final sending = controller.status == TransferStatus.sending;
    final error = controller.error;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Confirma el envío'),
        leading: IconButton(
          icon: const Icon(Icons.close),
          tooltip: 'Cancelar',
          onPressed: sending ? null : controller.cancelConfirmation,
        ),
      ),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.all(AppSpacing.xl),
          children: [
            if (challenge != null) ...[
              Text(
                Money(challenge.amountMicros, challenge.currency).format(),
                textAlign: TextAlign.center,
                style: theme.textTheme.headlineMedium,
              ),
              const SizedBox(height: AppSpacing.xs),
              Text('para ${challenge.toName}', textAlign: TextAlign.center, style: theme.textTheme.bodyLarge),
              const SizedBox(height: AppSpacing.xl),
            ],
            if (!controller.confirmingWithPin) ...[
              Text('Confirma con tu cara', textAlign: TextAlign.center, style: theme.textTheme.titleMedium),
              const SizedBox(height: AppSpacing.xl),
              const Center(child: CircularProgressIndicator()),
            ] else ...[
              Text('Escribe tu clave para confirmar', textAlign: TextAlign.center, style: theme.textTheme.titleMedium),
              const SizedBox(height: AppSpacing.lg),
              if (error != null) ...[
                Text(error.userMessage, textAlign: TextAlign.center, style: TextStyle(color: theme.colorScheme.error)),
                const SizedBox(height: AppSpacing.lg),
              ],
              PinPad(enabled: !sending, onCompleted: controller.confirmWithPin),
            ],
          ],
        ),
      ),
    );
  }
}

/// Un destinatario reciente: sus iniciales en un círculo y su nombre.
class _RecentPayee extends StatelessWidget {
  const _RecentPayee({required this.payee, required this.onTap});

  final Account payee;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return InkWell(
      onTap: onTap,
      borderRadius: BorderRadius.circular(AppRadius.md),
      child: SizedBox(
        width: 72,
        child: Column(
          children: [
            CircleAvatar(
              radius: 26,
              backgroundColor: scheme.secondaryContainer,
              foregroundColor: scheme.onSecondaryContainer,
              child: Text(initials(payee.name)),
            ),
            const SizedBox(height: AppSpacing.xs),
            Text(
              payee.name,
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              textAlign: TextAlign.center,
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ],
        ),
      ),
    );
  }
}

/// El destinatario elegido, con la opción de cambiarlo.
class _ChosenPayee extends StatelessWidget {
  const _ChosenPayee({required this.payee, required this.onChange});

  final Account payee;
  final VoidCallback? onChange;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Card(
      margin: EdgeInsets.zero,
      child: ListTile(
        leading: CircleAvatar(
          backgroundColor: scheme.secondaryContainer,
          foregroundColor: scheme.onSecondaryContainer,
          child: Text(initials(payee.name)),
        ),
        title: Text(payee.name),
        subtitle: const Text('Para'),
        trailing: TextButton(onPressed: onChange, child: const Text('Cambiar')),
      ),
    );
  }
}

/// Las iniciales de un nombre, para el círculo: "Juan Pérez (prueba)" → "JP".
String initials(String name) {
  final words = name
      .replaceAll(RegExp(r'\(.*?\)'), '')
      .split(RegExp(r'\s+'))
      .where((w) => w.isNotEmpty)
      .toList();
  if (words.isEmpty) return '?';
  final first = words.first.characters.first;
  final last = words.length > 1 ? words.last.characters.first : '';
  return (first + last).toUpperCase();
}
