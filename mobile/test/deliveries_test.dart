// The delivery log: who filters it, and what the filter asks for.
//
// The filtering is the whole point of the test. The server applies the limit
// before the filter, so filtering here instead would show the failures among
// the last 200 notifications rather than the last 200 failures -- a log that
// looks empty when everything is broken.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/sections.dart';

import 'fake_server.dart';

Map<String, Object?> delivery(String id, {String status = 'delivered'}) => {
  'id': id,
  'incident_id': 'i1',
  'rule_id': 'r1',
  'rule_name': 'checkout hata orani',
  'channel_id': 'ch1',
  'channel_name': 'nobetci',
  'channel_type': 'slack',
  'kind': 'opened',
  'status': status,
  'attempts': 2,
  'idempotency_key': 'k',
  'created_at': '2026-10-09T09:00:00.000000000Z',
  'finished_at': null,
  'next_attempt_at': null,
  'last_error': status == 'failed' ? 'channel_error: 502' : '',
  'attempt_log': [
    {
      'attempt': 1,
      'at': '2026-10-09T09:00:00.000000000Z',
      'duration_ms': 120,
      'success': false,
      'status_code': 502,
      'error': 'bad gateway',
    },
    {
      'attempt': 2,
      'at': '2026-10-09T09:00:30.000000000Z',
      'duration_ms': 90,
      'success': status != 'failed',
      'status_code': status == 'failed' ? 502 : 200,
      'error': '',
    },
  ],
};

void main() {
  test(
    'the channel and the status go to the server, not to a local filter',
    () async {
      final queries = <String>[];
      final server = await FakeServer.start((req, seen) {
        queries.add(seen.query);
        writeJson(req, 200, {
          'deliveries': [delivery('d1', status: 'failed')],
        });
      });
      addTearDown(server.stop);
      final c = AlertDeliveriesController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        channelId: 'ch1',
      );

      await c.refresh();
      c.status = 'failed';
      await c.refresh();

      expect(queries.first, contains('channel_id=ch1'));
      expect(queries.first, contains('limit=200'));
      // No status on the first request: an empty filter must not go out as
      // `status=`, which some endpoints read as "the empty status".
      expect(queries.first, isNot(contains('status=')));
      expect(queries.last, contains('status=failed'));
    },
  );

  test('the log keeps the server order and every attempt', () async {
    final server = await FakeServer.start((req, seen) {
      writeJson(req, 200, {
        'deliveries': [delivery('d2'), delivery('d1', status: 'failed')],
      });
    });
    addTearDown(server.stop);
    final c = AlertDeliveriesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    // Newest first is the server's order and nothing re-sorts it: a log that
    // reordered itself would not be a log.
    expect(c.items.map((d) => d.id), ['d2', 'd1']);
    expect(c.items.first.attemptLog.length, 2);
    expect(c.items.first.attemptLog.first.statusCode, 502);
  });

  test('no channel means no channel_id, not an empty one', () async {
    final queries = <String>[];
    final server = await FakeServer.start((req, seen) {
      queries.add(seen.query);
      writeJson(req, 200, {'deliveries': <Object>[]});
    });
    addTearDown(server.stop);
    final c = AlertDeliveriesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();
    expect(queries.single, isNot(contains('channel_id')));
  });
}
