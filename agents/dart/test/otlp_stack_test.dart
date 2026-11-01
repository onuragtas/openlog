import 'package:openlog/src/otlp.dart';
import 'package:openlog/src/limits.dart';
import 'package:test/test.dart';

void main() {
  test('a stack is not cut to the limit of an attribute', () {
    final long = 'x' * 9000;
    final encoded = encodeAttributes({'exception.stacktrace': long, 'other': long});
    expect((encoded[0]['value'] as Map)['stringValue'], long);
    expect(((encoded[1]['value'] as Map)['stringValue'] as String).length, maxAttrValueBytes);
  });
}
