// The window: what it resolves to, and which requests carry it.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/time_range.dart';

import 'fake_server.dart';

void main() {
  group('resolving a window', () {
    final now = DateTime.utc(2026, 10, 10, 12);

    test('a preset is measured from now, every time it is asked', () {
      const range = TimeRange.preset('6h');
      expect(range.resolve(now).from, DateTime.utc(2026, 10, 10, 6));
      expect(range.resolve(now).to, now);
      // An hour later it means an hour later: the same object, a
      // different window. That is what makes "last 6 hours" true after a
      // refresh instead of frozen at the moment it was picked.
      final later = now.add(const Duration(hours: 1));
      expect(range.resolve(later).from, DateTime.utc(2026, 10, 10, 7));
    });

    test('nonsense falls back to the default, not to nothing', () {
      expect(parseDuration('7d'), 604800000);
      expect(parseDuration('0h'), isNull);
      expect(parseDuration('fortnight'), isNull);
      const bad = TimeRange.preset('fortnight');
      expect(bad.resolve(now).from, DateTime.utc(2026, 10, 10, 11));
    });

    test('an absolute window does not move', () {
      final range = TimeRange.absolute(
        DateTime.utc(2026, 10, 1),
        DateTime.utc(2026, 10, 2),
      );
      expect(range.absolute, isTrue);
      expect(range.resolve(now).from, DateTime.utc(2026, 10, 1));
      expect(
        range.resolve(now.add(const Duration(days: 3))).to,
        DateTime.utc(2026, 10, 2),
      );
    });

    test('a backwards window is refused and the default stands', () {
      final range = TimeRange.absolute(
        DateTime.utc(2026, 10, 2),
        DateTime.utc(2026, 10, 1),
      );
      expect(range.resolve(now).from, DateTime.utc(2026, 10, 10, 11));
    });
  });

  group('which paths take a window', () {
    test('the ones the contract gives a from/to', () {
      expect(pathTakesRange('/api/v1/logs'), isTrue);
      expect(pathTakesRange('/api/v1/kubernetes/pods'), isTrue);
      // A path parameter matches whatever is in that segment.
      expect(pathTakesRange('/api/v1/apm/services/checkout/overview'), isTrue);
      expect(pathTakesRange('/api/v1/kubernetes/pods/abc-123/events'), isTrue);
    });

    test('and nothing else', () {
      // Alerts, settings and the console take no window; sending one
      // would make a screen look filtered by a range it never applied.
      expect(pathTakesRange('/api/v1/alerts/incidents'), isFalse);
      expect(pathTakesRange('/api/v1/saved-views'), isFalse);
      expect(pathTakesRange('/api/v1/query'), isFalse);
      expect(pathTakesRange('/api/v1/fleet/hosts'), isFalse);
      // A shorter or longer path is not a match for a pattern.
      expect(pathTakesRange('/api/v1/apm/services/checkout'), isFalse);
      expect(pathTakesRange('/api/v1/logs/extra'), isFalse);
    });
  });

  test(
    'a ranged request carries the window, an unranged one does not',
    () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/incidents')) {
          writeJson(req, 200, {
            'incidents': <Object>[],
            'counts': {'open': 0, 'acknowledged': 0, 'resolved': 0},
          });
        } else {
          writeJson(req, 200, {'logs': <Object>[], 'next_cursor': null});
        }
      });
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';
      client.window = () => const TimeRange.preset('6h');

      await client.logs();
      await client.incidents();

      final logs = Uri.splitQueryString(server.requests.first.query);
      expect(logs['from'], isNotNull);
      expect(logs['to'], isNotNull);
      expect(
        DateTime.parse(logs['to']!).difference(DateTime.parse(logs['from']!)),
        const Duration(hours: 6),
      );
      expect(
        Uri.splitQueryString(server.requests.last.query),
        isNot(contains('from')),
      );
    },
  );

  test('a caller with its own window keeps it', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(req, 200, {
        'from': '2026-10-10T08:00:00.000000000Z',
        'to': '2026-10-10T09:00:00.000000000Z',
        'baseline_from': '2026-10-10T07:00:00.000000000Z',
        'baseline_to': '2026-10-10T08:00:00.000000000Z',
        'series_compared': 0,
        'correlations': <Object>[],
      }),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';
    client.window = () => const TimeRange.preset('7d');

    // The correlation screen is about an incident's window, not the
    // screen's; the shared range must not overwrite it.
    await client.correlateMetrics(
      from: DateTime.utc(2026, 10, 10, 8),
      to: DateTime.utc(2026, 10, 10, 9),
    );

    final q = Uri.splitQueryString(server.requests.single.query);
    expect(q['from'], '2026-10-10T08:00:00.000Z');
    expect(q['to'], '2026-10-10T09:00:00.000Z');
  });

  test('a new window makes every loaded section stale', () async {
    final sections = Sections(
      client: OpenlogClient(baseUrl: 'http://127.0.0.1:1'),
    );
    addTearDown(sections.dispose);
    sections.hosts.loaded = true;
    sections.logs.loaded = true;
    sections.onboarding.loaded = true;

    expect(sections.range.choose(const TimeRange.preset('24h')), isTrue);
    sections.markRangeStale();

    // Not reloaded -- marked stale, so each asks again the first time it
    // is looked at rather than thirteen requests at once.
    expect(sections.hosts.loaded, isFalse);
    expect(sections.logs.loaded, isFalse);
    expect(sections.onboarding.loaded, isFalse);
    // The same window twice is not a reason to reload anything.
    expect(sections.range.choose(const TimeRange.preset('24h')), isFalse);
  });
}
