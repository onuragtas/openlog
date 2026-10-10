// The database screens: what each tab asks for, and who is blocking whom.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/databases.dart';

import 'fake_server.dart';

DbSession session(
  String id, {
  List<String> blockedBy = const [],
  int blocks = 0,
  double durationMs = 1000,
  String text = 'UPDATE orders SET ...',
}) => DbSession(
  sessionId: id,
  state: 'active',
  waitType: blockedBy.isEmpty ? 'CPU' : 'Lock',
  waitEvent: blockedBy.isEmpty ? '' : 'transactionid',
  dbName: 'shop',
  user: 'app',
  application: 'checkout',
  clientAddress: '10.0.0.5',
  durationMs: durationMs,
  fingerprint: 'fp-$id',
  text: text,
  blockingSessionIds: blockedBy,
  blocks: blocks,
);

Map<String, Object?> activity() => {
  'from': '2026-10-10T08:00:00.000000000Z',
  'to': '2026-10-10T09:00:00.000000000Z',
  'step': '60s',
  'series': [
    {
      'wait_type': 'CPU',
      'points': [
        [1760000000000, 1.5],
        [1760000060000, 2.5],
      ],
    },
  ],
  'waits': [
    {'type': 'Lock', 'event': 'transactionid', 'samples': 12, 'share': 0.4},
  ],
  'top_queries': [
    {
      'fingerprint': '42',
      'text': 'UPDATE orders SET status = ?',
      'samples': 9,
      'avg_active_sessions': 1.25,
      'top_wait': 'Lock',
    },
  ],
};

void main() {
  group('the blocking forest', () {
    test('nothing is blocked, so there is no tree', () {
      final forest = blockingForest([session('1'), session('2')]);
      expect(forest.roots, isEmpty);
      expect(forest.others.length, 2);
    });

    test('the holder is the head and its waiters hang under it', () {
      final forest = blockingForest([
        session('holder', blocks: 2),
        session('waiter-1', blockedBy: ['holder']),
        session('waiter-2', blockedBy: ['waiter-1']),
        session('idle'),
      ]);

      expect(forest.roots.single.session.sessionId, 'holder');
      expect(forest.roots.single.children.single.session.sessionId, 'waiter-1');
      expect(
        forest.roots.single.children.single.children.single.session.sessionId,
        'waiter-2',
      );
      // Nobody in a chain is in the flat list, and the idle one is.
      expect([for (final s in forest.others) s.sessionId], ['idle']);
    });

    test('a waiter whose holder was not sampled is still the head', () {
      final forest = blockingForest([
        session('waiter', blockedBy: ['gone']),
      ]);
      expect(forest.roots.single.session.sessionId, 'waiter');
      expect(forest.others, isEmpty);
    });

    test('two sessions waiting on each other do not recurse forever', () {
      final forest = blockingForest([
        session('a', blockedBy: ['b'], blocks: 1),
        session('b', blockedBy: ['a'], blocks: 3),
      ]);

      // A cycle has no head, so it starts at whichever blocks the most.
      expect(forest.roots.single.session.sessionId, 'b');
      expect(forest.roots.single.children.single.session.sessionId, 'a');
      expect(forest.roots.single.children.single.children, isEmpty);
    });
  });

  test('each tab asks for its own instance', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/activity')) {
        writeJson(req, 200, activity());
      } else if (seen.path.endsWith('/sessions')) {
        writeJson(req, 200, {
          'sampled_at': '2026-10-10T09:00:00.000000000Z',
          'sessions': <Object>[],
        });
      } else {
        writeJson(req, 200, {'queries': <Object>[], 'total_time_ms': 1200.0});
      }
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';

    await DbActivityController(client, 'db1.internal:5432').refresh();
    await (DbQueriesController(client, 'db1.internal:5432')
          ..sort = 'avg'
          ..query = 'orders')
        .refresh();
    final sessions = DbSessionsController(client, 'db1.internal:5432');
    await sessions.refresh();

    expect(
      Uri.decodeQueryComponent(server.requests[0].query),
      'instance=db1.internal:5432',
    );
    expect(
      Uri.decodeQueryComponent(server.requests[1].query),
      'instance=db1.internal:5432&sort=avg&limit=50&q=orders',
    );
    expect(
      Uri.decodeQueryComponent(server.requests[2].query),
      'instance=db1.internal:5432',
    );
    // A session list is a photograph; its time comes back with it.
    expect(sessions.sampledAt, isNotNull);
  });
}
