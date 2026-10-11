// The advisory catalog under the vulnerability list: an empty list means
// three different things and only this says which.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/sections.dart';

import 'fake_server.dart';

Map<String, Object?> catalog({String? syncedAt, String error = ''}) => {
  'vulnerabilities': 284531,
  'affected_ranges': 912004,
  'ecosystems': 11,
  'last_synced_at': syncedAt,
  'sources': [
    {
      'source': 'osv',
      'ecosystem': 'Debian',
      'synced_at': '2026-10-12T06:00:00.000000000Z',
      'count': 120,
      'error': error,
    },
  ],
};

void main() {
  test('the catalog is read with the list', () async {
    final server = await FakeServer.start((req, s) {
      if (s.path.endsWith('/catalog/status')) {
        writeJson(
          req,
          200,
          catalog(syncedAt: '2026-10-12T06:00:00.000000000Z'),
        );
        return;
      }
      writeJson(req, 200, {
        'vulnerabilities': <Object>[],
        'severity_counts': {
          'critical': 0,
          'high': 0,
          'medium': 0,
          'low': 0,
          'unknown': 0,
        },
      });
    });
    addTearDown(server.stop);
    final c = VulnerabilitiesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    expect(c.items, isEmpty);
    expect(c.catalog!.vulnerabilities, 284531);
    expect(c.catalogUnavailable, isFalse);
  });

  test('a feed that never synced is not a clean installation', () async {
    final server = await FakeServer.start((req, s) {
      if (s.path.endsWith('/catalog/status')) {
        writeJson(req, 200, catalog());
        return;
      }
      writeJson(req, 200, {
        'vulnerabilities': <Object>[],
        'severity_counts': {
          'critical': 0,
          'high': 0,
          'medium': 0,
          'low': 0,
          'unknown': 0,
        },
      });
    });
    addTearDown(server.stop);
    final c = VulnerabilitiesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    // Nothing was compared against anything; the screen has to say so.
    expect(c.catalog!.lastSyncedAt, isNull);
  });

  test('no catalog at all is a third thing, and not a failure', () async {
    final server = await FakeServer.start((req, s) {
      if (s.path.endsWith('/catalog/status')) {
        writeJson(req, 404, {
          'error': {'code': 'not_found', 'message': 'no catalog'},
        });
        return;
      }
      writeJson(req, 200, {
        'vulnerabilities': <Object>[],
        'severity_counts': {
          'critical': 0,
          'high': 0,
          'medium': 0,
          'low': 0,
          'unknown': 0,
        },
      });
    });
    addTearDown(server.stop);
    final c = VulnerabilitiesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    expect(c.catalogUnavailable, isTrue);
    expect(c.catalog, isNull);
    // And the list itself is fine: the catalog is a note under it.
    expect(c.failure, isNull);
    expect(c.loaded, isTrue);
  });
}
