import 'dart:convert';

import 'package:aibank_app/api/client.dart';
import 'package:aibank_app/screens/mandates_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// La pantalla donde la persona ve y retira los permisos que otorgó.
///
/// Sin ella, revocar sería imposible en la práctica: no se puede retirar un
/// permiso que no se puede ver. Por eso lo que se prueba aquí es sobre todo que
/// se VEA lo que hace falta para decidir — cuánto se gastó, cuánto queda — y que
/// retirar no ocurra por accidente.

Map<String, dynamic> _mandate({
  String id = 'm-1',
  String grantedTo = 'Mercatus',
  String status = 'active',
  int consumed = 8000000,
  int? remaining = 12000000,
  int? maxPerOperation = 5000000,
  String? revokedAt,
}) =>
    {
      'mandate_id': id,
      'account_id': 'acc-1',
      'granted_to': grantedTo,
      'currency': 'USD',
      'status': status,
      if (maxPerOperation != null) 'max_per_operation': maxPerOperation,
      'max_total': 20000000,
      'consumed': consumed,
      if (remaining != null) 'remaining': remaining,
      'expires_at': '2026-10-01T20:57:00Z',
      if (revokedAt != null) 'revoked_at': revokedAt,
    };

class _Recorder {
  final List<http.Request> requests = [];
}

(BffClient, _Recorder) _clientWith(
  List<Map<String, dynamic>> mandates, {
  int revokeStatus = 200,
}) {
  final recorder = _Recorder();
  final mock = MockClient((request) async {
    recorder.requests.add(request);
    if (request.method == 'GET') {
      return http.Response(jsonEncode({'mandates': mandates}), 200,
          headers: {'content-type': 'application/json'});
    }
    if (revokeStatus != 200) {
      return http.Response(
          jsonEncode({'code': 'unavailable', 'message': 'x'}), revokeStatus,
          headers: {'content-type': 'application/json'});
    }
    return http.Response(
      jsonEncode(_mandate(status: 'revoked', revokedAt: '2026-09-05T10:00:00Z')),
      200,
      headers: {'content-type': 'application/json'},
    );
  });
  return (
    BffClient(
      baseUrl: Uri.parse('http://test'),
      token: () => 'token',
      httpClient: mock,
    ),
    recorder,
  );
}

