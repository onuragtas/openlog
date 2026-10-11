// The browser screens past the overview: the routes, the visits, one visit's
// timeline, the versions and a funnel.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/rum.dart';

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
    });

    c.sort = 'slowest';
    await c.refresh();
    expect(Uri.splitQueryString(server.requests.last.query)['sort'], 'slowest');
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
    expect(c.value!.releases.first.crashFreeRate, closeTo(0.9875, 0.0001));
    expect(c.value!.releases.last.sessions, 0);
    expect(c.value!.deployments.single.rollback, isTrue);
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
