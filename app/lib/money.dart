/// Representación de dinero en el cliente.
///
/// Regla que atraviesa toda la app: el dinero NUNCA es `double`. Se guarda como
/// entero en micras —millonésimas (10⁻⁶) de la unidad mayor— igual que en el
/// servidor, y solo se convierte a texto para mostrarlo. Un `double` de 0.1 + 0.2
/// no da 0.3, y un saldo mal redondeado en pantalla es una llamada al servicio al
/// cliente.
///
/// La escala del servidor es 6 porque el banco liquida pagos entre agentes de
/// software, donde $0,001 por operación es un precio normal. **Esta app le sigue
/// mostrando dos decimales a una persona**: la escala es de almacenamiento, no de
/// presentación. Lo único que cambia es que un importe con fracción de centavo ya
/// no se puede redondear a cero sin que se note — ver [Money.format].
library;

/// Micras por unidad mayor: 1 USD = 1.000.000 micras.
const int microsPerUnit = 1000000;

/// Micras por centavo.
const int microsPerCent = 10000;

class Money {
  /// Importe en micras. Puede ser negativo (un movimiento que resta).
  final int amountMicros;

  /// Moneda ISO-4217.
  final String currency;

  const Money(this.amountMicros, this.currency);

  /// Lee un importe del BFF.
  ///
  /// Exige que el importe llegue como entero: si el servidor mandara un decimal,
  /// es preferible fallar de forma visible a redondear en silencio.
  factory Money.fromJson(Map<String, dynamic> json) {
    final raw = json['amount_micros'];
    if (raw is! int) {
      throw FormatException(
        'el importe debe llegar como entero en micras, llegó: $raw',
      );
    }
    final currency = json['currency'];
    if (currency is! String || currency.length != 3) {
      throw FormatException('moneda inválida: $currency');
    }
    return Money(raw, currency);
  }

  Money operator -() => Money(-amountMicros, currency);

  bool get isNegative => amountMicros < 0;

  /// `true` si el importe cae exactamente en un centavo.
  bool get isWholeCents => amountMicros % microsPerCent == 0;

  /// Formatea para mostrar, con separador de miles.
  ///
  /// Dos decimales, que es lo que una persona espera ver. **Salvo que el importe
  /// tenga fracción de centavo**: en ese caso se muestran los decimales que hagan
  /// falta, hasta seis.
  ///
  /// Esa excepción es deliberada y es la razón de ser de toda la escala. Un cobro
  /// de $0,001 formateado a dos decimales se lee "0,00" — el importe desaparece
  /// de la pantalla justo en el caso que el banco existe para soportar. Un número
  /// con más decimales de los habituales llama la atención; un cero silencioso,
  /// no. En la app de consumo esta rama no se ejecuta nunca, porque los importes
  /// vienen de rieles que cuentan en centavos.
  ///
  /// Toda la aritmética es entera: se separan unidades y fracción con división y
  /// módulo, sin pasar en ningún momento por coma flotante.
  String format({bool withSign = false}) {
    final negative = amountMicros < 0;
    final absolute = amountMicros.abs();

    final units = absolute ~/ microsPerUnit;
    final fraction = absolute % microsPerUnit;

    final sign = negative ? '-' : (withSign ? '+' : '');
    return '$sign${_groupThousands(units)},${_formatFraction(fraction)} $currency';
  }

  /// Convierte la parte fraccionaria (0–999999 micras) a texto.
  ///
  /// Siempre al menos dos dígitos; los otros cuatro solo si alguno es distinto de
  /// cero, para no llenar la pantalla de ceros que no dicen nada.
  static String _formatFraction(int fraction) {
    final digits = fraction.toString().padLeft(6, '0');
    var end = digits.length;
    while (end > 2 && digits[end - 1] == '0') {
      end--;
    }
    return digits.substring(0, end);
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
      other.amountMicros == amountMicros &&
      other.currency == currency;

  @override
  int get hashCode => Object.hash(amountMicros, currency);
}

/// Error al interpretar un importe escrito por la persona usuaria.
class AmountFormatException implements Exception {
  final String message;
  const AmountFormatException(this.message);
  @override
  String toString() => message;
}

/// Máximo de decimales que se aceptan al escribir un importe: la escala del
/// ledger. Más dígitos no se pueden representar y por eso se rechazan en vez de
/// truncarse.
const int _maxDecimals = 6;

/// Convierte lo que se escribe en pantalla a micras.
///
/// Se hace con manipulación de texto y aritmética entera, no con `double.parse`:
/// convertir "0.29" a double y multiplicar puede dar 28.999... y truncar a 28
/// centavos. Con montos grandes eso deja de ser teórico.
int parseAmountToMicros(String input) {
  final cleaned = input.trim().replaceAll(' ', '');
  if (cleaned.isEmpty) {
    throw const AmountFormatException('Escribe un monto');
  }

  // En la región conviven "1.500,25" y "1,500.25", así que ni el punto ni la coma
  // identifican por sí solos al separador decimal. Lo decide cuántos dígitos le
  // siguen al ÚLTIMO separador: exactamente tres son miles, cualquier otra
  // cantidad son decimales. Sin esta regla, "25.50" se leería como veinticinco
  // mil cincuenta.
  final lastSeparator = [cleaned.lastIndexOf('.'), cleaned.lastIndexOf(',')]
      .reduce((a, b) => a > b ? a : b);

  final String unitsText;
  final String fractionText;

  if (lastSeparator < 0) {
    unitsText = cleaned;
    fractionText = '';
  } else {
    final digitsAfter = cleaned.length - lastSeparator - 1;
    final head = cleaned.substring(0, lastSeparator);
    // Un grupo de miles nunca va precedido de un cero solo: "0.001" no es
    // "cero mil uno". Cuando la parte entera es exactamente 0, tres dígitos son
    // decimales — que ahora, con escala 6, sí se pueden representar.
    final headIsBareZero = head == '0';

    if (digitsAfter == 0) {
      throw const AmountFormatException('El monto no es válido');
    } else if (digitsAfter == 3 && !headIsBareZero) {
      // Separador de miles: el monto no tiene decimales.
      unitsText = cleaned;
      fractionText = '';
    } else if (digitsAfter >= 1 && digitsAfter <= _maxDecimals) {
      unitsText = head;
      fractionText = cleaned.substring(lastSeparator + 1);
    } else {
      throw const AmountFormatException('Como máximo seis decimales');
    }
  }

  // Los separadores restantes son agrupadores de miles.
  final units = unitsText.replaceAll('.', '').replaceAll(',', '');
  final digitsOnly = RegExp(r'^\d*$');
  if (!digitsOnly.hasMatch(units) || !digitsOnly.hasMatch(fractionText)) {
    throw const AmountFormatException('El monto solo puede tener números');
  }
  if (units.isEmpty && fractionText.isEmpty) {
    throw const AmountFormatException('El monto no es válido');
  }

  final unitsValue = units.isEmpty ? 0 : int.parse(units);
  // Se completa a seis dígitos por la derecha: "5" son 5 décimas, no 5 micras.
  final fraction = fractionText.isEmpty
      ? 0
      : int.parse(fractionText.padRight(_maxDecimals, '0'));

  final total = unitsValue * microsPerUnit + fraction;
  if (total <= 0) {
    throw const AmountFormatException('El monto debe ser mayor a cero');
  }
  return total;
}
