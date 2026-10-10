// The two list controllers added in phase 3 and 4, against a real server: what
// they ask for and how they order what comes back.
import 'dart:convert';

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
  'id': body,
  'timestamp': '2026-10-08T09:00:00.000000000Z',
  'observed_timestamp': '2026-10-08T09:00:00.000000000Z',
  'severity_text': severity >= 17 ? 'ERROR' : 'WARN',
  'severity_number': severity,
  'body': body,
  'host_id': 'h1',
  'host_name': 'web-1',
  'service_name': 'checkout',
  'trace_id': '',
  'span_id': '',
  'fields': <String, String>{},
};

/// An empty page, in the shape `POST /logs/query` answers with.
Map<String, Object?> noRows = {'rows': <Object>[], 'next_cursor': null};

/// What the conditions of the one request were.
List<Object?> sentFilters(Seen seen) =>
    (jsonDecode(seen.body) as Map<String, Object?>)['filters'] as List<Object?>;

void main() {
  test('a request\'s logs are asked for by trace, at every level', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, noRows),
    );
    addTearDown(server.stop);
    final c = LogsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      traceId: 'abc123',
    );

    await c.refresh();

    // The explorer endpoint, because it is the only one that applies
    // conditions: `GET /logs` has no `filters` parameter at all and
    // silently answered with the unfiltered stream.
    expect(server.requests.single.method, 'POST');
    expect(server.requests.single.path, '/api/v1/logs/query');
    // No severity floor: narrowing one request's logs to WARN is how you
    // miss the line that explains it. The default WARN is for the whole
    // stream.
    expect(sentFilters(server.requests.single), [
      {'key': 'trace_id', 'op': '=', 'value': 'abc123'},
    ]);
    expect(c.scoped, isTrue);
  });

  test('a pod and a container ask by their own key', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, noRows),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';

    await LogsController(client, podUid: 'u1').refresh();
    await LogsController(client, containerId: 'c1').refresh();

    // The server's own keys (`internal/querybuilder`), not the names the
    // old GET parameters had: a pod's and a container's logs are resource
    // attributes.
    expect(server.requests.map(sentFilters), [
      [
        {'key': 'resource.k8s.pod.uid', 'op': '=', 'value': 'u1'},
      ],
      [
        {'key': 'resource.container.id', 'op': '=', 'value': 'c1'},
      ],
    ]);
  });

  test(
    'the unscoped list keeps its severity floor and takes a service',
    () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, noRows),
      );
      addTearDown(server.stop);
      final c = LogsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      c.service = '  checkout  ';
      await c.refresh();

      expect(sentFilters(server.requests.single), [
        {'key': 'service.name', 'op': '=', 'value': 'checkout'},
        {'key': 'severity_number', 'op': '>=', 'value': 13},
      ]);
      expect(c.scoped, isFalse);
    },
  );

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
            'rows': [line('boom')],
            'next_cursor': null,
          }),
        );
        addTearDown(server.stop);
        final client = OpenlogClient(baseUrl: server.baseUrl);
        addTearDown(client.close);
        final c = LogsController(client);

        await c.refresh();

        expect(c.severityMin, 'WARN');
        expect(sentFilters(server.requests.single), [
          {'key': 'severity_number', 'op': '>=', 'value': 13},
        ]);
        expect(
          jsonDecode(server.requests.single.body),
          containsPair('limit', 50),
        );
        expect(c.items.single.body, 'boom');
        expect(c.items.single.severityNumber, 17);
      },
    );

    test(
      'choosing "all" drops the filter instead of sending an empty one',
      () async {
        final server = await FakeServer.start(
          (req, _) => writeJson(req, 200, noRows),
        );
        addTearDown(server.stop);
        final client = OpenlogClient(baseUrl: server.baseUrl);
        addTearDown(client.close);
        final c = LogsController(client)..severityMin = '';

        await c.refresh();

        // No conditions at all rather than an empty one: `filters: []`
        // and no `filters` are the same question, and the shorter one is
        // the one the browser sends.
        expect(
          jsonDecode(server.requests.single.body),
          isNot(contains('filters')),
        );
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
          'rows': [line('first')],
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
