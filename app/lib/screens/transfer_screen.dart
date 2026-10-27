import 'package:flutter/material.dart';

import '../api/client.dart';
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

  /// Un destinatario a mano —en el sandbox, el de prueba— para no tener que
  /// escribir un identificador de cuenta en el teléfono.
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

  @override
  void initState() {
    super.initState();
    _controller.addListener(_onControllerChanged);
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
            if (widget.suggestedPayee case final payee?)
              Padding(
                padding: const EdgeInsets.only(bottom: 12),
                child: Align(
                  alignment: Alignment.centerLeft,
                  child: ActionChip(
                    avatar: const Icon(Icons.person_outline, size: 18),
                    label: Text(payee.name),
                    onPressed: sending ? null : () => _destinationField.text = payee.id,
                  ),
                ),
              ),
            TextField(
              key: const Key('destination'),
              controller: _destinationField,
              enabled: !sending,
              decoration: const InputDecoration(
                labelText: 'Cuenta de destino',
                border: OutlineInputBorder(),
              ),
            ),
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
      toAccountId: _destinationField.text.trim(),
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
