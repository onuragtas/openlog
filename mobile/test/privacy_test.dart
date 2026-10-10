// What this account may ask for, and what it must prove first.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/account.dart';
import 'package:openlog_mobile/src/api/client.dart';

import 'fake_server.dart';

Map<String, Object?> privacy({
  bool hasPassword = true,
  bool exports = true,
  List<Map<String, Object?>> deletions = const [],
}) => {
  'has_password': hasPassword,
  'data_export_enabled': exports,
  'org_deletion_grace_seconds': 7 * 86400,
  'reauth_max_age_seconds': 600,
  'org_deletions': deletions,
};

void main() {
  test('exports are only asked for when the server offers them', () async {
    final paths = <String>[];
    var enabled = false;
    final server = await FakeServer.start((req, seen) {
      paths.add(seen.path);
      if (seen.path.endsWith('/data-exports')) {
        writeJson(req, 200, {'exports': <Object>[]});
      } else {
        writeJson(req, 200, privacy(exports: enabled));
      }
    });
    addTearDown(server.stop);
    final c = PrivacyController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.load();
    expect(paths.where((p) => p.endsWith('/data-exports')), isEmpty);

    enabled = true;
    await c.load();
    expect(paths.where((p) => p.endsWith('/data-exports')), hasLength(1));
  });

  test(
    'a wrong password on a deletion asks for proof, not a server error',
    () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'POST') {
          writeJson(req, 403, {
            'error': {'message': 'reauthentication required'},
          });
          return;
        }
        writeJson(req, 200, privacy());
      });
      addTearDown(server.stop);
      final c = PrivacyController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );
      addTearDown(c.dispose);

      final ok = await c.deleteOrganization(
        confirmName: 'Resoft',
        password: 'yanlis',
      );

      expect(ok, isFalse);
      expect(c.failure?.kind, 'reauthNeeded');
    },
  );

  test('deleting the account signs the device out afterwards', () async {
    Map<String, Object?>? body;
    var signedOut = false;
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        body = jsonDecode(seen.body) as Map<String, Object?>;
        req.response.statusCode = 204;
        return;
      }
      writeJson(req, 200, privacy());
    });
    addTearDown(server.stop);
    final c = PrivacyController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    final ok = await c.deleteAccount(
      confirmEmail: 'onur@example.com',
      password: 'dogru',
      signOut: () async => signedOut = true,
    );

    expect(ok, isTrue);
    expect(body?['confirm_email'], 'onur@example.com');
    // The token belongs to an account that no longer exists.
    expect(signedOut, isTrue);
  });

  test('an account with no password sends none', () async {
    Map<String, Object?>? body;
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        body = jsonDecode(seen.body) as Map<String, Object?>;
        req.response.statusCode = 204;
        return;
      }
      writeJson(req, 200, privacy(hasPassword: false));
    });
    addTearDown(server.stop);
    final c = PrivacyController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.deleteAccount(
      confirmEmail: 'onur@example.com',
      signOut: () async {},
    );

    // Single sign-on accounts prove themselves with a recent session; an
    // empty password field would be a 400 about a field they do not have.
    expect(body!.containsKey('password'), isFalse);
  });

  test('a string timestamp is parsed, or shown as it came', () {
    expect(whenOf('2026-10-09T10:00:00Z', (t) => '${t.year}'), '2026');
    // Nothing to parse: better an unreadable date than a wrong one.
    expect(whenOf('soon', (t) => 'parsed'), 'soon');
  });
}
