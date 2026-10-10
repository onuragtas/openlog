// The eight list sections. They share a shape, so what is worth testing is
// what each one does that the others do not -- the SLO sort, the empty query,
// and that a 403 is reported as a permission rather than as an empty list.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
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

Map<String, Object?> mute(
  String id, {
  bool active = true,
  int endsInHours = 2,
}) => {
  'id': id,
  'name': 'mute $id',
  'comment': '',
  'starts_at': DateTime.now().toUtc().toIso8601String(),
  'ends_at': DateTime.now()
      .toUtc()
      .add(Duration(hours: endsInHours))
      .toIso8601String(),
  'rule_ids': <String>[],
  'matchers': <Object>[],
  'schedule': null,
  'upcoming': <Object>[],
  'active': active,
  'created_by_user_id': null,
  'created_by_email': 'owner@example.com',
  'created_at': '2026-10-01T09:00:00.000000000Z',
  'updated_at': '2026-10-01T09:00:00.000000000Z',
};

Map<String, Object?> route(String id, int position, {bool enabled = true}) => {
  'id': id,
  'name': 'route $id',
  'position': position,
  'enabled': enabled,
  'is_default': false,
  'match': <String, Object?>{},
  'channel_ids': ['c1'],
  'created_by_email': 'owner@example.com',
  'created_at': '2026-10-01T09:00:00.000000000Z',
  'updated_at': '2026-10-01T09:00:00.000000000Z',
};

