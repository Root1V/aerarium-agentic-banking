import 'dart:convert';

import 'package:aibank_app/api/client.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// Un banco de mentira: responde por ruta y guarda lo que recibió.
class FakeBank {
  FakeBank(this.routes);

  final Map<String, http.Response Function(Map<String, dynamic> body)> routes;
  final received = <String, Map<String, dynamic>>{};
  final authorization = <String, String?>{};

  /// Las peticiones en orden, como `MÉTODO /ruta`.
  final calls = <String>[];

  late final http.Client httpClient = MockClient((request) async {
    final key = '${request.method} ${request.url.path}';
    final body = request.body.isEmpty
        ? <String, dynamic>{}
        : jsonDecode(request.body) as Map<String, dynamic>;
    received[key] = body;
    authorization[key] = request.headers['Authorization'];
    calls.add(key);
    final route = routes[key];
    return route == null ? http.Response('{}', 404) : route(body);
  });

  BffClient client({String token = ''}) =>
      BffClient(baseUrl: Uri.parse('http://bff.test'), token: () => token, httpClient: httpClient);
}

http.Response json(Object body, [int status = 200]) => http.Response(jsonEncode(body), status);
