// The query console against a real server: what it posts, what it keeps, and
// what it says when the server will not run what was written.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/query.dart';

import 'fake_server.dart';

OpenlogClient client(String baseUrl) =>
    OpenlogClient(baseUrl: baseUrl)..token = 'olm_x';

Map<String, Object?> answer({int rowsRead = 1200, int elapsedMs = 34}) => {
  'kind': 'single',
  'event_type': 'logs',
  'columns': [
    {'name': 'count', 'function': 'count', 'type': 'number'},
  ],
  'facets': <String>[],
  'rows': [
    {
      'facets': <String>[],
      'values': [42],
    },
  ],
  'series': <Object>[],
  'buckets': <Object>[],
  'compare': null,
  'metadata': {
    'from': '2026-10-07T19:00:00.000000000Z',
    'to': '2026-10-07T20:00:00.000000000Z',
    'bucket_seconds': null,
    'rollup': false,
    'table': 'logs',
    'rows_read': rowsRead,
    'bytes_read': 99000,
    'elapsed_ms': elapsedMs,
    'queries': 1,
    'facet_limit': 20,
    'truncated': false,
    'warnings': <String>[],
    'ignored_filters': null,
  },
};

void main() {
  test('a run posts the query and keeps it in reach', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, answer()),
    );
    addTearDown(server.stop);
    final c = QueryController(client(server.baseUrl));

    // Whitespace is the person's, not the query's.
    await c.run('  SELECT count(*) FROM logs  ');

    expect(server.requests.single.path, '/api/v1/query');
    expect(jsonDecode(server.requests.single.body), {
      'query': 'SELECT count(*) FROM logs',
    });
    expect(c.ran, 'SELECT count(*) FROM logs');
    expect(c.result, isNotNull);
    expect(c.history, ['SELECT count(*) FROM logs']);
    expect(c.failure, isNull);
  });

  test('an empty box and a run already in flight both do nothing', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, answer()),
    );
    addTearDown(server.stop);
    final c = QueryController(client(server.baseUrl));

    await c.run('   ');
    expect(server.requests, isEmpty);

    c.running = true;
    await c.run('SELECT 1');
    expect(server.requests, isEmpty);
  });

  test('the same query twice moves up rather than repeating', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, answer()),
    );
    addTearDown(server.stop);
    final c = QueryController(client(server.baseUrl));

    await c.run('A');
    await c.run('B');
    await c.run('A');

    // Newest first, and A is one entry, not two: a history that repeats is a
    // history nobody can find anything in.
    expect(c.history, ['A', 'B']);
  });

  test('a rejected query is reported in the server words', () async {
    final server = await FakeServer.start(
      (req, seen) => jsonDecode(seen.body)['query'] == 'bad'
          ? writeJson(req, 400, {
              'error': {'message': 'unexpected token at position 4'},
            })
          : writeJson(req, 200, answer()),
    );
    addTearDown(server.stop);
    final c = QueryController(client(server.baseUrl));

    await c.run('SELECT count(*) FROM logs');
    expect(c.result, isNotNull);

    await c.run('bad');

    expect(c.failure?.kind, 'queryRejected');
    expect(c.failure?.detail, contains('unexpected token'));
    // The old answer belonged to the old query; leaving it under the new one
    // would be a lie about what the server said.
    expect(c.result, isNull);
    expect(c.ran, '');
    // And the query that failed is not offered as something that worked.
    expect(c.history, ['SELECT count(*) FROM logs']);
  });

  test('a role that may not query is told, not shown a syntax error', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 403, {
        'error': {'message': 'forbidden'},
      }),
    );
    addTearDown(server.stop);
    final c = QueryController(client(server.baseUrl));

    await c.run('SELECT 1');

    expect(c.failure?.kind, 'queryForbidden');
  });
}
