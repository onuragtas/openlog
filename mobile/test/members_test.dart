// Who is in the organization, and who has been asked to join.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/members.dart';

import 'fake_server.dart';

Map<String, Object?> member(String id, String role) => {
  'user_id': id,
  'email': '$id@example.com',
  'name': '',
  'role': role,
  'joined_at': '2026-09-01T00:00:00.000000000Z',
};

Map<String, Object?> invitation(String id, {bool expired = false}) => {
  'id': id,
  'email': '$id@example.com',
  'role': 'member',
  'invited_by_email': 'owner@example.com',
  'created_at': '2026-10-01T00:00:00.000000000Z',
  'expires_at': '2026-10-15T00:00:00.000000000Z',
  'expired': expired,
  'last_sent_at': null,
  'send_count': 1,
};

void main() {
  test('both lists come back together, and again after a change', () async {
    final paths = <String>[];
    final server = await FakeServer.start((req, seen) {
      paths.add('${seen.method} ${seen.path}');
      if (seen.method == 'PATCH') {
        req.response.statusCode = 204;
        return;
      }
      if (seen.path.endsWith('/invitations')) {
        writeJson(req, 200, {
          'invitations': [invitation('i1')],
        });
      } else {
        writeJson(req, 200, {
          'members': [member('u1', 'owner'), member('u2', 'member')],
        });
      }
    });
    addTearDown(server.stop);
    final c = MembersController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.load();
    expect(c.members, hasLength(2));
    expect(c.invitations, hasLength(1));

    await c.setRole('u2', Role.admin);

    // The change, then both lists again: a role shown next to a member is
    // the server's answer, not what was tapped.
    expect(paths, [
      'GET /api/v1/members',
      'GET /api/v1/invitations',
      'PATCH /api/v1/members/u2',
      'GET /api/v1/members',
      'GET /api/v1/invitations',
    ]);
  });

  test(
    'an invitation hands its token over once, and it stays on screen',
    () async {
      Map<String, Object?>? posted;
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'POST') {
          posted = jsonDecode(seen.body) as Map<String, Object?>;
          writeJson(req, 201, {
            'invitation': invitation('i2'),
            'token': 'oli_secret',
            'email_sent': false,
          });
          return;
        }
        if (seen.path.endsWith('/invitations')) {
          writeJson(req, 200, {
            'invitations': [invitation('i2')],
          });
        } else {
          writeJson(req, 200, {'members': <Object>[]});
        }
      });
      addTearDown(server.stop);
      final c = MembersController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );
      addTearDown(c.dispose);

      await c.invite(email: 'yeni@example.com', role: Role.viewer);

      expect(posted, {'email': 'yeni@example.com', 'role': 'viewer'});
      // The e-mail did not go out, so this token is the only copy there is.
      expect(c.created?.token, 'oli_secret');
      expect(c.created?.emailSent, isFalse);

      c.dismissCreated();
      expect(c.created, isNull);
    },
  );

  test(
    'the server keeps the last owner, and says so in its own words',
    () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'PATCH') {
          writeJson(req, 409, {
            'error': {'message': 'an organization needs an owner'},
          });
          return;
        }
        if (seen.path.endsWith('/invitations')) {
          writeJson(req, 200, {'invitations': <Object>[]});
        } else {
          writeJson(req, 200, {
            'members': [member('u1', 'owner')],
          });
        }
      });
      addTearDown(server.stop);
      final c = MembersController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );
      addTearDown(c.dispose);

      await c.load();
      await c.setRole('u1', Role.member);

      // Reloaded first, so the row still shows what the server has, and the
      // server's own sentence rather than a guess about why.
      expect(c.members.single.role, Role.owner);
      expect(c.failure?.detail, contains('needs an owner'));
    },
  );
}
