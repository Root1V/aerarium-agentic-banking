import 'dart:convert';

import 'package:aibank_app/api/client.dart';
import 'package:aibank_app/api/errors.dart';
import 'package:aibank_app/transfer_controller.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// Registra las peticiones que la app envía, para poder afirmar sobre ellas.
class _Recorder {
  final List<http.Request> requests = [];

  List<String> get idempotencyKeys =>
      requests.map((r) => r.headers['Idempotency-Key'] ?? '').toList();
}

/// Construye un cliente cuyas respuestas se programan de antemano.
(BffClient, _Recorder) clientWith(List<http.Response> Function(int attempt) responses) {
  final recorder = _Recorder();
  var attempt = 0;

  final mock = MockClient((request) async {
    recorder.requests.add(request);
    final list = responses(attempt);
    final response = list[attempt.clamp(0, list.length - 1)];
    attempt++;
    return response;
  });

  return (
    BffClient(
      baseUrl: Uri.parse('http://bff.test'),
      token: () => 'token-de-prueba',
      httpClient: mock,
    ),
    recorder
  );
}

http.Response ok({String transactionId = 'tx-1', bool duplicate = false}) =>
    http.Response(
      jsonEncode({'transaction_id': transactionId, 'duplicate': duplicate}),
      201,
    );

http.Response failure(int status, String code, String message) =>
    http.Response(jsonEncode({'code': code, 'message': message}), status);

void main() {
  group('idempotencia', () {
    // La garantía central del envío desde un móvil: si el primer intento cayó por
    // red, el servidor PUDO haberlo procesado. Reintentar con una clave nueva lo
    // convertiría en una operación distinta y cobraría dos veces.
    test('un reintento conserva la clave del intento fallido', () async {
      final (client, recorder) = clientWith((attempt) => [
            http.Response('', 503), // primer intento: servicio caído
            ok(duplicate: true), // el reintento descubre que ya estaba hecho
          ]);
      final controller = TransferController(client);

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 12550,
        currency: 'USD',
      );
      expect(controller.status, TransferStatus.failed);
      final firstKey = controller.idempotencyKey;
      expect(firstKey, isNotNull);

      await controller.retry();

      expect(controller.status, TransferStatus.success);
      expect(controller.idempotencyKey, firstKey,
          reason: 'el reintento no puede generar una clave nueva');
      expect(recorder.idempotencyKeys, [firstKey, firstKey]);
      expect(controller.result!.duplicate, isTrue);
    });

    test('varios reintentos siguen usando la misma clave', () async {
      final (client, recorder) = clientWith((_) => [http.Response('', 503)]);
      final controller = TransferController(client);

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 100,
        currency: 'USD',
      );
      await controller.retry();
      await controller.retry();

      expect(recorder.idempotencyKeys.toSet().length, 1,
          reason: 'los tres intentos son la misma operación');
    });

    test('un envío nuevo estrena clave', () async {
      final (client, recorder) = clientWith((_) => [ok()]);
      final controller = TransferController(client);

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 100,
        currency: 'USD',
      );
      final first = controller.idempotencyKey;

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 200,
        currency: 'USD',
      );

      expect(controller.idempotencyKey, isNot(first),
          reason: 'son dos envíos distintos, no un reintento');
      expect(recorder.idempotencyKeys.toSet().length, 2);
    });

    test('reset descarta la clave', () async {
      final (client, _) = clientWith((_) => [http.Response('', 503)]);
      final controller = TransferController(client);

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 100,
        currency: 'USD',
      );
      controller.reset();

      expect(controller.idempotencyKey, isNull);
      expect(controller.status, TransferStatus.idle);
    });

    test('la clave no se repite entre operaciones', () async {
      final (client, recorder) = clientWith((_) => [ok()]);
      final controller = TransferController(client);

      for (var i = 0; i < 20; i++) {
        await controller.send(
          fromAccountId: 'origen',
          toAccountId: 'destino',
          amountMinor: 100,
          currency: 'USD',
        );
      }

      expect(recorder.idempotencyKeys.toSet().length, 20);
    });
  });

  group('errores', () {
    test('el saldo insuficiente NO ofrece reintentar', () async {
      final (client, _) = clientWith(
          (_) => [failure(422, 'insufficient_funds', 'saldo insuficiente')]);
      final controller = TransferController(client);

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 999999,
        currency: 'USD',
      );

      expect(controller.status, TransferStatus.failed);
      expect(controller.error!.code, ApiErrorCode.insufficientFunds);
      expect(controller.canRetry, isFalse,
          reason: 'reintentar no crea saldo: ofrecerlo invita a insistir en vano');
      expect(controller.error!.userMessage, 'No tienes saldo suficiente.');
    });

    test('un fallo de servicio sí ofrece reintentar', () async {
      final (client, _) =
          clientWith((_) => [failure(503, 'unavailable', 'no disponible')]);
      final controller = TransferController(client);

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 100,
        currency: 'USD',
      );

      expect(controller.canRetry, isTrue);
    });

    test('el detalle interno del servidor no llega a la pantalla', () async {
      final (client, _) = clientWith((_) => [
            failure(422, 'insufficient_funds',
                'insufficient funds: account 8f3c-... would end at -500 (AB001)')
          ]);
      final controller = TransferController(client);

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 100,
        currency: 'USD',
      );

      final shown = controller.error!.userMessage;
      expect(shown, isNot(contains('AB001')));
      expect(shown, isNot(contains('account')));
      expect(shown, 'No tienes saldo suficiente.');
    });

    test('un código desconocido no rompe la app', () async {
      final (client, _) = clientWith(
          (_) => [failure(418, 'codigo_del_futuro', 'algo nuevo')]);
      final controller = TransferController(client);

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 100,
        currency: 'USD',
      );

      expect(controller.error!.code, ApiErrorCode.unknown);
      expect(controller.error!.userMessage, isNotEmpty);
    });
  });

  group('petición', () {
    test('el monto viaja como entero en unidades menores', () async {
      final (client, recorder) = clientWith((_) => [ok()]);
      final controller = TransferController(client);

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 12550,
        currency: 'USD',
      );

      final body = jsonDecode(recorder.requests.single.body) as Map<String, dynamic>;
      expect(body['amount_minor'], 12550);
      expect(body['amount_minor'], isA<int>());
      expect(recorder.requests.single.body, isNot(contains('125.5')));
    });

    test('la petición lleva el token de sesión', () async {
      final (client, recorder) = clientWith((_) => [ok()]);
      final controller = TransferController(client);

      await controller.send(
        fromAccountId: 'origen',
        toAccountId: 'destino',
        amountMinor: 100,
        currency: 'USD',
      );

      expect(recorder.requests.single.headers['Authorization'],
          'Bearer token-de-prueba');
    });
  });
}
