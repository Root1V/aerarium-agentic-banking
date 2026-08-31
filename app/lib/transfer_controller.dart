import 'dart:math';

import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/errors.dart';
import 'api/models.dart';

/// Estado de un envío de dinero.
enum TransferStatus { idle, sending, success, failed }

/// Orquesta el envío de dinero desde la app.
///
/// Su razón de existir es una sola: **garantizar que un reintento no envíe el
/// dinero dos veces**.
///
/// En un móvil, un envío que falla por red deja el resultado en duda — el
/// servidor pudo haberlo procesado igual. Si cada reintento generase una clave
/// nueva, el servidor lo vería como una operación distinta y cobraría de nuevo.
/// Por eso la clave se genera UNA vez por operación de negocio y se conserva
/// mientras esa operación siga viva; solo se descarta al confirmarse o al
/// empezar un envío distinto.
class TransferController extends ChangeNotifier {
  TransferController(this._client);

  final BffClient _client;
  final Random _random = Random.secure();

  TransferStatus _status = TransferStatus.idle;
  ApiError? _error;
  TransferResult? _result;

  // Operación en curso: sus datos y, sobre todo, su clave estable.
  String? _idempotencyKey;
  String? _fromAccountId;
  String? _toAccountId;
  int? _amountMinor;
  String? _currency;
  String _description = '';

  TransferStatus get status => _status;
  ApiError? get error => _error;
  TransferResult? get result => _result;

  /// Visible para pruebas: permite comprobar que un reintento no cambia la clave.
  @visibleForTesting
  String? get idempotencyKey => _idempotencyKey;

  bool get canRetry =>
      _status == TransferStatus.failed && (_error?.isRetryable ?? false);

  /// Inicia un envío. Genera una clave nueva: es una operación nueva.
  Future<void> send({
    required String fromAccountId,
    required String toAccountId,
    required int amountMinor,
    required String currency,
    String description = '',
  }) async {
    _idempotencyKey = _newKey();
    _fromAccountId = fromAccountId;
    _toAccountId = toAccountId;
    _amountMinor = amountMinor;
    _currency = currency;
    _description = description;
    await _execute();
  }

  /// Reintenta el envío que falló, CON LA MISMA CLAVE.
  ///
  /// Si el servidor ya lo había procesado, responderá que es un duplicado y el
  /// dinero saldrá una sola vez.
  Future<void> retry() async {
    if (_idempotencyKey == null) return;
    await _execute();
  }

  /// Limpia el estado para empezar de cero. Descarta la clave: lo siguiente que
  /// se envíe será, deliberadamente, otra operación.
  void reset() {
    _status = TransferStatus.idle;
    _error = null;
    _result = null;
    _idempotencyKey = null;
    _fromAccountId = null;
    _toAccountId = null;
    _amountMinor = null;
    _currency = null;
    _description = '';
    notifyListeners();
  }

  Future<void> _execute() async {
    _status = TransferStatus.sending;
    _error = null;
    notifyListeners();

    try {
      _result = await _client.transfer(
        idempotencyKey: _idempotencyKey!,
        fromAccountId: _fromAccountId!,
        toAccountId: _toAccountId!,
        amountMinor: _amountMinor!,
        currency: _currency!,
        description: _description,
      );
      _status = TransferStatus.success;
    } on ApiError catch (e) {
      _error = e;
      _status = TransferStatus.failed;
    } finally {
      notifyListeners();
    }
  }

  /// Clave aleatoria de 128 bits, con generador criptográfico: no debe poder
  /// adivinarse ni repetirse entre operaciones.
  String _newKey() {
    final bytes = List<int>.generate(16, (_) => _random.nextInt(256));
    return bytes.map((b) => b.toRadixString(16).padLeft(2, '0')).join();
  }
}
