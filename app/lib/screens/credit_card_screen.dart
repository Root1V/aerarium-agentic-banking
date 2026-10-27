import 'dart:math';

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../money.dart';
import '../theme.dart';

/// La tarjeta en el inicio: cuánto queda disponible, o la invitación a pedirla.
class CreditCardTile extends StatelessWidget {
  const CreditCardTile({super.key, required this.card, required this.onTap});

  /// Null: todavía no tiene tarjeta.
  final CreditCard? card;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final card = this.card;
    return Card(
      margin: EdgeInsets.zero,
      child: ListTile(
        onTap: onTap,
        leading: const Icon(Icons.credit_card),
        title: Text(card == null ? 'Pide tu tarjeta de crédito' : 'Tarjeta de crédito'),
        subtitle: Text(
          card == null
              ? 'Evaluamos tu cuenta en segundos. No te cuesta nada pedirla.'
              : card.blocked
                  ? 'Bloqueada por un pago pendiente'
                  : 'Disponible ${card.available.format()} de ${card.limit.format()}',
          style: card != null && card.blocked ? TextStyle(color: theme.colorScheme.error) : null,
        ),
        trailing: const Icon(Icons.chevron_right),
      ),
    );
  }
}

/// Pedir la tarjeta: el banco evalúa los movimientos de la cuenta y responde en
/// el momento. Si no se aprueba, dice qué falta; nunca el puntaje.
class CreditApplyScreen extends StatefulWidget {
  const CreditApplyScreen({super.key, required this.client, required this.accountId});

  final BffClient client;
  final String accountId;

  @override
  State<CreditApplyScreen> createState() => _CreditApplyScreenState();
}

class _CreditApplyScreenState extends State<CreditApplyScreen> {
  bool _busy = false;
  CreditDecision? _decision;
  String? _error;

  Future<void> _apply() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final decision = await widget.client.applyForCredit(widget.accountId);
      if (mounted) setState(() => _decision = decision);
    } on ApiError catch (e) {
      if (mounted) setState(() => _error = e.userMessage);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodyLarge?.copyWith(color: theme.colorScheme.onSurfaceVariant);
    final decision = _decision;

    final List<Widget> body;
    if (decision == null) {
      body = [
        Icon(Icons.credit_card, size: 40, color: theme.colorScheme.primary),
        const SizedBox(height: AppSpacing.md),
        Text('Tu tarjeta de crédito', style: theme.textTheme.headlineSmall),
        const SizedBox(height: AppSpacing.sm),
        Text(
          'Miramos cómo usas tu cuenta —lo que entra y lo que sale— y te respondemos al '
          'momento. Empezamos con un límite prudente y lo subimos según cómo pagues.',
          style: muted,
        ),
        const SizedBox(height: AppSpacing.xl),
        FilledButton(
          onPressed: _busy ? null : _apply,
          child: _busy
              ? const SizedBox.square(dimension: 20, child: CircularProgressIndicator(strokeWidth: 2))
              : const Text('Solicitar'),
        ),
      ];
    } else if (decision.card case final card?) {
      body = [
        Icon(Icons.celebration_outlined, size: 40, color: theme.colorScheme.primary),
        const SizedBox(height: AppSpacing.md),
        Text('¡Aprobada!', style: theme.textTheme.headlineSmall),
        const SizedBox(height: AppSpacing.sm),
        Text('Tu límite es ${card.limit.format()}.', style: theme.textTheme.titleMedium),
        const SizedBox(height: AppSpacing.sm),
        Text(
          'Tasa efectiva anual (TEA): ${card.annualRate}. Tu estado de cuenta se corta el día '
          '${card.cycleDay} de cada mes y tienes ${card.paymentDays} días para pagar. Si pagas el '
          'total a tiempo, no pagas intereses.',
          style: muted,
        ),
        if (decision.syntheticHistory) ...[
          const SizedBox(height: AppSpacing.md),
          const _SandboxNote(),
        ],
        const SizedBox(height: AppSpacing.xl),
        FilledButton(onPressed: () => Navigator.of(context).pop(card), child: const Text('Ver mi tarjeta')),
      ];
    } else {
      body = [
        Icon(Icons.hourglass_empty, size: 40, color: theme.colorScheme.primary),
        const SizedBox(height: AppSpacing.md),
        Text(
          decision.outcome == 'declined' ? 'Por ahora no podemos darte una tarjeta' : 'Todavía no podemos darte una tarjeta',
          style: theme.textTheme.headlineSmall,
        ),
        const SizedBox(height: AppSpacing.sm),
        Text(
          decision.outcome == 'declined'
              ? 'Esto es lo que más pesó en la evaluación:'
              : 'Necesitamos ver un poco más de cómo usas tu cuenta. Esto es lo que falta:',
          style: muted,
        ),
        const SizedBox(height: AppSpacing.md),
        for (final reason in decision.reasons)
          Padding(
            padding: const EdgeInsets.only(bottom: AppSpacing.sm),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                const Text('•  '),
                Expanded(child: Text(reason)),
              ],
            ),
          ),
        const SizedBox(height: AppSpacing.lg),
        OutlinedButton(onPressed: () => Navigator.of(context).pop(), child: const Text('Entendido')),
      ];
    }

    return Scaffold(
      appBar: AppBar(title: const Text('Tarjeta de crédito')),
      body: ListView(
        padding: const EdgeInsets.all(AppSpacing.xl),
        children: [
          ...body,
          if (_error != null) ...[
            const SizedBox(height: AppSpacing.md),
            Text(_error!, style: TextStyle(color: theme.colorScheme.error)),
          ],
        ],
      ),
    );
  }
}

