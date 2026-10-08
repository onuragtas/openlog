// The detail controllers against a real server: which URL they ask for, what
// they put in the body, and what they say when the record has gone or the role
// may not see it.
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/detail.dart';

import 'fake_server.dart';

/// A signed-in client: the token is set after construction, the way the session
/// controller sets it once a device login has come back.
OpenlogClient client(String baseUrl) =>
    OpenlogClient(baseUrl: baseUrl)..token = 'olm_x';

Map<String, Object?> detail({
  String id = 'i1',
  String state = 'open',
  Map<String, String> labels = const {
    'service.name': 'checkout',
    'alert.rule': 'r1',
  },
}) => {
  'id': id,
  'rule_id': 'r1',
  'rule_name': 'API error rate',
  'rule_type': 'apm_error_rate',
  'severity': 'critical',
  'state': state,
  'series_key': 'service.name=checkout',
  'labels': labels,
  'summary': 'error rate 12% over 5m',
  'value': 0.12,
  'last_value': 0.14,
  'threshold': 0.05,
  'flapping': false,
  'muted': false,
  'opened_at': '2026-10-07T20:00:00.000000000Z',
  'acknowledged_at': null,
  'acknowledged_by_email': null,
  'resolved_at': null,
  'resolved_by_email': null,
  'resolve_reason': null,
  'channel_ids': <String>[],
  'events': [
    {
      'id': 1,
      'at': '2026-10-07T20:00:00.000000000Z',
      'kind': 'opened',
      'actor_email': null,
      'message': 'error rate 12% over 5m',
      // An open bag the contract deliberately does not constrain: the
      // generator has to keep it as a map rather than invent a class for it.
      'details': {
        'value': 0.12,
        'breaching_for': '5m',
        'nested': {'a': 1},
      },
    },
  ],
  'deliveries': <Object>[],
};

Map<String, Object?> overview() => {
  'step': '1m',
  'apdex_t_ms': 500,
  'totals': {
    'requests': 1200,
    'throughput': 20,
    'errors': 144,
    'error_rate': 0.12,
    'avg_ms': 80,
    'p50_ms': 42,
    'p95_ms': 310,
    'p99_ms': 980,
    'apdex': 0.91,
  },
  'series': [
    {
      'requests': 300,
      'throughput': 5,
      'errors': 36,
      'error_rate': 0.12,
      // A gap in a series is null, not zero, and must survive as null.
      'avg_ms': null,
      'p50_ms': null,
      'p95_ms': null,
      'p99_ms': null,
      'apdex': null,
      't': 1760000000,
    },
  ],
};

Map<String, Object?> group(
  String id, {
  String status = 'unresolved',
  double count = 10,
  String traceId = 'abc',
}) => {
  'group_id': id,
  'service_name': 'checkout',
  'service_namespace': '',
  'environment': 'production',
  'error_type': 'TimeoutError',
  'message': 'upstream timed out',
  'count': count,
  'total_count': count * 3,
  'first_seen': '2026-10-07T19:00:00.000000000Z',
  'last_seen': '2026-10-07T20:00:00.000000000Z',
  'last_trace_id': traceId,
  'last_span_name': 'POST /checkout',
  'sparkline': <Object>[],
  'status': status,
  'assignee': null,
  'resolved_at': null,
  'resolved_in_version': '',
  'resolved_by_email': '',
  'regressed_at': null,
  'regression_count': 0,
  'comment_count': 0,
  'updated_at': null,
  'updated_by_email': '',
};

Map<String, Object?> inbox(List<Map<String, Object?>> groups) => {
  'step': '1m',
  'groups': groups,
  'counts': {'unresolved': 1, 'resolved': 0, 'ignored': 0},
  'truncated': false,
  'workflow': true,
};

/// `start` is an offset in milliseconds from a fixed instant, so a test can say
/// "this one began 20 ms in" without writing timestamps by hand.
Map<String, Object?> span(
  String id, {
  String parent = '',
  int start = 0,
  int durationMs = 10,
  String name = 'span',
  String status = 'unset',
  String service = 'checkout',
}) => {
  'span_id': id,
  'parent_span_id': parent,
  'name': name,
  'kind': 'server',
  'service_name': service,
  'start': DateTime.utc(
    2026,
    10,
    7,
    20,
  ).add(Duration(milliseconds: start)).toIso8601String(),
  'duration_ns': durationMs * 1000000,
  'status_code': status,
  'status_message': '',
  'attributes': <String, String>{},
  'resource_attributes': <String, String>{},
  'events': <Object>[],
};

