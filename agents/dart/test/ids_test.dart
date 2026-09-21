import 'package:openlog/src/ids.dart';
import 'package:test/test.dart';

void main() {
  test('ids have the shapes the backend requires', () {
    expect(newTraceId(), matches(RegExp(r'^[0-9a-f]{32}$')));
    expect(newSessionId(), matches(RegExp(r'^[0-9a-f]{32}$')));
    expect(newSpanId(), matches(RegExp(r'^[0-9a-f]{16}$')));
  });

  test('ids are not reused', () {
    final ids = {for (var i = 0; i < 200; i++) newTraceId()};
    // A repeat here would mean two visits merged into one, or two spans claiming the same identity.
    expect(ids.length, 200);
  });
}
