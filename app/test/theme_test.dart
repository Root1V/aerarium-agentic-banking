import 'dart:math' as math;

import 'package:aibank_app/theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

/// Contraste de la paleta frente a WCAG 2.2 AA.
///
/// Se prueba porque es lo que se rompe en silencio: alguien aclara un color
/// "porque se ve mejor" y una parte de los usuarios deja de poder leer su
/// saldo. Con este test, un cambio de color que baje del mínimo no compila en
/// CI.

/// Luminancia relativa, según la definición de WCAG.
double _luminance(Color c) {
  double channel(double v) =>
      v <= 0.03928 ? v / 12.92 : math.pow((v + 0.055) / 1.055, 2.4).toDouble();
  return 0.2126 * channel(c.r) + 0.7152 * channel(c.g) + 0.0722 * channel(c.b);
}

double _contrast(Color a, Color b) {
  final la = _luminance(a);
  final lb = _luminance(b);
  final (hi, lo) = la > lb ? (la, lb) : (lb, la);
  return (hi + 0.05) / (lo + 0.05);
}

/// Texto normal: 4,5:1.
const _aaText = 4.5;

void main() {
  for (final (name, theme) in [('claro', appLightTheme), ('oscuro', appDarkTheme)]) {
    group('tema $name', () {
      final scheme = theme.colorScheme;
      final money = theme.extension<MoneyColors>()!;
      final container = scheme.surfaceContainer;

      final pairs = <String, (Color, Color)>{
        'texto sobre fondo': (scheme.onSurface, scheme.surface),
        'texto secundario sobre fondo': (scheme.onSurfaceVariant, scheme.surface),
        // Las tarjetas de saldo van sobre `surfaceContainer`: el saldo es lo
        // primero que se lee, y tiene que leerse ahí.
        'texto sobre tarjeta': (scheme.onSurface, container),
        'texto secundario sobre tarjeta': (scheme.onSurfaceVariant, container),
        'botón principal': (scheme.onPrimary, scheme.primary),
        'acento de marca sobre fondo': (scheme.secondary, scheme.surface),
        'ingreso sobre tarjeta': (money.positive, container),
        'error sobre fondo': (scheme.error, scheme.surface),
      };

      for (final MapEntry(key: label, value: (fg, bg)) in pairs.entries) {
        test('$label cumple AA', () {
          final ratio = _contrast(fg, bg);
          expect(ratio, greaterThanOrEqualTo(_aaText),
              reason: '$label: ${ratio.toStringAsFixed(2)}:1, mínimo $_aaText:1');
        });
      }
    });
  }

  test('los importes usan cifras tabulares', () {
    // Sin esto, las columnas de importes no se alinean.
    final features = appLightTheme.textTheme.headlineMedium?.fontFeatures ?? [];
    expect(features, contains(const FontFeature.tabularFigures()));
  });
}
