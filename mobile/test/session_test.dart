// The controller against a real server, because what matters here is the
// sequence: what is stored, what is checked on the next launch, and what is
// thrown away when the answer is 401.
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';

import 'fake_server.dart';

const _authConfig = {
  'mode': 'postgres',
  'signup_enabled': true,
  'password_min_length': 12,
  'email_enabled': true,
  'email_verification_required': false,
  'captcha': null,
  'sso_enabled': true,
};

Map<String, Object?> _me({
  String org = 'o1',
  List<String> orgs = const ['o1'],
}) => {
  'auth': 'device',
  'user': {
    'id': 'u1',
    'email': 'owner@example.com',
    'name': 'Onur',
    'email_verified': true,
    'language': 'tr',
  },
  'api_key': null,
  'organization': {
    'id': org,
    'tenant_id': 't1',
    'name': 'Org $org',
    'role': 'owner',
  },
  'role': 'owner',
  'organizations': [
    for (final o in orgs)
      {'id': o, 'tenant_id': 't$o', 'name': 'Org $o', 'role': 'owner'},
  ],
  'csrf_token': null,
};

Map<String, Object?> _session(
  String token, {
  String org = 'o1',
  List<String> orgs = const ['o1'],
}) => {
  'token': token,
  'session_id': 's-1',
  'expires_at': '2027-01-05T10:00:00.000000000Z',
  'me': _me(org: org, orgs: orgs),
};

/// A server that signs in, answers /auth/me and records the org header.
Future<FakeServer> happyServer({List<String> orgs = const ['o1']}) {
  return FakeServer.start((req, seen) {
    switch (seen.path) {
      case '/api/v1/auth/config':
        writeJson(req, 200, _authConfig);
      case '/api/v1/auth/device':
        writeJson(req, 201, _session('olm_${'a' * 48}', orgs: orgs));
      case '/api/v1/auth/signup':
        writeJson(req, 201, _me(orgs: orgs));
      case '/api/v1/auth/me':
        writeJson(
          req,
          200,
          _me(org: seen.headers['x-openlog-org-id'] ?? 'o1', orgs: orgs),
        );
      case '/api/v1/auth/logout':
        req.response.statusCode = HttpStatus.noContent;
      default:
        writeJson(req, 404, {
          'error': {'code': 'not_found', 'message': 'no'},
        });
    }
  });
}

SessionController controllerFor(FakeServer server, TokenStore store) =>
    SessionController(
      store: store,
      openClient: (baseUrl) =>
          OpenlogClient(baseUrl: baseUrl, timeout: const Duration(seconds: 5)),
    );

