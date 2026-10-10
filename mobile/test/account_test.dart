// The account behind the token: its password and its language.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/account.dart';
import 'package:openlog_mobile/src/api/client.dart';

import 'fake_server.dart';

void main() {
  test(
    'a wrong current password is said plainly, not as a server error',
    () async {
      final server = await FakeServer.start((req, seen) {
        writeJson(req, 403, {
          'error': {'message': 'invalid credentials'},
        });
      });
      addTearDown(server.stop);
      final c = AccountController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );
      addTearDown(c.dispose);

      expect(
        await c.changePassword(
          currentPassword: 'wrong',
          newPassword: 'muzyedi8',
        ),
        isFalse,
      );
      // The usual reason, and the one the person can do something about.
      expect(c.failure?.kind, 'badCredentials');
      expect(c.passwordChanged, isFalse);
    },
  );

  test('too many tries is its own answer', () async {
    final server = await FakeServer.start((req, seen) {
      writeJson(req, 429, {
        'error': {'message': 'slow down'},
      });
    });
    addTearDown(server.stop);
    final c = AccountController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.changePassword(currentPassword: 'a', newPassword: 'muzyedi8');
    expect(c.failure?.kind, 'rateLimited');
  });

  test('a changed password is reported, with what else happened', () async {
    Map<String, Object?>? body;
    final server = await FakeServer.start((req, seen) {
      body = jsonDecode(seen.body) as Map<String, Object?>;
      req.response.statusCode = 204;
    });
    addTearDown(server.stop);
    final c = AccountController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    expect(
      await c.changePassword(currentPassword: 'eski', newPassword: 'muzyedi8'),
      isTrue,
    );
    expect(body, {'current_password': 'eski', 'new_password': 'muzyedi8'});
    // The screen says the other sessions are gone, which it can only do
    // because the controller remembers that the change went through.
    expect(c.passwordChanged, isTrue);
    expect(c.failure, isNull);
  });

  test('the language goes to the account, and the new Me comes back', () async {
    final server = await FakeServer.start((req, seen) {
      expect(seen.method, 'PATCH');
      writeJson(req, 200, {
        'auth': 'session',
        'user': {
          'id': 'u1',
          'email': 'a@b.c',
          'name': 'Onur',
          'email_verified': true,
          'language': 'tr',
        },
        'organizations': <Object>[],
      });
    });
    addTearDown(server.stop);
    var got = '';
    final c = AccountController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      onMe: (me) => got = me.user?.language.wire ?? '',
    );
    addTearDown(c.dispose);

    await c.setLanguage('tr');
    // Handed back to the session, so the rest of the app sees the change
    // without asking again.
    expect(got, 'tr');
  });
}
