// The Databases section's other side: the statements as the applications
// measured them, their plans, the way to the server's view, and what activity
// lists on its own. A real server on loopback, so what leaves is what is
// checked. Screens are in app_db_queries_screen_test.dart.
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/databases.dart';
import 'package:openlog_mobile/src/sections.dart';

import 'fake_server.dart';

Map<String, Object?> statement(String text, {List<String>? services}) => {
  'services': ?services,
  'db_system': 'postgresql',
  'db_name': 'shop',
  'db_operation': 'SELECT',
  'statement': text,
  'calls': 1200.0,
  'throughput': 20.0,
  'errors': 0.0,
  'error_rate': 0.0,
  'avg_ms': 12.5,
  'p95_ms': 40.0,
  'max_ms': 90.0,
  'time_consumed_ms': 15000.0,
  'time_share': 0.25,
};

/// What an older server answers for a route it has not got.
void noSuchEndpoint(HttpRequest req) => writeJson(req, 404, {
  'error': {'code': 'not_found', 'message': 'no such endpoint'},
});

Map<String, String> queryOf(Seen seen) => Uri.splitQueryString(seen.query);

void main() {
  group('where the section opens', () {
    test('on the servers, unless they answered and there are none', () {
      // Still asking, or failed: nothing is known about the servers yet.
      expect(
        defaultDatabaseSource(loaded: false, failed: false, count: 0),
        'servers',
      );
      expect(
        defaultDatabaseSource(loaded: false, failed: true, count: 0),
        'servers',
      );
      expect(
        defaultDatabaseSource(loaded: true, failed: false, count: 2),
        'servers',
      );
      expect(
        defaultDatabaseSource(loaded: true, failed: false, count: 0),
        'applications',
      );
    });
  });

  group('the statements of the applications', () {
    test('the web\'s defaults go out, and the search with them', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, {
          'queries': [
            statement('SELECT 1', services: ['checkout']),
          ],
          'has_more': true,
        }),
      );
      addTearDown(server.stop);
      final c = AppDbQueriesController(OpenlogClient(baseUrl: server.baseUrl))
        ..query = '  orders ';

      await c.refresh();

      expect(server.requests.single.path, '/api/v1/apm/db-queries');
      expect(queryOf(server.requests.single), {
        'sort': 'time',
        'dir': 'desc',
        'limit': '50',
        'q': 'orders',
      });
      expect(c.items.single.services, ['checkout']);
      expect(c.hasMore, isTrue);

      await c.loadMore();
      expect(queryOf(server.requests.last)['offset'], '1');
      expect(c.items, hasLength(2));
    });

    test('text columns start from the beginning of the alphabet', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, {'queries': <Object>[]}),
      );
      addTearDown(server.stop);
      final c = AppDbQueriesController(OpenlogClient(baseUrl: server.baseUrl));

      await c.setSort('statement');
      expect(queryOf(server.requests.last)['dir'], 'asc');
      await c.setSort('database');
      expect(queryOf(server.requests.last)['dir'], 'asc');
      await c.setSort('calls');
      expect(queryOf(server.requests.last)['dir'], 'desc');
    });

    test('a statement with no services field is still a statement', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, {
          'queries': [statement('SELECT 1')],
        }),
      );
      addTearDown(server.stop);
      final c = AppDbQueriesController(OpenlogClient(baseUrl: server.baseUrl));

      await c.refresh();

      expect(c.failure, isNull);
      expect(c.items.single.services, isNull);
      // No has_more from an older server means no next page, not a crash.
      expect(c.hasMore, isFalse);
    });

    test('an older server\'s 404 is a state, not a failure', () async {
      final server = await FakeServer.start((req, _) => noSuchEndpoint(req));
      addTearDown(server.stop);
      final c = AppDbQueriesController(OpenlogClient(baseUrl: server.baseUrl));

      await c.refresh();

      expect(c.notOffered, isTrue);
      expect(c.failure, isNull);
      expect(c.items, isEmpty);
      expect(c.loaded, isTrue);
    });

    test('a 404 that names what is missing is not an older server', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 404, {
          'error': {'code': 'not_found', 'message': 'organization not found'},
        }),
      );
      addTearDown(server.stop);
      final c = AppDbQueriesController(OpenlogClient(baseUrl: server.baseUrl));

      await c.refresh();

      expect(c.notOffered, isFalse);
      expect(c.failure?.detail, 'organization not found');
    });

    test('any other failure stays a failure', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 500, {
          'error': {'code': 'x', 'message': 'broke'},
        }),
      );
      addTearDown(server.stop);
      final c = AppDbQueriesController(OpenlogClient(baseUrl: server.baseUrl));

      await c.refresh();

      expect(c.notOffered, isFalse);
      expect(c.failure?.detail, 'broke');
    });

    test(
      'a required field missing is said, not thrown past the list',
      () async {
        final server = await FakeServer.start(
          (req, _) => writeJson(req, 200, {
            'queries': [
              {'statement': 'SELECT 1'},
            ],
          }),
        );
        addTearDown(server.stop);
        final c = AppDbQueriesController(
          OpenlogClient(baseUrl: server.baseUrl),
        );

        await c.refresh();

        expect(c.failure?.kind, 'unexpected');
        expect(c.failure?.detail, contains('queries[0]'));
      },
    );

    test('the section keeps it, and a new range makes it stale', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {'queries': <Object>[]}),
      );
      addTearDown(server.stop);
      final sections = Sections(client: OpenlogClient(baseUrl: server.baseUrl));
      addTearDown(sections.dispose);

      await sections.appDbQueries.refresh();
      expect(sections.appDbQueries.loaded, isTrue);

      sections.markRangeStale();
      expect(sections.appDbQueries.loaded, isFalse);
    });
  });

  group('the plans of a statement', () {
    test('asks about the statement, and keeps the generic flag', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, {
          'plans': [
            {
              'plan': 'Seq Scan on orders',
              'service': 'checkout',
              'generic': true,
              'captured_at': '2026-10-10T09:00:00.000000000Z',
              'trace_id': 'abc',
            },
          ],
        }),
      );
      addTearDown(server.stop);
      final c = AppDbPlansController(
        OpenlogClient(baseUrl: server.baseUrl),
        dbSystem: 'postgresql',
        statement: 'SELECT * FROM orders WHERE id = ?',
      );

      await c.refresh();

      expect(server.requests.single.path, '/api/v1/apm/db-queries/plans');
      expect(queryOf(server.requests.single), {
        'db_system': 'postgresql',
        'statement': 'SELECT * FROM orders WHERE id = ?',
      });
      expect(c.value!.single.generic, isTrue);
    });

    test('none is an answer, and an older server another', () async {
      var answer = 200;
      final server = await FakeServer.start((req, _) {
        if (answer == 404) return noSuchEndpoint(req);
        writeJson(req, 200, {'plans': <Object>[]});
      });
      addTearDown(server.stop);
      final c = AppDbPlansController(
        OpenlogClient(baseUrl: server.baseUrl),
        dbSystem: 'postgresql',
        statement: 'SELECT 1',
      );

      await c.refresh();
      expect(c.value, isEmpty);
      expect(c.notOffered, isFalse);

      answer = 404;
      await c.refresh();
      expect(c.notOffered, isTrue);
      expect(c.failure, isNull);
    });
  });

  group('the way to the server\'s view', () {
    test('the first match is where the statement cost the most', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {
          'matches': [
            {
              'instance': 'db1:5432',
              'fingerprint': '42',
              'host_name': 'db1',
              'calls': 900,
              'avg_ms': 1.5,
            },
            {
              'instance': 'db2:5432',
              'fingerprint': '42',
              'host_name': 'db2',
              'calls': 10,
              'avg_ms': null,
            },
          ],
        }),
      );
      addTearDown(server.stop);
      final c = DbLookupController(OpenlogClient(baseUrl: server.baseUrl));

      final match = await c.ask(dbSystem: 'postgresql', statement: 'SELECT 1');

      expect(server.requests.single.path, '/api/v1/db/lookup');
      expect(queryOf(server.requests.single), {
        'db_system': 'postgresql',
        'statement': 'SELECT 1',
      });
      expect(match?.instance, 'db1:5432');
      expect(c.notMonitored, isFalse);
    });

    test('no match says it is not monitored there', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {'matches': <Object>[]}),
      );
      addTearDown(server.stop);
      final c = DbLookupController(OpenlogClient(baseUrl: server.baseUrl));

      expect(await c.ask(dbSystem: 'mysql', statement: 'SELECT 1'), isNull);
      expect(c.notMonitored, isTrue);
      expect(c.failure, isNull);
    });

    test(
      'an older server\'s 404 is neither a failure nor "no match"',
      () async {
        final server = await FakeServer.start((req, _) => noSuchEndpoint(req));
        addTearDown(server.stop);
        final c = DbLookupController(OpenlogClient(baseUrl: server.baseUrl));

        expect(await c.ask(dbSystem: 'mysql', statement: 'SELECT 1'), isNull);
        expect(c.notOffered, isTrue);
        expect(c.notMonitored, isFalse);
        expect(c.failure, isNull);
      },
    );
  });

  group('activity\'s own lists', () {
    test('waits and statements, with the web\'s defaults', () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/waits')) {
          writeJson(req, 200, {
            'waits': [
              {'type': 'Lock', 'event': 'tuple', 'samples': 9, 'share': 0.5},
            ],
            'has_more': true,
          });
        } else {
          writeJson(req, 200, {
            'queries': [
              {
                'fingerprint': '7',
                'text': 'SELECT 1',
                'samples': 3,
                'avg_active_sessions': 0.5,
                'top_wait': 'CPU',
              },
            ],
          });
        }
      });
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      final waits = DbActivityWaitsController(client, 'db1:5432');
      final top = DbActivityQueriesController(client, 'db1:5432');

      await waits.refresh();
      await top.refresh();

      expect(server.requests[0].path, '/api/v1/db/activity/waits');
      expect(server.requests[1].path, '/api/v1/db/activity/queries');
      for (final r in server.requests) {
        expect(queryOf(r), {
          'instance': 'db1:5432',
          'sort': 'samples',
          'dir': 'desc',
          'limit': '50',
        });
      }
      expect(waits.items.single.event, 'tuple');
      expect(waits.hasMore, isTrue);
      expect(top.items.single.topWait, 'CPU');

      await waits.setSort('type');
      expect(queryOf(server.requests.last)['dir'], 'asc');
      await top.setSort('wait');
      expect(queryOf(server.requests.last)['dir'], 'asc');
    });

    test('an older server\'s 404 leaves them not offered', () async {
      final server = await FakeServer.start((req, _) => noSuchEndpoint(req));
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      final waits = DbActivityWaitsController(client, 'db1:5432');
      final top = DbActivityQueriesController(client, 'db1:5432');

      await waits.refresh();
      await top.refresh();

      expect(waits.notOffered, isTrue);
      expect(top.notOffered, isTrue);
      expect(waits.failure, isNull);
      expect(top.failure, isNull);
    });
  });

  group('sort and pages on the older lists', () {
    test('the instances send the web\'s defaults and turn pages', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {
          'instances': [
            {
              'instance': 'db1:5432',
              'db_system': 'postgresql',
              'host_id': 'h1',
              'host_name': 'db1',
              'server_address': 'db1',
              'server_port': 5432,
              'calls': 10,
              'throughput': 1.5,
              'total_time_ms': 100.0,
              'avg_ms': 2.0,
              'statements': 3,
              'errors': 0,
              'avg_active_sessions': null,
              'top_wait': '',
              'last_seen': '2026-10-10T09:00:00.000000000Z',
            },
          ],
          'has_more': true,
        }),
      );
      addTearDown(server.stop);
      final c = DatabasesController(OpenlogClient(baseUrl: server.baseUrl));

      await c.refresh();
      expect(queryOf(server.requests.last), {
        'sort': 'time',
        'dir': 'desc',
        'limit': '50',
      });

      await c.loadMore();
      expect(queryOf(server.requests.last)['offset'], '1');

      await c.setSort('host');
      expect(queryOf(server.requests.last)['sort'], 'host');
      expect(queryOf(server.requests.last)['dir'], 'asc');
    });

    test('a server older than has_more has no next page', () async {
      final server = await FakeServer.start(
        (req, _) =>
            writeJson(req, 200, {'queries': <Object>[], 'total_time_ms': 0.0}),
      );
      addTearDown(server.stop);
      final c = DbQueriesController(
        OpenlogClient(baseUrl: server.baseUrl),
        'db1:5432',
      );

      await c.refresh();

      expect(c.hasMore, isFalse);
      expect(queryOf(server.requests.single)['dir'], 'desc');
      await c.setSort('statement');
      expect(queryOf(server.requests.last)['dir'], 'asc');
    });

    test('an instance\'s name is the server it monitors', () {
      expect(dbSystemName('mssql'), 'SQL Server');
      expect(dbSystemName('oracle'), 'oracle');
    });
  });
}
