import 'api/models.dart';
import 'money.dart';

/// Una fila del historial: un movimiento suelto o un grupo de pagos seguidos a
/// la misma plataforma.
sealed class HistoryItem {
  const HistoryItem();
}

class SingleMovement extends HistoryItem {
  const SingleMovement(this.movement);
  final Movement movement;
}

/// Pagos consecutivos a una misma plataforma.
///
/// Un agente que paga $0,001 por llamada deja cientos de movimientos en una
/// tarde. Mostrarlos uno por uno entierra todo lo demás —una transferencia
/// recibida queda a cien filas de distancia— y no le dice nada a nadie: la
/// pregunta que se hace la persona es cuánto y a quién, no cada llamada.
class PaymentGroup extends HistoryItem {
  const PaymentGroup(this.merchant, this.movements, {this.continuesBeyond = false});

  final String merchant;

  /// La racha llega al final de lo cargado y hay movimientos más antiguos: puede
  /// seguir más atrás. Entonces el conteo no es "N pagos" sino "los últimos N".
  final bool continuesBeyond;

  /// Del más reciente al más antiguo, como el historial.
  final List<Movement> movements;

  int get count => movements.length;

  /// El agente que hizo los pagos, si fue uno solo. Vacío si fueron varios o si
  /// los pagos no dicen de qué agente son.
  String get agentName {
    final names = movements.map((m) => m.agentName).toSet();
    return names.length == 1 ? names.first : '';
  }

  /// Total, con signo: lo que salió del saldo.
  Money get total => Money(
        -movements.fold<int>(0, (sum, m) => sum + m.amount.amountMicros),
        movements.first.amount.currency,
      );
}

/// Agrupa los pagos consecutivos a una misma plataforma.
///
/// Solo agrupa lo CONSECUTIVO: si entre dos pagos a Mercatus llegó una
/// transferencia, quedan dos grupos con la transferencia en medio. Fundirlos
/// la sacaría de su sitio, y el historial dejaría de ser un historial.
///
/// Trabaja sobre la página que se tiene. Si la última racha toca el final de lo
/// cargado y `hasMore` dice que hay más atrás, el grupo se marca: puede seguir, y
/// rotularlo "10 pagos" cuando hoy hubo 121 sería afirmar algo falso.
List<HistoryItem> groupHistory(List<Movement> movements, {bool hasMore = false}) {
  final out = <HistoryItem>[];
  var run = <Movement>[];

  void flush({bool atEnd = false}) {
    if (run.length >= 2) {
      out.add(PaymentGroup(run.first.merchant, run, continuesBeyond: atEnd && hasMore));
    } else if (run.length == 1) {
      out.add(SingleMovement(run.first));
    }
    run = <Movement>[];
  }

  for (final m in movements) {
    final continues = m.isPlatformPayment &&
        run.isNotEmpty &&
        run.first.merchant == m.merchant &&
        run.first.amount.currency == m.amount.currency;
    if (continues) {
      run.add(m);
      continue;
    }
    flush();
    if (m.isPlatformPayment) {
      run.add(m);
    } else {
      out.add(SingleMovement(m));
    }
  }
  flush(atEnd: true);
  return out;
}
