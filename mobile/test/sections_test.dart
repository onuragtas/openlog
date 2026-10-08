// The eight list sections. They share a shape, so what is worth testing is
// what each one does that the others do not -- the SLO sort, the empty query,
// and that a 403 is reported as a permission rather than as an empty list.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/list_controller.dart';
import 'package:openlog_mobile/src/sections.dart';

import 'fake_server.dart';

Map<String, Object?> host(String id, {double? cpu}) => {
  'host_id': id,
  'host_name': '$id.example.com',
  'os_description': 'Ubuntu 24.04',
  'arch': 'arm64',
  'agent_version': '0.1.0',
  'last_seen': '2026-10-08T09:00:00.000000000Z',
  'resource_attributes': <String, String>{},
  'usage': cpu == null
      ? null
      : {
          'cpu': cpu,
          'memory': 0.4,
          'disk': 0.2,
          'load1': 1.0,
          'load_per_cpu': 0.25,
        },
};

Map<String, Object?> slo(String id, {double? remaining, bool met = true}) => {
  'id': id,
  'name': 'SLO $id',
  'description': '',
  'service_name': 'checkout',
  'service_namespace': null,
  'environment': null,
  'sli_type': 'availability',
  'latency_threshold_ms': null,
  'objective': 99.9,
  'window_days': 30,
  'created_by_email': 'a@b.c',
  'updated_by_email': 'a@b.c',
  'created_at': '2026-01-01T00:00:00.000000000Z',
  'updated_at': '2026-01-01T00:00:00.000000000Z',
  'enabled': true,
  'filters': <Object>[],
  'burn_windows': <Object>[],
  'status': remaining == null
      ? null
      : {
          'from': '2026-09-08T00:00:00.000000000Z',
          'to': '2026-10-08T00:00:00.000000000Z',
          'window_days': 30,
          'budget': {
            'requests': 1000.0,
            'good': 999.0,
            'bad': 1.0,
            'sli': 0.999,
            'budget_requests': 1.0,
            'budget_consumed': 1 - remaining,
            'budget_remaining': remaining,
            'remaining_ratio': remaining,
            'burn_rate': 1.0,
            'met': met,
          },
        },
};

Map<String, Object?> spanRow(
  String traceId, {
  bool error = false,
  double durationMs = 42,
}) => {
  'id': traceId,
  'timestamp': '2026-10-08T09:00:00.000000000Z',
  'trace_id': traceId,
  'span_id': 's1',
  'parent_span_id': '',
  'name': 'POST /checkout',
  'kind': 'server',
  'status_code': error ? 'error' : 'ok',
  'status_message': '',
  'service_name': 'checkout',
  'host_id': 'h1',
  'duration_ns': (durationMs * 1000000).round(),
  'duration_ms': durationMs,
  'is_entry': true,
  'is_error': error,
  'http_status_code': error ? 500 : 200,
  'transaction_name': 'POST /checkout',
  'fields': <String, String>{},
};

