import 'dart:math';

import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/errors.dart';
import 'api/models.dart';
import 'auth/device_keys.dart';
import 'money.dart';

/// Estado de un envío de dinero.
///
/// `needsPin`: el banco pidió confirmar la operación y la cara no pudo —se
/// canceló a medias, se prefirió la clave o la biometría está bloqueada—. Toca
/// escribir la clave.
enum TransferStatus { idle, sending, needsPin, success, failed }

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
  TransferController(this._client, {this.confirmer});

  final BffClient _client;

  /// Firma con el teléfono las operaciones que el banco pide confirmar. Sin él
  /// —una sesión de desarrollo— esas operaciones no se pueden hacer.
  final TransferConfirmer? confirmer;

  /// Lo que se está confirmando, para mostrárselo a la persona.
  ConfirmationChallenge? _challenge;
  ConfirmationChallenge? get challenge => _challenge;

  /// La confirmación va por la clave porque la cara no pudo. Mientras se pide
  /// la cara, la pantalla no muestra el teclado: confundiría.
  bool _withPin = false;
  bool get confirmingWithPin => _withPin;
  final Random _random = Random.secure();

  TransferStatus _status = TransferStatus.idle;
  ApiError? _error;
  TransferResult? _result;

  // Operación en curso: sus datos y, sobre todo, su clave estable.
  String? _idempotencyKey;
  String? _fromAccountId;
  String? _toAccountId;
  int? _amountMicros;
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
    required int amountMicros,
    required String currency,
    String description = '',
  }) async {
    _idempotencyKey = _newKey();
    _fromAccountId = fromAccountId;
    _toAccountId = toAccountId;
    _amountMicros = amountMicros;
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
    _challenge = null;
    _withPin = false;
    _status = TransferStatus.idle;
    _error = null;
    _result = null;
    _idempotencyKey = null;
    _fromAccountId = null;
    _toAccountId = null;
    _amountMicros = null;
    _currency = null;
    _description = '';
    notifyListeners();
  }

  /// Confirma con la clave de 6 dígitos la operación que el banco pidió confirmar.
  ///
  /// Pide un desafío nuevo en cada intento: el banco consume el anterior al
  /// comprobarlo, acierte o no la clave.
  Future<void> confirmWithPin(String pin) async {
    final confirmer = this.confirmer;
    if (confirmer == null || _idempotencyKey == null) return;
    _status = TransferStatus.sending;
    _error = null;
    notifyListeners();

    try {
      _result = await _transfer();
      // Dejó de pedir confirmación (la política cambió): ya se envió.
      _status = TransferStatus.success;
      _challenge = null;
      notifyListeners();
      return;
    } on ApiError catch (e) {
      if (e.code != ApiErrorCode.confirmationRequired) {
        _fail(e);
        return;
      }
      _challenge = ConfirmationChallenge.fromJson(e.details);
    }

    try {
      final confirmation = await confirmer.confirm(_challenge!, DeviceUnlock.pin, pin: pin, reason: '');
      await _execute(confirmation: confirmation);
    } on DeviceKeyException {
      _fail(const ApiError(ApiErrorCode.confirmationRejected, 'el teléfono no firmó'));
    }
  }

  /// Abandona la confirmación. No se envió nada.
  void cancelConfirmation() {
    _challenge = null;
    _withPin = false;
    _error = null;
    _status = TransferStatus.idle;
    notifyListeners();
  }

  Future<void> _execute({TransferConfirmation? confirmation}) async {
    _status = TransferStatus.sending;
    _error = null;
    notifyListeners();

    try {
      _result = await _transfer(confirmation: confirmation);
      _challenge = null;
      _withPin = false;
      _status = TransferStatus.success;
      notifyListeners();
    } on ApiError catch (e) {
      if (e.code == ApiErrorCode.confirmationRequired && confirmation == null) {
        await _confirm(ConfirmationChallenge.fromJson(e.details));
        return;
      }
      _fail(e);
    }
  }

  /// El banco pidió confirmar: primero con la cara, que es lo que la persona
  /// espera; si la cara no puede, con la clave.
  Future<void> _confirm(ConfirmationChallenge challenge) async {
    _challenge = challenge;
    final confirmer = this.confirmer;
    if (confirmer == null) {
      _fail(const ApiError(ApiErrorCode.confirmationUnavailable, 'sin teléfono vinculado'));
      return;
    }
    final TransferConfirmation confirmation;
    try {
      confirmation = await confirmer.confirm(
        challenge,
        DeviceUnlock.biometric,
        // Lo que la persona ve en el diálogo del sistema: qué está aprobando.
        reason: 'Envías ${Money(challenge.amountMicros, challenge.currency).format()} a ${challenge.toName}',
      );
    } on DeviceKeyException catch (e) {
      if (e.failure == DeviceKeyFailure.cancelled) {
        // Cerró el diálogo: no quiere enviar. Nada salió.
        cancelConfirmation();
        return;
      }
      _withPin = true;
      _status = TransferStatus.needsPin;
      notifyListeners();
      return;
    }
    await _execute(confirmation: confirmation);
  }

  void _fail(ApiError e) {
    _error = e;
    // Una clave mala deja seguir intentando con la clave, hasta el bloqueo.
    _status = e.code == ApiErrorCode.pinIncorrect ? TransferStatus.needsPin : TransferStatus.failed;
    notifyListeners();
  }

  Future<TransferResult> _transfer({TransferConfirmation? confirmation}) => _client.transfer(
        idempotencyKey: _idempotencyKey!,
        fromAccountId: _fromAccountId!,
        toAccountId: _toAccountId!,
        amountMicros: _amountMicros!,
        currency: _currency!,
        description: _description,
        confirmation: confirmation,
      );

  /// Clave aleatoria de 128 bits, con generador criptográfico: no debe poder
  /// adivinarse ni repetirse entre operaciones.
  String _newKey() {
    final bytes = List<int>.generate(16, (_) => _random.nextInt(256));
    return bytes.map((b) => b.toRadixString(16).padLeft(2, '0')).join();
  }
}
