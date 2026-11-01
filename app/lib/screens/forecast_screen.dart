import 'package:flutter/material.dart';

import '../api/models.dart';
import '../forecast_text.dart';

/// El detalle de la previsión: de dónde sale cada número, y qué se puede hacer.
///
/// Todo es una estimación con lo que el banco ve de la cuenta, y la pantalla lo
/// dice. Las opciones son eso, opciones: ninguna se ejecuta desde aquí.
class ForecastScreen extends StatelessWidget {
  const ForecastScreen({super.key, required this.forecast, this.onOpenCard});

  final Forecast forecast;

  /// Lleva a la tarjeta, para pagar. Nulo si no hay tarjeta que abrir.
  final VoidCallback? onOpenCard;

  @override
  Widget build(BuildContext context) {
    final f = forecast;
    final theme = Theme.of(context);
    final warn = f.isDeficit || f.isTight;
    return Scaffold(
      appBar: AppBar(title: const Text('Previsión de saldo')),
      body: ListView(
        padding: const EdgeInsets.fromLTRB(16, 8, 16, 24),
        children: [
          Text(headline(f),
              style: theme.textTheme.titleMedium?.copyWith(
                color: f.isDeficit ? theme.colorScheme.error : null,
              )),
          const SizedBox(height: 16),
          _Line(label: 'Hoy tienes', value: f.balance.format()),
          _Line(
            label: 'Sueles gastar por día',
            value: f.dailySpend.format(),
            note: f.lowHistory ? 'Con pocos días de historia: es una estimación débil.' : null,
          ),
          if (f.card case final card?)
            _Line(
              label: card.overdue ? 'Tarjeta, vencida' : 'Tarjeta, vence ${onDay(card.dueOn)}',
              value: card.total.format(),
              note: [
                'Pago mínimo: ${card.minimum.format()}',
                if (card.reserved case final reserved?) 'Apartado para pagarla: ${reserved.format()}',
              ].join(' · '),
            ),
          if (f.income case final income?)
            _Line(
              label: 'Ingreso esperado ${onDay(income.expectedOn)}',
              value: income.amount.format(),
              note: income.synthetic ? 'Es el sueldo de prueba del sandbox, no uno tuyo.' : null,
            )
          else
            const _Line(
              label: 'Ingreso esperado',
              value: '—',
              note: 'No vemos un ingreso que se repita; miramos los próximos 30 días.',
            ),
          _Line(label: 'Lo más bajo, ${onDay(f.lowest.on)}', value: f.lowest.amount.format()),
          if (warn && f.options.isNotEmpty) ...[
            const SizedBox(height: 24),
            Text('Qué puedes hacer', style: theme.textTheme.titleMedium),
            const SizedBox(height: 8),
            for (final o in f.options)
              Card(
                child: Padding(
                  padding: const EdgeInsets.all(16),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(optionText(f, o)),
                      if (o.kind == 'pay_minimum' && onOpenCard != null) ...[
                        const SizedBox(height: 8),
                        TextButton(onPressed: onOpenCard, child: const Text('Ir a la tarjeta')),
                      ],
                    ],
                  ),
                ),
              ),
          ],
          const SizedBox(height: 24),
          Text(
            'Es una estimación con lo que vemos en tu cuenta: tu saldo, lo que sueles gastar, '
            'tu tarjeta y tu ingreso. No movemos nada por ti.',
            style: theme.textTheme.bodySmall,
          ),
        ],
      ),
    );
  }
}

class _Line extends StatelessWidget {
  const _Line({required this.label, required this.value, this.note});

  final String label;
  final String value;
  final String? note;

  @override
  Widget build(BuildContext context) {
    return ListTile(
      contentPadding: EdgeInsets.zero,
      title: Text(label),
      subtitle: note == null ? null : Text(note!),
      trailing: Text(value, style: const TextStyle(fontWeight: FontWeight.w600)),
    );
  }
}
