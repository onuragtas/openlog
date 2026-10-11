// The four requests under the usage overview.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/usage.dart';

import 'fake_server.dart';

Map<String, Object?> day(String d, double bytes) => {
  'day': d,
  'signals': <Object>[],
  'ingest_bytes': bytes,
  'hosts': 3,
  'containers': 9,
  'services': 4,
  'query': {
    'queries': 10,
    'failed': 0,
    'read_rows': 100,
    'read_bytes': 1000,
    'cpu_seconds': 1.5,
    'memory_bytes': 2000,
  },
};

void main() {
  test('asks for the day, the top, the quota and the limits at once', () async {
    final seen = <String>[];
    final server = await FakeServer.start((req, s) {
      seen.add(s.path);
      if (s.path.endsWith('/daily')) {
        writeJson(req, 200, {
          'period': {
            'id': 'current',
            'start': '2026-10-01',
            'end': '2026-10-31',
            'data_until': '2026-10-31',
          },
          'days': [day('2026-10-01', 1000), day('2026-10-02', 3000)],
        });
      } else if (s.path.endsWith('/top')) {
        writeJson(req, 200, {
          'period': {
            'id': 'current',
            'start': '2026-10-01',
            'end': '2026-10-31',
            'data_until': '2026-10-31',
          },
          'by': 'service',
          'entries': [
            {
              'key': 'checkout',
              'items': 10,
              'bytes': 4000,
              'bytes_by_signal': {'logs': 4000},
            },
          ],
        });
      } else if (s.path.endsWith('/status')) {
        writeJson(req, 200, {
          'level': 'warning',
          'ingest_blocked': false,
          'saas_mode': false,
          'metrics': [
            {
              'metric': 'ingest_bytes',
              'used': 8000,
              'limit': 10000,
              'percent': 80,
              'level': 'warning',
            },
          ],
        });
      } else {
        writeJson(req, 200, {
          'defaults': {
            'max_memory_usage': 1,
            'max_rows_to_read': 1,
            'max_bytes_to_read': 1,
          },
          'plan': {
            'max_memory_usage': 2,
            'max_rows_to_read': 2,
            'max_bytes_to_read': 2,
          },
          'effective': {
            'max_memory_usage': 10737418240,
            'max_rows_to_read': 2000000000,
            'max_bytes_to_read': 107374182400,
          },
          'sources': {
            'max_memory_usage': 'plan',
            'max_rows_to_read': 'default',
            'max_bytes_to_read': 'organization',
          },
          'can_manage': false,
          'refresh_seconds': 60,
        });
      }
    });
    addTearDown(server.stop);
    final c = UsageDetailController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.load();

    expect(seen.length, 4);
    expect(c.daily!.days.length, 2);
    expect(c.top!.entries.single.key, 'checkout');
    expect(c.status!.level.wire, 'warning');
    expect(c.limits!.sources.maxMemoryUsage.wire, 'plan');
    expect(c.failure, isNull);
  });

  test('an installation without query limits still shows the rest', () async {
    final server = await FakeServer.start((req, s) {
      if (s.path.endsWith('/query-limits')) {
        // Only the PostgreSQL auth mode has them; a 404 here is "this
        // installation has none", not a broken screen.
        writeJson(req, 404, {
          'error': {'code': 'not_found', 'message': 'no query limits'},
        });
        return;
      }
      if (s.path.endsWith('/daily')) {
        writeJson(req, 200, {
          'period': {
            'id': 'current',
            'start': '2026-10-01',
            'end': '2026-10-31',
            'data_until': '2026-10-31',
          },
          'days': <Object>[],
        });
      } else if (s.path.endsWith('/top')) {
        writeJson(req, 200, {
          'period': {
            'id': 'current',
            'start': '2026-10-01',
            'end': '2026-10-31',
            'data_until': '2026-10-31',
          },
          'by': 'service',
          'entries': <Object>[],
        });
      } else {
        writeJson(req, 200, {
          'level': 'ok',
          'ingest_blocked': false,
          'saas_mode': false,
          'metrics': <Object>[],
        });
      }
    });
    addTearDown(server.stop);
    final c = UsageDetailController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.load();

    expect(c.limits, isNull);
    expect(c.loaded, isTrue);
    expect(c.failure, isNull);
  });

  test('the period and the breakdown travel with the request', () async {
    final queries = <String>[];
    final server = await FakeServer.start((req, s) {
      queries.add('${s.path}?${s.query}');
      if (s.path.endsWith('/daily')) {
        writeJson(req, 200, {
          'period': {
            'id': 'previous',
            'start': '2026-09-01',
            'end': '2026-09-30',
            'data_until': '2026-09-30',
          },
          'days': <Object>[],
        });
      } else if (s.path.endsWith('/top')) {
        writeJson(req, 200, {
          'period': {
            'id': 'previous',
            'start': '2026-09-01',
            'end': '2026-09-30',
            'data_until': '2026-09-30',
          },
          'by': 'host',
          'entries': <Object>[],
        });
      } else if (s.path.endsWith('/status')) {
        writeJson(req, 200, {
          'level': 'ok',
          'ingest_blocked': false,
          'saas_mode': false,
          'metrics': <Object>[],
        });
      } else {
        writeJson(req, 403, {
          'error': {'code': 'permission_denied', 'message': 'no'},
        });
      }
    });
    addTearDown(server.stop);
    final c =
        UsageDetailController(
            OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
          )
          ..period = 'previous'
          ..by = 'host';

    await c.load();

    expect(queries.any((q) => q.contains('/daily?period=previous')), isTrue);
    expect(queries.any((q) => q.contains('by=host')), isTrue);
    // A 403 on the limits is the same kind of answer as a 404: the rest of
    // the screen is still worth showing.
    expect(c.limits, isNull);
    expect(c.failure, isNull);
  });
}
