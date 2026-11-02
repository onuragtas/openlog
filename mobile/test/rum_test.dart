// The browser screens past the overview: the routes, the visits, one visit's
// timeline, the versions and a funnel.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/rum.dart';
import 'package:openlog_mobile/src/sections.dart';

import 'fake_server.dart';

Map<String, Object?> vital(
  String name, {
  String unit = 'ms',
  double p75 = 2210,
  String rating = 'good',
}) => {
  'name': name,
  'unit': unit,
  'count': 1840,
  'p50': 1620,
  'p75': p75,
  'p95': 3480,
  'avg': 1890,
  'good': 0.82,
  'needs_improvement': 0.14,
  'poor': 0.04,
  'rating': rating,
  'good_threshold': 2500,
  'poor_threshold': 4000,
};

Map<String, Object?> page(String route, {double views = 940}) => {
  'route': route,
  'views': views,
  'avg_ms': 1180,
  'p50_ms': 1040,
  'p75_ms': 1520,
  'p95_ms': 2400,
  'max_ms': 5200,
  'ttfb_avg_ms': 310,
  'lcp_p75': 1980,
  'errors': 3,
};

Map<String, Object?> visit(String id, {int errors = 1}) => {
  'session_id': id,
  'app': 'shop-web',
  'environment': 'production',
  'started_at': '2026-10-11T09:00:00.000000000Z',
  'ended_at': '2026-10-11T09:05:00.000000000Z',
  'duration_ms': 302000,
  'page_views': 4,
  'errors': errors,
  'entry_route': '/',
  'exit_route': '/checkout',
  'device_type': 'desktop',
  'browser_name': 'Chrome',
  'browser_version': '131',
  'os_name': 'macOS',
  'trace_id': '3a7f19c4b2e84d15a6c093f7e2b18d40',
  'user_id': '',
  'country': '',
};

Map<String, String> seenQuery(FakeServer server) =>
    Uri.splitQueryString(server.requests.last.query);