/// La tarjeta: cuánto se debe, cuánto queda, y mover dinero entre ella y la cuenta.
class CreditCardScreen extends StatefulWidget {
  const CreditCardScreen({
    super.key,
    required this.client,
    required this.accountId,
    required this.card,
    this.sandbox = false,
  });

  final BffClient client;

  /// Muestra los atajos del ciclo, que solo existen en el sandbox.
  final bool sandbox;

  /// La cuenta que recibe lo que se pasa de la tarjeta y desde la que se paga.
  final String accountId;
  final CreditCard card;

  @override
  State<CreditCardScreen> createState() => _CreditCardScreenState();
}

class _CreditCardScreenState extends State<CreditCardScreen> {
  late CreditCard _card = widget.card;
  bool _busy = false;
  final _random = Random.secure();

  /// Cada operación lleva su clave: si la red falla y se reintenta, la misma.
  String _newKey() => List.generate(16, (_) => _random.nextInt(256).toRadixString(16).padLeft(2, '0')).join();

  Future<void> _move({required bool draw}) async {
    final amount = await showModalBottomSheet<int>(
      context: context,
      isScrollControlled: true,
      builder: (_) => _AmountSheet(
        title: draw ? 'Pasar a mi cuenta' : 'Pagar mi tarjeta',
        hint: draw ? 'Disponible ${_card.available.format()}' : 'Debes ${_card.outstanding.format()}',
        action: draw ? 'Pasar' : 'Pagar',
        fullAmount: draw ? null : _card.outstanding,
        minimumAmount: draw ? null : _card.statement?.minimumLeft,
      ),
    );
    if (amount == null || !mounted) return;

    setState(() => _busy = true);
    final key = _newKey();
    try {
      if (draw) {
        await widget.client.drawFromCard(widget.accountId, amount, idempotencyKey: key);
      } else {
        await widget.client.payCard(widget.accountId, amount, idempotencyKey: key);
      }
      final card = await widget.client.creditCard();
      if (!mounted) return;
      setState(() => _card = card ?? _card);
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(
        content: Text(draw
            ? 'Pasamos ${Money(amount, _card.limit.currency).format()} a tu cuenta.'
            : 'Pagaste ${Money(amount, _card.limit.currency).format()}.'),
      ));
    } on ApiError catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.userMessage)));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  /// Atajos del sandbox: cortar ahora, o fingir que pasó el vencimiento.
  Future<void> _sandbox(Future<void> Function() action, String done) async {
    setState(() => _busy = true);
    try {
      await action();
      final card = await widget.client.creditCard();
      if (!mounted) return;
      setState(() => _card = card ?? _card);
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(done)));
    } on ApiError catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.userMessage)));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final card = _card;
    final used = card.limit.amountMicros == 0 ? 0.0 : card.outstanding.amountMicros / card.limit.amountMicros;

    return PopScope(
      canPop: !_busy,
      child: Scaffold(
        appBar: AppBar(title: const Text('Tarjeta de crédito')),
        body: ListView(
          padding: const EdgeInsets.all(AppSpacing.xl),
          children: [
            Text('Disponible', style: theme.textTheme.labelLarge),
            Text(card.available.format(), key: const Key('available'), style: theme.textTheme.headlineMedium),
            const SizedBox(height: AppSpacing.md),
            LinearProgressIndicator(value: used.clamp(0, 1), minHeight: 8, borderRadius: BorderRadius.circular(4)),
            const SizedBox(height: AppSpacing.sm),
            Text('Debes ${card.outstanding.format()} de un límite de ${card.limit.format()}'),
            if (card.blocked) ...[
              const SizedBox(height: AppSpacing.md),
              Text('Tu tarjeta está bloqueada por un pago pendiente. Puedes pagarla para desbloquearla.',
                  style: TextStyle(color: theme.colorScheme.error)),
            ],
            const SizedBox(height: AppSpacing.xl),
            Row(
              children: [
                Expanded(
                  child: FilledButton.tonal(
                    onPressed: _busy || card.blocked ? null : () => _move(draw: true),
                    child: const Text('Pasar a mi cuenta'),
                  ),
                ),
                const SizedBox(width: AppSpacing.md),
                Expanded(
                  child: FilledButton(
                    onPressed: _busy || card.outstanding.amountMicros == 0 ? null : () => _move(draw: false),
                    child: const Text('Pagar'),
                  ),
                ),
              ],
            ),
            if (card.statement case final statement?) ...[
              const SizedBox(height: AppSpacing.xl),
              _StatementCard(statement: statement),
            ],
            if (widget.sandbox) ...[
              const SizedBox(height: AppSpacing.lg),
              Text('Entorno de pruebas', style: theme.textTheme.labelLarge),
              TextButton.icon(
                onPressed: _busy ? null : () => _sandbox(widget.client.closeCycleNow, 'Cortamos tu estado de cuenta.'),
                icon: const Icon(Icons.science_outlined),
                label: const Text('Cortar el estado de cuenta ahora'),
              ),
              TextButton.icon(
                onPressed: _busy || card.statement == null
                    ? null
                    : () => _sandbox(widget.client.passDueDate, 'Hicimos como si hubiera pasado el vencimiento.'),
                icon: const Icon(Icons.science_outlined),
                label: const Text('Simular que pasó el vencimiento'),
              ),
            ],
            const SizedBox(height: AppSpacing.xl),
            const Divider(),
            ListTile(contentPadding: EdgeInsets.zero, title: const Text('Tasa efectiva anual (TEA)'), trailing: Text(card.annualRate)),
            ListTile(contentPadding: EdgeInsets.zero, title: const Text('Corte del estado de cuenta'), trailing: Text('Día ${card.cycleDay}')),
            ListTile(contentPadding: EdgeInsets.zero, title: const Text('Plazo para pagar'), trailing: Text('${card.paymentDays} días')),
          ],
        ),
      ),
    );
  }
}

