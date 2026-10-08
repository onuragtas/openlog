// A loopback HTTP server the tests answer from, so the bearer token and the
// organization header are checked as they actually leave rather than as a stub
// was asked to pretend.
import 'dart:convert';
import 'dart:io';

/// One request as the server saw it.
class Seen {
  Seen(this.method, this.path, this.query, this.headers, this.body);
  final String method;
  final String path;

  /// The raw query string, so a test can assert what actually went out rather
  /// than what the caller meant to send.
  final String query;
  final Map<String, String> headers;
  final String body;
}

/// A server that answers from [handler] and records what it was sent.
class FakeServer {
  FakeServer(this._server);

  static Future<FakeServer> start(
    void Function(HttpRequest req, Seen seen) handler,
  ) async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    final fake = FakeServer(server);
    server.listen((req) async {
      final body = await utf8.decoder.bind(req).join();
      final headers = <String, String>{};
      req.headers.forEach((k, v) => headers[k.toLowerCase()] = v.join(','));
      final seen = Seen(req.method, req.uri.path, req.uri.query, headers, body);
      fake.requests.add(seen);
      handler(req, seen);
      await req.response.close();
    });
    return fake;
  }

  final HttpServer _server;
  final requests = <Seen>[];

  String get baseUrl => 'http://127.0.0.1:${_server.port}';
  Future<void> stop() => _server.close(force: true);
}

/// Writes a JSON body with [status].
void writeJson(HttpRequest req, int status, Object? body) {
  req.response
    ..statusCode = status
    ..headers.contentType = ContentType.json
    ..write(jsonEncode(body));
}
