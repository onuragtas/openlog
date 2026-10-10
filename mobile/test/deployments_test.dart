// Deployments and the before/after comparison.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/services.dart';

import 'fake_server.dart';

Map<String, Object?> deployment(
  String version,
  String previous,
  int t, {
  bool rollback = false,
  bool initial = false,
}) => {
  'timestamp': '2026-10-09T10:00:00.000000000Z',
  't': t,
  'service_namespace': '',
  'environment': 'prod',
  'version': version,
  'previous_version': previous,
  'initial': initial,
  'rollback': rollback,
};

Map<String, Object?> period(double errorRate, double? p95) => {
  'from': '2026-10-09T09:30:00.000000000Z',
  'to': '2026-10-09T10:00:00.000000000Z',
  'requests': 1000.0,
  'throughput': 33.0,
  'errors': 1000 * errorRate,
  'error_rate': errorRate,
  'avg_ms': 20.0,
  'p50_ms': 15.0,
  'p95_ms': p95,
  'p99_ms': 90.0,
  'apdex': 0.97,
};

void main() {
  test('deployments come back newest first', () async {
    final server = await FakeServer.start((req, seen) {
      writeJson(req, 200, {
        'gap_seconds': 1800,
        'deployments': [
          deployment('1.4.0', '', 1000, initial: true),
          deployment('1.4.2', '1.4.1', 3000),
          deployment('1.4.1', '1.4.0', 2000),
        ],
      });
    });
    addTearDown(server.stop);
    final c = ServiceDeploymentsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'checkout',
    );

    await c.refresh();

    // The question is almost always about the last deployment.
    expect(c.items.map((d) => d.version), ['1.4.2', '1.4.1', '1.4.0']);
  });

  test(
    'comparing sends the timestamp the list carries, and toggles off',
    () async {
      final queries = <String>[];
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/compare')) {
          queries.add(seen.query);
          writeJson(req, 200, {
            'at': '2026-10-09T10:00:00.000000000Z',
            'window_seconds': 1800,
            'apdex_t_ms': 500.0,
            'before': period(0.001, 40.0),
            'after': period(0.02, 120.0),
            'new_error_groups': [
              {
                'group_id': 'g1',
                'error_type': 'TimeoutError',
                'message': 'upstream timed out',
                'first_seen': '2026-10-09T10:05:00.000000000Z',
                'total_count': 12.0,
              },
            ],
          });
          return;
        }
        writeJson(req, 200, {
          'gap_seconds': 1800,
          'deployments': [deployment('1.4.2', '1.4.1', 1760000000000)],
        });
      });
      addTearDown(server.stop);
      final c = ServiceDeploymentsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'checkout',
      );

      await c.refresh();
      await c.toggleCompare(1760000000000);

      // Unix milliseconds, exactly as the deployment list carries them: the
      // server truncates to the minute, and a timestamp this app reformatted
      // would compare a different moment.
      expect(queries.single, contains('at=1760000000000'));
      expect(c.compare?.newErrorGroups, hasLength(1));

      await c.toggleCompare(1760000000000);
      // Tapping the open one closes it; a comparison nobody is looking at is
      // a panel in the way.
      expect(c.compare, isNull);
      expect(c.comparing, isNull);
    },
  );

  test('a delta needs something to compare with', () {
    expect(deltaRatio(40, 120), closeTo(2, 0.0001));
    expect(deltaRatio(120, 60), closeTo(-0.5, 0.0001));
    // Null before, null after, or a zero before: dividing by that zero says
    // infinity, and reading it as "no change" would say the deployment did
    // nothing.
    expect(deltaRatio(null, 10), isNull);
    expect(deltaRatio(10, null), isNull);
    expect(deltaRatio(0, 10), isNull);
  });
}
