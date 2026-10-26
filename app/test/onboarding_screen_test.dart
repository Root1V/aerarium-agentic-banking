import 'dart:convert';

import 'package:aibank_app/api/client.dart';
import 'package:aibank_app/api/models.dart';
import 'package:aibank_app/screens/onboarding_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// El alta de nivel 1.
///
/// Lo que se prueba es lo que más cuesta en un alta: no perder a la persona
/// —avisos en el campo, sin viajes inútiles al banco— y no duplicarle la cuenta
/// —la referencia se conserva en cada reintento—.

final _today = DateTime(2026, 10, 25);

class _Recorder {
  final requests = <http.Request>[];
}

/// Un BFF falso que responde, en orden, lo que se le pase.
(BffClient, _Recorder) _bff(List<http.Response> responses) {
  final recorder = _Recorder();
  var i = 0;
  final mock = MockClient((request) async {
    recorder.requests.add(request);
    return responses[i < responses.length ? i++ : responses.length - 1];
  });
  return (
    BffClient(baseUrl: Uri.parse('http://bff.test'), token: () => '', httpClient: mock),
    recorder,
  );
}

http.Response _result(String status, {String ref = 'ref-1'}) => http.Response(
      jsonEncode({
        'reference': ref,
        'status': status,
        if (status == 'approved') 'customer_id': 'cust-1',
        if (status == 'approved') 'account_id': 'acc-1',
      }),
      200,
      headers: {'content-type': 'application/json'},
    );

http.Response _error(int status, String code) => http.Response(
      jsonEncode({'code': code, 'message': 'x'}),
      status,
      headers: {'content-type': 'application/json'},
    );

/// Referencias deterministas: 'ref-1', 'ref-2'...
String Function() _references() {
  var n = 0;
  return () => 'ref-${++n}';
}

Future<OnboardingResult?> _pump(
  WidgetTester tester,
  BffClient client, {
  void Function(OnboardingResult)? onApproved,
}) async {
  OnboardingResult? approved;
  await tester.binding.setSurfaceSize(const Size(800, 1400));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await tester.pumpWidget(MaterialApp(
    home: OnboardingScreen(
      client: client,
      now: () => _today,
      newReference: _references(),
      onApproved: onApproved ?? (r) => approved = r,
    ),
  ));
  return approved;
}

Future<void> _fill(
  WidgetTester tester, {
  String dni = '45678912',
  String first = 'Ana María',
  String last = 'Pérez Quispe',
  String birth = '14031995',
}) async {
  await tester.enterText(find.byKey(const Key('dni')), dni);
  await tester.enterText(find.byKey(const Key('first')), first);
  await tester.enterText(find.byKey(const Key('last')), last);
  await tester.enterText(find.byKey(const Key('birth')), birth);
}

Future<void> _send(WidgetTester tester) async {
  await tester.tap(find.text('Abrir mi cuenta'));
  await tester.pumpAndSettle();
}

Map<String, dynamic> _body(http.Request r) => jsonDecode(r.body) as Map<String, dynamic>;

