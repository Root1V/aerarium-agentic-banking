import 'dart:math';

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../money.dart';

/// Los bolsillos de la cuenta: dinero apartado para algo («para la tarjeta»,
/// «vacaciones»). Lo apartado no se gasta sin querer, y vuelve a la cuenta cuando
/// la persona lo decide o cierra el bolsillo.
///
/// Mover entre la cuenta y sus bolsillos no pide la cara: el dinero no sale de la
/// persona. Lo que no se puede es mandarlo a otro desde un bolsillo.
class PocketsScreen extends StatefulWidget {
  const PocketsScreen({super.key, required this.client, required this.accountId, required this.currency});

  final BffClient client;
  final String accountId;
  final String currency;

  @override
  State<PocketsScreen> createState() => _PocketsScreenState();
}

class _PocketsScreenState extends State<PocketsScreen> {
  List<Pocket>? _pockets;
  ApiError? _error;
  bool _busy = false;
  final _random = Random.secure();

  String _newKey() => List.generate(16, (_) => _random.nextInt(256).toRadixString(16).padLeft(2, '0')).join();

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final pockets = await widget.client.pockets(widget.accountId);
      if (mounted) {
        setState(() {
          _pockets = pockets;
          _error = null;
        });
      }
    } on ApiError catch (e) {
      if (mounted) setState(() => _error = e);
    }
  }

  Future<void> _run(Future<void> Function() action) async {
    setState(() => _busy = true);
    try {
      await action();
      await _load();
    } on ApiError catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.userMessage)));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _create() async {
    final hasCard = _pockets?.any((p) => p.isForCard) ?? false;
    final created = await showDialog<(String, String)>(
      context: context,
      builder: (_) => _NewPocketDialog(offerCard: !hasCard),
    );
    if (created == null) return;
    await _run(() => widget.client.openPocket(widget.accountId, created.$1, purpose: created.$2));
  }

  Future<void> _move(Pocket pocket, {required bool fill}) async {
    final micros = await showDialog<int>(
      context: context,
      builder: (_) => _AmountDialog(
        title: fill ? 'Apartar en ${pocket.name}' : 'Devolver de ${pocket.name}',
        action: fill ? 'Apartar' : 'Devolver',
      ),
    );
    if (micros == null) return;
    final key = _newKey();
    await _run(() => fill
        ? widget.client.fillPocket(pocket.id, micros, idempotencyKey: key)
        : widget.client.emptyPocket(pocket.id, micros, idempotencyKey: key));
  }

  Future<void> _close(Pocket pocket) async {
    final sure = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('¿Cerrar ${pocket.name}?'),
        content: Text(pocket.balance.amountMicros > 0
            ? 'Los ${pocket.balance.format()} que tiene vuelven a tu cuenta.'
            : 'Está vacío.'),
        actions: [
          TextButton(onPressed: () => Navigator.of(context).pop(false), child: const Text('Cancelar')),
          FilledButton(onPressed: () => Navigator.of(context).pop(true), child: const Text('Cerrar')),
        ],
      ),
    );
    if (sure == true) await _run(() => widget.client.closePocket(pocket.id));
  }

  @override
  Widget build(BuildContext context) {
    final pockets = _pockets;
    return Scaffold(
      appBar: AppBar(title: const Text('Bolsillos')),
      floatingActionButton: pockets == null
          ? null
          : FloatingActionButton.extended(
              onPressed: _busy ? null : _create,
              icon: const Icon(Icons.add),
              label: const Text('Nuevo bolsillo'),
            ),
      body: switch ((pockets, _error)) {
        (_, final ApiError error?) => Center(child: Text(error.userMessage)),
        (null, _) => const Center(child: CircularProgressIndicator()),
        (final List<Pocket> list, _) when list.isEmpty => const Padding(
            padding: EdgeInsets.all(24),
            child: Center(
              child: Text(
                'Aparta dinero para algo —para pagar la tarjeta, para unas vacaciones— y '
                'no lo gastarás sin querer. Sigue siendo tuyo y vuelve a tu cuenta cuando quieras.',
                textAlign: TextAlign.center,
              ),
            ),
          ),
        (final List<Pocket> list, _) => ListView(
            padding: const EdgeInsets.fromLTRB(16, 8, 16, 96),
            children: [
              for (final p in list)
                Card(
                  child: Padding(
                    padding: const EdgeInsets.fromLTRB(16, 12, 8, 4),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(
                          children: [
                            Icon(p.isForCard ? Icons.credit_card : Icons.savings_outlined),
                            const SizedBox(width: 12),
                            Expanded(child: Text(p.name, style: Theme.of(context).textTheme.titleMedium)),
                            Text(p.balance.format(), style: const TextStyle(fontWeight: FontWeight.w600)),
                            const SizedBox(width: 8),
                          ],
                        ),
                        if (p.isForCard)
                          const Padding(
                            padding: EdgeInsets.only(left: 36, top: 4),
                            child: Text('Para pagar la tarjeta. La previsión lo da por reservado.'),
                          ),
                        Row(
                          children: [
                            TextButton(onPressed: _busy ? null : () => _move(p, fill: true), child: const Text('Apartar')),
                            TextButton(
                                onPressed: _busy || p.balance.amountMicros == 0 ? null : () => _move(p, fill: false),
                                child: const Text('Devolver')),
                            const Spacer(),
                            IconButton(
                              tooltip: 'Cerrar el bolsillo',
                              onPressed: _busy ? null : () => _close(p),
                              icon: const Icon(Icons.delete_outline),
                            ),
                          ],
                        ),
                      ],
                    ),
                  ),
                ),
            ],
          ),
      },
    );
  }
}

