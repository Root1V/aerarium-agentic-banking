import 'package:flutter/material.dart';

/// Tokens de diseño de la app.
///
/// La paleta parte del bronce: *aerarium* viene de *aes*, el bronce con el que
/// Roma acuñaba moneda. El bronce se usa para la marca y los acentos; para
/// texto y acciones principales manda la tinta, que contrasta mejor. Un banco
/// que se lee mal no inspira confianza, por bonita que sea la paleta.
///
/// Los contrastes están verificados en `test/theme_test.dart` contra WCAG 2.2
/// AA (4,5:1 para texto). Si cambias un color, ese test te dice si sigue
/// siendo legible.
abstract final class AppColors {
  // Claro
  static const ink = Color(0xFF13233A);
  static const bronze = Color(0xFF8A5A1E);
  static const surface = Color(0xFFFAF8F5);
  static const surfaceContainer = Color(0xFFF1ECE4);
  static const onSurface = Color(0xFF1B1B1F);
  static const onSurfaceVariant = Color(0xFF4A4540);
  static const positive = Color(0xFF1E6B3A);
  static const error = Color(0xFFB3261E);

  // Oscuro
  static const inkDark = Color(0xFFB9C7DE);
  static const bronzeDark = Color(0xFFD9A35F);
  static const surfaceDark = Color(0xFF121418);
  static const surfaceContainerDark = Color(0xFF1E2127);
  static const onSurfaceDark = Color(0xFFECE6DD);
  static const onSurfaceVariantDark = Color(0xFFC4BCB1);
  static const positiveDark = Color(0xFF7DD3A0);
  static const errorDark = Color(0xFFFFB4AB);
}

/// Espaciado en múltiplos de 4.
abstract final class AppSpacing {
  static const xs = 4.0;
  static const sm = 8.0;
  static const md = 12.0;
  static const lg = 16.0;
  static const xl = 24.0;
  static const xxl = 32.0;
}

abstract final class AppRadius {
  static const sm = 8.0;
  static const md = 12.0;
  static const lg = 20.0;
}

/// Colores con significado monetario.
///
/// Van aparte del `ColorScheme` porque Material no tiene un rol para "dinero
/// que entra": usar `primary` para eso pintaría los ingresos del color de los
/// botones, y el verde fijo que había antes no se adaptaba al modo oscuro.
@immutable
class MoneyColors extends ThemeExtension<MoneyColors> {
  const MoneyColors({required this.positive});

  /// Dinero que entra.
  final Color positive;

  @override
  MoneyColors copyWith({Color? positive}) =>
      MoneyColors(positive: positive ?? this.positive);

  @override
  MoneyColors lerp(MoneyColors? other, double t) {
    if (other == null) return this;
    return MoneyColors(positive: Color.lerp(positive, other.positive, t)!);
  }
}

/// Cifras de ancho fijo.
///
/// En una lista de importes, los dígitos proporcionales hacen que las columnas
/// bailen y que 1.111 parezca más corto que 8.888. Con cifras tabulares los
/// importes se alinean y se comparan de un vistazo.
const _tabular = [FontFeature.tabularFigures()];

TextTheme _textTheme(TextTheme base) => base.copyWith(
      headlineMedium: base.headlineMedium?.copyWith(
        fontWeight: FontWeight.w600,
        fontFeatures: _tabular,
      ),
      titleMedium: base.titleMedium?.copyWith(fontWeight: FontWeight.w600),
      bodyMedium: base.bodyMedium?.copyWith(fontFeatures: _tabular),
      bodyLarge: base.bodyLarge?.copyWith(fontFeatures: _tabular),
    );

ThemeData _theme({
  required Brightness brightness,
  required Color primary,
  required Color secondary,
  required Color surface,
  required Color surfaceContainer,
  required Color onSurface,
  required Color onSurfaceVariant,
  required Color error,
  required Color positive,
}) {
  final scheme = ColorScheme.fromSeed(
    seedColor: AppColors.bronze,
    brightness: brightness,
  ).copyWith(
    primary: primary,
    onPrimary: brightness == Brightness.light ? Colors.white : AppColors.surfaceDark,
    secondary: secondary,
    onSecondary: brightness == Brightness.light ? Colors.white : AppColors.surfaceDark,
    surface: surface,
    surfaceContainer: surfaceContainer,
    onSurface: onSurface,
    onSurfaceVariant: onSurfaceVariant,
    error: error,
  );

  final base = ThemeData(colorScheme: scheme, useMaterial3: true);

  return base.copyWith(
    scaffoldBackgroundColor: surface,
    textTheme: _textTheme(base.textTheme),
    extensions: [MoneyColors(positive: positive)],
    appBarTheme: AppBarTheme(
      backgroundColor: surface,
      foregroundColor: onSurface,
      elevation: 0,
      scrolledUnderElevation: 0,
    ),
    cardTheme: CardThemeData(
      color: surfaceContainer,
      elevation: 0,
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(AppRadius.lg),
      ),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        minimumSize: const Size.fromHeight(48),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(AppRadius.md),
        ),
      ),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        minimumSize: const Size.fromHeight(48),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(AppRadius.md),
        ),
      ),
    ),
    inputDecorationTheme: InputDecorationTheme(
      border: OutlineInputBorder(
        borderRadius: BorderRadius.circular(AppRadius.md),
      ),
    ),
  );
}

final appLightTheme = _theme(
  brightness: Brightness.light,
  primary: AppColors.ink,
  secondary: AppColors.bronze,
  surface: AppColors.surface,
  surfaceContainer: AppColors.surfaceContainer,
  onSurface: AppColors.onSurface,
  onSurfaceVariant: AppColors.onSurfaceVariant,
  error: AppColors.error,
  positive: AppColors.positive,
);

final appDarkTheme = _theme(
  brightness: Brightness.dark,
  primary: AppColors.inkDark,
  secondary: AppColors.bronzeDark,
  surface: AppColors.surfaceDark,
  surfaceContainer: AppColors.surfaceContainerDark,
  onSurface: AppColors.onSurfaceDark,
  onSurfaceVariant: AppColors.onSurfaceVariantDark,
  error: AppColors.errorDark,
  positive: AppColors.positiveDark,
);

extension MoneyTheme on BuildContext {
  /// Los colores monetarios del tema activo.
  ///
  /// Si el tema no los trae —una pantalla montada suelta, en una prueba o en
  /// otra app— se usan los de la paleta según el brillo, en vez de fallar: un
  /// color de ingreso que falta no justifica una pantalla rota.
  MoneyColors get moneyColors {
    final theme = Theme.of(this);
    return theme.extension<MoneyColors>() ??
        MoneyColors(
          positive: theme.brightness == Brightness.dark
              ? AppColors.positiveDark
              : AppColors.positive,
        );
  }
}
