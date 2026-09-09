import 'package:aibank_app/money.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('formato', () {
    test('separa miles y muestra dos decimales', () {
      expect(const Money(1500000000, 'USD').format(), '1.500,00 USD');
      expect(const Money(50000, 'USD').format(), '0,05 USD');
      expect(const Money(1000000, 'USD').format(), '1,00 USD');
      expect(const Money(1234567890000, 'PEN').format(), '1.234.567,89 PEN');
    });

    test('los negativos llevan signo', () {
      expect(const Money(-25500000, 'USD').format(), '-25,50 USD');
    });

    test('withSign muestra el signo en los positivos', () {
      expect(const Money(25500000, 'USD').format(withSign: true), '+25,50 USD');
      expect(const Money(-25500000, 'USD').format(withSign: true), '-25,50 USD');
    });

    // La aritmética entera no acumula el error que sí tendría un double.
    test('formatea sin perder centavos en importes grandes', () {
      expect(
        const Money(9999999999990000, 'USD').format(),
        '9.999.999.999,99 USD',
      );
    });

    // La razón de ser de la escala 6: con dos decimales fijos, un cobro de
    // $0,001 se mostraría como "0,00" y desaparecería de la pantalla.
    test('un importe con fracción de centavo no se muestra como cero', () {
      expect(const Money(1000, 'USD').format(), '0,001 USD');
      expect(const Money(1, 'USD').format(), '0,000001 USD');
      expect(const Money(1234567, 'USD').format(), '1,234567 USD');
      expect(const Money(-1000, 'USD').format(), '-0,001 USD');
    });

    test('los decimales de más solo aparecen cuando aportan algo', () {
      expect(const Money(10000, 'USD').isWholeCents, isTrue);
      expect(const Money(10000, 'USD').format(), '0,01 USD');
      expect(const Money(10001, 'USD').isWholeCents, isFalse);
      expect(const Money(10001, 'USD').format(), '0,010001 USD');
    });
  });

  group('lectura desde el servidor', () {
    test('acepta un importe entero', () {
      final money = Money.fromJson({'amount_micros': 12345, 'currency': 'USD'});
      expect(money.amountMicros, 12345);
      expect(money.currency, 'USD');
    });

    // Antes redondear en silencio, fallar de forma visible.
    test('rechaza un importe decimal', () {
      expect(
        () => Money.fromJson({'amount_micros': 123.45, 'currency': 'USD'}),
        throwsA(isA<FormatException>()),
      );
    });

    test('rechaza una moneda inválida', () {
      expect(
        () => Money.fromJson({'amount_micros': 1, 'currency': 'DOLARES'}),
        throwsA(isA<FormatException>()),
      );
    });
  });

  group('lectura de lo que se escribe en pantalla', () {
    test('interpreta montos con coma o punto decimal', () {
      expect(parseAmountToMicros('25,50'), 25500000);
      expect(parseAmountToMicros('25.50'), 25500000);
      expect(parseAmountToMicros('100'), 100000000);
      expect(parseAmountToMicros(' 7,05 '), 7050000);
    });

    // La regla que resuelve la ambigüedad regional: manda cuántos dígitos siguen
    // al último separador, no cuál es el carácter.
    test('entiende ambas convenciones de miles y decimales', () {
      expect(parseAmountToMicros('1.500,25'), 1500250000);
      expect(parseAmountToMicros('1,500.25'), 1500250000);
      expect(parseAmountToMicros('1.500'), 1500000000, reason: 'tres dígitos = miles');
      expect(parseAmountToMicros('1,500'), 1500000000);
      expect(parseAmountToMicros('1.234.567,89'), 1234567890000);
    });

    test('completa un solo decimal', () {
      expect(parseAmountToMicros('3,5'), 3500000);
    });

    // "0.001" no es "cero mil uno": un grupo de miles nunca va precedido de un
    // cero solo, así que ahí los tres dígitos son decimales.
    test('un cero solo delante del separador desambigua los tres dígitos', () {
      expect(parseAmountToMicros('0,001'), 1000);
      expect(parseAmountToMicros('0.001'), 1000);
      expect(parseAmountToMicros('1,000'), 1000000000, reason: 'sigue siendo miles');
    });

    test('acepta hasta seis decimales, la escala del ledger', () {
      expect(parseAmountToMicros('1,2345'), 1234500);
      expect(parseAmountToMicros('0,123456'), 123456);
      expect(parseAmountToMicros('2,000001'), 2000001);
    });

    // El caso que rompe la conversión por double: 0.29 * 100 puede dar 28.999...
    test('no pierde un centavo en los importes conflictivos', () {
      expect(parseAmountToMicros('0,29'), 290000);
      expect(parseAmountToMicros('1,15'), 1150000);
      expect(parseAmountToMicros('8,20'), 8200000);
      expect(parseAmountToMicros('70,07'), 70070000);
    });

    test('rechaza montos inválidos', () {
      expect(() => parseAmountToMicros(''), throwsA(isA<AmountFormatException>()));
      expect(() => parseAmountToMicros('abc'), throwsA(isA<AmountFormatException>()));
      expect(() => parseAmountToMicros('0'), throwsA(isA<AmountFormatException>()));
      expect(() => parseAmountToMicros('-5'), throwsA(isA<AmountFormatException>()));
      // Siete decimales no se pueden representar: se rechazan en vez de truncarse.
      expect(
        () => parseAmountToMicros('1,1234567'),
        throwsA(isA<AmountFormatException>()),
      );
      expect(() => parseAmountToMicros('1,'), throwsA(isA<AmountFormatException>()));
    });
  });
}
