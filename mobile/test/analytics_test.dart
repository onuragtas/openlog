// Product analytics: the web's rules, with the web test's example values
// (web/src/components/rum/rum.test.tsx and mocks/rum.ts).
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/analytics.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/country_names.dart';
import 'package:openlog_mobile/src/time_range.dart';

import 'fake_server.dart';

Map<String, Object?> metric(num current, num? previous) => {
  'current': current,
  'previous': previous,
  'change': previous == null ? null : current - previous,
  'change_rate': previous == null || previous == 0
      ? null
      : (current - previous) / previous,
};

Map<String, Object?> compareJson({bool partial = false}) => {
  'days': 28,
  'current': {'from': '2026-10-04', 'to': '2026-10-31'},
  'previous': {'from': '2026-09-06', 'to': '2026-10-03'},
  'previous_partial': partial,
  'collecting': true,
  'metrics': {
    'users': metric(1200, 1000),
    'new_users': metric(300, 400),
    'avg_daily_users': metric(180, 180),
    'dau': metric(210, 190),
    'wau': metric(620, 600),
    'mau': metric(1200, 1000),
    'stickiness': metric(0.175, 0.19),
    'sessions': metric(5000, null),
    'page_views': metric(21000, 20000),
    'events': metric(900, 0),
  },
};

Map<String, Object?> activityJson({bool collecting = true}) => {
  'collecting': collecting,
  'days': collecting
      ? [
          for (var i = 1; i <= 3; i++)
            {
              'date': '2026-10-0$i',
              'dau': 100 * i,
              'wau': 250 * i,
              'mau': 500 * i,
              'new_users': 30,
              'stickiness': 0.2,
            },
        ]
      : <Object>[],
};

Map<String, Object?> retentionJson({int cohorts = 2}) => {
  'grain': 'week',
  'collecting': cohorts > 0,
  'sampling_changed': false,
  'cohorts': [
    for (var c = 0; c < cohorts; c++)
      {
        'cohort': '2026-09-0${c + 1}',
        'size': 200,
        'periods': [
          for (var p = 0; p < cohorts - c; p++)
            {'period': p, 'users': p == 0 ? 200 : 80, 'rate': p == 0 ? 1 : 0.4},
        ],
      },
  ],
};

Map<String, Object?> eventsJson() => {
  'events': [
    {
      'name': 'checkout_started',
      'count': 1200,
      'sessions': 800,
      'users': 640,
      'previous_count': 1000,
      'previous_sessions': 900,
      'previous_users': 640,
    },
    {
      'name': 'signup',
      'count': 300,
      'sessions': 290,
      'users': 280,
      'previous_count': 0,
      'previous_sessions': 0,
      'previous_users': 0,
    },
  ],
};

Map<String, Object?> sourcesJson() => {
  'by': 'referrer',
  'sources': [
    {'value': '', 'sessions': 900, 'users': 700},
    {'value': 'www.google.com', 'sessions': 500, 'users': 430},
  ],
};

Map<String, Object?> audienceJson({
  List<String> values = const ['TR', 'DE', ''],
}) => {
  'by': 'country',
  'rows': [
    for (final v in values)
      {
        'value': v,
        'sessions': 10,
        'users': 5,
        'previous_sessions': 5,
        'previous_users': 5,
      },
  ],
};

/// The body each endpoint answers, by the last part of its path.
Object? bodyFor(String path) => switch (path.split('/').last) {
  'activity' => activityJson(),
  'compare' => compareJson(),
  'retention' => retentionJson(),
  'events' => eventsJson(),
  'acquisition' => sourcesJson(),
  'audience' => audienceJson(),
  _ => null,
};

AnalyticsChange change(num? c, num? r, {String locale = 'en'}) =>
    AnalyticsChange.of(
      c?.toDouble(),
      r?.toDouble(),
      locale: locale,
      noChange: 'No change',
      fromNothing: '(new)',
    );

