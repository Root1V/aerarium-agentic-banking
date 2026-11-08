import 'dart:math';

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../auth/device_keys.dart';
import '../forecast_text.dart';
import '../goal_text.dart';
import '../money.dart';
import '../theme.dart';
import '../widgets/pin_pad.dart';

/// Una meta (F3-03): cuánto lleva, si llega a tiempo, su regla de ahorro y lo
/// que la regla hizo, también cuando no apartó y por qué.
///
/// La regla es un permiso: se confirma con la cara y se quita con un toque.
/// Antes de cada aporte el banco mira la previsión de saldo, y si el aporte te
/// dejaría corto antes de cobrar, aparta menos o nada.
class GoalScreen extends StatefulWidget {
  const GoalScreen({super.key, required this.client, required this.goalId, this.confirmer});

  final BffClient client;
  final String goalId;

  /// Firma con el teléfono vinculado. Nulo si no lo hay: entonces no se puede
  /// poner una regla, porque no hay con qué confirmarla.
  final TransferConfirmer? confirmer;

  @override
  State<GoalScreen> createState() => _GoalScreenState();
}

class _GoalScreenState extends State<GoalScreen> {
  Goal? _goal;
  ApiError? _error;
  bool _busy = false;
  final _random = Random.secure();

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final goal = await widget.client.goal(widget.goalId);
      if (mounted) {
        setState(() {
          _goal = goal;
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

  Future<void> _editRule(Goal goal) async {
    final chosen = await showModalBottomSheet<(int, String)>(
      context: context,
      isScrollControlled: true,
      showDragHandle: true,
      builder: (_) => _RuleSheet(goal: goal),
    );
    if (chosen == null) return;
    await _run(() async {
      final done = await _setRule(goal, chosen.$1, chosen.$2);
      if (done != null && mounted) {
        final first = done.contributions.isEmpty ? null : done.contributions.first;
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(
          content:
              Text(first == null ? 'Regla lista.' : 'Regla lista. Hoy: ${contributionText(first).$1.toLowerCase()}.'),
        ));
      }
    });
  }

  /// Pone la regla. La primera llamada devuelve lo que hay que firmar; la
  /// segunda lleva la firma. Nulo si la persona se echó atrás.
  Future<Goal?> _setRule(Goal goal, int amount, String cadence) async {
    final ConfirmationChallenge challenge;
    try {
      await widget.client.setGoalRule(goal.id, amount, cadence);
      return await widget.client.goal(goal.id);
    } on ApiError catch (e) {
      if (e.code != ApiErrorCode.confirmationRequired) rethrow;
      challenge = ConfirmationChallenge.fromJson(e.details);
    }
    final confirmer = widget.confirmer;
    if (confirmer == null) {
      throw const ApiError(ApiErrorCode.confirmationUnavailable, 'sin teléfono vinculado');
    }
    final what = 'Apartar ${Money(amount, goal.target.currency).format()} ${cadenceText(cadence)} para ${goal.name}';
    TransferConfirmation confirmation;
    try {
      confirmation = await confirmer.confirm(challenge, DeviceUnlock.biometric, reason: what);
    } on DeviceKeyException catch (e) {
      if (e.failure == DeviceKeyFailure.cancelled) return null;
      // La cara no pudo: con la clave. El desafío sigue vivo.
      if (!mounted) return null;
      final pin = await Navigator.of(context).push<String>(MaterialPageRoute(
        fullscreenDialog: true,
        builder: (_) => _PinPrompt(what: what),
      ));
      if (pin == null) return null;
      try {
        confirmation = await confirmer.confirm(challenge, DeviceUnlock.pin, pin: pin, reason: '');
      } on DeviceKeyException {
        throw const ApiError(ApiErrorCode.confirmationRejected, 'el teléfono no firmó');
      }
    }
    await widget.client.setGoalRule(goal.id, amount, cadence, confirmation: confirmation);
    return widget.client.goal(goal.id);
  }

  Future<void> _revokeRule(Goal goal) async {
    final sure = await _ask('¿Quitar la regla?', 'Deja de apartar sola. Lo juntado sigue en la meta.', 'Quitar');
    if (sure) await _run(() => widget.client.revokeGoalRule(goal.id));
  }

  Future<void> _move(Goal goal, {required bool fill}) async {
    final micros = await showDialog<int>(
      context: context,
      builder: (_) => _AmountDialog(
          title: fill ? 'Apartar en ${goal.name}' : 'Sacar de ${goal.name}', action: fill ? 'Apartar' : 'Sacar'),
    );
    if (micros == null) return;
    final key = List.generate(16, (_) => _random.nextInt(256).toRadixString(16).padLeft(2, '0')).join();
    await _run(() => fill
        ? widget.client.fillPocket(goal.pocketId, micros, idempotencyKey: key)
        : widget.client.emptyPocket(goal.pocketId, micros, idempotencyKey: key));
  }

  Future<void> _close(Goal goal) async {
    final sure = await _ask(
      '¿Cerrar ${goal.name}?',
      goal.saved.amountMicros > 0 ? 'Los ${goal.saved.format()} juntados vuelven a tu cuenta.' : 'No tiene dinero.',
      'Cerrar',
    );
    if (!sure) return;
    setState(() => _busy = true);
    try {
      await widget.client.closeGoal(goal.id);
      if (mounted) Navigator.of(context).pop();
    } on ApiError catch (e) {
      if (mounted) {
        setState(() => _busy = false);
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.userMessage)));
      }
    }
  }

  Future<bool> _ask(String title, String body, String action) async =>
      await showDialog<bool>(
        context: context,
        builder: (context) => AlertDialog(
          title: Text(title),
          content: Text(body),
          actions: [
            TextButton(onPressed: () => Navigator.of(context).pop(false), child: const Text('Cancelar')),
            FilledButton(onPressed: () => Navigator.of(context).pop(true), child: Text(action)),
          ],
        ),
      ) ==
      true;

  @override
  Widget build(BuildContext context) {
    final goal = _goal;
    return Scaffold(
      appBar: AppBar(title: Text(goal?.name ?? 'Meta')),
      body: switch ((goal, _error)) {
        (_, final ApiError error?) => Center(child: Text(error.userMessage)),
        (null, _) => const Center(child: CircularProgressIndicator()),
        (final Goal g, _) => RefreshIndicator(
            onRefresh: _load,
            child: ListView(
              padding: const EdgeInsets.fromLTRB(16, 8, 16, 32),
              children: [
                _Progress(goal: g),
                const SizedBox(height: AppSpacing.lg),
                Row(
                  children: [
                    Expanded(
                      child: FilledButton.tonalIcon(
                        onPressed: _busy ? null : () => _move(g, fill: true),
                        icon: const Icon(Icons.add),
                        label: const Text('Apartar'),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: OutlinedButton.icon(
                        onPressed: _busy || g.saved.amountMicros == 0 ? null : () => _move(g, fill: false),
                        icon: const Icon(Icons.undo),
                        label: const Text('Sacar'),
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: AppSpacing.lg),
                if (!g.reached)
                  _RuleCard(
                    goal: g,
                    canConfirm: widget.confirmer != null,
                    onEdit: _busy ? null : () => _editRule(g),
                    onRevoke: _busy || g.rule == null ? null : () => _revokeRule(g),
                  ),
                if (g.contributions.isNotEmpty) ...[
                  const SizedBox(height: AppSpacing.lg),
                  Text('Lo que hizo tu regla', style: Theme.of(context).textTheme.titleMedium),
                  const SizedBox(height: 8),
                  Card(
                    child: Column(
                      children: [
                        for (final (i, c) in g.contributions.indexed) ...[
                          if (i > 0) const Divider(height: 1, indent: 56),
                          _ContributionRow(contribution: c),
                        ],
                      ],
                    ),
                  ),
                ],
                const SizedBox(height: AppSpacing.xl),
                TextButton(
                  onPressed: _busy ? null : () => _close(g),
                  style: TextButton.styleFrom(foregroundColor: Theme.of(context).colorScheme.error),
                  child: const Text('Cerrar la meta'),
                ),
              ],
            ),
          ),
      },
    );
  }
}

class _Progress extends StatelessWidget {
  const _Progress({required this.goal});

  final Goal goal;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final pace = paceText(goal);
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(goal.saved.format(), style: theme.textTheme.headlineMedium),
            Text(
              [
                'de ${goal.target.format()}',
                if (goal.targetDate != null) 'para ${onDay(goal.targetDate!).substring(3)}',
              ].join(' · '),
              style: theme.textTheme.bodyMedium,
            ),
            const SizedBox(height: 12),
            ClipRRect(
              borderRadius: BorderRadius.circular(4),
              child: LinearProgressIndicator(value: goal.progress, minHeight: 8),
            ),
            if (pace != null) ...[
              const SizedBox(height: 12),
              Text(pace, key: const Key('pace'), style: theme.textTheme.bodyMedium),
            ],
          ],
        ),
      ),
    );
  }
}

