import 'package:flutter/material.dart';

import '../theme.dart';

/// Teclado de la clave de 6 dígitos: seis puntos y un teclado numérico propio.
///
/// No es un campo de texto a propósito: el teclado del sistema ofrece
/// autocompletar, sugerencias y portapapeles, y nada de eso debe tocar una clave.
/// Al completar los seis dígitos entrega la clave y se vacía.
class PinPad extends StatefulWidget {
  const PinPad({
    super.key,
    required this.onCompleted,
    this.enabled = true,
    this.extraKey,
  });

  final ValueChanged<String> onCompleted;
  final bool enabled;

  /// Tecla opcional a la izquierda del 0, por ejemplo volver a Face ID.
  final Widget? extraKey;

  @override
  State<PinPad> createState() => _PinPadState();
}

class _PinPadState extends State<PinPad> {
  static const length = 6;
  String _pin = '';

  void _press(String digit) {
    if (!widget.enabled || _pin.length >= length) return;
    setState(() => _pin += digit);
    if (_pin.length == length) {
      final pin = _pin;
      setState(() => _pin = '');
      widget.onCompleted(pin);
    }
  }

  void _delete() {
    if (!widget.enabled || _pin.isEmpty) return;
    setState(() => _pin = _pin.substring(0, _pin.length - 1));
  }

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;

    Widget key(String digit) => _Key(
          label: digit,
          onTap: widget.enabled ? () => _press(digit) : null,
          child: Text(digit, style: Theme.of(context).textTheme.headlineSmall),
        );

    return Column(
      mainAxisSize: MainAxisSize.min,
      children: [
        Semantics(
          label: '${_pin.length} de $length dígitos',
          child: Row(
            mainAxisAlignment: MainAxisAlignment.center,
            children: [
              for (var i = 0; i < length; i++)
                Container(
                  key: ValueKey('pin-dot-$i'),
                  margin: const EdgeInsets.symmetric(horizontal: AppSpacing.sm),
                  width: 14,
                  height: 14,
                  decoration: BoxDecoration(
                    shape: BoxShape.circle,
                    color: i < _pin.length ? scheme.primary : null,
                    border: Border.all(color: scheme.primary, width: 1.5),
                  ),
                ),
            ],
          ),
        ),
        const SizedBox(height: AppSpacing.xl),
        for (final row in const [
          ['1', '2', '3'],
          ['4', '5', '6'],
          ['7', '8', '9'],
        ])
          Row(mainAxisAlignment: MainAxisAlignment.center, children: row.map(key).toList()),
        Row(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            SizedBox.square(dimension: _Key.size, child: widget.extraKey),
            key('0'),
            _Key(
              label: 'Borrar',
              onTap: widget.enabled ? _delete : null,
              child: const Icon(Icons.backspace_outlined),
            ),
          ],
        ),
      ],
    );
  }
}

class _Key extends StatelessWidget {
  const _Key({required this.label, required this.onTap, required this.child});

  static const size = 76.0;

  final String label;
  final VoidCallback? onTap;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.all(AppSpacing.xs),
      child: SizedBox.square(
        dimension: size - AppSpacing.sm,
        child: Semantics(
          button: true,
          label: label,
          excludeSemantics: true,
          child: InkWell(
            customBorder: const CircleBorder(),
            onTap: onTap,
            child: Center(child: child),
          ),
        ),
      ),
    );
  }
}
