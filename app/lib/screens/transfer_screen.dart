import 'package:flutter/material.dart';

import '../api/client.dart';
import '../money.dart';
import '../transfer_controller.dart';

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
  });

  final BffClient client;
  final String fromAccountId;
  final String currency;

  @override
  State<TransferScreen> createState() => _TransferScreenState();
}

class _TransferScreenState extends State<TransferScreen> {
  late final TransferController _controller =
      TransferController(widget.client);

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

    return Scaffold(
      appBar: AppBar(title: const Text('Enviar dinero')),
      body: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
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