class _RuleCard extends StatelessWidget {
  const _RuleCard({required this.goal, required this.canConfirm, this.onEdit, this.onRevoke});

  final Goal goal;
  final bool canConfirm;
  final VoidCallback? onEdit;
  final VoidCallback? onRevoke;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final rule = goal.rule;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(Icons.autorenew, color: theme.colorScheme.primary),
                const SizedBox(width: 12),
                Expanded(
                  child: Text(rule == null ? 'Ahorro automático' : 'Aparta ${ruleText(rule)}',
                      style: theme.textTheme.titleMedium),
                ),
              ],
            ),
            const SizedBox(height: 8),
            Text(
              rule == null
                  ? 'Elige cuánto apartar cada semana o cada mes. Antes de cada aporte miramos tu previsión: '
                      'si te dejaría corto antes de cobrar, aparta menos o nada, y te lo decimos aquí.'
                  : 'Próximo aporte ${onDay(rule.nextRunOn)}. Si te dejaría corto antes de cobrar, aparta '
                      'menos o nada. Se detiene sola al completar la meta.',
              style: theme.textTheme.bodySmall,
            ),
            const SizedBox(height: 16),
            if (!canConfirm)
              Text('Para programar aportes, entra desde tu teléfono vinculado.', style: theme.textTheme.bodySmall)
            else
              Row(
                children: [
                  // Expandido: el tema da a los botones un ancho mínimo infinito,
                  // y suelto dentro de una fila no se puede dibujar.
                  Expanded(
                    child: FilledButton(
                      key: const Key('edit-rule'),
                      onPressed: onEdit,
                      child: Text(rule == null ? 'Programar aportes' : 'Cambiar'),
                    ),
                  ),
                  if (rule != null) ...[
                    const SizedBox(width: 8),
                    TextButton(onPressed: onRevoke, child: const Text('Quitar')),
                  ],
                ],
              ),
          ],
        ),
      ),
    );
  }
}