void main() {
  test('the incident is asked for by id, and its open details survive', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, detail()),
    );
    addTearDown(server.stop);
    final c = IncidentController(client(server.baseUrl), 'i 1/2');

    await c.refresh();

    expect(c.failure, isNull);
    // Encoded, not interpolated raw: an incident id is server-chosen and a
    // slash in it must not become another path segment.
    expect(server.requests.single.path, '/api/v1/alerts/incidents/i%201%2F2');
    expect(c.value!.events.single.details['breaching_for'], '5m');
    expect(c.value!.events.single.details['nested'], {'a': 1});
    // The label is what makes the tap through to the service possible.
    expect(c.serviceName, 'checkout');
    // The rule engine's own bookkeeping is not shown as if the person chose it.
    expect(c.labels.keys, ['service.name']);
  });

  test('a resolve carries the note, and no note sends no body', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        writeJson(req, 200, detail(state: 'resolved'));
      } else {
        writeJson(req, 200, detail(state: 'resolved'));
      }
    });
    addTearDown(server.stop);
    final c = IncidentController(client(server.baseUrl), 'i1');

    await c.resolve(note: 'restarted the pool');
    final withNote = server.requests.first;
    expect(withNote.method, 'POST');
    expect(withNote.path, '/api/v1/alerts/incidents/i1/resolve');
    expect(jsonDecode(withNote.body), {'note': 'restarted the pool'});

    server.requests.clear();
    await c.resolve();
    // The body is optional on the server, so an empty note sends nothing at
    // all rather than an empty string the server would store as a note.
    expect(server.requests.first.body, '');

    // Either way the screen reloads, so what it shows is the server's state.
    expect(c.value!.state, AlertIncidentState.resolved);
  });

  test('a note is posted, then the screen reloads', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/notes')) {
        writeJson(req, 201, {
          'id': 2,
          'at': '2026-10-07T20:10:00.000000000Z',
          'kind': 'note',
          'actor_email': 'owner@example.com',
          'message': 'looked at it',
          'details': <String, Object?>{},
        });
      } else {
        writeJson(req, 200, detail());
      }
    });
    addTearDown(server.stop);
    final c = IncidentController(client(server.baseUrl), 'i1');

    await c.addNote('looked at it');

    expect(jsonDecode(server.requests.first.body), {'text': 'looked at it'});
    expect(server.requests.map((r) => r.path), [
      '/api/v1/alerts/incidents/i1/notes',
      '/api/v1/alerts/incidents/i1',
    ]);
    expect(c.failure, isNull);
  });

  test('an incident that resolved under the button is said out loud', () async {
    var posts = 0;
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        posts++;
        writeJson(req, 409, {
          'error': {'message': 'already resolved'},
        });
      } else {
        writeJson(req, 200, detail(state: 'resolved'));
      }
    });
    addTearDown(server.stop);
    final c = IncidentController(client(server.baseUrl), 'i1');

    await c.acknowledge();

    expect(posts, 1);
    // Reloaded first and reported after: a successful refresh clears `failure`,
    // so reporting before the reload threw the message away.
    expect(c.failure?.kind, 'alreadyResolved');
    expect(c.value!.state, AlertIncidentState.resolved);
  });

  test('a purged incident reads as gone, not as an empty screen', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 404, {
        'error': {'message': 'no such incident'},
      }),
    );
    addTearDown(server.stop);
    final c = IncidentController(client(server.baseUrl), 'i1');

    await c.refresh();

    expect(c.failure?.kind, 'detailGone');
    expect(c.value, isNull);
  });

  test('a role that may not read alerts is told, not shown nothing', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 403, {
        'error': {'message': 'forbidden'},
      }),
    );
    addTearDown(server.stop);
    final c = IncidentController(client(server.baseUrl), 'i1');

    await c.refresh();

    expect(c.failure?.kind, 'alertsForbidden');
  });

  test('the service overview is asked for by name, gaps and all', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, overview()),
    );
    addTearDown(server.stop);
    final c = ServiceOverviewController(client(server.baseUrl), 'checkout/web');

    await c.refresh();

    expect(c.failure, isNull);
    expect(
      server.requests.single.path,
      '/api/v1/apm/services/checkout%2Fweb/overview',
    );
    expect(c.value!.totals.p95Ms, 310);
    // A latency the server could not compute is null, not zero: zero would
    // draw a line at the bottom of the chart and read as "very fast".
    expect(c.value!.series.single.p95Ms, isNull);
  });

  test('the error inbox puts what is still broken first', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {
        ...inbox([
          group('g1', status: 'resolved', count: 900),
          group('g2', count: 5),
          group('g3', count: 50),
        ]),
      }),
    );
    addTearDown(server.stop);
    final c = ServiceErrorsController(client(server.baseUrl), 'checkout');

    await c.refresh();

    expect(server.requests.single.path, '/api/v1/apm/services/checkout/errors');
    // Unresolved before resolved however loud the resolved one was, then by
    // how often: a phone list is read from the top.
    expect(c.groups.map((g) => g.groupId), ['g3', 'g2', 'g1']);
  });

  test('a trace becomes a tree, deepest path and all', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {
        'trace_id': 'abc',
        'spans': [
          // Deliberately out of order, and the child listed before its parent.
          span('c2', parent: 'root', start: 60, durationMs: 30, name: 'db'),
          span('root', start: 0, durationMs: 100, name: 'POST /checkout'),
          span('c1', parent: 'root', start: 10, durationMs: 40, name: 'auth'),
          span('g1', parent: 'c1', start: 20, durationMs: 10, name: 'redis'),
        ],
      }),
    );
    addTearDown(server.stop);
    final c = TraceController(client(server.baseUrl), 'abc');

    await c.refresh();

    final rows = c.rows;
    // Depth-first, children by start time: the order a person reads it in.
    expect(rows.map((r) => r.span.name), [
      'POST /checkout',
      'auth',
      'redis',
      'db',
    ]);
    expect(rows.map((r) => r.depth), [0, 1, 2, 1]);
    // The bars are shares of the whole request, not of the parent.
    expect(rows[0].offset, 0);
    expect(rows[0].width, 1);
    expect(rows[1].offset, closeTo(0.1, 0.001));
    expect(rows[1].width, closeTo(0.4, 0.001));
    expect(rows[3].offset, closeTo(0.6, 0.001));
  });

  test('a trace missing its middle still shows every span', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {
        'trace_id': 'abc',
        'spans': [
          span('root', start: 0, durationMs: 100),
          // Its parent never arrived. Dropping it would be the worst way to
          // show an incomplete trace, so it is treated as another root.
          span('orphan', parent: 'gone', start: 20, durationMs: 10),
        ],
      }),
    );
    addTearDown(server.stop);
    final c = TraceController(client(server.baseUrl), 'abc');

    await c.refresh();

    expect(c.rows.map((r) => r.span.spanId), ['root', 'orphan']);
    expect(c.rows.map((r) => r.depth), [0, 0]);
  });

  test(
    'a trace with no duration is a full bar, not a divide by zero',
    () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {
          'trace_id': 'abc',
          'spans': [span('root', durationMs: 0)],
        }),
      );
      addTearDown(server.stop);
      final c = TraceController(client(server.baseUrl), 'abc');

      await c.refresh();

      expect(c.rows.single.offset, 0);
      expect(c.rows.single.width, 1);
      expect(c.rows.single.width.isNaN, isFalse);
    },
  );

  test('a server that is not there reads as unreachable', () async {
    final c = ServiceOverviewController(
      // Nothing listens here, and the controller has to name the address rather
      // than show a stack trace.
      OpenlogClient(
        baseUrl: 'http://${InternetAddress.loopbackIPv4.address}:1',
      ),
      'checkout',
    );

    await c.refresh();

    expect(c.failure?.kind, 'unreachable');
  });
}