/// Pide un monto. Para pagar ofrece "Pagar el mínimo" y "Pagar todo".
class _AmountSheet extends StatefulWidget {
  const _AmountSheet({
    required this.title,
    required this.hint,
    required this.action,
    this.fullAmount,
    this.minimumAmount,
  });

  final String title;
  final String hint;
  final String action;
  final Money? fullAmount;

  /// Lo que falta del pago mínimo del último estado de cuenta.
  final Money? minimumAmount;

  @override
  State<_AmountSheet> createState() => _AmountSheetState();
}

class _AmountSheetState extends State<_AmountSheet> {
  final _amount = TextEditingController();
  String? _error;

  @override
  void dispose() {
    _amount.dispose();
    super.dispose();
  }

  void _submit() {
    try {
      Navigator.of(context).pop(parseAmountToMicros(_amount.text));
    } on AmountFormatException catch (e) {
      setState(() => _error = e.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: EdgeInsets.fromLTRB(
          AppSpacing.xl, AppSpacing.xl, AppSpacing.xl, AppSpacing.xl + MediaQuery.of(context).viewInsets.bottom),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(widget.title, style: Theme.of(context).textTheme.titleLarge),
          const SizedBox(height: AppSpacing.xs),
          Text(widget.hint),
          const SizedBox(height: AppSpacing.lg),
          TextField(
            key: const Key('credit-amount'),
            controller: _amount,
            autofocus: true,
            keyboardType: const TextInputType.numberWithOptions(decimal: true),
            decoration: InputDecoration(labelText: 'Monto', errorText: _error),
            onSubmitted: (_) => _submit(),
          ),
          if (widget.minimumAmount case final minimum? when minimum.amountMicros > 0)
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton(
                onPressed: () => Navigator.of(context).pop(minimum.amountMicros),
                child: Text('Pagar el mínimo (${minimum.format()})'),
              ),
            ),
          if (widget.fullAmount case final full?)
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton(
                onPressed: () => Navigator.of(context).pop(full.amountMicros),
                child: Text('Pagar todo (${full.format()})'),
              ),
            ),
          const SizedBox(height: AppSpacing.md),
          FilledButton(onPressed: _submit, child: Text(widget.action)),
        ],
      ),
    );
  }
}