void main() {
  testWidgets('dice cuánto tarda y pide solo cuatro datos', (tester) async {
    final (client, _) = _bff([]);
    await _pump(tester, client);

    expect(find.textContaining('menos de 2 minutos'), findsOneWidget);
    // Cada campo de más es gente que abandona el formulario.
    expect(find.byType(TextField), findsNWidgets(4));
  });

  testWidgets('la fecha se escribe solo con números', (tester) async {
    final (client, _) = _bff([]);
    await _pump(tester, client);

    // El teclado numérico de iOS no tiene "/": las barras se ponen solas.
    await tester.enterText(find.byKey(const Key('birth')), '14031995');
    expect(find.text('14/03/1995'), findsOneWidget);
  });

  testWidgets('un DNI incompleto se avisa en el campo, sin llamar al banco',
      (tester) async {
    final (client, recorder) = _bff([]);
    await _pump(tester, client);

    await _fill(tester, dni: '4567891');
    await _send(tester);

    expect(find.text('El DNI tiene 8 dígitos.'), findsOneWidget);
    expect(recorder.requests, isEmpty);
  });

  testWidgets('la mayoría de edad se cuenta al día', (tester) async {
    final (client, recorder) = _bff([_result('approved')]);
    await _pump(tester, client);

    // Cumple 18 mañana: todavía no.
    await _fill(tester, birth: '26102008');
    await _send(tester);
    expect(find.textContaining('mayor de edad'), findsOneWidget);
    expect(recorder.requests, isEmpty);

    // Cumple 18 hoy: sí.
    await tester.enterText(find.byKey(const Key('birth')), '25102008');
    await _send(tester);
    expect(recorder.requests, hasLength(1));
  });

  testWidgets('una fecha que no existe no pasa', (tester) async {
    final (client, recorder) = _bff([]);
    await _pump(tester, client);

    await _fill(tester, birth: '30021995');
    await _send(tester);

    expect(find.textContaining('DD/MM/AAAA'), findsWidgets);
    expect(recorder.requests, isEmpty);
  });

  testWidgets('manda los datos limpios, la fecha en ISO y sin sesión',
      (tester) async {
    final (client, recorder) = _bff([_result('approved')]);
    await _pump(tester, client);

    await _fill(tester, first: '  Ana   María ', last: ' Pérez Quispe ');
    await _send(tester);

    final request = recorder.requests.single;
    final body = _body(request);
    expect(body['document_number'], '45678912');
    expect(body['first_names'], 'Ana   María');
    expect(body['last_names'], 'Pérez Quispe');
    expect(body['date_of_birth'], '1995-03-14');
    expect(body['reference'], 'ref-1');
    // Quien se da de alta todavía no tiene sesión: no se manda un Bearer vacío.
    expect(request.headers.containsKey('Authorization'), isFalse);
  });

  testWidgets('aprobada: entrega el cliente y la cuenta', (tester) async {
    final (client, _) = _bff([_result('approved')]);
    OnboardingResult? approved;
    await _pump(tester, client, onApproved: (r) => approved = r);

    await _fill(tester);
    await _send(tester);

    expect(approved?.customerId, 'cust-1');
    expect(approved?.accountId, 'acc-1');
  });

  testWidgets('si la red falla, el reintento es el mismo alta', (tester) async {
    final (client, recorder) = _bff([
      _error(503, 'unavailable'),
      _result('approved'),
    ]);
    OnboardingResult? approved;
    await _pump(tester, client, onApproved: (r) => approved = r);

    await _fill(tester);
    await _send(tester);
    expect(find.textContaining('No pudimos conectarnos'), findsOneWidget);

    await _send(tester);
    expect(approved, isNotNull);
    // La misma referencia en los dos envíos: si el primero sí llegó al banco,
    // el segundo lo retoma en vez de abrir otra cuenta.
    expect(_body(recorder.requests[0])['reference'],
        _body(recorder.requests[1])['reference']);
  });

  testWidgets('en revisión se dice sin dar motivos, y se puede volver a consultar',
      (tester) async {
    final (client, recorder) = _bff([
      _result('in_review'),
      _result('in_review'),
      _result('approved'),
    ]);
    OnboardingResult? approved;
    await _pump(tester, client, onApproved: (r) => approved = r);

    await _fill(tester);
    await _send(tester);
    expect(find.text('Estamos revisando tus datos'), findsOneWidget);

    await tester.tap(find.text('Consultar de nuevo'));
    await tester.pumpAndSettle();
    expect(find.textContaining('Sigue en revisión'), findsOneWidget);
    // La consulta lleva la referencia en una cabecera, no en la URL.
    final check = recorder.requests[1];
    expect(check.method, 'GET');
    expect(check.headers['X-Onboarding-Reference'], 'ref-1');
    expect(check.url.query, isEmpty);

    await tester.tap(find.text('Consultar de nuevo'));
    await tester.pumpAndSettle();
    expect(approved, isNotNull);
  });

  testWidgets('rechazada: mensaje neutro y corregir es un alta nueva',
      (tester) async {
    final (client, recorder) = _bff([_result('rejected'), _result('approved')]);
    await _pump(tester, client);

    await _fill(tester);
    await _send(tester);
    expect(find.text('No pudimos abrir tu cuenta con estos datos'), findsOneWidget);

    await tester.tap(find.text('Revisar mis datos'));
    await tester.pumpAndSettle();
    // Los datos siguen ahí para corregirlos.
    expect(find.text('45678912'), findsOneWidget);

    await _send(tester);
    // El alta rechazada está cerrada: la nueva lleva otra referencia.
    expect(_body(recorder.requests[1])['reference'], 'ref-2');
  });

  testWidgets('un error de campo del servidor se muestra en su campo',
      (tester) async {
    final (client, _) = _bff([_error(400, 'invalid_document')]);
    await _pump(tester, client);

    await _fill(tester);
    await _send(tester);

    expect(find.text('El DNI tiene 8 dígitos.'), findsOneWidget);
  });

  test('las fechas imposibles no se aceptan', () {
    expect(parseBirthDate('30/02/1995'), isNull);
    expect(parseBirthDate('31/04/1995'), isNull);
    expect(parseBirthDate('29/02/2024'), DateTime(2024, 2, 29));
    expect(parseBirthDate('1995-03-14'), isNull);
  });

  test('el formato de la fecha no deja pasar de ocho dígitos', () {
    final out = BirthDateFormatter().formatEditUpdate(
      TextEditingValue.empty,
      const TextEditingValue(text: '1403199512'),
    );
    expect(out.text, '14/03/1995');
  });
}
