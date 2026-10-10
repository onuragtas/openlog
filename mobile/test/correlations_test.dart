// What else changed while an incident was firing.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/detail.dart';

void main() {
  test('the window is the incident, clamped at both ends', () {
    final opened = DateTime.utc(2026, 10, 9, 10);
    final now = DateTime.utc(2026, 10, 9, 18);

    // Still open after eight hours: an incident that long correlates
    // everything that happened in eight hours, which is everything.
    final (_, longTo) = correlationWindow(openedAt: opened, now: now);
    expect(longTo.difference(opened), const Duration(hours: 1));

    // Resolved in forty seconds: too short to compare, so five minutes.
    final (_, shortTo) = correlationWindow(
      openedAt: opened,
      resolvedAt: opened.add(const Duration(seconds: 40)),
      now: now,
    );
    expect(shortTo.difference(opened), const Duration(minutes: 5));

    // In between, the incident's own length.
    final (from, to) = correlationWindow(
      openedAt: opened,
      resolvedAt: opened.add(const Duration(minutes: 12)),
      now: now,
    );
    expect(from, opened);
    expect(to.difference(from), const Duration(minutes: 12));
  });
}