void main() {
  test('nothing stored means the first screen asks for an address', () async {
    final c = SessionController(store: MemoryTokenStore());
    addTearDown(c.dispose);

    await c.restore();
    expect(c.stage, SessionStage.needsServer);
    expect(c.failure, isNull);
  });

  test(
    'an address that answers moves on to sign-in and remembers what it allows',
    () async {
      final server = await happyServer();
      addTearDown(server.stop);
      final c = controllerFor(server, MemoryTokenStore());
      addTearDown(c.dispose);

      await c.useServer(server.baseUrl);
      expect(c.stage, SessionStage.needsSignIn);
      expect(c.authConfig!.signupEnabled, isTrue);
      expect(c.authConfig!.passwordMinLength, 12);
      expect(c.baseUrl, server.baseUrl);
    },
  );

  test(
    'an address with nothing behind it is reported without leaving the screen',
    () async {
      final c = SessionController(
        store: MemoryTokenStore(),
        openClient: (b) =>
            OpenlogClient(baseUrl: b, timeout: const Duration(seconds: 2)),
      );
      addTearDown(c.dispose);

      await c.useServer('http://127.0.0.1:1');
      expect(
        c.stage,
        SessionStage.needsServer,
        reason: 'the person has to be able to correct the address',
      );
      expect(c.failure!.kind, 'unreachable');
    },
  );

  test(
    'something that is not openlog is a different message from a wrong password',
    () async {
      final server = await FakeServer.start(
        (req, _) => req.response
          ..statusCode = 200
          ..headers.contentType = ContentType.html
          ..write('<html>hello</html>'),
      );
      addTearDown(server.stop);
      final c = controllerFor(server, MemoryTokenStore());
      addTearDown(c.dispose);

      await c.useServer(server.baseUrl);
      expect(c.failure!.kind, 'notOpenlog');
    },
  );

  test(
    'signing in stores the token and the organization it signed into',
    () async {
      final server = await happyServer();
      addTearDown(server.stop);
      final store = MemoryTokenStore();
      final c = controllerFor(server, store);
      addTearDown(c.dispose);

      await c.useServer(server.baseUrl);
      await c.signIn(
        email: 'owner@example.com',
        password: 'pw',
        deviceName: "Onur's iPhone",
      );

      expect(c.stage, SessionStage.signedIn);
      expect(c.me!.user!.email, 'owner@example.com');
      final stored = await store.read();
      expect(stored!.token.startsWith('olm_'), isTrue);
      expect(stored.baseUrl, server.baseUrl);
      expect(stored.orgId, 'o1');
    },
  );

  test(
    'a stored session is checked against the server before it is trusted',
    () async {
      final server = await happyServer();
      addTearDown(server.stop);
      final store = MemoryTokenStore(
        StoredSession(baseUrl: server.baseUrl, token: 'olm_x', orgId: 'o1'),
      );
      final c = controllerFor(server, store);
      addTearDown(c.dispose);

      await c.restore();
      expect(c.stage, SessionStage.signedIn);
      // The check is a real request, not a guess from what is in storage.
      expect(server.requests.map((r) => r.path), contains('/api/v1/auth/me'));
    },
  );

  test(
    'a revoked session lands on sign-in and is thrown away, not retried forever',
    () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path == '/api/v1/auth/config') {
          writeJson(req, 200, _authConfig);
        } else {
          writeJson(req, 401, {
            'error': {
              'code': 'unauthenticated',
              'message': 'session expired or revoked',
            },
          });
        }
      });
      addTearDown(server.stop);
      final store = MemoryTokenStore(
        StoredSession(baseUrl: server.baseUrl, token: 'olm_dead'),
      );
      final c = controllerFor(server, store);
      addTearDown(c.dispose);

      await c.restore();
      expect(c.stage, SessionStage.needsSignIn);
      expect(
        await store.read(),
        isNull,
        reason: 'a token the server refuses is not worth keeping',
      );
    },
  );

  test('being offline does not sign the person out', () async {
    // Nothing listens, so restore cannot check the token. The token is fine and
    // the train is in a tunnel; throwing it away would be the wrong lesson.
    final store = MemoryTokenStore(
      const StoredSession(baseUrl: 'http://127.0.0.1:1', token: 'olm_x'),
    );
    final c = SessionController(
      store: store,
      openClient: (b) =>
          OpenlogClient(baseUrl: b, timeout: const Duration(seconds: 2)),
    );
    addTearDown(c.dispose);

    await c.restore();
    expect(c.stage, SessionStage.needsSignIn);
    expect(c.failure!.kind, 'unreachable');
    expect(
      await store.read(),
      isNotNull,
      reason: 'the token survives a bad network',
    );
  });

  test(
    'switching organization sends the header and keeps the choice',
    () async {
      final server = await happyServer(orgs: ['o1', 'o2']);
      addTearDown(server.stop);
      final store = MemoryTokenStore();
      final c = controllerFor(server, store);
      addTearDown(c.dispose);

      await c.useServer(server.baseUrl);
      await c.signIn(
        email: 'owner@example.com',
        password: 'pw',
        deviceName: 'phone',
      );
      await c.switchOrganization('o2');

      expect(c.organization!.id, 'o2');
      expect((await store.read())!.orgId, 'o2');
      expect(server.requests.last.headers['x-openlog-org-id'], 'o2');
    },
  );

  test('a failed switch puts the previous organization back', () async {
    var allow = true;
    final server = await FakeServer.start((req, seen) {
      switch (seen.path) {
        case '/api/v1/auth/config':
          writeJson(req, 200, _authConfig);
        case '/api/v1/auth/device':
          writeJson(req, 201, _session('olm_${'a' * 48}', orgs: ['o1', 'o2']));
        case '/api/v1/auth/me':
          if (!allow) {
            writeJson(req, 403, {
              'error': {'code': 'permission_denied', 'message': 'not a member'},
            });
          } else {
            writeJson(req, 200, _me(orgs: ['o1', 'o2']));
          }
        default:
          writeJson(req, 404, {
            'error': {'code': 'not_found', 'message': 'no'},
          });
      }
    });
    addTearDown(server.stop);
    final c = controllerFor(server, MemoryTokenStore());
    addTearDown(c.dispose);

    await c.useServer(server.baseUrl);
    await c.signIn(
      email: 'owner@example.com',
      password: 'pw',
      deviceName: 'phone',
    );
    allow = false;
    await c.switchOrganization('o2');

    // The app must not be acting in an organization the screen does not name.
    expect(c.organization!.id, 'o1');
    expect(c.failure, isNotNull);
  });

  test('signing up creates the account and signs the device in', () async {
    final server = await happyServer();
    addTearDown(server.stop);
    final store = MemoryTokenStore();
    final c = controllerFor(server, store);
    addTearDown(c.dispose);

    await c.useServer(server.baseUrl);
    await c.signUp(
      email: 'new@example.com',
      password: 'a long enough password',
      name: 'Onur',
      organizationName: 'Acme',
      deviceName: 'phone',
    );

    expect(c.stage, SessionStage.signedIn);
    // Two calls, because signup answers with a cookie and a phone needs a token.
    expect(
      server.requests.map((r) => r.path),
      containsAllInOrder(['/api/v1/auth/signup', '/api/v1/auth/device']),
    );
    expect((await store.read())!.token.startsWith('olm_'), isTrue);
  });

  test(
    'sign-up is refused before a request when the server has closed it',
    () async {
      final server = await FakeServer.start(
        (req, _) =>
            writeJson(req, 200, {..._authConfig, 'signup_enabled': false}),
      );
      addTearDown(server.stop);
      final c = controllerFor(server, MemoryTokenStore());
      addTearDown(c.dispose);

      await c.useServer(server.baseUrl);
      final before = server.requests.length;
      await c.signUp(
        email: 'a@b.c',
        password: 'pw',
        name: 'x',
        organizationName: 'y',
        deviceName: 'p',
      );

      expect(c.failure!.kind, 'signUpClosed');
      expect(
        server.requests.length,
        before,
        reason: 'no round trip to be told the server rule',
      );
    },
  );

  test(
    'a CAPTCHA sends the person to a browser instead of failing obscurely',
    () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {
          ..._authConfig,
          'captcha': {'provider': 'turnstile', 'site_key': '0x4AAA'},
        }),
      );
      addTearDown(server.stop);
      final c = controllerFor(server, MemoryTokenStore());
      addTearDown(c.dispose);

      await c.useServer(server.baseUrl);
      await c.signUp(
        email: 'a@b.c',
        password: 'pw',
        name: 'x',
        organizationName: 'y',
        deviceName: 'p',
      );
      expect(c.failure!.kind, 'signUpNeedsCaptcha');
    },
  );

  test(
    'signing out clears the token even if the server never hears about it',
    () async {
      final server = await happyServer();
      addTearDown(server.stop);
      final store = MemoryTokenStore();
      final c = controllerFor(server, store);
      addTearDown(c.dispose);

      await c.useServer(server.baseUrl);
      await c.signIn(
        email: 'owner@example.com',
        password: 'pw',
        deviceName: 'phone',
      );
      await server.stop(); // the network goes away mid-sign-out
      await c.signOut();

      expect(c.stage, SessionStage.needsSignIn);
      expect(c.me, isNull);
      expect(await store.read(), isNull);
    },
  );

  test(
    'changing server forgets the token, which belonged to the old one',
    () async {
      final server = await happyServer();
      addTearDown(server.stop);
      final store = MemoryTokenStore();
      final c = controllerFor(server, store);
      addTearDown(c.dispose);

      await c.useServer(server.baseUrl);
      await c.signIn(
        email: 'owner@example.com',
        password: 'pw',
        deviceName: 'phone',
      );
      await c.forgetServer();

      expect(c.stage, SessionStage.needsServer);
      expect(c.authConfig, isNull);
      expect(await store.read(), isNull);
    },
  );
}
