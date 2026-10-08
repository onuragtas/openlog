// The alerts controller against a real server: what it asks for, what it does
// with the answer, and what it does when acknowledging races a resolve.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/alerts.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';

import 'fake_server.dart';

Map<String, Object?> incident(
  String id, {
  String state = 'open',
  String severity = 'critical',
  String? ackBy,
  bool muted = false,
  bool flapping = false,
}) => {
  'id': id,
  'rule_id': 'r1',
  'rule_name': 'API error rate',
  'rule_type': 'apm_error_rate',
  'severity': severity,
  'state': state,
  'series_key': 'service.name=checkout',
  'labels': {'service.name': 'checkout'},
  'summary': 'error rate 12% over 5m',
  'value': 0.12,
  'last_value': 0.12,
  'threshold': 0.05,
  'flapping': flapping,
  'muted': muted,
  'opened_at': '2026-10-07T20:00:00.000000000Z',
  'acknowledged_at': ackBy == null ? null : '2026-10-07T20:05:00.000000000Z',
  'acknowledged_by_email': ackBy,
  'resolved_at': null,
  'resolved_by_email': null,
  'resolve_reason': null,
  'channel_ids': <String>[],
};

Map<String, Object?> page(
  List<Map<String, Object?>> incidents, {
  int open = 1,
  int acknowledged = 0,
  int resolved = 0,
}) => {
  'incidents': incidents,
  'next_cursor': null,
  'counts': {'open': open, 'acknowledged': acknowledged, 'resolved': resolved},
};

void main() {
  test('the list asks for what is firing, not for history', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, page([incident('i1')])),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);
    final c = AlertsController(client);

    await c.refresh();

    final seen = Uri.parse('http://x${server.requests.single.path}');
    expect(server.requests.single.path, '/api/v1/alerts/incidents');
    expect(seen.path, '/api/v1/alerts/incidents');
    // The query is asserted as the server saw it: `limit` was once sent as the
    // literal text "$limit" because a generated string escaped the dollar, and
    // nothing but a check against the wire would have noticed.
    expect(server.requests.single.query, contains('limit=50'));
    expect(server.requests.single.query, contains('state=open%2Cacknowledged'));
  });

  test(
    'a loaded list keeps its incidents and the counts beside them',
    () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(
          req,
          200,
          page(
            [
              incident('i1'),
              incident('i2', state: 'acknowledged', ackBy: 'a@b.c'),
            ],
            open: 1,
            acknowledged: 1,
            resolved: 11,
          ),
        ),
      );
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);
      final c = AlertsController(client);

      await c.refresh();

      expect(c.incidents.map((i) => i.id), ['i1', 'i2']);
      expect(c.incidents.first.severity, AlertSeverity.critical);
      expect(c.incidents.last.state, AlertIncidentState.acknowledged);
      expect(c.incidents.last.acknowledgedByEmail, 'a@b.c');
      expect(
        c.counts!.resolved,
        11,
        reason: 'an empty screen still says what recovered today',
      );
      expect(c.failure, isNull);
      expect(c.loaded, isTrue);
    },
  );

  test('acknowledging reloads, so the row shows what the server did', () async {
    var acked = false;
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/acknowledge')) {
        acked = true;
        req.response.statusCode = 204;
        return;
      }
      writeJson(
        req,
        200,
        page([
          incident(
            'i1',
            state: acked ? 'acknowledged' : 'open',
            ackBy: acked ? 'me@example.com' : null,
          ),
        ]),
      );
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);
    final c = AlertsController(client);

    await c.refresh();
    expect(c.incidents.single.state, AlertIncidentState.open);

    await c.acknowledge('i1');

    expect(acked, isTrue);
    expect(c.incidents.single.state, AlertIncidentState.acknowledged);
    expect(c.incidents.single.acknowledgedByEmail, 'me@example.com');
    expect(c.acknowledging, isNull);
  });

  test(
    'acknowledging something that just resolved says so instead of hiding it',
    () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/acknowledge')) {
          writeJson(req, 409, {
            'error': {
              'code': 'already_exists',
              'message': 'incident is resolved',
            },
          });
          return;
        }
        writeJson(req, 200, page([], open: 0));
      });
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);
      final c = AlertsController(client);

      await c.refresh();
      await c.acknowledge('i1');

      // The person pressed a button about something that is no longer true.
      expect(c.failure!.kind, 'alreadyResolved');
      // And the list is reloaded, so the screen stops showing the stale row.
      expect(c.incidents, isEmpty);
    },
  );

  test(
    'a role that may not read alerts is told that, not shown an empty list',
    () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 403, {
          'error': {
            'code': 'permission_denied',
            'message': 'your role does not allow this operation',
          },
        }),
      );
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);
      final c = AlertsController(client);

      await c.refresh();

      // "No alerts" and "you may not see the alerts" look identical on screen and
      // mean opposite things.
      expect(c.failure!.kind, 'alertsForbidden');
      expect(c.incidents, isEmpty);
    },
  );

  test('a reload that fails keeps the list that is already on screen', () async {
    var fail = false;
    final server = await FakeServer.start((req, _) {
      if (fail) {
        writeJson(req, 500, {
          'error': {'code': 'internal', 'message': 'boom'},
        });
        return;
      }
      writeJson(req, 200, page([incident('i1')]));
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);
    final c = AlertsController(client);

    await c.refresh();
    fail = true;
    await c.refresh();

    expect(c.failure, isNotNull);
    // Blanking the screen on a failed pull-to-refresh takes away the thing the
    // person was reading.
    expect(c.incidents.single.id, 'i1');
  });

  test('only the first load shows a spinner over the whole screen', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, page([incident('i1')])),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);
    final c = AlertsController(client);

    final states = <bool>[];
    c.addListener(() => states.add(c.loadingFirst));
    await c.refresh();
    expect(states, contains(true));

    states.clear();
    await c.refresh();
    expect(
      states,
      isNot(contains(true)),
      reason: 'a refresh must not blank a list that is already there',
    );
  });
}
