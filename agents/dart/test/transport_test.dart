import 'dart:convert';

import 'package:openlog/src/config.dart';
import 'package:openlog/src/otlp.dart';
import 'package:openlog/src/transport.dart';
import 'package:test/test.dart';

ResolvedConfig cfg({int batch = 3}) => resolveConfig(OpenlogOptions(
      key: 'olb_1a2b3c4d5e6f708192a3b4c5d6e7f809',
      endpoint: 'https://ingest.example.com:4318',
      appId: 'com.example.shop',
      serviceVersion: '4.2.1',
      maxBatchSize: batch,
    ));

class FakeServer {
  FakeServer(this.status);
  int status;
  final List<Map<String, String>> headers = [];
  final List<String> bodies = [];

  Future<int> send(String url, Map<String, String> h, String body) async {
    headers.add(h);
    bodies.add(body);
    return status;
  }
}

Map<String, Object?> span(String name) => buildSpan(
      name: name,
      event: RumEvent.screen,
      traceId: 'a' * 32,
      spanId: 'b' * 16,
      startMs: 1700000000000,
    );

List<String> spanNames(String body) {
  final decoded = jsonDecode(body) as Map<String, Object?>;
  final resourceSpans = decoded['resourceSpans']! as List<Object?>;
  final scopeSpans = (resourceSpans.first! as Map<String, Object?>)['scopeSpans']! as List<Object?>;
  final spans = (scopeSpans.first! as Map<String, Object?>)['spans']! as List<Object?>;
  return [for (final s in spans.cast<Map<String, Object?>>()) s['name']! as String];
}

void main() {
  test('buffers until the batch is full, then sends one request with every span', () async {
    final server = FakeServer(200);
    final t = Transport(cfg(batch: 3), send: server.send);
    t.add(span('a'));
    t.add(span('b'));
    expect(server.bodies, isEmpty, reason: 'an incomplete batch is not sent');
    t.add(span('c'));
    await t.flush();
    expect(server.bodies.length, 1);
    expect(spanNames(server.bodies.first), ['a', 'b', 'c']);
  });

  test('sends the key and the application id, because a mobile key is scoped by both', () async {
    final server = FakeServer(200);
    final t = Transport(cfg(batch: 1), send: server.send);
    t.add(span('a'));
    await t.flush();
    expect(server.headers.first['openlog-browser-key'], startsWith('olb_'));
    expect(server.headers.first['openlog-app-id'], 'com.example.shop');
    expect(server.headers.first['content-type'], 'application/json');
  });

  test('a refused key stops the SDK instead of retrying a permanent answer', () async {
    final server = FakeServer(403);
    final t = Transport(cfg(batch: 1), send: server.send);
    t.add(span('a'));
    await t.flush();
    expect(t.refused, isTrue);

    t.add(span('b'));
    await t.flush();
    expect(server.bodies.length, 1, reason: 'nothing about the next attempt would differ');
  });

  test('a temporary refusal keeps the batch for the next flush', () async {
    final server = FakeServer(503);
    final t = Transport(cfg(batch: 1), send: server.send);
    t.add(span('a'));
    await t.flush();
    expect(server.bodies.length, 1);

    server.status = 200;
    await t.flush();
    expect(server.bodies.length, 2);
    expect(spanNames(server.bodies.last), ['a'], reason: 'the spans were not lost');
  });

  test('the build travels as a resource attribute, not as a span one', () async {
    final server = FakeServer(200);
    final t = Transport(cfg(batch: 1), send: server.send);
    t.add(span('a'));
    await t.flush();
    expect(server.bodies.first, contains('service.version'));
    expect(server.bodies.first, contains('4.2.1'));
  });
}