void main() {
  test('the routes are asked for by the chosen measure', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(req, 200, {
        'pages': [page('/'), page('/checkout', views: 280)],
      }),
    );
    addTearDown(server.stop);
    final c = RumPagesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'shop-web',
    );

    await c.refresh();
    expect(c.items.length, 2);
    expect(Uri.splitQueryString(server.requests.single.query), {
      'app': 'shop-web',
      'sort': 'views',
      'dir': 'desc',
      'limit': '50',
    });

    c.sort = 'slowest';
    await c.refresh();
    expect(Uri.splitQueryString(server.requests.last.query)['sort'], 'slowest');
    // Picking the same column turns it around, and a name starts from A.
    await c.setSort('slowest');
    expect(Uri.splitQueryString(server.requests.last.query)['dir'], 'asc');
    await c.setSort('route');
    expect(Uri.splitQueryString(server.requests.last.query)['dir'], 'asc');
  });

  test('a route asks for its own vitals, not the application\'s', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(req, 200, {
        'from': '2026-10-11T08:00:00.000000000Z',
        'to': '2026-10-11T09:00:00.000000000Z',
        'vitals': [vital('lcp', p75: 4300, rating: 'poor')],
      }),
    );
    addTearDown(server.stop);
    final c = RumRouteVitalsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'shop-web',
      '/checkout',
    );

    await c.refresh();

    // The route is part of the question: a route's p75 is not derivable from
    // the application's, which is the whole reason this is a second request.
    expect(Uri.splitQueryString(server.requests.single.query), {
      'app': 'shop-web',
      'route': '/checkout',
    });
    expect(c.value!.vitals.single.rating, 'poor');
  });

  test('a kind of visitor travels with the request', () async {
    final queries = <String>[];
    final server = await FakeServer.start((req, seen) {
      queries.add('${seen.path}?${seen.query}');
      if (seen.path.endsWith('/pages')) {
        writeJson(req, 200, {
          'pages': [page('/')],
        });
      } else if (seen.path.endsWith('/vitals')) {
        writeJson(req, 200, {
          'from': '2026-10-12T08:00:00.000000000Z',
          'to': '2026-10-12T09:00:00.000000000Z',
          'vitals': [vital('lcp')],
        });
      } else {
        writeJson(req, 200, {'sessions': <Object>[]});
      }
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';

    final pages = RumPagesController(client, 'shop-web')
      ..deviceType = 'mobile'
      ..browser = 'Safari';
    await pages.refresh();

    // The page-view rollup is keyed by device and browser, so both go.
    expect(queries.last, contains('device_type=mobile'));
    expect(queries.last, contains('browser=Safari'));

    // The vitals rollup has no browser column; only the device travels, and
    // the client does not invent a parameter the server would refuse.
    final vitals = RumRouteVitalsController(
      client,
      'shop-web',
      '/checkout',
      deviceType: 'mobile',
    );
    await vitals.refresh();
    expect(queries.last, contains('device_type=mobile'));
    expect(queries.last, isNot(contains('browser=')));

    final sessions = RumSessionsController(client, 'shop-web')
      ..deviceType = 'desktop'
      ..userId = 'user-42';
    await sessions.refresh();
    expect(queries.last, contains('device_type=desktop'));
    expect(queries.last, contains('user_id=user-42'));
  });

  test('no facet, no parameter', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(req, 200, {'sessions': <Object>[]}),
    );
    addTearDown(server.stop);
    final c = RumSessionsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'shop-web',
    );

    await c.refresh();

    // An empty facet is not "" on the wire: the server would read it as a
    // device whose name is the empty string and answer nothing.
    final asked = Uri.splitQueryString(server.requests.single.query);
    expect(asked.containsKey('device_type'), isFalse);
    expect(asked.containsKey('browser'), isFalse);
    expect(asked.containsKey('user_id'), isFalse);
  });

  test('the visits of an application, and one of them in full', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/sessions')) {
        writeJson(req, 200, {
          'sessions': [visit('9f2c41b7a80d4e6fb35c1d8e07a4b620')],
        });
        return;
      }
      writeJson(req, 200, {
        'session': visit('9f2c41b7a80d4e6fb35c1d8e07a4b620'),
        'events': [
          {
            'timestamp': '2026-10-11T09:01:00.000000000Z',
            'event': 'page_view',
            'name': '/checkout',
            'route': '/checkout',
            'duration_ms': 1240,
            'trace_id': '3a7f19c4b2e84d15a6c093f7e2b18d40',
            'span_id': '00f067aa0ba902b7',
            'error_group_id': '',
            'status_code': 200,
          },
        ],
      });
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';

    final list = RumSessionsController(client, 'shop-web');
    await list.refresh();
    expect(list.items.single.browserName, 'Chrome');

    final one = RumSessionController(
      client,
      '9f2c41b7a80d4e6fb35c1d8e07a4b620',
    );
    await one.refresh();
    expect(
      server.requests.last.path,
      '/api/v1/rum/sessions/9f2c41b7a80d4e6fb35c1d8e07a4b620',
    );
    expect(one.value!.events.single.route, '/checkout');
  });

  test('an older visit keeps its summary and loses its timeline', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(req, 200, {
        'session': visit('old', errors: 0),
        // The events live in the spans, so they go with the trace retention
        // while the rollup stays.
        'events': <Object>[],
      }),
    );
    addTearDown(server.stop);
    final c = RumSessionController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'old',
    );

    await c.refresh();
    expect(c.value!.events, isEmpty);
    expect(c.value!.session.pageViews, 4);
  });

  test('versions and the deployments that introduced them', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(req, 200, {
        'releases': [
          {
            'version': '1.4.2',
            'sessions': 480,
            'error_sessions': 6,
            'crash_free_rate': 0.9875,
            'first_seen': '2026-10-10T09:00:00.000000000Z',
            'last_seen': '2026-10-11T09:00:00.000000000Z',
          },
          // Older than the trace retention: the deployment is known, the
          // sessions are not.
          {
            'version': '1.3.9',
            'sessions': 0,
            'error_sessions': 0,
            'crash_free_rate': 1,
            'first_seen': '2026-10-01T09:00:00.000000000Z',
            'last_seen': '2026-10-02T09:00:00.000000000Z',
          },
        ],
        'deployments': [
          {
            'timestamp': '2026-10-11T06:00:00.000000000Z',
            't': 1791698400000,
            'service_namespace': '',
            'environment': 'production',
            'version': '1.4.2',
            'previous_version': '1.4.1',
            'initial': false,
            'rollback': true,
          },
        ],
      }),
    );
    addTearDown(server.stop);
    final c = RumReleasesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'shop-web',
    );

    await c.refresh();
    expect(c.items.first.crashFreeRate, closeTo(0.9875, 0.0001));
    expect(c.items.last.sessions, 0);
    expect(c.deployments.single.rollback, isTrue);
    expect(Uri.splitQueryString(server.requests.single.query), {
      'app': 'shop-web',
      'sort': 'last_seen',
      'dir': 'desc',
      'limit': '50',
    });
  });

  group('the other lists sort and page as the web does', () {
    Map<String, Object?> app(String name) => {
      'app': name,
      'environment': 'production',
      'views': 10,
      'sessions': 2,
      'errors': 0,
      'last_seen': '2026-10-11T09:00:00.000000000Z',
    };

    test('the applications, busiest first', () async {
      final server = await FakeServer.start((req, seen) {
        final q = Uri.splitQueryString(seen.query);
        writeJson(req, 200, {
          'apps': [app(q['offset'] == null ? 'one' : 'two')],
          'has_more': q['offset'] == null,
        });
      });
      addTearDown(server.stop);
      final c = RumController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      await c.refresh();
      expect(Uri.splitQueryString(server.requests.last.query), {
        'sort': 'views',
        'dir': 'desc',
        'limit': '50',
      });
      expect(c.hasMore, isTrue);

      await c.loadMore();
      expect(c.items.map((a) => a.app), ['one', 'two']);
      expect(c.hasMore, isFalse);
      expect(Uri.splitQueryString(server.requests.last.query)['offset'], '1');

      // A name starts from A; a number from the largest.
      await c.setSort('app');
      expect(Uri.splitQueryString(server.requests.last.query)['dir'], 'asc');
      await c.setSort('environment');
      expect(Uri.splitQueryString(server.requests.last.query)['dir'], 'asc');
      await c.setSort('errors');
      expect(Uri.splitQueryString(server.requests.last.query)['dir'], 'desc');
    });

    test('the sessions, by when they started', () async {
      final server = await FakeServer.start((req, seen) {
        final q = Uri.splitQueryString(seen.query);
        writeJson(req, 200, {
          'sessions': [
            visit(q['offset'] == null ? 'first-one-xxx' : 'next-one-xxxx'),
          ],
          'has_more': q['offset'] == null,
        });
      });
      addTearDown(server.stop);
      final c = RumSessionsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );

      await c.refresh();
      expect(Uri.splitQueryString(server.requests.last.query), {
        'app': 'shop-web',
        'sort': 'started',
        'dir': 'desc',
        'limit': '50',
      });
      await c.loadMore();
      expect(c.items.length, 2);

      await c.setSort('entry');
      expect(Uri.splitQueryString(server.requests.last.query)['dir'], 'asc');
      await c.setSort('page_views');
      expect(Uri.splitQueryString(server.requests.last.query)['dir'], 'desc');
    });

    test('the versions page while the deployments stay', () async {
      Map<String, Object?> release(String v) => {
        'version': v,
        'sessions': 10,
        'error_sessions': 1,
        'crash_free_rate': 0.9,
        'first_seen': '2026-10-11T09:00:00.000000000Z',
        'last_seen': '2026-10-11T09:00:00.000000000Z',
      };
      Map<String, Object?> deployment(String v) => {
        'timestamp': '2026-10-11T09:00:00.000000000Z',
        't': 1,
        'service_namespace': '',
        'environment': 'production',
        'version': v,
        'previous_version': '',
        'initial': true,
        'rollback': false,
      };
      final server = await FakeServer.start((req, seen) {
        final q = Uri.splitQueryString(seen.query);
        final first = q['offset'] == null;
        writeJson(req, 200, {
          'releases': [release(first ? '1.2' : '1.1')],
          'deployments': first ? [deployment('1.2')] : <Object>[],
          'has_more': first,
        });
      });
      addTearDown(server.stop);
      final c = RumReleasesController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );

      await c.refresh();
      await c.loadMore();
      expect(c.items.map((r) => r.version), ['1.2', '1.1']);
      expect(c.deployments.single.version, '1.2');

      await c.setSort('crash_free');
      expect(
        Uri.splitQueryString(server.requests.last.query)['sort'],
        'crash_free',
      );
      expect(Uri.splitQueryString(server.requests.last.query)['dir'], 'desc');
    });

    test('an older server sends no next page, and nothing breaks', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, {
          'sessions': [visit('old-server-xxxx')],
        }),
      );
      addTearDown(server.stop);
      final c = RumSessionsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );

      await c.refresh();
      expect(c.hasMore, isFalse);
      expect(c.items.length, 1);
    });
  });

  group('the crashes', () {
    test('are asked for by application and read the new fields', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, {
          'crashes': [
            {
              'group_id': 'a1',
              'type': 'Boom',
              'message': 'bad',
              'kind': 'signal',
              'count': 3,
              'sessions': 2,
              'last_version': '1.0',
              'last_seen': '2026-10-11T09:00:00.000000000Z',
              'stack': 'x',
              'frame': 'main',
              'missing_symbols': ['libshop.so'],
            },
          ],
        }),
      );
      addTearDown(server.stop);
      final c = RumCrashesController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );

      await c.refresh();
      expect(seenQuery(server), {'app': 'shop-web'});
      expect(c.items.single.frame, 'main');
      expect(c.items.single.missingSymbols, ['libshop.so']);
      expect(c.serverTooOld, isFalse);
    });

    test(
      'a 404 is an old server, not a failure and not an empty list',
      () async {
        final server = await FakeServer.start(
          (req, seen) => writeJson(req, 404, {
            'error': {'code': 'not_found', 'message': 'no such route'},
          }),
        );
        addTearDown(server.stop);
        final c = RumCrashesController(
          OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
          'shop-web',
        );

        await c.refresh();
        expect(c.serverTooOld, isTrue);
        expect(c.failure, isNull);
      },
    );

    test('another failure stays a failure', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 500, {
          'error': {'code': 'internal', 'message': 'boom'},
        }),
      );
      addTearDown(server.stop);
      final c = RumCrashesController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );

      await c.refresh();
      expect(c.serverTooOld, isFalse);
      expect(c.failure?.kind, 'unexpected');
    });

    test('a missing field is a failure, not an exception', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, {
          'crashes': [
            {'group_id': 'a1'},
          ],
        }),
      );
      addTearDown(server.stop);
      final c = RumCrashesController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );

      await c.refresh();
      expect(c.failure?.kind, 'unexpected');
    });
  });

  group('the funnel', () {
    test('asks nothing until there are two steps', () async {
      final server = await FakeServer.start(
        (req, seen) =>
            writeJson(req, 200, {'steps': <Object>[], 'window_seconds': 1800}),
      );
      addTearDown(server.stop);
      final c = RumFunnelController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );

      await c.run(steps: ['cart_viewed']);

      // The endpoint refuses one step with a 400; on a screen where the
      // steps are typed that would be an error per keystroke.
      expect(c.ready, isFalse);
      expect(server.requests, isEmpty);
      expect(c.value, isNull);
    });

    test('sends the steps in order, with the window', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, {
          'steps': [
            {'step': 'cart_viewed', 'sessions': 400, 'rate': 1},
            {'step': 'order_placed', 'sessions': 300, 'rate': 0.75},
          ],
          'window_seconds': 3600,
        }),
      );
      addTearDown(server.stop);
      final c = RumFunnelController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );

      await c.run(steps: ['cart_viewed', 'order_placed'], window: '1h');

      final q = server.requests.single.query;
      expect(q, contains('step=cart_viewed'));
      expect(q, contains('step=order_placed'));
      expect(q, contains('window=1h'));
      // In order: the first step named in the URL is the first step of the
      // funnel, and the server counts the rest against it.
      expect(q.indexOf('cart_viewed') < q.indexOf('order_placed'), isTrue);
      expect(c.value!.steps.last.rate, 0.75);
    });

    test('a refused funnel is reported rather than left blank', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 400, {
          'error': {
            'code': 'invalid_argument',
            'message': 'at least two steps',
          },
        }),
      );
      addTearDown(server.stop);
      final c = RumFunnelController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'shop-web',
      );

      await c.run(steps: ['a', 'b']);

      expect(c.failure!.kind, 'unexpected');
      expect(c.value, isNull);
    });
  });
}
