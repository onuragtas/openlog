// Where a service runs, and the one setting on that panel.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/services.dart';

import 'fake_server.dart';

Map<String, Object?> detail({bool knownHost = true}) => {
  'service_name': 'checkout',
  'apdex_t_ms': 500.0,
  'apdex_t_default': true,
  'instances': [
    {
      'service_namespace': '',
      'environment': 'prod',
      'first_seen': '2026-09-01T00:00:00.000000000Z',
      'last_seen': '2026-10-10T00:00:00.000000000Z',
      'version': '1.4.2',
      'language': 'go',
      'sdk_name': 'openlog-go',
      'resource_attributes': <String, String>{},
    },
  ],
  'hosts': [
    {
      'host_id': 'h1',
      'host_name': 'web-1',
      'first_seen': '2026-09-01T00:00:00.000000000Z',
      'last_seen': '2026-10-10T00:00:00.000000000Z',
      'known': knownHost,
    },
  ],
};

Map<String, Object?> settings({bool isDefault = true, double ms = 500}) => {
  'service_name': 'checkout',
  'service_namespace': '',
  'environment': '',
  'apdex_t_ms': ms,
  'is_default': isDefault,
  'updated_at': null,
  'updated_by_email': '',
};

void main() {
  test('the panel asks for everything the web shows above the tabs', () async {
    final paths = <String>[];
    final server = await FakeServer.start((req, seen) {
      paths.add(seen.path);
      if (seen.path.endsWith('/settings')) {
        writeJson(req, 200, settings());
      } else if (seen.path.endsWith('/containers')) {
        writeJson(req, 200, {'containers': <Object>[]});
      } else if (seen.path.endsWith('/kubernetes')) {
        writeJson(req, 200, {'pods': <Object>[]});
      } else {
        writeJson(req, 200, detail());
      }
    });
    addTearDown(server.stop);
    final c = ServiceAboutController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'checkout',
    );
    addTearDown(c.dispose);

    await c.load();

    expect(paths, [
      '/api/v1/apm/services/checkout',
      '/api/v1/apm/services/checkout/settings',
      '/api/v1/apm/services/checkout/containers',
      '/api/v1/apm/services/checkout/kubernetes',
    ]);
    expect(c.detail?.instances.single.language, 'go');
    expect(c.settings?.isDefault, isTrue);
    expect(c.failure, isNull);
  });

  test(
    'setting the Apdex sends milliseconds and keeps what came back',
    () async {
      Map<String, Object?>? body;
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'PUT') {
          body = jsonDecode(seen.body) as Map<String, Object?>;
          writeJson(req, 200, settings(isDefault: false, ms: 300));
          return;
        }
        if (seen.path.endsWith('/settings')) {
          writeJson(req, 200, settings());
        } else if (seen.path.endsWith('/containers')) {
          writeJson(req, 200, {'containers': <Object>[]});
        } else if (seen.path.endsWith('/kubernetes')) {
          writeJson(req, 200, {'pods': <Object>[]});
        } else {
          writeJson(req, 200, detail());
        }
      });
      addTearDown(server.stop);
      final c = ServiceAboutController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'checkout',
      );
      addTearDown(c.dispose);

      await c.load();
      expect(await c.setApdex(300), isTrue);

      expect(body, {'apdex_t_ms': 300});
      // No longer the organization's default: the panel has to stop saying so.
      expect(c.settings?.isDefault, isFalse);
      expect(c.settings?.apdexTMs, 300);
    },
  );

  test('static auth mode has nowhere to store settings, and says so', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'PUT') {
        writeJson(req, 404, {
          'error': {'message': 'not found'},
        });
        return;
      }
      if (seen.path.endsWith('/settings')) {
        writeJson(req, 200, settings());
      } else if (seen.path.endsWith('/containers')) {
        writeJson(req, 200, {'containers': <Object>[]});
      } else if (seen.path.endsWith('/kubernetes')) {
        writeJson(req, 200, {'pods': <Object>[]});
      } else {
        writeJson(req, 200, detail());
      }
    });
    addTearDown(server.stop);
    final c = ServiceAboutController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'checkout',
    );
    addTearDown(c.dispose);

    await c.load();
    expect(await c.setApdex(300), isFalse);
    expect(c.failure?.kind, 'apdexUnavailable');
  });
}
