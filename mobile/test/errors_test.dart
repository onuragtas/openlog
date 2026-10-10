// The error inbox: what the filters ask the server for, and what a workflow
// change does to the list afterwards.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/errors.dart';

import 'fake_server.dart';

Map<String, Object?> group(
  String id, {
  String status = 'unresolved',
  String? regressedAt,
  int comments = 0,
}) => {
  'group_id': id,
  'service_name': 'checkout',
  'service_namespace': '',
  'environment': 'prod',
  'error_type': 'TimeoutError',
  'message': 'upstream timed out',
  'count': 42.0,
  'total_count': 120.0,
  'first_seen': '2026-10-01T09:00:00.000000000Z',
  'last_seen': '2026-10-10T01:00:00.000000000Z',
  'last_trace_id': 'abc',
  'last_span_name': 'POST /checkout',
  'sparkline': [
    [1760000000000.0, 3.0],
    [1760000060000.0, 7.0],
  ],
  'status': status,
  'assignee': null,
  'resolved_at': null,
  'resolved_in_version': '',
  'resolved_by_email': '',
  'regressed_at': regressedAt,
  'regression_count': regressedAt == null ? 0 : 2,
  'comment_count': comments,
  'updated_at': null,
  'updated_by_email': '',
};

Map<String, Object?> inbox(
  List<Map<String, Object?>> groups, {
  bool workflow = true,
  bool truncated = false,
}) => {
  'step': '1m',
  'groups': groups,
  'counts': {'unresolved': 3, 'resolved': 1, 'ignored': 0},
  'truncated': truncated,
  'workflow': workflow,
};

void main() {
  test(
    'the filters are the server\'s, and "all" is no filter at all',
    () async {
      final queries = <String>[];
      final server = await FakeServer.start((req, seen) {
        queries.add(seen.query);
        writeJson(req, 200, inbox([group('a')]));
      });
      addTearDown(server.stop);
      final c = ErrorInboxController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      await c.refresh();
      c.status = 'all';
      c.query = 'timeout';
      c.sort = 'last_seen';
      await c.refresh();

      expect(queries.first, contains('status=unresolved'));
      // `all` is this app's word for the tab, not a status the server knows:
      // sending it would be a 400, and filtering here would hide groups that
      // never came back in the first place.
      expect(queries.last, isNot(contains('status=')));
      expect(queries.last, contains('q=timeout'));
      expect(queries.last, contains('sort=last_seen'));
      expect(c.counts?.unresolved, 3);
    },
  );

  test(
    'resolving reloads, so the row leaves the tab it no longer belongs in',
    () async {
      var patched = 0;
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'PATCH') {
          patched++;
          final body = jsonDecode(seen.body) as Map<String, Object?>;
          expect(body['group_ids'], ['a']);
          expect(body['status'], 'resolved');
          // Omitted means unchanged: sending an empty assignee here would
          // unassign somebody as a side effect of resolving.
          expect(body.containsKey('assignee_user_id'), isFalse);
          writeJson(req, 200, {'groups': <Object>[]});
          return;
        }
        // After the change the unresolved tab no longer has it.
        writeJson(req, 200, inbox(patched == 0 ? [group('a')] : []));
      });
      addTearDown(server.stop);
      final c = ErrorInboxController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      await c.refresh();
      expect(c.items, hasLength(1));

      await c.setStatus('a', 'resolved');
      expect(patched, 1);
      expect(c.items, isEmpty);
      expect(c.failure, isNull);
    },
  );

  test('resolving in a version sends the version', () async {
    Map<String, Object?>? body;
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'PATCH') {
        body = jsonDecode(seen.body) as Map<String, Object?>;
        writeJson(req, 200, {'groups': <Object>[]});
        return;
      }
      writeJson(req, 200, inbox([group('a')]));
    });
    addTearDown(server.stop);
    final c = ErrorInboxController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.setStatus('a', 'resolved', version: '1.4.2');
    expect(body?['resolved_in_version'], '1.4.2');
  });

  test('a rejected change survives the reload that follows it', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'PATCH') {
        writeJson(req, 403, {
          'error': {'message': 'members and above'},
        });
        return;
      }
      writeJson(req, 200, inbox([group('a')]));
    });
    addTearDown(server.stop);
    final c = ErrorInboxController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.setStatus('a', 'ignored');

    // The reload is what keeps the row; reporting before it would have the
    // successful refresh() throw the message away.
    expect(c.items, hasLength(1));
    expect(c.failure?.kind, 'servicesForbidden');
  });

  test('a group that came back is not the same as one nobody looked at', () {
    final fresh = ApmErrorGroup.fromJson(group('a'));
    final back = ApmErrorGroup.fromJson(
      group('b', regressedAt: '2026-10-09T10:00:00.000000000Z'),
    );
    final done = ApmErrorGroup.fromJson(group('c', status: 'resolved'));

    expect(isRegressed(fresh), isFalse);
    expect(isRegressed(back), isTrue);
    expect(isRegressed(done), isFalse);
  });

  test('without PostgreSQL the screen is told there is no workflow', () async {
    final server = await FakeServer.start((req, seen) {
      writeJson(
        req,
        200,
        inbox([group('a')], workflow: false, truncated: true),
      );
    });
    addTearDown(server.stop);
    final c = ErrorInboxController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();
    expect(c.workflow, isFalse);
    expect(c.truncated, isTrue);
  });

  test(
    'comments are reloaded after writing one, and only mine can go',
    () async {
      final posted = <String>[];
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'POST') {
          posted.add((jsonDecode(seen.body) as Map)['body'] as String);
        }
        writeJson(req, 200, {
          'comments': [
            {
              'id': 'c1',
              'author_user_id': 'u1',
              'author_email': 'a@b.c',
              'author_name': 'Onur',
              'body': 'bakıyorum',
              'created_at': '2026-10-10T01:00:00.000000000Z',
            },
            {
              'id': 'c2',
              'author_user_id': 'u2',
              'author_email': 'x@y.z',
              'author_name': 'Başkası',
              'body': 'ben de',
              'created_at': '2026-10-10T01:05:00.000000000Z',
            },
          ],
        });
      });
      addTearDown(server.stop);
      final c = ErrorGroupController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        ApmErrorGroup.fromJson(group('a')),
      )..myUserId = 'u1';
      addTearDown(c.dispose);

      await c.comment('bakıyorum');
      expect(posted.single, 'bakıyorum');
      expect(c.comments, hasLength(2));

      // Authors delete their own; an admin deleting somebody else's is a web
      // thing, because the role cannot be checked here without a request.
      expect(c.canDelete(c.comments.first), isTrue);
      expect(c.canDelete(c.comments.last), isFalse);
    },
  );
}
