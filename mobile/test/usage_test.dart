// What the usage tab shows, and how it says the awkward numbers.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/usage.dart';

import 'fake_server.dart';

Map<String, Object?> metric(String name, double used, double limit) => {
  'metric': name,
  'used': used,
  'limit': limit,
  'percent': limit == 0 ? 0.0 : used / limit * 100,
  'level': 'ok',
};

Map<String, Object?> overview() => {
  'organization': {'id': 'o1', 'name': 'Resoft', 'tenant_id': 'resoft'},
  'period': {
    'id': '2026-10',
    'start': '2026-10-01T00:00:00.000000000Z',
    'end': '2026-11-01T00:00:00.000000000Z',
    'data_until': '2026-10-10T00:00:00.000000000Z',
  },
  'saas_mode': false,
  'plan': {
    'id': 'free',
    'name': 'Free',
    'description': '',
    'limits': {
      'retention_days': {'logs': 30, 'traces': 7, 'metrics': 90},
      'query': <String, Object?>{},
    },
    'enforcement': <String, Object?>{},
    'trial_days': 0,
    'trial_fallback_plan': '',
  },
  'plan_assigned': false,
  'usage': {
    'signals': [
      {
        'signal': 'logs',
        'items': 1200000.0,
        'bytes': 4.0e9,
        'ingest_bytes': 4.0e9,
        'ingest_requests': 900.0,
      },
    ],
    'ingest_bytes': 4.0e9,
    'hosts': 12.0,
    'containers': 40.0,
    'services': 7.0,
    'active_hosts': 11.0,
    'query': {
      'queries': 320.0,
      'failed': 3.0,
      'read_rows': 9.0e6,
      'read_bytes': 1.0e9,
      'cpu_seconds': 42.5,
      'memory_bytes': 2.0e8,
    },
  },
  'stored': [
    {
      'signal': 'logs',
      'retention_days': 30,
      'bytes': 9.0e9,
      'compressed_bytes': 1.5e9,
    },
  ],
  'limits': [metric('ingest_bytes', 4.0e9, 1.0e10), metric('hosts', 12.0, 0)],
  'level': 'ok',
  'ingest_blocked': false,
  'projection': {'ingest_bytes': 1.2e10, 'ingest_percent': 120.0},
  'can_manage_plan': false,
  'billing_enabled': false,
};

void main() {
  test('the period goes to the server by its own name', () async {
    final queries = <String>[];
    final server = await FakeServer.start((req, seen) {
      queries.add(seen.query);
      writeJson(req, 200, overview());
    });
    addTearDown(server.stop);
    final c = UsageController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.load();
    c.period = 'previous';
    await c.load();

    expect(queries.first, contains('period=current'));
    expect(queries.last, contains('period=previous'));
    expect(c.overview?.plan.name, 'Free');
  });

  test('a limit of zero is unlimited, not a limit of nothing', () {
    final unlimited = QuotaMetric.fromJson(metric('hosts', 12, 0));
    final bounded = QuotaMetric.fromJson(metric('ingest_bytes', 4.0e9, 1.0e10));

    // The contract's own convention: 0 means no limit, and reading it as
    // "nothing allowed" would put every organization over quota.
    expect(quotaValue(unlimited), '12');
    expect(quotaValue(bounded), contains('/'));
  });

  test('bytes are binary multiples, as the plan counts them', () {
    expect(formatBytes(0), '0 B');
    expect(formatBytes(1024), '1.0 KB');
    expect(formatBytes(4.0e9), '3.7 GB');
    expect(formatBytes(1.5e12), '1.4 TB');
  });

  test('counts group their thousands', () {
    expect(formatCount(7), '7');
    expect(formatCount(1200000), '1 200 000');
  });
}