class _NewPocketDialog extends StatefulWidget {
  const _NewPocketDialog({required this.offerCard});

  /// Si todavía se puede abrir el bolsillo para la tarjeta: hay uno por cuenta.
  final bool offerCard;

  @override
  State<_NewPocketDialog> createState() => _NewPocketDialogState();
}

class _NewPocketDialogState extends State<_NewPocketDialog> {
  final _name = TextEditingController();
  bool _forCard = false;

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final name = _name.text.trim();
    return AlertDialog(
      title: const Text('Nuevo bolsillo'),
      content: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          TextField(
            controller: _name,
            autofocus: true,
            maxLength: 40,
            decoration: const InputDecoration(labelText: 'Para qué'),
            onChanged: (_) => setState(() {}),
          ),
          if (widget.offerCard)
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('Es para pagar la tarjeta'),
              value: _forCard,
              onChanged: (v) => setState(() {
                _forCard = v;
                if (v && _name.text.trim().isEmpty) _name.text = 'Para la tarjeta';
              }),
            ),
        ],
      ),
      actions: [
        TextButton(onPressed: () => Navigator.of(context).pop(), child: const Text('Cancelar')),
        FilledButton(
          onPressed: name.isEmpty ? null : () => Navigator.of(context).pop((name, _forCard ? 'card' : 'general')),
          child: const Text('Abrir'),
        ),
      ],
    );
  }
}

class _AmountDialog extends StatefulWidget {
  const _AmountDialog({required this.title, required this.action});

  final String title;
  final String action;

  @override
  State<_AmountDialog> createState() => _AmountDialogState();
}

class _AmountDialogState extends State<_AmountDialog> {
  final _amount = TextEditingController();
  String? _problem;

  @override
  void dispose() {
    _amount.dispose();
    super.dispose();
  }

  void _submit() {
    try {
      final micros = parseAmountToMicros(_amount.text);
      if (micros <= 0) {
        setState(() => _problem = 'El monto tiene que ser mayor que cero');
        return;
      }
      Navigator.of(context).pop(micros);
    } on AmountFormatException catch (e) {
      setState(() => _problem = e.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: Text(widget.title),
      content: TextField(
        controller: _amount,
        autofocus: true,
        keyboardType: const TextInputType.numberWithOptions(decimal: true),
        decoration: InputDecoration(labelText: 'Monto', errorText: _problem),
        onSubmitted: (_) => _submit(),
      ),
      actions: [
        TextButton(onPressed: () => Navigator.of(context).pop(), child: const Text('Cancelar')),
        FilledButton(onPressed: _submit, child: Text(widget.action)),
      ],
    );
  }
}
