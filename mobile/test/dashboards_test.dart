// A dashboard is a layout plus one query per widget, so what matters here is
// that the queries actually run, that one failing does not take the dashboard
// down with it, and that a result is read by what it is rather than by what
// the widget asked to be called.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/dashboards.dart';

import 'fake_server.dart';

Map<String, Object?> widget(
  String id,
  String query, {
  String vis = 'billboard',
  String title = 'w',
}) => {
  'id': id,
  'title': title,
  'visualization': vis,
  'layout': {'x': 0, 'y': 0, 'w': 4, 'h': 3},
  'query': query,
  'markdown': '',
  'unit': 'number',
  'thresholds': <Object>[],
  'options': <String, Object?>{},
};

Map<String, Object?> dashboard(List<Map<String, Object?>> widgets) => {
  'id': 'd1',
  'name': 'Checkout health',
  'description': '',
  'visibility': 'organization',
  'version': 3,
  'variables': <Object>[],
  'pages': [
    {'id': 'p1', 'name': 'Overview', 'widgets': widgets},
  ],
  'created_by_user_id': null,
  'created_by_email': 'ada@example.com',
  'created_at': '2026-01-01T00:00:00.000000000Z',
  'updated_at': '2026-10-08T00:00:00.000000000Z',
  'can_edit': false,
};

Map<String, Object?> result({
  required String kind,
  List<Map<String, Object?>> rows = const [],
  List<Map<String, Object?>> series = const [],
}) => {
  'kind': kind,
  'event_type': 'Span',
  'columns': [
    {'name': 'count(*)', 'function': 'count', 'type': 'number'},
  ],
  'facets': <String>[],
  'rows': rows,
  'series': series,
  'buckets': <Object>[],
  'compare': null,
  'metadata': {
    'from': '2026-10-08T08:00:00.000000000Z',
    'to': '2026-10-08T09:00:00.000000000Z',
    'bucket_seconds': 60,
    'rollup': false,
    'table': 'spans',
    'rows_read': 10,
    'bytes_read': 100,
    'elapsed_ms': 3,
    'queries': 1,
    'facet_limit': 10,
    'truncated': false,
    'warnings': <String>[],
  },
};

void main() {
  test(
    'opening a dashboard runs one query per widget and fills them in',
    () async {
      final queries = <String>[];
      final server = await FakeServer.start((req, seen) {
        if (seen.path == '/api/v1/dashboards/d1') {
          writeJson(
            req,
            200,
            dashboard([
              widget('w1', 'SELECT count(*) FROM Span'),
              widget('w2', 'SELECT count(*) FROM Log'),
            ]),
          );
          return;
        }
        queries.add((jsonDecode(seen.body) as Map)['query'] as String);
        writeJson(
          req,
          200,
          result(
            kind: 'single',
            rows: [
              {
                'facets': <String>[],
                'values': [queries.length * 10],
              },
            ],
          ),
        );
      });
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);
      final c = DashboardViewController(client, 'd1');

      await c.load();

      expect(c.dashboard!.name, 'Checkout health');
      expect(c.widgets.map((w) => w.id), ['w1', 'w2']);
      expect(queries, [
        'SELECT count(*) FROM Span',
        'SELECT count(*) FROM Log',
      ]);
      expect(singleValue(c.results['w1']!), 10);
      expect(singleValue(c.results['w2']!), 20);
      expect(c.loading, isFalse);
    },
  );

  test('one widget failing does not take the other nineteen with it', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path == '/api/v1/dashboards/d1') {
        writeJson(
          req,
          200,
          dashboard([
            widget('w1', 'BAD'),
            widget('w2', 'SELECT count(*) FROM Span'),
          ]),
        );
        return;
      }
      if ((jsonDecode(seen.body) as Map)['query'] == 'BAD') {
        writeJson(req, 400, {
          'error': {
            'code': 'invalid_argument',
            'message': 'syntax error at 1:1',
          },
        });
        return;
      }
      writeJson(
        req,
        200,
        result(
          kind: 'single',
          rows: [
            {
              'facets': <String>[],
              'values': [42],
            },
          ],
        ),
      );
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);
    final c = DashboardViewController(client, 'd1');

    await c.load();

    expect(c.widgetErrors['w1'], contains('syntax error'));
    expect(c.results.containsKey('w1'), isFalse);
    // The dashboard itself is fine, and the widget that answered still shows.
    expect(c.failure, isNull);
    expect(singleValue(c.results['w2']!), 42);
  });

  test('a widget with no query is not asked about', () async {
    var queries = 0;
    final server = await FakeServer.start((req, seen) {
      if (seen.path == '/api/v1/dashboards/d1') {
        writeJson(req, 200, dashboard([widget('w1', '   ', vis: 'markdown')]));
        return;
      }
      queries++;
      writeJson(req, 200, result(kind: 'single'));
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);
    final c = DashboardViewController(client, 'd1');

    await c.load();

    expect(queries, 0);
  });

  test(
    'a dashboard nobody may read says so rather than showing nothing',
    () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 403, {
          'error': {'code': 'permission_denied', 'message': 'no'},
        }),
      );
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);
      final c = DashboardViewController(client, 'd1');

      await c.load();

      expect(c.failure!.kind, 'dashboardsForbidden');
      expect(c.dashboard, isNull);
    },
  );

  group('reading a result by what it is', () {
    test('a single result is the first number', () {
      final r = OqlResult.fromJson(
        result(
          kind: 'single',
          rows: [
            {
              'facets': <String>[],
              'values': [1234],
            },
          ],
        ),
      );
      expect(singleValue(r), 1234);
    });

    test('a facet result is a ranked list, largest first', () {
      final r = OqlResult.fromJson(
        result(
          kind: 'facets',
          rows: [
            {
              'facets': ['checkout'],
              'values': [12],
            },
            {
              'facets': ['payments'],
              'values': [97],
            },
            // A row whose value is text is not a bar; dropping it beats drawing 0.
            {
              'facets': ['weird'],
              'values': ['n/a'],
            },
          ],
        ),
      );
      expect(facetRows(r), [('payments', 97.0), ('checkout', 12.0)]);
    });

    test('a timeseries drops gaps rather than drawing them as zero', () {
      final r = OqlResult.fromJson(
        result(
          kind: 'timeseries',
          series: [
            {
              'facets': <String>[],
              'column': 0,
              'points': [
                [1760000000000, 5],
                [1760000060000, null],
                [1760000120000, 7],
              ],
            },
          ],
        ),
      );
      // A null is "no data in this bucket". Zero is a measurement, and drawing
      // one for the other invents a dip that never happened.
      expect(seriesValues(r), [5.0, 7.0]);
    });

    test('an empty result reads as empty, not as an error', () {
      expect(singleValue(OqlResult.fromJson(result(kind: 'single'))), isNull);
      expect(facetRows(OqlResult.fromJson(result(kind: 'facets'))), isEmpty);
      expect(
        seriesValues(OqlResult.fromJson(result(kind: 'timeseries'))),
        isEmpty,
      );
    });
  });
}
