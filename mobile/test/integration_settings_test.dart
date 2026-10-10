// Telling an integration how to reach its service: what a save sends,
// and what "saved" means afterwards.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/integration_settings.dart';

import 'fake_server.dart';

Map<String, Object?> setting({
  String id = 's1',
  String integration = 'redis',
  String instance = '/usr/bin/redis-server',
  bool passwordSet = true,
  bool enabled = true,
}) => {
  'id': id,
  'host_id': 'h1',
  'integration': integration,
  'match': {
    'port': null,
    'container': '',
    'endpoint': '',
    'instance': instance,
  },
  'enabled': enabled,
  'endpoint': '127.0.0.1:6379',
  'username': 'stats',
  'password_set': passwordSet,
  'database': '',
  'databases': <String>[],
  'created_at': '2026-10-01T09:00:00.000000000Z',
  'updated_at': '2026-10-01T09:00:00.000000000Z',
  'updated_by_email': 'owner@example.com',
};

Map<String, Object?> host({
  String revision = 'sha256:a',
  String applied = 'sha256:a',
  bool remoteOff = false,
}) => {
  'host_id': 'h1',
  'revision': revision,
  'applied_revision': applied,
  'applied_at': '2026-10-10T09:00:00.000000000Z',
  'remote_config_disabled': remoteOff,
};

void main() {
  group('which fields an integration has', () {
    test('docker has none, redis has three, postgres has four', () {
      expect(isConfigurable('docker'), isFalse);
      expect(isConfigurable('iis'), isFalse);
      expect(configFields['redis'], ['endpoint', 'username', 'password']);
      expect(configFields['postgresql']!.length, 4);
    });
  });

  group('how far a saved change has got', () {
    test('nothing saved and nothing pending is idle', () {
      expect(
        applyPhase(IntegrationSettingsHost.fromJson(host()), false),
        ApplyPhase.idle,
      );
    });

    test('a revision the agent has not reported is on its way', () {
      expect(
        applyPhase(
          IntegrationSettingsHost.fromJson(
            host(revision: 'sha256:b', applied: 'sha256:a'),
          ),
          true,
        ),
        ApplyPhase.sent,
      );
    });

    test('an agent with remote config off will never apply it', () {
      expect(
        applyPhase(
          IntegrationSettingsHost.fromJson(host(remoteOff: true)),
          true,
        ),
        ApplyPhase.disabled,
      );
    });
  });

  test('a new instance is created, an existing one replaced', () async {
    final sent = <(String, Map<String, Object?>)>[];
    var exists = false;
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'GET') {
        writeJson(req, 200, {
          'items': exists ? [setting()] : <Object>[],
          'host': host(),
        });
        return;
      }
      sent.add((seen.method, jsonDecode(seen.body) as Map<String, Object?>));
      exists = true;
      writeJson(req, seen.method == 'POST' ? 201 : 200, setting());
    });
    addTearDown(server.stop);
    final c = IntegrationSettingsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      hostId: 'h1',
    );

    await c.load();
    await c.save(
      integration: 'redis',
      instance: '/usr/bin/redis-server',
      enabled: true,
      endpoint: ' 127.0.0.1:6379 ',
      username: 'stats',
      password: 'hunter2',
    );

    expect(sent.single.$1, 'POST');
    expect(sent.single.$2['host_id'], 'h1');
    expect(sent.single.$2['match'], {'instance': '/usr/bin/redis-server'});
    // Trimmed, as the web trims it.
    expect(sent.single.$2['endpoint'], '127.0.0.1:6379');
    expect(sent.single.$2['password'], 'hunter2');
    // Redis has no database, so no database field is sent at all.
    expect(sent.single.$2.containsKey('database'), isFalse);
    // The reload saw the stored setting, so the next save replaces it.
    expect(c.settingFor('redis', '/usr/bin/redis-server'), isNotNull);

    await c.save(
      integration: 'redis',
      instance: '/usr/bin/redis-server',
      enabled: false,
      endpoint: '127.0.0.1:6379',
      username: 'stats',
    );
    expect(sent.last.$1, 'PUT');
    expect(sent.last.$2['enabled'], isFalse);
    // An empty password box keeps what is stored; it does not clear it.
    expect(sent.last.$2['password'], isNull);
  });

  test('clearing a password is a choice, not an empty box', () async {
    final bodies = <Map<String, Object?>>[];
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'GET') {
        writeJson(req, 200, {
          'items': [setting()],
          'host': host(),
        });
        return;
      }
      bodies.add(jsonDecode(seen.body) as Map<String, Object?>);
      writeJson(req, 200, setting(passwordSet: false));
    });
    addTearDown(server.stop);
    final c = IntegrationSettingsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      hostId: 'h1',
    );

    await c.load();
    await c.save(
      integration: 'redis',
      instance: '/usr/bin/redis-server',
      enabled: true,
      endpoint: '127.0.0.1:6379',
      clearPassword: true,
    );

    expect(bodies.single['password'], '');
  });

  test('a member who tries anyway is told it needs an admin', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'GET') {
        writeJson(req, 200, {'items': <Object>[], 'host': host()});
        return;
      }
      writeJson(req, 403, {
        'error': {'code': 'permission_denied', 'message': 'admin required'},
      });
    });
    addTearDown(server.stop);
    final c = IntegrationSettingsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      hostId: 'h1',
    );

    await c.load();
    final ok = await c.save(
      integration: 'nginx',
      instance: '/usr/sbin/nginx',
      enabled: true,
      endpoint: 'http://127.0.0.1:8080/nginx_status',
    );

    expect(ok, isFalse);
    expect(c.failure?.kind, 'fleetForbidden');
  });
}