Future<void> _pump(WidgetTester tester, BffClient client) async {
  await tester.binding.setSurfaceSize(const Size(800, 1600));
  addTearDown(() => tester.binding.setSurfaceSize(null));

  await tester.pumpWidget(MaterialApp(home: MandatesScreen(client: client)));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('muestra a quién se le dio permiso y cuánto se gastó',
      (tester) async {
    final (client, _) = _clientWith([_mandate()]);
    await _pump(tester, client);

    expect(find.text('Mercatus'), findsOneWidget);
    // Lo gastado va primero porque es la pregunta que trae aquí a alguien que
    // sospecha de un cobro.
    expect(find.text('Gastado'), findsOneWidget);
    expect(find.text('8,00 USD'), findsOneWidget);
    expect(find.text('Disponible'), findsOneWidget);
    expect(find.text('12,00 USD'), findsOneWidget);
  });

  testWidgets('sin permisos lo dice con todas las letras', (tester) async {
    final (client, _) = _clientWith([]);
    await _pump(tester, client);

    expect(
      find.textContaining('No le diste permiso de pago a ninguna plataforma'),
      findsOneWidget,
    );
  });

  testWidgets('los permisos retirados y vencidos también se ven',
      (tester) async {
    final (client, _) = _clientWith([
      _mandate(id: 'm-1', grantedTo: 'Mercatus'),
      _mandate(id: 'm-2', grantedTo: 'Otra', status: 'revoked', revokedAt: '2026-09-05T10:00:00Z'),
      _mandate(id: 'm-3', grantedTo: 'Tercera', status: 'expired'),
    ]);
    await _pump(tester, client);

    // La pregunta que trae aquí a alguien suele ser "¿a quién le di acceso?", y
    // esa respuesta incluye el pasado.
    expect(find.text('Activo'), findsOneWidget);
    expect(find.text('Retirado'), findsOneWidget);
    expect(find.text('Vencido'), findsOneWidget);
  });

  testWidgets('solo se puede retirar un permiso vivo', (tester) async {
    final (client, _) = _clientWith([
      _mandate(id: 'm-2', status: 'revoked', revokedAt: '2026-09-05T10:00:00Z'),
      _mandate(id: 'm-3', status: 'expired'),
    ]);
    await _pump(tester, client);

    // Revocar uno vencido no cambia nada, y ofrecer el botón invita a una acción
    // sin efecto.
    expect(find.text('Retirar permiso'), findsNothing);
  });

  testWidgets('retirar pide confirmación antes de hacerlo', (tester) async {
    final (client, recorder) = _clientWith([_mandate()]);
    await _pump(tester, client);

    await tester.tap(find.text('Retirar permiso'));
    await tester.pumpAndSettle();

    expect(find.text('¿Retirar el permiso?'), findsOneWidget);
    // Es irreversible: un permiso retirado no se reactiva, hay que volver a
    // otorgarlo. La confirmación dice eso.
    expect(find.textContaining('tendrán que pedírtelo otra vez'), findsOneWidget);
    expect(recorder.requests.where((r) => r.method == 'POST'), isEmpty);
  });

  testWidgets('cancelar la confirmación no retira nada', (tester) async {
    final (client, recorder) = _clientWith([_mandate()]);
    await _pump(tester, client);

    await tester.tap(find.text('Retirar permiso'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Cancelar'));
    await tester.pumpAndSettle();

    expect(recorder.requests.where((r) => r.method == 'POST'), isEmpty);
    expect(find.text('Mercatus'), findsOneWidget);
  });

  testWidgets('confirmar retira el permiso', (tester) async {
    final (client, recorder) = _clientWith([_mandate()]);
    await _pump(tester, client);

    await tester.tap(find.text('Retirar permiso'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Retirar'));
    await tester.pumpAndSettle();

    final post = recorder.requests.firstWhere((r) => r.method == 'POST');
    expect(post.url.path, '/v1/mandates/m-1/revoke');
  });

  testWidgets('la confirmación advierte sobre los pagos en curso',
      (tester) async {
    final (client, _) = _clientWith([_mandate()]);
    await _pump(tester, client);

    await tester.tap(find.text('Retirar permiso'));
    await tester.pumpAndSettle();

    // Retirar no cancela las retenciones vivas, y prometer que sí sería mentir
    // en la pantalla donde más se confía.
    expect(
      find.textContaining('Los pagos que ya estén en curso pueden completarse'),
      findsOneWidget,
    );
  });

  testWidgets('un fallo al retirar se explica sin tecnicismos', (tester) async {
    final (client, _) = _clientWith([_mandate()], revokeStatus: 503);
    await _pump(tester, client);

    await tester.tap(find.text('Retirar permiso'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Retirar'));
    await tester.pumpAndSettle();

    expect(find.textContaining('No pudimos conectarnos'), findsOneWidget);
    // Y el permiso sigue en la lista: no se le dice a la persona que se retiró
    // algo que sigue vivo.
    expect(find.text('Mercatus'), findsOneWidget);
  });

  testWidgets('un permiso sin tope total no muestra disponible', (tester) async {
    final (client, _) = _clientWith([
      _mandate(remaining: null, maxPerOperation: null),
    ]);
    await _pump(tester, client);

    // Mostrar "Disponible: 0" donde no hay tope sería exactamente lo contrario
    // de lo que ocurre.
    expect(find.text('Disponible'), findsNothing);
    expect(find.text('Gastado'), findsOneWidget);
  });
}