void main() {
  test('the traces list asks for requests, not for every span', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {
        'rows': [spanRow('t1'), spanRow('t2', error: true)],
        'next_cursor': null,
      }),
    );
    addTearDown(server.stop);
    final c = TracesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    final sent = server.requests.single;
    expect(sent.method, 'POST');
    expect(sent.path, '/api/v1/traces/query');
    // root_only, or the first page would be database calls belonging to three
    // requests -- a span list, not the list of requests the person opened.
    expect(jsonDecode(sent.body), {'root_only': true, 'limit': 50});
    expect(c.items.map((r) => r.traceId), ['t1', 't2']);
  });

  test('slowest and a search change what is asked, not what is kept', () async {
    final server = await FakeServer.start(
      (req, _) =>
          writeJson(req, 200, {'rows': <Object>[], 'next_cursor': null}),
    );
    addTearDown(server.stop);
    final c = TracesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    c.slowest = true;
    c.query = '  checkout  ';
    await c.refresh();

    expect(jsonDecode(server.requests.single.body), {
      'root_only': true,
      'limit': 50,
      'sort': 'duration',
      'filters': [
        {'key': 'service_name', 'op': 'contains', 'value': 'checkout'},
      ],
    });
  });

  test('a section sends its search and reads the rows back', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {
        'hosts': [host('h1', cpu: 0.92)],
      }),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);
    final c = HostsController(client)..query = 'web';

    await c.refresh();

    expect(server.requests.single.query, contains('q=web'));
    expect(c.items.single.hostName, 'h1.example.com');
    expect(c.items.single.usage!.cpu, closeTo(0.92, 1e-9));
  });

  test('an empty search box sends no q at all', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {'hosts': <Object>[]}),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);

    // `q=` is a filter that matches the empty string on some endpoints and
    // everything on others, and neither is what an empty box means.
    await HostsController(client).refresh();
    expect(server.requests.single.query, isEmpty);
  });

  test('a whitespace-only search is an empty one', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {'hosts': <Object>[]}),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);

    await (HostsController(client)..query = '   ').refresh();
    expect(server.requests.single.query, isEmpty);
  });

  test('SLOs come back with the emptiest budget first', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(req, 200, {
        'slos': [
          slo('healthy', remaining: 0.80),
          slo('dying', remaining: 0.02, met: false),
          slo('nostatus'),
          slo('tight', remaining: 0.15),
        ],
        'status_truncated': false,
      }),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);
    final c = SlosController(client);

    await c.refresh();

    // Worst first; an SLO with no computed status sorts last rather than
    // pretending to be fine or pretending to be broken.
    expect(c.items.map((s) => s.id), ['dying', 'tight', 'healthy', 'nostatus']);
    expect(c.items.first.status!.budget.met, isFalse);
  });

  test('the SLO list asks the server to compute the budgets', () async {
    final server = await FakeServer.start(
      (req, _) =>
          writeJson(req, 200, {'slos': <Object>[], 'status_truncated': false}),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);

    await SlosController(client).refresh();

    // Without it the list is names and nothing else, which is not what anyone
    // opens an SLO screen for.
    expect(server.requests.single.query, contains('status=true'));
  });

  test('a role that may not read a section is told so', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 403, {
        'error': {'code': 'permission_denied', 'message': 'no'},
      }),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);
    final c = VulnerabilitiesController(client);

    await c.refresh();

    // An empty list and "you may not look" render identically and mean
    // opposite things.
    expect(c.failure!.kind, 'sectionForbidden');
    expect(c.items, isEmpty);
  });

  test('every section reads its own key out of its own answer', () async {
    final server = await FakeServer.start((req, seen) {
      final body = switch (seen.path) {
        '/api/v1/containers' => {
          'containers': <Object>[],
          'total': 0,
          'step': '60s',
        },
        '/api/v1/kubernetes/pods' => {'pods': <Object>[], 'total': 0},
        '/api/v1/synthetics/checks' => {
          'checks': <Object>[],
          'locations': <Object>[],
        },
        '/api/v1/jobs/monitors' => {'monitors': <Object>[]},
        '/api/v1/vulnerabilities' => {
          'vulnerabilities': <Object>[],
          'severity_counts': <String, int>{},
        },
        '/api/v1/db/instances' => {'instances': <Object>[]},
        _ => <String, Object?>{},
      };
      writeJson(req, 200, body);
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);

    // Each wrapper names its list differently, and reading the wrong key is a
    // mistake that looks exactly like "nothing is reporting".
    for (final c in <ListController<Object?>>[
      ContainersController(client),
      PodsController(client),
      SyntheticsController(client),
      JobsController(client),
      VulnerabilitiesController(client),
      DatabasesController(client),
    ]) {
      await c.refresh();
      expect(
        c.failure,
        isNull,
        reason: '${c.runtimeType} did not read its answer',
      );
      expect(c.loaded, isTrue);
    }
  });
}
