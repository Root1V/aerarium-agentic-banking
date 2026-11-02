import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../movement_text.dart';

/// Comprobar que una constancia es real, con su código.
///
/// Contra las capturas falsas de un pago: no basta con ver una imagen. Con el
/// código, el banco dice si ese pago existe, por cuánto y entre quiénes. Quien no
/// tiene la app puede hacer lo mismo en la página de la constancia.
class VerifyReceiptScreen extends StatefulWidget {
  const VerifyReceiptScreen({super.key, required this.client});

  final BffClient client;

  @override
  State<VerifyReceiptScreen> createState() => _VerifyReceiptScreenState();
}

class _VerifyReceiptScreenState extends State<VerifyReceiptScreen> {
  final _code = TextEditingController();
  bool _checking = false;
  bool _checked = false;
  VerifiableReceipt? _receipt;
  String? _problem;

  @override
  void initState() {
    super.initState();
    // Si el código está en el portapapeles —se lo mandaron por chat—, se propone.
    Clipboard.getData(Clipboard.kTextPlain).then((data) {
      final match = RegExp(r'\b[0-9A-Za-z]{5}-[0-9A-Za-z]{5}\b').firstMatch(data?.text ?? '');
      if (match != null && mounted && _code.text.isEmpty) setState(() => _code.text = match.group(0)!);
    }).catchError((_) {});
  }

  @override
  void dispose() {
    _code.dispose();
    super.dispose();
  }

  Future<void> _verify() async {
    setState(() {
      _checking = true;
      _problem = null;
    });
    try {
      final receipt = await widget.client.verifyReceipt(_code.text);
      if (mounted) {
        setState(() {
          _receipt = receipt;
          _checked = true;
        });
      }
    } on ApiError catch (e) {
      if (mounted) setState(() => _problem = e.userMessage);
    } finally {
      if (mounted) setState(() => _checking = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final r = _receipt;
    return Scaffold(
      appBar: AppBar(title: const Text('Verificar una constancia')),
      body: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          const Text('¿Te mostraron un pago? Escribe el código de su constancia y comprueba con el banco que '
              'es real antes de entregar nada.'),
          const SizedBox(height: 16),
          TextField(
            controller: _code,
            textCapitalization: TextCapitalization.characters,
            autocorrect: false,
            decoration: InputDecoration(labelText: 'Código (XXXXX-XXXXX)', errorText: _problem),
            onSubmitted: (_) => _verify(),
          ),
          const SizedBox(height: 12),
          FilledButton(onPressed: _checking ? null : _verify, child: const Text('Verificar')),
          if (_checked) ...[
            const SizedBox(height: 24),
            Card(
              color: r == null ? scheme.errorContainer : scheme.primaryContainer,
              child: Padding(
                padding: const EdgeInsets.all(16),
                child: r == null
                    ? Text(
                        'No existe una constancia con ese código. El comprobante que te mostraron no es de '
                        'Aerarium: no entregues nada hasta ver el dinero en tu cuenta.',
                        style: TextStyle(color: scheme.onErrorContainer),
                      )
                    : Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text('Este pago es real', style: theme.textTheme.titleMedium),
                          const SizedBox(height: 8),
                          Text('${r.amount.format()} · ${r.from} → ${r.to}'),
                          Text(longDateTime(r.postedAt)),
                          if (r.status != 'completed' && r.status.isNotEmpty) Text('Estado: ${r.status}'),
                          const SizedBox(height: 8),
                          Text('Compara estos datos con lo que te mostraron: si no coinciden, no es este pago.',
                              style: theme.textTheme.bodySmall),
                        ],
                      ),
              ),
            ),
          ],
        ],
      ),
    );
  }
}