/// En el sandbox, la evaluación usó un historial simulado: se dice.
class _SandboxNote extends StatelessWidget {
  const _SandboxNote();

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.all(AppSpacing.md),
      decoration: BoxDecoration(color: scheme.secondaryContainer, borderRadius: BorderRadius.circular(AppRadius.md)),
      child: Row(
        children: [
          Icon(Icons.science_outlined, color: scheme.onSecondaryContainer),
          const SizedBox(width: AppSpacing.md),
          Expanded(
            child: Text(
              'Entorno de pruebas: tu cuenta es nueva, así que la evaluación usó seis meses de ingresos simulados.',
              style: TextStyle(color: scheme.onSecondaryContainer),
            ),
          ),
        ],
      ),
    );
  }
}

/// El último estado de cuenta: cuánto se debía al corte, el mínimo y hasta cuándo.
class _StatementCard extends StatelessWidget {
  const _StatementCard({required this.statement});

  final CreditStatement statement;

  static String _date(DateTime d) => '${d.day.toString().padLeft(2, '0')}/${d.month.toString().padLeft(2, '0')}/${d.year}';

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final s = statement;
    final (label, color) = switch (s.status) {
      'paid' => ('Pagado', context.moneyColors.positive),
      'minimum_paid' => ('Mínimo pagado', context.moneyColors.positive),
      'past_due' => ('Vencido', scheme.error),
      _ => ('Pendiente', scheme.onSurfaceVariant),
    };
    return Card(
      margin: EdgeInsets.zero,
      child: Padding(
        padding: const EdgeInsets.all(AppSpacing.lg),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(child: Text('Estado de cuenta al ${_date(s.periodEnd.toLocal())}', style: theme.textTheme.titleSmall)),
                Text(label, key: const Key('statement-status'), style: TextStyle(color: color, fontWeight: FontWeight.w600)),
              ],
            ),
            const SizedBox(height: AppSpacing.md),
            Text('Total a pagar: ${s.closing.format()}', style: theme.textTheme.titleMedium),
            Text('Pago mínimo: ${s.minimumPayment.format()}'),
            Text('Vence el ${_date(s.dueDate)}'),
            if (s.interest.amountMicros > 0) Text('Incluye ${s.interest.format()} de intereses'),
            if (s.paidSinceCut.amountMicros > 0) Text('Ya pagaste ${s.paidSinceCut.format()}'),
          ],
        ),
      ),
    );
  }
}
