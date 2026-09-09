import 'dart:convert';

import 'package:aibank_app/api/client.dart';
import 'package:aibank_app/screens/consent_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// La pantalla donde una persona decide si le da a una plataforma permiso para
/// pagar desde su cuenta.
///
/// Lo que se prueba es que la decisión sea de verdad de la persona: que vea
/// quién pide y para qué, que pueda bajar los topes, y que no pueda conceder
/// más de lo que le pidieron.

const _handoff = 'csr_abc123';

Map<String, dynamic> _prompt({
  int? maxPerOperation = 5000000,
  int? maxTotal = 50000000,
  String status = 'PENDING',
}) =>
    {
      'consent_request_id': '9c2f1e88',
      'requested_by': 'Mercatus',
      'purpose': 'Pagos del agente de investigación',
      'currency': 'USD',
      if (maxPerOperation != null) 'max_per_operation': maxPerOperation,
      if (maxTotal != null) 'max_total': maxTotal,
      'status': status,
      'expires_at': '2026-09-01T20:57:00Z',
    };

/// Registra las peticiones para poder afirmar QUÉ se mandó, no solo que no falló.
class _Recorder {
  final List<http.Request> requests = [];
}

(BffClient, _Recorder) _clientWith(
  Map<String, dynamic> prompt, {
  Map<String, dynamic>? approveResponse,
  int approveStatus = 201,
}) {
  final recorder = _Recorder();
  final mock = MockClient((request) async {
    recorder.requests.add(request);
    if (request.method == 'GET') {
      return http.Response(jsonEncode(prompt), 200,
          headers: {'content-type': 'application/json'});
    }
    if (request.url.path.endsWith('/reject')) {
      return http.Response('', 204);
    }
    return http.Response(
      jsonEncode(approveResponse ?? _mandateJson()),
      approveStatus,
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

Map<String, dynamic> _mandateJson() => {
      'mandate_id': 'm-1',
      'account_id': 'acc-1',
      'granted_to': 'mercatus_sandbox',
      'currency': 'USD',
      'status': 'active',
      'consumed': 0,
      'expires_at': '2026-10-01T20:57:00Z',
    };

Future<void> _tap(WidgetTester tester, Finder finder) async {
  await tester.tap(finder);
  await tester.pumpAndSettle();
}

/// Monta la pantalla en un lienzo alto.
///
/// El viewport por defecto de las pruebas mide 800x600 y esta pantalla es más
/// alta: con el tamaño de serie, los botones del final quedan fuera y el toque
/// no llega. Ampliarlo prueba la pantalla completa en vez de obligar a cada
/// caso a desplazarse — el desplazamiento es de la lista, no de la decisión que
/// se está probando.
Future<void> _pump(WidgetTester tester, BffClient client) async {
  await tester.binding.setSurfaceSize(const Size(800, 1600));
  addTearDown(() => tester.binding.setSurfaceSize(null));

  await tester.pumpWidget(MaterialApp(
    home: ConsentScreen(
      client: client,
      handoffCode: _handoff,
      accountId: 'acc-1',
    ),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('muestra quién pide, para qué y cuánto', (tester) async {
    final (client, _) = _clientWith(_prompt());
    await _pump(tester, client);

    // Lo primero que tiene que ver quien decide es quién le está pidiendo. Un
    // nombre que la plataforma eligiera al vuelo permitiría hacerse pasar por
    // otra; este viene del registro del banco.
    expect(find.text('Mercatus'), findsOneWidget);
    expect(find.text('Pagos del agente de investigación'), findsOneWidget);
    expect(
      find.textContaining('hasta 5,00 USD por pago'),
      findsOneWidget,
    );
    expect(find.textContaining('hasta 50,00 USD en total'), findsOneWidget);
  });

  testWidgets('una petición sin topes se anuncia como tal', (tester) async {
    final (client, _) = _clientWith(_prompt(maxPerOperation: null, maxTotal: null));
    await _pump(tester, client);

    // Es la petición más amplia posible y quien decide tiene que verla como tal,
    // no como una ausencia de datos.
    expect(find.text('Sin límite de monto'), findsOneWidget);
  });

  testWidgets('los topes vienen precargados con lo pedido', (tester) async {
    final (client, _) = _clientWith(_prompt());
    await _pump(tester, client);

    // Dejarlos vacíos empujaría a conceder sin límite por comodidad.
    final fields = tester.widgetList<TextField>(find.byType(TextField)).toList();
    expect(fields[0].controller!.text, '5,00');
    expect(fields[1].controller!.text, '50,00');
  });

  testWidgets('la persona puede conceder menos de lo que le piden', (tester) async {
    final (client, recorder) = _clientWith(_prompt());
    await _pump(tester, client);

    await tester.enterText(find.byType(TextField).first, '2,00');
    await _tap(tester, find.text('Dar permiso'));

    final approve = recorder.requests.last;
    final body = jsonDecode(approve.body) as Map<String, dynamic>;
    // Se manda lo que fijó la persona, no lo que pidió la plataforma. Que se
    // pueda bajar sin fricción es la diferencia entre un consentimiento y un
    // botón de aceptar.
    expect(body['max_per_operation'], 2000000);
    expect(body['max_total'], 50000000);
    expect(body['expires_in_days'], 30);
  });

  testWidgets('no se puede conceder más de lo pedido', (tester) async {
    final (client, recorder) = _clientWith(_prompt());
    await _pump(tester, client);

    await tester.enterText(find.byType(TextField).first, '999,00');
    await _tap(tester, find.text('Dar permiso'));

    expect(find.textContaining('No puedes dar más por pago'), findsOneWidget);
    // Y no se llamó al servidor: decírselo en el momento es mejor que devolver
    // un error del banco por algo que la pantalla ya sabía.
    expect(recorder.requests.where((r) => r.method == 'POST'), isEmpty);
  });

  testWidgets('vaciar un tope que se pidió acotado también es conceder más',
      (tester) async {
    final (client, recorder) = _clientWith(_prompt());
    await _pump(tester, client);

    await tester.enterText(find.byType(TextField).first, '');
    await _tap(tester, find.text('Dar permiso'));

    // No poner tope donde te pidieron uno es dar MÁS, aunque el campo vacío
    // parezca lo contrario.
    expect(find.textContaining('No puedes dar más por pago'), findsOneWidget);
    expect(recorder.requests.where((r) => r.method == 'POST'), isEmpty);
  });

  testWidgets('sin tope pedido se puede dejar el campo vacío', (tester) async {
    final (client, recorder) =
        _clientWith(_prompt(maxPerOperation: null, maxTotal: null));
    await _pump(tester, client);

    await _tap(tester, find.text('Dar permiso'));

    final body = jsonDecode(recorder.requests.last.body) as Map<String, dynamic>;
    expect(body.containsKey('max_per_operation'), isFalse);
    expect(body.containsKey('max_total'), isFalse);
  });

  testWidgets('se puede elegir la vigencia', (tester) async {
    final (client, recorder) = _clientWith(_prompt());
    await _pump(tester, client);

    await _tap(tester, find.text('7 días'));
    await _tap(tester, find.text('Dar permiso'));

    final body = jsonDecode(recorder.requests.last.body) as Map<String, dynamic>;
    expect(body['expires_in_days'], 7);
  });

  testWidgets('rechazar no otorga nada', (tester) async {
    final (client, recorder) = _clientWith(_prompt());
    await _pump(tester, client);

    await _tap(tester, find.text('Rechazar'));

    final post = recorder.requests.lastWhere((r) => r.method == 'POST');
    expect(post.url.path, endsWith('/reject'));
  });

  testWidgets('una solicitud ya resuelta no se puede volver a responder',
      (tester) async {
    final (client, _) = _clientWith(_prompt(status: 'APPROVED'));
    await _pump(tester, client);

    expect(find.text('Ya diste este permiso.'), findsOneWidget);
    expect(find.text('Dar permiso'), findsNothing);
  });

  testWidgets('una solicitud vencida explica qué hacer', (tester) async {
    final (client, _) = _clientWith(_prompt(status: 'EXPIRED'));
    await _pump(tester, client);

    // Decir solo "venció" deja a la persona sin saber cómo seguir.
    expect(find.textContaining('Pide una nueva desde la plataforma'),
        findsOneWidget);
  });

  testWidgets('un error del servidor se muestra en términos entendibles',
      (tester) async {
    final (client, _) = _clientWith(_prompt(),
        approveResponse: {'code': 'already_resolved', 'message': 'ya resuelta'},
        approveStatus: 409);
    await _pump(tester, client);

    await _tap(tester, find.text('Dar permiso'));

    // Nunca el detalle técnico del servidor.
    expect(find.textContaining('ya resuelta'), findsNothing);
    expect(find.textContaining('Algo salió mal'), findsOneWidget);
  });
}
