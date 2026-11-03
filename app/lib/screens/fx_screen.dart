import 'dart:async';

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../money.dart';
import '../theme.dart';

/// Cambiar entre las cuentas propias, soles y dólares (F3-09).
///
/// Lo que los bancos no dicen, aquí se dice: el tipo de cambio de referencia, el
/// que se aplica y cuánto cuesta el cambio en dinero. La cotización se congela
/// unos segundos: lo que se ve es lo que se ejecuta.
class FxScreen extends StatefulWidget {
  const FxScreen({super.key, required this.client, required this.from, required this.to});

  final BffClient client;
  final Account from;
  final Account to;

  @override
  State<FxScreen> createState() => _FxScreenState();
}

class _FxScreenState extends State<FxScreen> {
  final _amount = TextEditingController();
  FxQuote? _quote;
  String? _problem;
  bool _busy = false;
  Timer? _tick;
  int _secondsLeft = 0;

  @override
  void dispose() {
    _tick?.cancel();
    _amount.dispose();
    super.dispose();
  }

  Future<void> _getQuote() async {
    int micros;
    try {
      micros = parseAmountToMicros(_amount.text);
    } on AmountFormatException catch (e) {
      setState(() => _problem = e.message);
      return;
    }
    setState(() {
      _busy = true;
      _problem = null;
    });
    try {
      final quote = await widget.client.fxQuote(widget.from.id, widget.to.id, micros);
      if (!mounted) return;
      _tick?.cancel();
      setState(() {
        _quote = quote;
        _secondsLeft = quote.expiresAt.difference(DateTime.now()).inSeconds.clamp(0, 60);
      });
      _tick = Timer.periodic(const Duration(seconds: 1), (_) {
        if (!mounted) return;
        setState(() => _secondsLeft = (_secondsLeft - 1).clamp(0, 60));
        if (_secondsLeft == 0) _tick?.cancel();
      });
    } on ApiError catch (e) {
      if (mounted) setState(() => _problem = e.userMessage);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _convert() async {
    final quote = _quote;
    if (quote == null) return;
    setState(() => _busy = true);
    try {
      await widget.client.fxConvert(quote.id);
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('Cambiaste ${quote.from.format()} a ${quote.to.format()}')));
      Navigator.of(context).pop(true);
    } on ApiError catch (e) {
      if (mounted) {
        setState(() {
          _problem = e.userMessage;
          if (e.code == ApiErrorCode.quoteExpired) _quote = null;
        });
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final quote = _quote;
    final expired = quote != null && _secondsLeft == 0;
    final toName = Money.currencyName(widget.to.currency);
    return Scaffold(
      appBar: AppBar(title: Text('Cambiar a $toName')),
      body: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          TextField(
            controller: _amount,
            autofocus: true,
            keyboardType: const TextInputType.numberWithOptions(decimal: true),
            decoration: InputDecoration(
              labelText: 'Cuánto cambias, en ${Money.currencyName(widget.from.currency)}',
              errorText: _problem,
            ),
            onChanged: (_) {
              if (_quote != null) setState(() => _quote = null);
            },
            onSubmitted: (_) => _getQuote(),
          ),
          const SizedBox(height: AppSpacing.md),
          if (quote == null || expired)
            FilledButton(
              onPressed: _busy ? null : _getQuote,
              child: Text(expired ? 'Cotizar de nuevo' : 'Ver cuánto recibo'),
            ),
          if (quote != null) ...[
            const SizedBox(height: AppSpacing.md),
            Card(
              child: Padding(
                padding: const EdgeInsets.all(16),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text('Recibes', style: theme.textTheme.labelLarge),
                    Text(quote.to.format(), key: const Key('fx-to'), style: theme.textTheme.headlineMedium),
                    const SizedBox(height: AppSpacing.md),
                    _Line('Tipo de cambio', 'S/ ${rateText(quote.appliedRate)} por dólar'),
                    _Line('Referencia del día', 'S/ ${rateText(quote.referenceRate)} por dólar'),
                    _Line('Te cuesta el cambio', quote.cost.format(), strong: true),
                    const SizedBox(height: AppSpacing.sm),
                    Text(
                      'Es lo que dejas de recibir frente al tipo de referencia. Es todo: no hay otra comisión.',
                      style: theme.textTheme.bodySmall,
                    ),
                  ],
                ),
              ),
            ),
            const SizedBox(height: AppSpacing.md),
            if (!expired)
              FilledButton(
                onPressed: _busy ? null : _convert,
                child: Text('Cambiar · vale $_secondsLeft s'),
              )
            else
              Text('La cotización venció: el tipo de cambio pudo moverse.', style: theme.textTheme.bodySmall),
          ],
        ],
      ),
    );
  }
}

class _Line extends StatelessWidget {
  const _Line(this.label, this.value, {this.strong = false});

  final String label;
  final String value;
  final bool strong;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        children: [
          Expanded(child: Text(label)),
          Text(value, style: TextStyle(fontWeight: strong ? FontWeight.w700 : FontWeight.w500)),
        ],
      ),
    );
  }
}
