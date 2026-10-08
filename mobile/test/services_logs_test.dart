// The two list controllers added in phase 3 and 4, against a real server: what
// they ask for and how they order what comes back.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/logs.dart';
import 'package:openlog_mobile/src/services.dart';

import 'fake_server.dart';

Map<String, Object?> svc(
  String name, {
  double errorRate = 0,
  double throughput = 100,
}) => {
  'requests': 1200,
  'throughput': throughput,
  'errors': 3,
  'error_rate': errorRate,
  'avg_ms': 90,
  'p50_ms': 70,
  'p95_ms': 180,
  'p99_ms': 400,
  'apdex': 0.97,
  'service_name': name,
  'service_namespace': '',
  'environment': 'prod',
  'language': 'go',
  'version': '1.2.3',
  'last_seen': '2026-10-08T09:00:00.000000000Z',
  'apdex_t_ms': 500,
  'sparkline': [
    [1760000000000, 12.5],
  ],
};

Map<String, Object?> line(String body, {int severity = 17}) => {
  'timestamp': '2026-10-08T09:00:00.000000000Z',
  'severity_text': severity >= 17 ? 'ERROR' : 'WARN',
  'severity_number': severity,
  'body': body,
  'host_id': 'h1',
  'service_name': 'checkout',
  'trace_id': '',
  'span_id': '',
  'attributes': <String, String>{},
  'resource_attributes': <String, String>{},
};

void main() {
  group('services', () {
    test(
      'the worst service is first, because the top is what gets read',
      () async {
        final server = await FakeServer.start(
          (req, _) => writeJson(req, 200, {
            'step': '60s',
            'services': [
              svc('quiet'),
              svc('broken', errorRate: 0.12),
              svc('busy', throughput: 900),
            ],
          }),
        );
        addTearDown(server.stop);
        final client = OpenlogClient(baseUrl: server.baseUrl);
        addTearDown(client.close);
        final c = ServicesController(client);

        await c.refresh();

        // Errors first, then throughput: a phone list is read from the top and
        // rarely scrolled, so the broken one has to be there.
        expect(c.items.map((s) => s.serviceName), ['broken', 'busy', 'quiet']);
        expect(c.items.first.errorRate, closeTo(0.12, 1e-9));
        expect(c.items.first.p95Ms, 180);
        // allOf composed ApmRed into the same class rather than a nested one.
        expect(c.items.first.requests, 1200);
        expect(c.items.first.sparkline.single, [1760000000000, 12.5]);
      },
    );

    test('the search box becomes a query the server can answer', () async {
      final server = await FakeServer.start(
        (req, _) =>
            writeJson(req, 200, {'step': '60s', 'services': <Object>[]}),
      );
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);
      final c = ServicesController(client)..query = 'check';

      await c.refresh();

      expect(server.requests.single.query, contains('q=check'));
    });

    test('an empty search asks for everything rather than for ""', () async {
      final server = await FakeServer.start(
        (req, _) =>
            writeJson(req, 200, {'step': '60s', 'services': <Object>[]}),
      );
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);

      await ServicesController(client).refresh();

      expect(server.requests.single.query, isEmpty);
    });
  });

  group('logs', () {
    test(
      'warnings and worse by default, because ten lines is the screen',
      () async {
        final server = await FakeServer.start(
          (req, _) => writeJson(req, 200, {
            'logs': [line('boom')],
            'next_cursor': null,
          }),
        );
        addTearDown(server.stop);
        final client = OpenlogClient(baseUrl: server.baseUrl);
        addTearDown(client.close);
        final c = LogsController(client);

        await c.refresh();

        expect(c.severityMin, 'WARN');
        expect(server.requests.single.query, contains('severity_min=WARN'));
        expect(server.requests.single.query, contains('limit=50'));
        expect(c.items.single.body, 'boom');
        expect(c.items.single.severityNumber, 17);
      },
    );

    test(
      'choosing "all" drops the filter instead of sending an empty one',
      () async {
        final server = await FakeServer.start(
          (req, _) =>
              writeJson(req, 200, {'logs': <Object>[], 'next_cursor': null}),
        );
        addTearDown(server.stop);
        final client = OpenlogClient(baseUrl: server.baseUrl);
        addTearDown(client.close);
        final c = LogsController(client)..severityMin = '';

        await c.refresh();

        expect(server.requests.single.query, isNot(contains('severity_min')));
      },
    );

    test('a failed reload keeps the lines already on screen', () async {
      var fail = false;
      final server = await FakeServer.start((req, _) {
        if (fail) {
          writeJson(req, 500, {
            'error': {'code': 'internal', 'message': 'boom'},
          });
          return;
        }
        writeJson(req, 200, {
          'logs': [line('first')],
          'next_cursor': null,
        });
      });
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);
      final c = LogsController(client);

      await c.refresh();
      fail = true;
      await c.refresh();

      expect(c.failure, isNotNull);
      expect(c.items.single.body, 'first');
    });

    test('403 says it is a permission, not an empty log stream', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 403, {
          'error': {'code': 'permission_denied', 'message': 'no'},
        }),
      );
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);
      final c = LogsController(client);

      await c.refresh();

      expect(c.failure!.kind, 'logsForbidden');
    });
  });
}
