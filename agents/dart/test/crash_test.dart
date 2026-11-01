import 'dart:convert';

import 'package:openlog/openlog.dart';
import 'package:test/test.dart';

void main() {
  tearDown(Openlog.reset);

  Openlog start(List<String> bodies, {bool capture = true}) => Openlog.init(
        OpenlogOptions(key: 'olb_x', endpoint: 'https://ingest.example.com', appId: 'com.example', captureCrashes: capture),
        send: (url, headers, body) async {
          bodies.add(body);
          return 200;
        },
        fetchConfig: (url, headers) async => null,
      );

  Map<String, String> attributes(String body) {
    final decoded = jsonDecode(body) as Map<String, dynamic>;
    final span = decoded['resourceSpans'][0]['scopeSpans'][0]['spans'][0] as Map<String, dynamic>;
    return {for (final a in span['attributes'] as List) a['key'] as String: a['value']['stringValue'] as String};
  }

  test('recordFatal marks the error as a crash and sends it at once', () async {
    final bodies = <String>[];
    final sdk = start(bodies);
    sdk.recordScreen('/cart');
    await sdk.flush();
    bodies.clear();
    await sdk.recordFatal(StateError('boom'), stack: StackTrace.current);
    expect(bodies, hasLength(1));
    final a = attributes(bodies.single);
    expect(a['openlog.rum.route'], '/cart');
    expect(a['openlog.rum.event'], 'error');
    expect(a['openlog.rum.crash.kind'], 'uncaught_exception');
    expect(a['openlog.rum.crash.id'], matches(RegExp(r'^[0-9a-f]{32}$')));
    expect(a['session.id'], sdk.sessionId);
    expect(bodies.single, contains('boom'));
  });

  test('recordError stays an ordinary error', () async {
    final bodies = <String>[];
    final sdk = start(bodies);
    sdk.recordError(StateError('handled'));
    await sdk.flush();
    expect(attributes(bodies.single).containsKey('openlog.rum.crash.kind'), isFalse);
  });

  test('a build says it can report crashes only when it can', () async {
    final bodies = <String>[];
    final sdk = start(bodies);
    sdk.recordScreen('/x');
    await sdk.flush();
    expect(bodies.single, contains('openlog.rum.crash.capture'));
    await sdk.shutdown();

    final plain = <String>[];
    final other = start(plain, capture: false);
    other.recordScreen('/x');
    await other.flush();
    expect(plain.single, isNot(contains('openlog.rum.crash.capture')));
  });
}
