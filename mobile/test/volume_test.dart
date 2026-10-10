// The chart above the explorers: what it asks, and how it adds up.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/fields.dart';
import 'package:openlog_mobile/src/volume.dart';

import 'fake_server.dart';

void main() {
  test('every series is added up, not just the first', () async {
    final server = await FakeServer.start((req, seen) {
      writeJson(req, 200, {
        'step': '60s',
        'total': 30,
        'series': [
          {
            'group': 'checkout',
            'other': false,
            'total': 20,
            'points': [
              [1.0, 10.0],
              [2.0, 10.0],
            ],
          },
          {
            'group': 'cart',
            'other': false,
            'total': 10,
            'points': [
              [1.0, 4.0],
              [2.0, 6.0],
            ],
          },
        ],
      });
    });
    addTearDown(server.stop);
    final c = VolumeController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      signal: 'logs',
    );
    addTearDown(c.dispose);

    await c.load();

    // A chart of only the first series would be a chart of part of the
    // answer.
    expect(c.counts, [14.0, 16.0]);
    expect(c.total, 30);
    expect(c.step, '60s');
    expect(c.p95, isEmpty);
  });

  test('the traces chart asks with the same conditions as the list', () async {
    Map<String, Object?>? body;
    final server = await FakeServer.start((req, seen) {
      body = jsonDecode(seen.body) as Map<String, Object?>;
      writeJson(req, 200, {
        'step': '60s',
        'total': 4,
        'series': [
          {
            'group': '',
            'other': false,
            'total': 4,
            'points': [
              [1.0, 4.0],
            ],
          },
        ],
        'latency': {
          'p50': [
            [1.0, 12.0],
          ],
          'p95': [
            [1.0, 240.0],
          ],
          'p99': [
            [1.0, 900.0],
          ],
        },
      });
    });
    addTearDown(server.stop);
    final c = VolumeController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      signal: 'traces',
    );
    addTearDown(c.dispose);

    await c.load(
      q: 'checkout',
      filters: const [
        Filter(key: 'http.status_code', op: 'eq', values: ['500']),
      ],
    );

    // Entry spans only, like the list, and the search box as the same
    // contains over the service name.
    expect(body?['root_only'], isTrue);
    final filters = (body?['filters']! as List).cast<Map>();
    expect(filters.first['op'], 'contains');
    expect(filters.last['key'], 'http.status_code');
    // A bucket count alone does not say whether the requests were slow.
    expect(c.p95, [240.0]);
  });
}