void main() {
  test('reordering sends every id once, in the new order', () async {
    final sent = <Map<String, Object?>>[];
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        sent.add(jsonDecode(seen.body) as Map<String, Object?>);
        writeJson(req, 200, {'routing_rules': <Object>[]});
      } else {
        writeJson(req, 200, {
          'routing_rules': [route('a', 0), route('b', 1), route('c', 2)],
        });
      }
    });
    addTearDown(server.stop);
    final c = AlertRoutesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();
    expect(c.items.map((r) => r.id), ['a', 'b', 'c']);

    // Drag the last one to the top.
    await c.move(2, 0);

    // The whole order, every id exactly once: the server rejects a partial
    // list rather than reshuffling quietly.
    expect(sent.single['ids'], ['c', 'a', 'b']);
  });

  test('a rejected reorder puts the list back', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        writeJson(req, 403, {
          'error': {'message': 'admin or owner'},
        });
      } else {
        writeJson(req, 200, {
          'routing_rules': [route('a', 0), route('b', 1)],
        });
      }
    });
    addTearDown(server.stop);
    final c = AlertRoutesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();
    await c.move(1, 0);

    // The row followed the finger, then the server said no; the reload is
    // what puts it back, so the screen never shows an order the server does
    // not have.
    expect(c.items.map((r) => r.id), ['a', 'b']);
    expect(c.failure?.kind, 'alertsForbidden');
  });

  test('turning a route off sends the rest of it back unchanged', () async {
    final puts = <Map<String, Object?>>[];
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'PUT') {
        puts.add(jsonDecode(seen.body) as Map<String, Object?>);
        writeJson(req, 200, route('a', 0, enabled: false));
      } else {
        writeJson(req, 200, {
          'routing_rules': [route('a', 0)],
        });
      }
    });
    addTearDown(server.stop);
    final c = AlertRoutesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();
    await c.setEnabled(c.items.single, enabled: false);

    // There is no enable endpoint, so this is a PUT of the whole rule. Every
    // other field has to go back as it came, or it gets edited by omission.
    expect(puts.single['enabled'], false);
    expect(puts.single['name'], 'route a');
    expect(puts.single['position'], 0);
    expect(puts.single['channel_ids'], ['c1']);
    expect(puts.single['is_default'], false);
  });

  test('a mute is a window that starts now, in UTC', () async {
    final posted = <Map<String, Object?>>[];
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        posted.add(jsonDecode(seen.body) as Map<String, Object?>);
        writeJson(req, 201, mute('m9', active: true));
      } else {
        writeJson(req, 200, {'mutes': <Object>[]});
      }
    });
    addTearDown(server.stop);
    final c = AlertMutesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    final before = DateTime.now().toUtc();
    await c.createFor(
      name: 'checkout dagitimi',
      duration: const Duration(hours: 2),
    );

    expect(posted.single['name'], 'checkout dagitimi');
    final starts = DateTime.parse(posted.single['starts_at']! as String);
    final ends = DateTime.parse(posted.single['ends_at']! as String);
    // UTC, and two hours apart: a phone in another timezone must not open a
    // window that already closed.
    expect(starts.isUtc, isTrue);
    expect(ends.difference(starts), const Duration(hours: 2));
    expect(
      starts.isBefore(before.subtract(const Duration(minutes: 1))),
      isFalse,
    );
    // No rule_ids at all rather than an empty list: empty means every rule to
    // the server either way, and sending nothing says it once.
    expect(posted.single.containsKey('rule_ids'), isFalse);
  });

  test('what is silencing now comes first', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {
        'mutes': [
          mute('m1', active: false, endsInHours: 1),
          mute('m2', active: true, endsInHours: 5),
          mute('m3', active: true, endsInHours: 2),
        ],
      }),
    );
    addTearDown(server.stop);
    final c = AlertMutesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    // Active first, then by when they end.
    expect(c.items.map((m) => m.id), ['m3', 'm2', 'm1']);
  });

  test('a mute that expired under the button is not an error', () async {
    var deletes = 0;
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'DELETE') {
        deletes++;
        writeJson(req, 404, {
          'error': {'message': 'no such mute'},
        });
      } else {
        writeJson(req, 200, {'mutes': <Object>[]});
      }
    });
    addTearDown(server.stop);
    final c = AlertMutesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.end('m1');

    expect(deletes, 1);
    // It ended on its own between the list being drawn and the button being
    // pressed. The row goes; no red banner.
    expect(c.failure, isNull);
  });

  test('a channel test that the receiver refused is not a success', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/test')) {
        // 200, and it did not work. This is the trap: the status code is
        // about the API call, the body is about the pager.
        writeJson(req, 200, {
          'success': false,
          'status_code': 404,
          'error': 'no_service: that Slack webhook no longer exists',
          'duration_ms': 143,
          'notification_id': 'n1',
        });
      } else {
        writeJson(req, 200, {
          'channels': [
            {
              'id': 'ch1',
              'name': 'oncall-slack',
              'type': 'slack',
              'enabled': true,
              'config': <String, Object?>{},
              'secret_hints': {'url': 'https://hooks.slack.com/…/•••f3a9'},
              'created_by_email': 'owner@example.com',
              'created_at': '2026-10-01T09:00:00.000000000Z',
              'updated_at': '2026-10-01T09:00:00.000000000Z',
              'last_delivery': null,
            },
          ],
          'secrets_configured': true,
        });
      }
    });
    addTearDown(server.stop);
    final c = AlertChannelsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();
    await c.test('ch1');

    // No failure banner -- the call itself worked.
    expect(c.failure, isNull);
    // But the answer is a refusal, and the screen reads it from the body.
    expect(c.results['ch1']!.success, isFalse);
    expect(c.results['ch1']!.statusCode, 404);
    expect(c.results['ch1']!.error, contains('no longer exists'));
  });

  test(
    'an installation with no secrets key says so, not "no channels"',
    () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {
          'channels': <Object>[],
          'secrets_configured': false,
        }),
      );
      addTearDown(server.stop);
      final c = AlertChannelsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      await c.refresh();

      // An empty list means two different things and the screen has to tell
      // them apart: nobody made one, or this installation cannot store one.
      expect(c.items, isEmpty);
      expect(c.secretsConfigured, isFalse);
    },
  );

  test(
    'testing without a secrets key is reported as that, not forbidden',
    () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/test')) {
          writeJson(req, 409, {
            'error': {'message': 'failed_precondition: no secrets key'},
          });
        } else {
          writeJson(req, 200, {
            'channels': <Object>[],
            'secrets_configured': false,
          });
        }
      });
      addTearDown(server.stop);
      final c = AlertChannelsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      await c.test('ch1');

      expect(c.failure?.kind, 'channelsNoSecrets');
    },
  );

  test('integrations read the status the agent reported', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {
        'items': [
          {
            'category': 'discovered_service',
            'key': '/usr/bin/redis-check-rdb',
            'data': {
              'rule_id': 'redis',
              'name': 'redis',
              'instance': '/usr/bin/redis-check-rdb',
              'display_instance': '/usr/bin/redis-server',
              'version': '7.2.4',
              'integration': {
                'id': 'redis',
                'status': 'enabled',
                'endpoint': '127.0.0.1:6379',
                'error': '',
              },
            },
            'host_id': 'h1',
            'host_name': 'web-1',
          },
          {
            'category': 'discovered_service',
            'key': '/usr/sbin/mysqld',
            'data': {
              'rule_id': 'mysql',
              'integration': {
                'id': 'mysql',
                'status': 'needs_configuration',
                'hint': 'mysql:\n  user: openlog',
              },
            },
            'host_id': 'h1',
            'host_name': 'web-1',
          },
          {
            'category': 'discovered_service',
            'key': '/usr/sbin/nginx',
            'data': {
              'rule_id': 'nginx',
              'integration': {
                'id': 'nginx',
                'status': 'error',
                'error': 'connection refused',
              },
            },
            'host_id': 'h2',
            'host_name': 'web-2',
          },
          // A non-JSON body is a string; there is nothing to show for one and
          // it must not take the whole list down with it.
          {
            'category': 'discovered_service',
            'key': '/opt/weird',
            'data': 'not json',
            'host_id': 'h2',
          },
        ],
      }),
    );
    addTearDown(server.stop);
    final c = IntegrationsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    expect(
      server.requests.single.query,
      'category=discovered_service&limit=200',
    );
    // What is wrong first, then what needs a hand: those are the two the
    // person opened this screen for.
    expect(c.items.map((i) => i.name), ['nginx', 'mysql', 'redis']);
    expect(c.counts.enabled, 1);
    expect(c.counts.needsConfiguration, 1);
    expect(c.counts.error, 1);
    // The invoked path, not the matched one: redis-server and redis-check-rdb
    // are the same binary and the key names the wrong one.
    expect(c.items.last.service.displayInstance, '/usr/bin/redis-server');
  });

  test('the fleet summary and the agents come from one fetch', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/policy')) {
        // The screen reads the policy and the rollouts beside the hosts.
        writeJson(req, 200, {
          'mode': 'notify',
          'channel': 'stable',
          'target': 'latest',
          'pinned_version': null,
          'waves': [10, 50, 100],
          'wave_soak_minutes': 30,
          'halt_failure_rate': 0.1,
          'maintenance_windows': <Object>[],
          'php_agent': {
            'mode': 'off',
            'version': '',
            'reload': 'none',
            'exclude_bins': <String>[],
            'changed_at': null,
          },
          'java_agent': {'mode': 'off', 'version': '', 'changed_at': null},
          'is_default': true,
          'updated_at': null,
          'updated_by_email': '',
        });
      } else if (seen.path.endsWith('/rollouts')) {
        writeJson(req, 200, {'rollouts': <Object>[]});
      } else if (seen.path.endsWith('/summary')) {
        writeJson(req, 200, {
          'total_hosts': 12,
          'active_hosts': 11,
          'update_capable': 9,
          'not_update_capable': [
            {'reason': 'container', 'hosts': 2},
          ],
          'outdated': 3,
          'unsupported': 1,
          'in_progress': 1,
          'failed': 1,
          'held': 0,
          'pinned': 0,
          'versions': <Object>[],
          'latest': {
            'stable': {
              'version': '0.1.113',
              'channel': 'stable',
              'released_at': '2026-10-08T09:00:00.000000000Z',
              'notes_url': '',
            },
            'beta': null,
          },
          'target': null,
          'oldest_supported_version': '0.1.100',
          'policy_mode': 'notify',
          'update_available': true,
          'stale_after_seconds': 900,
          'catalog': {
            'status': 'ok',
            'source': 'github',
            'checked_at': '2026-10-08T09:00:00.000000000Z',
            'last_success_at': '2026-10-08T09:00:00.000000000Z',
            'error': '',
            'releases': 40,
            'warnings': <String>[],
          },
          'current_rollout': null,
        });
      } else {
        writeJson(req, 200, {
          'hosts': [
            {
              'host_id': 'h1',
              'host_name': 'web-1',
              'agent': {
                'name': 'openlog-infra-agent',
                'version': '0.1.110',
                'commit': 'abc',
                'os': 'linux',
                'arch': 'arm64',
                'install_method': 'deb',
                'update_capable': true,
              },
              'update': {
                'state': 'failed',
                'from_version': '0.1.110',
                'to_version': '0.1.113',
                'error': 'checksum mismatch',
                'changed_at': '2026-10-08T09:00:00.000000000Z',
              },
              'first_seen_at': '2026-09-08T09:00:00.000000000Z',
              'last_sync_at': '2026-10-08T09:00:00.000000000Z',
              'rollout_id': null,
              'override': null,
              'outdated': true,
              'supported': true,
              'status': 'offer',
              'status_target': '0.1.113',
              // Filled out in full on purpose: the parser is strict and
              // tells you exactly which field is missing, which is the whole
              // point of generating it from the contract.
              'php_agent': {
                'reported': false,
                'mode': 'off',
                'agent_mode': '',
                'source': '',
                'capable': false,
                'reason': '',
                'managed_by': 'none',
                'version': null,
                'runtimes': <Object>[],
                'update': null,
                'override': null,
                'status': 'not_capable',
                'status_target': null,
              },
              'php_access': null,
              'java_agent': {
                'reported': false,
                'mode': 'off',
                'agent_mode': '',
                'source': '',
                'capable': false,
                'reason': '',
                'managed': false,
                'version': null,
                'state': '',
                'detail': '',
                'link_path': '',
                'link_state': '',
                'jvms': <Object>[],
                'update': null,
                'override': null,
                'status': 'not_capable',
                'status_target': null,
              },
            },
          ],
          'next_cursor': null,
        });
      }
    });
    addTearDown(server.stop);
    final c = FleetController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    // One fetch for all four, so the header, the rollout, the policy and
    // the list describe one moment.
    expect(
      {for (final r in server.requests) r.path},
      {
        '/api/v1/fleet/summary',
        '/api/v1/fleet/hosts',
        '/api/v1/fleet/policy',
        '/api/v1/fleet/rollouts',
      },
    );
    expect(c.summary!.outdated, 3);
    expect(c.policy!.mode, FleetMode.notify);
    expect(c.rollouts, isEmpty);
    expect(c.items.single.agent.version, '0.1.110');
    expect(c.items.single.update.error, 'checksum mismatch');
  });

  test('inventory asks for one category and the typed key', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {
        'items': [
          {
            'category': 'package',
            'key': 'openssl',
            'data': {'version': '3.0.13', 'arch': 'arm64'},
            'host_id': 'h1',
            'host_name': 'web-1',
          },
          {
            'category': 'package',
            'key': 'openssl',
            // The body is whatever the category defines; the contract does
            // not type it, and a string body has to survive too.
            'data': 'openssl 1.1.1 (no package manager)',
            'host_id': 'h2',
          },
        ],
      }),
    );
    addTearDown(server.stop);
    final c = InventoryController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    c.category = 'package';
    c.query = ' openssl ';
    await c.refresh();

    expect(
      server.requests.single.query,
      'category=package&limit=100&q=openssl',
    );
    expect(c.items.length, 2);
    expect((c.items.first.data! as Map)['version'], '3.0.13');
    expect(c.items.last.data, isA<String>());
    // The host name is omitted when the host has left the hosts table.
    expect(c.items.last.hostName, isNull);
  });

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
        // The containers list asks for the compose projects too: the
        // project filter's options come from there.
        '/api/v1/containers/groups' => {'projects': <Object>[]},
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
