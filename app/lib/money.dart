/// Representación de dinero en el cliente.
///
/// Regla que atraviesa toda la app: el dinero NUNCA es `double`. Se guarda como
/// entero en unidades menores (centavos) igual que en el servidor, y solo se
/// convierte a texto para mostrarlo. Un `double` de 0.1 + 0.2 no da 0.3, y un
/// saldo mal redondeado en pantalla es una llamada al servicio al cliente.
library;

class Money {
  /// Importe en unidades menores. Puede ser negativo (un movimiento que resta).
  final int amountMinor;

  /// Moneda ISO-4217.
  final String currency;

  const Money(this.amountMinor, this.currency);

  /// Lee un importe del BFF.
  ///
  /// Exige que el importe llegue como entero: si el servidor mandara un decimal,
  /// es preferible fallar de forma visible a redondear en silencio.
  factory Money.fromJson(Map<String, dynamic> json) {
    final raw = json['amount_minor'];
    if (raw is! int) {
      throw FormatException(
        'el importe debe llegar como entero en unidades menores, llegó: $raw',
      );
    }
    final currency = json['currency'];
    if (currency is! String || currency.length != 3) {
      throw FormatException('moneda inválida: $currency');
    }
    return Money(raw, currency);
  }

  Money operator -() => Money(-amountMinor, currency);

  bool get isNegative => amountMinor < 0;

  /// Formatea para mostrar, con separador de miles y dos decimales.
  ///
  /// Toda la aritmética es entera: se separan unidades y centavos con división y
  /// módulo, sin pasar en ningún momento por coma flotante.
  String format({bool withSign = false}) {
    final negative = amountMinor < 0;
    final absolute = amountMinor.abs();

    final units = absolute ~/ 100;
    final cents = absolute % 100;

    final sign = negative ? '-' : (withSign ? '+' : '');
    return '$sign${_groupThousands(units)},${cents.toString().padLeft(2, '0')} $currency';
  }

  static String _groupThousands(int value) {
    final digits = value.toString();
    final buffer = StringBuffer();
    for (var i = 0; i < digits.length; i++) {
      if (i > 0 && (digits.length - i) % 3 == 0) buffer.write('.');
      buffer.write(digits[i]);
    }
    return buffer.toString();
  }

  @override
  String toString() => format();

  @override
  bool operator ==(Object other) =>
      other is Money &&
      other.amountMinor == amountMinor &&
      other.currency == currency;

  @override
  int get hashCode => Object.hash(amountMinor, currency);
}

/// Error al interpretar un importe escrito por la persona usuaria.
class AmountFormatException implements Exception {
  final String message;
  const AmountFormatException(this.message);
  @override
  String toString() => message;
}

/// Convierte lo que se escribe en pantalla a unidades menores.
///
/// Se hace con manipulación de texto y aritmética entera, no con `double.parse`:
/// convertir "0.29" a double y multiplicar por 100 puede dar 28.999... y truncar
/// a 28 centavos. Con dos decimales y montos grandes eso deja de ser teórico.
int parseAmountToMinor(String input) {
  final cleaned = input.trim().replaceAll(' ', '');
  if (cleaned.isEmpty) {
    throw const AmountFormatException('Escribe un monto');
  }

  // En la región conviven "1.500,25" y "1,500.25", así que ni el punto ni la coma
  // identifican por sí solos al separador decimal. Lo decide cuántos dígitos le
  // siguen al ÚLTIMO separador: uno o dos son centavos; tres son miles.
  // Sin esta regla, "25.50" se leería como veinticinco mil cincuenta.
  final lastSeparator = [cleaned.lastIndexOf('.'), cleaned.lastIndexOf(',')]
      .reduce((a, b) => a > b ? a : b);

  final String unitsText;
  final String centsText;

  if (lastSeparator < 0) {
    unitsText = cleaned;
    centsText = '';
  } else {
    final digitsAfter = cleaned.length - lastSeparator - 1;
    if (digitsAfter == 1 || digitsAfter == 2) {
      unitsText = cleaned.substring(0, lastSeparator);
      centsText = cleaned.substring(lastSeparator + 1);
    } else if (digitsAfter == 3) {
      // Separador de miles: el monto no tiene decimales.
      unitsText = cleaned;
      centsText = '';
    } else {
      throw const AmountFormatException('Como máximo dos decimales');
    }
  }

  // Los separadores restantes son agrupadores de miles.
  final units = unitsText.replaceAll('.', '').replaceAll(',', '');
  final digitsOnly = RegExp(r'^\d*$');
  if (!digitsOnly.hasMatch(units) || !digitsOnly.hasMatch(centsText)) {
    throw const AmountFormatException('El monto solo puede tener números');
  }
  if (units.isEmpty && centsText.isEmpty) {
    throw const AmountFormatException('El monto no es válido');
  }

  final unitsValue = units.isEmpty ? 0 : int.parse(units);
  final cents = centsText.isEmpty ? 0 : int.parse(centsText.padRight(2, '0'));

  final total = unitsValue * 100 + cents;
  if (total <= 0) {
    throw const AmountFormatException('El monto debe ser mayor a cero');
  }
  return total;
}
