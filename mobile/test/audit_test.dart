// The audit log: what the server is asked, and how a page continues.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/audit.dart';

import 'fake_server.dart';

Map<String, Object?> event(int id, {Map<String, Object?>? apiKey}) => {
  'id': id,
  'actor_email': apiKey == null ? 'onur@example.com' : '',
  'actor_api_key': apiKey,
  'action': 'member.remove',
  'target_type': 'user',
  'target_id': 'u9',
  'details': <String, Object?>{},
  'ip': '10.0.0.4',
  'created_at': '2026-10-09T10:00:00.000000000Z',
};

void main() {
  test(
    'filters go to the server, and a page continues with its cursor',
    () async {
      final queries = <String>[];
      final server = await FakeServer.start((req, seen) {
        queries.add(Uri.decodeQueryComponent(seen.query));
        writeJson(req, 200, {
          'events': [event(queries.length)],
          'next_cursor': queries.length < 2 ? 'c2' : null,
        });
      });
      addTearDown(server.stop);
      final c = AuditController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );
      addTearDown(c.dispose);

      c.actor = 'onur';
      c.action = 'member.';
      await c.load();
      expect(queries.single, contains('actor=onur'));
      expect(queries.single, contains('action=member.'));
      expect(c.nextCursor, 'c2');

      await c.more();
      // The cursor goes with the same filters: the server built it from them.
      expect(queries.last, contains('cursor=c2'));
      expect(queries.last, contains('actor=onur'));
      // The next page is added, not swapped in.
      expect(c.events, hasLength(2));
      // And the end of the log is the end: no cursor, no button.
      expect(c.nextCursor, isNull);
    },
  );

  test('more() does nothing at the end of the log', () async {
    var calls = 0;
    final server = await FakeServer.start((req, seen) {
      calls++;
      writeJson(req, 200, {
        'events': [event(1)],
        'next_cursor': null,
      });
    });
    addTearDown(server.stop);
    final c = AuditController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.load();
    await c.more();
    expect(calls, 1);
  });

  test('a change made with an API key names the key, not an empty person', () {
    final byKey = AuditEvent.fromJson(
      event(1, apiKey: {'id': 'k1', 'name': 'ci'}),
    );
    final byPerson = AuditEvent.fromJson(event(2));

    // The key is the actor; "—" there would hide which key could do it.
    expect(auditActor(byKey, 'bilinmiyor'), 'ci');
    expect(auditActor(byPerson, 'bilinmiyor'), 'onur@example.com');
  });
}