void main() {
  group('a change reads as on the web', () {
    test('a rise and a fall, as a count and a share', () {
      expect(change(200, 0.2).text, '+200 (+20%)');
      expect(change(200, 0.2).direction, ChangeDirection.up);
      expect(change(-100, -0.25).text, '−100 (−25%)');
      expect(change(-100, -0.25).direction, ChangeDirection.down);
    });

    test('a rise from nothing has no share', () {
      expect(change(900, null).text, '+900 (new)');
    });

    test('an unknown previous number is a dash, not a zero', () {
      expect(change(null, null).text, '–');
      expect(change(null, null).direction, ChangeDirection.unknown);
    });

    test('no move is said in words', () {
      expect(change(0, 0).text, 'No change');
      expect(change(0, null).direction, ChangeDirection.none);
    });

    test('a share keeps one decimal, and a small move is not nothing', () {
      expect(change(4, 0.004).text, '+4 (+0.4%)');
      expect(change(1234, 0.1234).text, '+1,234 (+12.3%)');
    });

    test('the reader\'s language writes the numbers', () {
      expect(change(1234, 0.125, locale: 'tr').text, '+1.234 (+%12,5)');
    });

    test('a share of a whole is written as a share', () {
      final c = AnalyticsChange.of(
        -0.015,
        -0.0789,
        locale: 'en',
        noChange: '',
        fromNothing: '',
        format: formatShare,
      );
      expect(c.text, '−2% (−7.9%)');
    });
  });

  group('a list\'s change', () {
    test('comes from the two numbers the server sends', () {
      const row = BreakdownRow('x', [1200, 0, 5], [1000, 5000, 5]);
      expect(row.changeOf(0)!.change, 200);
      expect(row.changeOf(0)!.rate, closeTo(0.2, 1e-9));
      expect(row.changeOf(1)!.rate, -1);
      expect(row.changeOf(2)!.change, 0);
    });

    test('from zero has no share, and without a number there is none', () {
      const row = BreakdownRow('x', [300, 5], [0, null]);
      expect(row.changeOf(0)!.rate, isNull);
      expect(row.changeOf(1), isNull);
      expect(const BreakdownRow('x', [1]).changeOf(0), isNull);
    });
  });

  group('a list held here', () {
    // The web test's: sixty names, and one that stopped altogether at the end.
    List<BreakdownRow> rows() => [
      for (var i = 0; i < 60; i++)
        BreakdownRow(
          'e${i.toString().padLeft(2, '0')}',
          [1000.0 - i, i + 1.0, 1],
          [1, 1, 1],
        ),
      const BreakdownRow('stopped', [0, 0, 0], [5000, 900, 500]),
    ];

    test('shows fifty, largest first, and keeps the zero reachable', () async {
      final list = BreakdownList();
      addTearDown(list.dispose);
      await list.setRows(rows());

      expect(list.items.length, 50);
      expect(list.items.first.name, 'e00');
      expect(list.hasMore, isTrue);
      expect(list.items.map((r) => r.name), isNot(contains('stopped')));

      await list.loadMore();
      expect(list.items.length, 61);
      expect(list.items.last.name, 'stopped');
      expect(list.hasMore, isFalse);
      expect(list.items.last.changeOf(0)!.change, -5000);
    });

    test('a new order starts over, where the zero now leads', () async {
      final list = BreakdownList();
      addTearDown(list.dispose);
      await list.setRows(rows());
      await list.loadMore();

      await list.setSort('value0');
      expect(list.dir, 'asc');
      expect(list.items.first.name, 'stopped');
      expect(list.items.length, 50);

      // A name starts from A.
      await list.setSort('name');
      expect(list.dir, 'asc');
      expect(list.items.first.name, 'e00');
    });

    test('names sort as the collator does', () {
      final names = ['b10', 'B2', 'Çin', 'cuba', 'a'];
      names.sort(compareNames);
      expect(names, ['a', 'B2', 'b10', 'Çin', 'cuba']);
    });
  });

  test('a country is named in the reader\'s language', () {
    expect(countryName('TR', 'tr'), 'Türkiye');
    expect(countryName('DE', 'tr'), 'Almanya');
    expect(countryName('DE', 'en'), 'Germany');
    // A code it does not know stays a code.
    expect(countryName('T1', 'en'), 'T1');
  });

  group('the tab', () {
    Future<(AnalyticsController, FakeServer)> start({
      Set<String> missing = const {},
    }) async {
      final server = await FakeServer.start((req, seen) {
        final last = seen.path.split('/').last;
        if (missing.contains(last)) {
          writeJson(req, 404, {
            'error': {'code': 'not_found', 'message': 'no such route'},
          });
        } else {
          writeJson(req, 200, bodyFor(seen.path));
        }
      });
      addTearDown(server.stop);
      final c = AnalyticsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
        now: () => DateTime.utc(2026, 11, 2),
      );
      addTearDown(c.dispose);
      return (c, server);
    }

    Map<String, String> asked(FakeServer s, String last) =>
        Uri.splitQueryString(
          s.requests.lastWhere((r) => r.path.endsWith('/$last')).query,
        );

    test('opens on the web\'s defaults', () async {
      final (c, server) = await start();
      await c.refresh();

      expect(asked(server, 'compare'), {'app': 'shop-web', 'days': '28'});
      expect(asked(server, 'activity')['days'], '28');
      expect(asked(server, 'events')['days'], '28');
      expect(asked(server, 'acquisition')['by'], 'referrer');
      expect(asked(server, 'audience')['by'], 'country');
      final retention = asked(server, 'retention');
      expect(retention['grain'], 'week');
      // Week reads back 180 days.
      expect(
        retention['from'],
        '${DateTime.utc(2026, 11, 2).subtract(const Duration(days: 180)).millisecondsSinceEpoch}',
      );
      expect(c.compare.value!.metrics.sessions.change, isNull);
      expect(c.eventRows.items.first.name, 'checkout_started');
    });

    test('a period changes what depends on one, and not retention', () async {
      final (c, server) = await start();
      await c.refresh();
      final before = server.requests.length;

      await c.setDays(7);

      final paths = server.requests.skip(before).map((r) => r.path).toList();
      expect(paths.length, 5);
      expect(paths.any((p) => p.endsWith('/retention')), isFalse);
      expect(asked(server, 'compare')['days'], '7');
    });

    test('the picked window does not reach it', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, bodyFor(seen.path)),
      );
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl)
        ..token = 'olm_x'
        ..window = () => const TimeRange.preset('1h');
      final c = AnalyticsController(client, 'shop-web');
      addTearDown(c.dispose);

      await c.refresh();

      for (final r in server.requests) {
        final q = Uri.splitQueryString(r.query);
        expect(q.containsKey('to'), isFalse, reason: r.path);
        if (!r.path.endsWith('/retention')) {
          expect(q.containsKey('from'), isFalse, reason: r.path);
        }
      }
    });

    test('a blank is a direct visit, or unknown, in the given words', () async {
      final (c, _) = await start();
      c.setLabels(
        language: 'tr',
        direct: '(doğrudan)',
        unknown: '(bilinmiyor)',
      );
      await c.refresh();
      await pumpEventQueue();

      expect(c.sourceRows.items.map((r) => r.name), contains('(doğrudan)'));
      expect(
        c.audienceRows.items.map((r) => r.name),
        containsAll(['Türkiye', 'Almanya', '(bilinmiyor)']),
      );
      // No previous numbers from this server, so no change, not a dash.
      expect(c.sourceRows.items.first.changeOf(0), isNull);
    });

    test('says when nobody reported a country', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(
          req,
          200,
          seen.path.endsWith('/audience')
              ? audienceJson(values: const [''])
              : bodyFor(seen.path),
        ),
      );
      addTearDown(server.stop);
      final c = AnalyticsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );
      addTearDown(c.dispose);

      await c.refresh();
      expect(c.noCountryRecorded, isTrue);
    });

    test('a server without the comparison keeps everything else', () async {
      final (c, _) = await start(missing: {'compare'});
      await c.refresh();

      expect(c.compare.tooOld, isTrue);
      expect(c.compare.failure, isNull);
      expect(c.compare.pending, isFalse);
      expect(c.activity.value, isNotNull);
      expect(c.eventRows.items, isNotEmpty);
    });

    test('a server without any of it says so, and fails nothing', () async {
      final (c, _) = await start(
        missing: {
          'activity',
          'compare',
          'retention',
          'events',
          'acquisition',
          'audience',
        },
      );
      await c.refresh();

      for (final p in [c.activity, c.compare, c.retention, c.events]) {
        expect(p.tooOld, isTrue);
        expect(p.failure, isNull);
      }
    });

    test('a missing field takes down its part and nothing else', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(
          req,
          200,
          seen.path.endsWith('/compare') ? {'days': 28} : bodyFor(seen.path),
        ),
      );
      addTearDown(server.stop);
      final c = AnalyticsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );
      addTearDown(c.dispose);

      await c.refresh();

      expect(c.compare.failure?.kind, 'unexpected');
      expect(c.activity.value, isNotNull);
    });

    test('the retention table stops at twelve columns', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(
          req,
          200,
          seen.path.endsWith('/retention')
              ? {
                  'grain': 'week',
                  'collecting': true,
                  'sampling_changed': false,
                  'cohorts': [
                    {
                      'cohort': '2026-01-01',
                      'size': 10,
                      'periods': [
                        {'period': 30, 'users': 1, 'rate': 0.1},
                      ],
                    },
                  ],
                }
              : bodyFor(seen.path),
        ),
      );
      addTearDown(server.stop);
      final c = AnalyticsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );
      addTearDown(c.dispose);

      await c.refresh();
      expect(c.retentionColumns, analyticsMaxPeriods);
    });
  });
}
