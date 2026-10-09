// The detail controllers against a real server: which URL they ask for, what
// they put in the body, and what they say when the record has gone or the role
// may not see it.
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/detail.dart';
import 'package:openlog_mobile/src/ui/profiles_screen.dart';

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

Map<String, Object?> metricDetail({String agg = 'avg'}) => {
  'name': 'http.server.duration',
  'type': 'histogram',
  'unit': 'ms',
  'description': 'request duration',
  'temporality': 'delta',
  'monotonic': false,
  'last_seen': '2026-10-08T09:00:00.000000000Z',
  'series': 7,
  'services': ['checkout'],
  'attribute_keys': [
    {
      'key': 'http.route',
      'name': 'http.route',
      'source': 'attribute',
      'type': 'string',
      'count': 10,
      'cardinality': 4,
    },
  ],
  'resource_keys': <Object>[],
  'aggregations': ['p50', 'p95', 'p99'],
  'default_aggregation': agg,
};

Map<String, Object?> metricSeries() => {
  'metric': {
    'name': 'http.server.duration',
    'type': 'histogram',
    'unit': 'ms',
    'temporality': 'delta',
    'monotonic': false,
  },
  'aggregation': 'p95',
  'step': '60s',
  'series': [
    {
      'attributes': {'http.route': '/checkout'},
      'points': [
        [1760000000000, 12.5],
        [1760000060000, 18.0],
      ],
    },
    {'attributes': <String, String>{}, 'points': <Object>[]},
  ],
  'truncated': false,
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

  test('a metric is read first, then drawn with its own aggregation', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        writeJson(req, 200, metricSeries());
      } else {
        writeJson(req, 200, metricDetail(agg: 'p95'));
      }
    });
    addTearDown(server.stop);
    final c = MetricController(client(server.baseUrl), 'http.server.duration');

    await c.refresh();

    expect(server.requests.map((r) => '${r.method} ${r.path}'), [
      'GET /api/v1/metrics/http.server.duration',
      'POST /api/v1/metrics/query',
    ]);
    // Which aggregation is meaningful depends on the metric's type, so it
    // comes from the metadata rather than from a guess in this app.
    expect(jsonDecode(server.requests.last.body), {
      'metric': 'http.server.duration',
      'aggregation': 'p95',
    });
    expect(c.values, [12.5, 18.0]);
    expect(c.seriesError, isNull);
  });

  test('a metric with no chart still shows what it is', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        writeJson(req, 422, {
          'error': {'message': 'too many series'},
        });
      } else {
        writeJson(req, 200, metricDetail());
      }
    });
    addTearDown(server.stop);
    final c = MetricController(client(server.baseUrl), 'http.server.duration');

    await c.refresh();

    // The metadata arrived, so the screen is worth showing; only the chart is
    // missing, and the reason is kept rather than the whole screen failing.
    expect(c.failure, isNull);
    expect(c.value!.attributeKeys.single.key, 'http.route');
    expect(c.seriesError, contains('too many series'));
    expect(c.values, isEmpty);
  });

  test('a metric name with a slash is encoded, not split', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        writeJson(req, 200, metricSeries());
      } else {
        writeJson(req, 200, metricDetail());
      }
    });
    addTearDown(server.stop);
    final c = MetricController(client(server.baseUrl), 'queue/depth');

    await c.refresh();

    expect(server.requests.first.path, '/api/v1/metrics/queue%2Fdepth');
  });

  test('costs off is said as off, not as something gone missing', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 404, {
        'error': {'message': 'no such endpoint'},
      }),
    );
    addTearDown(server.stop);
    final c = CostsController(client(server.baseUrl));

    await c.refresh();

    // With OPENLOG_COST_ENABLED=false the server never registers the route,
    // so this 404 is a statement about the installation. The base class would
    // have called it "no longer on the server", which is a different claim.
    expect(c.failure?.kind, 'costsOff');
  });

  test('the fleet summary and its hosts arrive together', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {
        'hosts': [
          {
            'host_id': 'h1',
            'host_name': 'web-1',
            'provider': 'aws',
            'instance_type': 'm5.large',
            'region': 'eu-central-1',
            'zone': 'eu-central-1a',
            'lifecycle': 'on-demand',
            'vcpus': 2,
            'memory_bytes': 8589934592,
            'hours': 24,
            'price': {
              'usd_per_hour': 0.096,
              'source': 'table',
              'region_multiplier': 1.0,
            },
            'total': 2.3,
            'services': 1.1,
            'unallocated': 0.2,
            'unattributed': 0.4,
            'idle': 0.6,
            'used_share': 0.74,
            'idle_share': 0.26,
            'oversubscribed': false,
            'priced': true,
          },
        ],
        'total': 12,
        'summary': {
          'currency': 'USD',
          'total': 41.2,
          'services': 20.0,
          'unallocated': 4.0,
          'unattributed': 5.2,
          'idle': 12.0,
          'idle_share': 0.29,
          'per_hour': 1.7,
          'hosts': 12,
          'priced_hosts': 11,
          'unpriced_hosts': 1,
          'host_hours': 288,
        },
        'pricing': {
          'version': 3,
          'updated': '2026-09-17',
          'currency': 'USD',
          'note': 'discounts, taxes, storage and egress are not included',
          'estimated': true,
        },
      }),
    );
    addTearDown(server.stop);
    final c = CostsController(client(server.baseUrl));

    await c.refresh();

    // One request answers all three: a summary without the hosts behind it is
    // a number nobody can act on.
    expect(server.requests.single.path, '/api/v1/costs/hosts');
    expect(c.value!.summary.total, 41.2);
    expect(c.value!.hosts.single.hostName, 'web-1');
    // The list is capped; the screen has to be able to say how many were left.
    expect(c.value!.total, 12);
    expect(c.value!.pricing.estimated, isTrue);
  });

  test('a profile value is read in the unit the profile declared', () {
    // The same number in two profiles. Reading four gigabytes as four seconds
    // would be the worst kind of wrong, so the unit comes from the data and
    // never from the type's name.
    expect(formatProfileValue(4000000000, 'nanoseconds'), '4.00 s');
    expect(formatProfileValue(4000000000, 'bytes'), '3.73 GiB');
    expect(formatProfileValue(4000000, 'nanoseconds'), '4 ms');
    expect(formatProfileValue(4000, 'nanoseconds'), '4 µs');
    expect(formatProfileValue(512, 'bytes'), '512 B');
    // A unit this build does not know is shown as the profile named it,
    // rather than silently turned into milliseconds.
    expect(formatProfileValue(42, 'count'), '42 count');
    expect(formatProfileValue(42, ''), '42');
  });

  test(
    'a function share is of the rows shown, which is what can be checked',
    () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {
          'unit': 'nanoseconds',
          'type': 'cpu',
          // The server's own words: total is the sum of the rows returned, not
          // of the window.
          'total': 1000,
          'functions': [
            {'function': 'main.hot', 'self': 600, 'samples': 60},
            {'function': 'main.warm', 'self': 400, 'samples': 40},
          ],
        }),
      );
      addTearDown(server.stop);
      final c = ProfileFunctionsController(
        client(server.baseUrl),
        service: 'checkout',
        type: 'cpu',
        environment: 'production',
      );

      await c.refresh();

      expect(
        server.requests.single.query,
        'service=checkout&type=cpu&limit=50&environment=production',
      );
      expect(c.shareOf(c.value!.functions.first), 0.6);
      expect(c.shareOf(c.value!.functions.last), 0.4);
    },
  );

  test('an empty profile does not divide by zero', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {
        'unit': 'nanoseconds',
        'type': 'cpu',
        'total': 0,
        'functions': <Object>[],
      }),
    );
    addTearDown(server.stop);
    final c = ProfileFunctionsController(
      client(server.baseUrl),
      service: 'checkout',
      type: 'cpu',
      environment: '',
    );

    await c.refresh();

    expect(c.value!.functions, isEmpty);
    expect(
      c.shareOf(const ProfileFunction(function: 'x', self: 1, samples: 1)),
      0,
    );
  });

  test(
    'a host says what runs on it, and survives having no snapshot',
    () async {
      var snapshots = 0;
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/services')) {
          snapshots++;
          writeJson(req, 200, {
            'snapshot_id': 's1',
            'snapshot_time': '2026-10-08T09:00:00.000000000Z',
            'items': [
              {
                'category': 'discovered_service',
                'key': '/usr/sbin/nginx',
                'data': {
                  'rule_id': 'nginx',
                  'version': '1.27.0',
                  'integration': {
                    'id': 'nginx',
                    'status': 'error',
                    'error': 'connection refused',
                  },
                },
              },
              {
                'category': 'discovered_service',
                'key': '/usr/bin/redis-server',
                'data': {
                  'rule_id': 'redis',
                  'integration': {'id': 'redis', 'status': 'enabled'},
                },
              },
              // Not JSON: skipped rather than taking the screen down.
              {
                'category': 'discovered_service',
                'key': '/opt/weird',
                'data': 'not json',
              },
            ],
          });
        } else {
          writeJson(req, 200, {
            'host_id': 'h1',
            'host_name': 'web-1',
            'os_description': 'Ubuntu 24.04',
            'arch': 'arm64',
            'agent_version': '0.1.113',
            'last_seen': '2026-10-08T09:00:00.000000000Z',
            'resource_attributes': {'cloud.provider': 'aws'},
            'usage': {
              'cpu': 0.42,
              'memory': 0.77,
              'disk': 0.2,
              'load1': 3.1,
              'load_per_cpu': 0.78,
            },
          });
        }
      });
      addTearDown(server.stop);
      final c = HostController(client(server.baseUrl), 'h1');

      await c.refresh();

      expect(server.requests.map((r) => r.path), [
        '/api/v1/hosts/h1',
        '/api/v1/hosts/h1/services',
      ]);
      expect(snapshots, 1);
      // Same ordering as the Integrations section: what is wrong comes first.
      expect(c.services.map((s) => s.name), ['nginx', 'redis']);
      expect(c.servicesError, isNull);
    },
  );

  test('a host with no snapshot still shows the host', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/services')) {
        writeJson(req, 404, {
          'error': {'message': 'no snapshot for this host'},
        });
      } else {
        writeJson(req, 200, {
          'host_id': 'h1',
          'host_name': 'web-1',
          'os_description': 'Ubuntu 24.04',
          'arch': 'arm64',
          'agent_version': '0.1.113',
          'last_seen': '2026-10-08T09:00:00.000000000Z',
          'resource_attributes': <String, String>{},
          'usage': null,
        });
      }
    });
    addTearDown(server.stop);
    final c = HostController(client(server.baseUrl), 'h1');

    await c.refresh();

    // The host arrived, so the screen is worth showing; only the list of what
    // runs on it is missing, and the reason is kept.
    expect(c.failure, isNull);
    expect(c.value!.hostName, 'web-1');
    expect(c.servicesError, contains('no snapshot'));
    expect(c.services, isEmpty);
  });

  test('a container without a memory limit has no share to show', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/timeseries')) {
        writeJson(req, 200, {
          'container_id': 'c1',
          'host_id': 'h1',
          'step': '20s',
          'from': 1760000000000,
          'to': 1760000600000,
          'series': {
            'cpu_utilization': [
              [1760000000000, 0.12],
              [1760000020000, 0.31],
            ],
            'memory_usage': [
              [1760000000000, 104857600],
              [1760000020000, 125829120],
            ],
            // No limit: the server reports zeroes rather than omitting it.
            'memory_limit': [
              [1760000000000, 0],
              [1760000020000, 0],
            ],
            'network_receive': <Object>[],
            'network_transmit': <Object>[],
            'blockio_read': <Object>[],
            'blockio_write': <Object>[],
          },
        });
      } else {
        writeJson(req, 200, {
          'container_id': 'c1',
          'name': 'checkout-1',
          'image_name': 'ghcr.io/resoft/checkout',
          'image_tags': ['v3'],
          'runtime': 'docker',
          'host_id': 'h1',
          'host_name': 'web-1',
          'compose_project': '',
          'compose_service': '',
          'k8s_pod_name': '',
          'k8s_namespace_name': '',
          'k8s_container_name': '',
          'state': 'running',
          'health': 'healthy',
          'started_at': null,
          'restart_count': 0,
          'first_seen': '2026-10-01T09:00:00.000000000Z',
          'last_seen': '2026-10-08T09:00:00.000000000Z',
          'reporting': true,
          'cpu_utilization': 0.31,
          'memory_usage': 125829120,
          'memory_limit': 0,
          'cpu_sparkline': <Object>[],
          'memory_sparkline': <Object>[],
          'attributes': {'com.docker.compose.project': 'resoft'},
        });
      }
    });
    addTearDown(server.stop);
    final c = ContainerController(client(server.baseUrl), 'c1');

    await c.refresh();

    expect(server.requests.map((r) => r.path), [
      '/api/v1/containers/c1',
      '/api/v1/containers/c1/timeseries',
    ]);
    expect(ContainerController.values(c.series!.series.cpuUtilization), [
      0.12,
      0.31,
    ]);
    // A limit of zero is not a limit: plotting a share against it would
    // invent a ceiling the container does not have.
    expect(c.memoryShare, isEmpty);
  });

  test('a container with a limit gets a share of it', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/timeseries')) {
        writeJson(req, 200, {
          'container_id': 'c1',
          'host_id': 'h1',
          'step': '20s',
          'from': 1760000000000,
          'to': 1760000600000,
          'series': {
            'cpu_utilization': <Object>[],
            'memory_usage': [
              [1760000000000, 536870912],
            ],
            'memory_limit': [
              [1760000000000, 1073741824],
            ],
            'network_receive': <Object>[],
            'network_transmit': <Object>[],
            'blockio_read': <Object>[],
            'blockio_write': <Object>[],
          },
        });
      } else {
        writeJson(req, 200, {
          'container_id': 'c1',
          'name': 'checkout-1',
          'image_name': 'ghcr.io/resoft/checkout',
          'image_tags': <String>[],
          'runtime': 'docker',
          'host_id': 'h1',
          'host_name': 'web-1',
          'compose_project': '',
          'compose_service': '',
          'k8s_pod_name': '',
          'k8s_namespace_name': '',
          'k8s_container_name': '',
          'state': 'running',
          'health': '',
          'started_at': null,
          'restart_count': 3,
          'first_seen': '2026-10-01T09:00:00.000000000Z',
          'last_seen': '2026-10-08T09:00:00.000000000Z',
          'reporting': true,
          'cpu_utilization': null,
          'memory_usage': 536870912,
          'memory_limit': 1073741824,
          'cpu_sparkline': <Object>[],
          'memory_sparkline': <Object>[],
          'attributes': <String, String>{},
        });
      }
    });
    addTearDown(server.stop);
    final c = ContainerController(client(server.baseUrl), 'c1');

    await c.refresh();

    expect(c.memoryShare, [0.5]);
    expect(c.value!.restartCount, 3);
  });

  test(
    'a stuck pod opens on the warning, not on the thirty normal events',
    () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/events')) {
          writeJson(req, 200, {
            'events': [
              {
                'timestamp': '2026-10-08T09:00:00.000000000Z',
                'type': 'Normal',
                'reason': 'Pulled',
                'message': 'Container image already present',
                'count': 1,
                'namespace': 'prod',
                'object_kind': 'Pod',
                'object_name': 'checkout-abc',
                'object_uid': 'u1',
                'source': 'kubelet',
                'cluster_uid': 'c1',
                'cluster_name': 'prod-1',
              },
              {
                // Older than the Normal above, and still the one that matters.
                'timestamp': '2026-10-08T08:30:00.000000000Z',
                'type': 'Warning',
                'reason': 'BackOff',
                'message': 'Back-off restarting failed container',
                'count': 42,
                'namespace': 'prod',
                'object_kind': 'Pod',
                'object_name': 'checkout-abc',
                'object_uid': 'u1',
                'source': 'kubelet',
                'cluster_uid': 'c1',
                'cluster_name': 'prod-1',
              },
            ],
          });
        } else if (seen.path.endsWith('/timeseries')) {
          writeJson(req, 500, {
            'error': {'message': 'metrics unavailable'},
          });
        } else {
          writeJson(req, 200, {
            'cluster_uid': 'c1',
            'cluster_name': 'prod-1',
            'namespace': 'prod',
            'pod_name': 'checkout-abc',
            'pod_uid': 'u1',
            'node_name': 'node-3',
            'workload_kind': 'Deployment',
            'workload_name': 'checkout',
            'phase': 'Running',
            'ready': false,
            'reason': 'CrashLoopBackOff',
            'status': 'CrashLoopBackOff',
            'restarts': 42,
            'pod_ip': '10.1.2.3',
            'qos_class': 'Burstable',
            'created_at': null,
            'started_at': null,
            'cpu_usage': null,
            'memory_working_set': null,
            'cpu_request': null,
            'cpu_limit': null,
            'memory_request': null,
            'memory_limit': null,
            'first_seen': '2026-10-01T09:00:00.000000000Z',
            'last_seen': '2026-10-08T09:00:00.000000000Z',
            'reporting': true,
            'containers': <Object>[],
            'labels': {'app': 'checkout'},
            'services': <Object>[],
            'host_id': null,
            'host_name': null,
          });
        }
      });
      addTearDown(server.stop);
      final c = PodController(client(server.baseUrl), 'u1');

      await c.refresh();

      expect(server.requests.map((r) => r.path), [
        '/api/v1/kubernetes/pods/u1',
        '/api/v1/kubernetes/pods/u1/timeseries',
        '/api/v1/kubernetes/pods/u1/events',
      ]);
      // Warnings first even when a Normal event is newer: a pod with thirty
      // Normal events and one Warning is a pod with one problem.
      expect(c.sortedEvents.first.reason, 'BackOff');
      expect(c.sortedEvents.first.count, 42);
      // One missing piece does not hide the others: the metrics failed, the
      // events did not, and the pod itself is on screen.
      expect(c.failure, isNull);
      expect(c.seriesError, contains('metrics unavailable'));
      expect(c.eventsError, isNull);
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
