import 'package:aibank_app/money.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('formato', () {
    test('separa miles y siempre muestra dos decimales', () {
      expect(const Money(150000, 'USD').format(), '1.500,00 USD');
      expect(const Money(5, 'USD').format(), '0,05 USD');
      expect(const Money(100, 'USD').format(), '1,00 USD');
      expect(const Money(123456789, 'PEN').format(), '1.234.567,89 PEN');
    });

    test('los negativos llevan signo', () {
      expect(const Money(-2550, 'USD').format(), '-25,50 USD');
    });

    test('withSign muestra el signo en los positivos', () {
      expect(const Money(2550, 'USD').format(withSign: true), '+25,50 USD');
      expect(const Money(-2550, 'USD').format(withSign: true), '-25,50 USD');
    });

    // La aritmética entera no acumula el error que sí tendría un double.
    test('formatea sin perder centavos en importes grandes', () {
      expect(const Money(999999999999, 'USD').format(), '9.999.999.999,99 USD');
    });
  });

  group('lectura desde el servidor', () {
    test('acepta un importe entero', () {
      final money = Money.fromJson({'amount_minor': 12345, 'currency': 'USD'});
      expect(money.amountMinor, 12345);
      expect(money.currency, 'USD');
    });

    // Antes redondear en silencio, fallar de forma visible.
    test('rechaza un importe decimal', () {
      expect(
        () => Money.fromJson({'amount_minor': 123.45, 'currency': 'USD'}),
        throwsA(isA<FormatException>()),
      );
    });

    test('rechaza una moneda inválida', () {
      expect(
        () => Money.fromJson({'amount_minor': 1, 'currency': 'DOLARES'}),
        throwsA(isA<FormatException>()),
      );
    });
  });

  group('lectura de lo que se escribe en pantalla', () {
    test('interpreta montos con coma o punto decimal', () {
      expect(parseAmountToMinor('25,50'), 2550);
      expect(parseAmountToMinor('25.50'), 2550);
      expect(parseAmountToMinor('100'), 10000);
      expect(parseAmountToMinor(' 7,05 '), 705);
    });

    // La regla que resuelve la ambigüedad regional: manda cuántos dígitos siguen
    // al último separador, no cuál es el carácter.
    test('entiende ambas convenciones de miles y decimales', () {
      expect(parseAmountToMinor('1.500,25'), 150025);
      expect(parseAmountToMinor('1,500.25'), 150025);
      expect(parseAmountToMinor('1.500'), 150000, reason: 'tres dígitos = miles');
      expect(parseAmountToMinor('1,500'), 150000);
      expect(parseAmountToMinor('1.234.567,89'), 123456789);
    });

    test('completa un solo decimal', () {
      expect(parseAmountToMinor('3,5'), 350);
    });

    // El caso que rompe la conversión por double: 0.29 * 100 puede dar 28.999...
    test('no pierde un centavo en los importes conflictivos', () {
      expect(parseAmountToMinor('0,29'), 29);
      expect(parseAmountToMinor('1,15'), 115);
      expect(parseAmountToMinor('8,20'), 820);
      expect(parseAmountToMinor('70,07'), 7007);
    });

    test('rechaza montos inválidos', () {
      expect(() => parseAmountToMinor(''), throwsA(isA<AmountFormatException>()));
      expect(() => parseAmountToMinor('abc'), throwsA(isA<AmountFormatException>()));
      expect(() => parseAmountToMinor('0'), throwsA(isA<AmountFormatException>()));
      expect(() => parseAmountToMinor('-5'), throwsA(isA<AmountFormatException>()));
      // Cuatro dígitos tras el separador no son ni centavos ni miles.
      expect(() => parseAmountToMinor('1,2345'), throwsA(isA<AmountFormatException>()));
      expect(() => parseAmountToMinor('1,'), throwsA(isA<AmountFormatException>()));
    });
  });
}