class _ContributionRow extends StatelessWidget {
  const _ContributionRow({required this.contribution});

  final GoalContribution contribution;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final (title, detail) = contributionText(contribution);
    final (icon, color) = switch (contribution.outcome) {
      'saved' || 'reached' => (Icons.check_circle_outline, scheme.primary),
      'reduced' => (Icons.shield_outlined, scheme.tertiary),
      _ => (Icons.pause_circle_outline, scheme.onSurfaceVariant),
    };
    return ListTile(
      leading: Icon(icon, color: color),
      title: Text(title),
      subtitle: Text(detail),
    );
  }
}

/// Cuánto y cada cuánto. Si la meta tiene fecha, propone lo que hace falta.
class _RuleSheet extends StatefulWidget {
  const _RuleSheet({required this.goal});

  final Goal goal;

  @override
  State<_RuleSheet> createState() => _RuleSheetState();
}

class _RuleSheetState extends State<_RuleSheet> {
  late final TextEditingController _amount;
  late String _cadence;
  String? _problem;

  @override
  void initState() {
    super.initState();
    final rule = widget.goal.rule;
    final suggested = widget.goal.pace.suggested;
    _cadence = rule?.cadence ?? widget.goal.pace.suggestedCadence;
    final start = rule?.amount ?? suggested;
    _amount = TextEditingController(text: start == null ? '' : (start.amountMicros ~/ 1000000).toString());
  }

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
      Navigator.of(context).pop((micros, _cadence));
    } on AmountFormatException catch (e) {
      setState(() => _problem = e.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final suggested = widget.goal.pace.suggested;
    return Padding(
      padding: EdgeInsets.fromLTRB(24, 0, 24, 24 + MediaQuery.of(context).viewInsets.bottom),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text('Ahorro automático para ${widget.goal.name}', style: theme.textTheme.titleLarge),
          const SizedBox(height: 16),
          SegmentedButton<String>(
            segments: const [
              ButtonSegment(value: 'weekly', label: Text('Cada semana')),
              ButtonSegment(value: 'monthly', label: Text('Cada mes')),
            ],
            selected: {_cadence},
            onSelectionChanged: (v) => setState(() => _cadence = v.first),
          ),
          const SizedBox(height: 16),
          TextField(
            controller: _amount,
            autofocus: true,
            keyboardType: const TextInputType.numberWithOptions(decimal: true),
            decoration: InputDecoration(
              labelText: 'Cuánto apartar',
              prefixText: widget.goal.target.currency == 'PEN' ? 'S/ ' : null,
              suffixText: widget.goal.target.currency == 'PEN' ? null : widget.goal.target.currency,
              errorText: _problem,
              helperText: suggested != null && widget.goal.targetDate != null
                  ? 'Para llegar ${onDay(widget.goal.targetDate!)}: ${suggested.format()} '
                      '${perCadence(widget.goal.pace.suggestedCadence)}'
                  : null,
            ),
            onSubmitted: (_) => _submit(),
          ),
          const SizedBox(height: 12),
          Text('El primer aporte es hoy. Lo confirmas con tu cara, y lo quitas cuando quieras.',
              style: theme.textTheme.bodySmall),
          const SizedBox(height: 20),
          FilledButton(onPressed: _submit, child: const Text('Continuar')),
        ],
      ),
    );
  }
}

