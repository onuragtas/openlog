import 'package:openlog/src/limits.dart';
import 'package:openlog/src/otlp.dart';
import 'package:test/test.dart';

Map<String, String> attrs(List<Object?> encoded) => {
      for (final a in encoded.cast<Map<String, Object>>())
        a['key']! as String: (a['value']! as Map<String, Object>)['stringValue']! as String,
    };

void main() {
  test('every attribute travels as a string', () {
    final got = attrs(encodeAttributes({'a': 'x', 'n': 42, 'b': true}));
    expect(got, {'a': 'x', 'n': '42', 'b': 'true'});
  });

  test('empty and null attributes are left out rather than sent blank', () {
    expect(attrs(encodeAttributes({'a': '', 'b': null, 'c': 'x'})), {'c': 'x'});
  });

  test('values are bounded, and a URL has its own larger bound', () {
    final got = attrs(encodeAttributes({
      'note': 'x' * (maxAttrValueBytes * 2),
      'url.full': 'y' * (maxUrlBytes * 2),
    }));
    expect(got['note']!.length, maxAttrValueBytes);
    expect(got['url.full']!.length, maxUrlBytes);
  });

  test('a span carries its event kind and a nanosecond timestamp', () {
    final span = buildSpan(
      name: 'screen /cart',
      event: RumEvent.screen,
      traceId: 'a' * 32,
      spanId: 'b' * 16,
      startMs: 1700000000000,
      durationMs: 250,
    );
    expect(span['startTimeUnixNano'], '1700000000000000000');
    expect(span['endTimeUnixNano'], '1700000000250000000');
    expect(attrs(span['attributes']! as List<Object?>)['openlog.rum.event'], 'page_view');
  });

  test('an error span carries the exception as a span event', () {
    final span = buildSpan(
      name: 'error StateError',
      event: RumEvent.error,
      traceId: 'a' * 32,
      spanId: 'b' * 16,
      startMs: 1700000000000,
      isError: true,
      exception: {'type': 'StateError', 'message': 'bad', 'stacktrace': 'frame'},
    );
    expect(span['status'], {'code': 2});
    final events = span['events']! as List<Object?>;
    expect(attrs((events.first! as Map<String, Object?>)['attributes']! as List<Object?>)['exception.type'], 'StateError');
  });

  test('truncate never splits a surrogate pair', () {
    // A half character would make the payload invalid JSON on the way out.
    final s = '${'a' * 9}😀';
    expect(truncate(s, 10).length, 9);
  });
}
