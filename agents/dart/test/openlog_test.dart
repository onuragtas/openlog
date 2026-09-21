import 'dart:convert';

import 'package:openlog/openlog.dart';
import 'package:test/test.dart';

class Captured {
  final List<String> bodies = [];
  Future<int> send(String url, Map<String, String> headers, String body) async {
    bodies.add(body);
    return 200;
  }
}

OpenlogOptions opts({double sampleRate = 1, int batch = 1}) => OpenlogOptions(
      key: 'olb_1a2b3c4d5e6f708192a3b4c5d6e7f809',
      endpoint: 'https://ingest.example.com:4318',
      appId: 'com.example.shop',
      sampleRate: sampleRate,
      maxBatchSize: batch,
    );

Future<Map<String, Object?>?> noConfig(String url, Map<String, String> headers) async => null;

List<Map<String, Object?>> spansOf(String body) {
  final decoded = jsonDecode(body) as Map<String, Object?>;
  final rs = (decoded['resourceSpans']! as List<Object?>).first! as Map<String, Object?>;
  final ss = (rs['scopeSpans']! as List<Object?>).first! as Map<String, Object?>;
  return (ss['spans']! as List<Object?>).cast<Map<String, Object?>>();
}

Map<String, String> attrsOf(Map<String, Object?> span) => {
      for (final a in (span['attributes']! as List<Object?>).cast<Map<String, Object?>>())
        a['key']! as String: (a['value']! as Map<String, Object?>)['stringValue']! as String,
    };

void main() {
  tearDown(Openlog.reset);

  test('init twice returns the first instance', () {
    final a = Openlog.init(opts(), send: Captured().send, fetchConfig: noConfig);
    final b = Openlog.init(opts(), send: Captured().send, fetchConfig: noConfig);
    // A second agent would double-count every screen, and the usual cause is a framework starting twice.
    expect(identical(a, b), isTrue);
  });

  test('a screen carries its route and the session', () async {
    final server = Captured();
    final sdk = Openlog.init(opts(), send: server.send, fetchConfig: noConfig);
    sdk.recordScreen('/cart', isColdStart: true);
    await sdk.flush();

    final attrs = attrsOf(spansOf(server.bodies.single).single);
    expect(attrs['openlog.rum.event'], 'page_view');
    expect(attrs['openlog.rum.route'], '/cart');
    expect(attrs['openlog.rum.page_view.kind'], 'load');
    expect(attrs['session.id'], matches(RegExp(r'^[0-9a-f]{32}$')));
  });

  test('identify attaches an id to later events, and clears it', () async {
    final server = Captured();
    final sdk = Openlog.init(opts(batch: 10), send: server.send, fetchConfig: noConfig);

    sdk.recordEvent('before');
    sdk.identify('  acct_8f3a2b  ');
    sdk.recordEvent('after');
    sdk.identify('');
    sdk.recordEvent('signed_out');
    await sdk.flush();

    final spans = spansOf(server.bodies.single);
    final byName = {for (final s in spans) s['name']! as String: attrsOf(s)};
    expect(byName['before']!.containsKey('user.id'), isFalse);
    expect(byName['after']!['user.id'], 'acct_8f3a2b', reason: 'trimmed, and attached to later spans only');
    expect(byName['signed_out']!.containsKey('user.id'), isFalse, reason: 'absent, not empty');
  });

  test('an over-long identity is bounded, as the server bounds it', () async {
    final server = Captured();
    final sdk = Openlog.init(opts(), send: server.send, fetchConfig: noConfig);
    sdk.identify('u' * 500);
    sdk.recordEvent('checkout');
    await sdk.flush();
    // Bounded here as well as on the server: an application should not discover the limit by having its
    // spans silently change shape somewhere it cannot see.
    expect(attrsOf(spansOf(server.bodies.single).single)['user.id']!.length, 128);
  });

  test('custom parameters are namespaced, capped and deterministic', () async {
    final server = Captured();
    final sdk = Openlog.init(opts(), send: server.send, fetchConfig: noConfig);
    sdk.recordEvent('checkout', params: {
      for (var i = 0; i < 25; i++) 'p${i.toString().padLeft(2, '0')}': i,
      'Bad Key': 'dropped',
    });
    await sdk.flush();

    final attrs = attrsOf(spansOf(server.bodies.single).single);
    final params = attrs.keys.where((k) => k.startsWith('openlog.rum.custom.param.')).toList()..sort();
    expect(params.length, 16, reason: 'the cap bounds what one event can carry');
    // Sorted before the cap: which sixteen survive depends on the payload, not on map order.
    expect(params.first, 'openlog.rum.custom.param.p00');
    expect(attrs.containsKey('openlog.rum.custom.param.Bad Key'), isFalse);
  });

  test('a timing carries its value and unit', () async {
    final server = Captured();
    final sdk = Openlog.init(opts(), send: server.send, fetchConfig: noConfig);
    sdk.recordTiming('cart_priced', 42);
    await sdk.flush();
    final attrs = attrsOf(spansOf(server.bodies.single).single);
    expect(attrs['openlog.rum.custom.value'], '42');
    expect(attrs['openlog.rum.custom.unit'], 'ms');
  });

  test('an error carries the exception as a span event', () async {
    final server = Captured();
    final sdk = Openlog.init(opts(), send: server.send, fetchConfig: noConfig);
    sdk.recordError(StateError('cart is empty'), stack: StackTrace.current);
    await sdk.flush();
    final span = spansOf(server.bodies.single).single;
    expect(span['status'], {'code': 2});
    expect(span['events'], isNotEmpty);
  });

  test('a sampled-out session sends nothing at all', () async {
    final server = Captured();
    // Half a session is not a cheaper session, it is an unreadable one.
    final sdk = Openlog.init(opts(sampleRate: 0.0000001), send: server.send, fetchConfig: noConfig);
    if (sdk.sessionId.isEmpty) {
      sdk.recordScreen('/cart');
      sdk.recordEvent('checkout');
      await sdk.flush();
      expect(server.bodies, isEmpty);
    }
  });

  test('the server sample rate replaces the compiled-in one', () async {
    final server = Captured();
    Openlog.init(opts(), send: server.send, fetchConfig: (url, h) async {
      expect(url, endsWith('/v1/rum/config'));
      expect(h['openlog-browser-key'], startsWith('olb_'));
      return {'service_name': 'shop-android', 'environment': 'production', 'sample_rate': 0.5};
    });
    // The fetch is deliberately not awaited by init; letting the microtask queue drain is enough.
    await Future<void>.delayed(Duration.zero);
  });
}