/// Crear una meta: para qué, cuánto y, si quiere, para cuándo.
class NewGoalScreen extends StatefulWidget {
  const NewGoalScreen({super.key, required this.client, required this.accountId, required this.currency});

  final BffClient client;
  final String accountId;
  final String currency;

  @override
  State<NewGoalScreen> createState() => _NewGoalScreenState();
}

class _NewGoalScreenState extends State<NewGoalScreen> {
  final _name = TextEditingController();
  final _target = TextEditingController();
  DateTime? _date;
  String? _problem;
  bool _busy = false;

  @override
  void dispose() {
    _name.dispose();
    _target.dispose();
    super.dispose();
  }

  Future<void> _pickDate() async {
    final now = DateTime.now();
    final picked = await showDatePicker(
      context: context,
      initialDate: _date ?? DateTime(now.year, now.month + 3, now.day),
      firstDate: now.add(const Duration(days: 1)),
      lastDate: DateTime(now.year + 10),
    );
    if (picked != null) setState(() => _date = picked);
  }

  Future<void> _submit() async {
    final int micros;
    try {
      micros = parseAmountToMicros(_target.text);
    } on AmountFormatException catch (e) {
      setState(() => _problem = e.message);
      return;
    }
    if (micros <= 0) {
      setState(() => _problem = 'El monto tiene que ser mayor que cero');
      return;
    }
    setState(() {
      _busy = true;
      _problem = null;
    });
    try {
      final goal = await widget.client.createGoal(widget.accountId, _name.text.trim(), micros, targetDate: _date);
      if (mounted) Navigator.of(context).pop(goal);
    } on ApiError catch (e) {
      if (mounted) {
        setState(() {
          _busy = false;
          _problem = e.userMessage;
        });
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Scaffold(
      appBar: AppBar(title: const Text('Nueva meta')),
      body: ListView(
        padding: const EdgeInsets.all(AppSpacing.xl),
        children: [
          TextField(
            controller: _name,
            autofocus: true,
            maxLength: 40,
            decoration: const InputDecoration(labelText: 'Para qué', hintText: 'Viaje a Cusco, laptop, emergencias'),
            onChanged: (_) => setState(() {}),
          ),
          const SizedBox(height: 8),
          TextField(
            controller: _target,
            keyboardType: const TextInputType.numberWithOptions(decimal: true),
            decoration: InputDecoration(
              labelText: 'Cuánto quieres juntar',
              prefixText: widget.currency == 'PEN' ? 'S/ ' : null,
              suffixText: widget.currency == 'PEN' ? null : widget.currency,
              errorText: _problem,
            ),
          ),
          const SizedBox(height: 16),
          ListTile(
            contentPadding: EdgeInsets.zero,
            leading: const Icon(Icons.event_outlined),
            title: Text(_date == null ? 'Sin fecha' : 'Para ${onDay(_date!).substring(3)} de ${_date!.year}'),
            subtitle: const Text('Con fecha te decimos si llegas, y cuánto apartar'),
            trailing: _date == null
                ? TextButton(onPressed: _pickDate, child: const Text('Elegir'))
                : IconButton(onPressed: () => setState(() => _date = null), icon: const Icon(Icons.close)),
            onTap: _pickDate,
          ),
          const SizedBox(height: 24),
          FilledButton(
            onPressed: _busy || _name.text.trim().isEmpty ? null : _submit,
            child: const Text('Crear la meta'),
          ),
          const SizedBox(height: 12),
          Text('Crearla no mueve dinero. Después eliges si apartar a mano o programar aportes.',
              textAlign: TextAlign.center, style: theme.textTheme.bodySmall),
        ],
      ),
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
  Widget build(BuildContext context) => AlertDialog(
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

class _PinPrompt extends StatelessWidget {
  const _PinPrompt({required this.what});

  final String what;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Scaffold(
      appBar: AppBar(title: const Text('Confirma la regla')),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.all(AppSpacing.xl),
          children: [
            Text(what, textAlign: TextAlign.center, style: theme.textTheme.bodyLarge),
            const SizedBox(height: AppSpacing.xl),
            Text('Escribe tu clave para confirmar', textAlign: TextAlign.center, style: theme.textTheme.titleMedium),
            const SizedBox(height: AppSpacing.lg),
            PinPad(onCompleted: (pin) => Navigator.of(context).pop(pin)),
          ],
        ),
      ),
    );
  }
}
