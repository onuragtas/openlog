// The public status page, and the dashboard sharing setting that rides with
// the dashboards list.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/dashboards.dart';
import 'package:openlog_mobile/src/status.dart';

import 'fake_server.dart';

Map<String, Object?> page({String status = 'operational'}) => {
  'status': status,
  'checked_at': '2026-10-12T09:00:00Z',
  'components': [
    {
      'id': 'ingest',
      'status': status,
      'uptime_90d': 99.98,
      'days': [
        {'date': '2026-10-11', 'status': 'operational', 'uptime': 100},
        {'date': '2026-10-12', 'status': 'degraded', 'uptime': 97.5},
      ],
    },
  ],
  'incidents': <Object>[],
  'maintenance': <Object>[],
  'history': <Object>[],
};

void main() {
  test('the status page needs no token', () async {
    String? auth;
    final server = await FakeServer.start((req, s) {
      auth = s.headers['authorization'];
      writeJson(req, 200, page());
    });
    addTearDown(server.stop);
    // No token on the client at all: the endpoint is public, and the one
    // screen worth having when nothing else answers cannot need a session.
    final c = StatusPageController(OpenlogClient(baseUrl: server.baseUrl));

    await c.refresh();

    expect(auth, isNull);
    expect(c.value!.components.single.uptime90d, 99.98);
    expect(c.unavailable, isFalse);
  });

  test('a turned-off status page is a setting, not an outage', () async {
    final server = await FakeServer.start(
      (req, s) => writeJson(req, 404, {
        'error': {'code': 'not_found', 'message': 'status page disabled'},
      }),
    );
    addTearDown(server.stop);
    final c = StatusPageController(OpenlogClient(baseUrl: server.baseUrl));

    await c.refresh();

    expect(c.unavailable, isTrue);
  });

  test('the dashboards list carries the sharing setting', () async {
    final server = await FakeServer.start((req, s) {
      if (s.path.endsWith('/settings')) {
        writeJson(req, 200, {
          'share_links_enabled': true,
          'report_domains': ['reports.example.com'],
          'updated_at': '2026-10-01T09:00:00Z',
          'can_edit': true,
        });
        return;
      }
      writeJson(req, 200, {'dashboards': <Object>[]});
    });
    addTearDown(server.stop);
    final c = DashboardsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    expect(c.settings!.shareLinksEnabled, isTrue);
    expect(c.settings!.reportDomains, ['reports.example.com']);
  });

  test('a viewer who may not read the setting still gets the list', () async {
    final server = await FakeServer.start((req, s) {
      if (s.path.endsWith('/settings')) {
        writeJson(req, 403, {
          'error': {'code': 'permission_denied', 'message': 'no'},
        });
        return;
      }
      writeJson(req, 200, {'dashboards': <Object>[]});
    });
    addTearDown(server.stop);
    final c = DashboardsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    expect(c.settings, isNull);
    expect(c.failure, isNull);
    expect(c.loaded, isTrue);
  });
}
