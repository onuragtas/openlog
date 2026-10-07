// Against a real HttpServer rather than a stubbed http.Client: what matters
// here is that the bearer token and the organization header actually leave the
// phone, and that failures that do not come from openlog still read sensibly.
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';

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

Map<String, Object?> _deviceSession(String token) => {
  'token': token,
  'session_id': 's-1',
  'expires_at': '2027-01-05T10:00:00.000000000Z',
  'me': {
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
      'id': 'o1',
      'tenant_id': 't1',
      'name': 'Org A',
      'role': 'owner',
    },
    'role': 'owner',
    'organizations': [
      {'id': 'o1', 'tenant_id': 't1', 'name': 'Org A', 'role': 'owner'},
    ],
    'csrf_token': null,
  },
};

void main() {
  group('normalizeBaseUrl', () {
    test('a bare host becomes https, because that is what people type', () {
      expect(normalizeBaseUrl('apm.resoft.org'), 'https://apm.resoft.org');
      expect(normalizeBaseUrl('  apm.resoft.org  '), 'https://apm.resoft.org');
    });

    test('a trailing slash goes, so the path is not built with two', () {
      expect(
        normalizeBaseUrl('https://apm.resoft.org/'),
        'https://apm.resoft.org',
      );
      expect(
        normalizeBaseUrl('https://apm.resoft.org///'),
        'https://apm.resoft.org',
      );
    });

    test('plain http is kept rather than upgraded', () {
      // A self-hosted install on a private network is a real case, and a silent
      // upgrade to https fails in a way that reads as "wrong address".
      expect(
        normalizeBaseUrl('http://openlog.lan:8080'),
        'http://openlog.lan:8080',
      );
    });

    test('a path is kept and a pasted page URL is trimmed to the address', () {
      expect(
        normalizeBaseUrl('https://example.com/openlog'),
        'https://example.com/openlog',
      );
      expect(
        normalizeBaseUrl('https://apm.resoft.org/alerts?range=1h#x'),
        'https://apm.resoft.org/alerts',
      );
    });

    test('what cannot be an address is refused with something to read', () {
      expect(() => normalizeBaseUrl(''), throwsA(isA<FormatException>()));
      expect(() => normalizeBaseUrl('   '), throwsA(isA<FormatException>()));
      expect(
        () => normalizeBaseUrl('ftp://example.com'),
        throwsA(
          isA<FormatException>().having(
            (e) => e.message,
            'message',
            contains('https://'),
          ),
        ),
      );
    });
  });

  test('authConfig reads what the address allows', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, _authConfig),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);

    final cfg = await client.authConfig();
    expect(cfg.signupEnabled, isTrue);
    expect(cfg.passwordMinLength, 12);
    expect(cfg.captcha, isNull);
    expect(server.requests.single.path, '/api/v1/auth/config');
    // No credentials: this is the call that proves an address before anyone types a password.
    expect(
      server.requests.single.headers.containsKey('authorization'),
      isFalse,
    );
  });

  test(
    'signing in sends the device name and the token then rides on every call',
    () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path == '/api/v1/auth/device') {
          writeJson(req, 201, _deviceSession('olm_${'a' * 48}'));
        } else {
          writeJson(req, 200, _deviceSession('x')['me']);
        }
      });
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);

      final session = await client.signIn(
        email: 'owner@example.com',
        password: 'pw',
        deviceName: "Onur's iPhone",
      );
      expect(session.token.startsWith('olm_'), isTrue);
      expect(jsonDecode(server.requests.first.body), {
        'email': 'owner@example.com',
        'password': 'pw',
        'device_name': "Onur's iPhone",
      });

      final me = await client.me();
      expect(me.user!.email, 'owner@example.com');
      final second = server.requests[1];
      expect(second.headers['authorization'], 'Bearer ${session.token}');
      // The organization of the sign-in is carried, so a person in several does
      // not silently read a different one than the screen says.
      expect(second.headers['x-openlog-org-id'], 'o1');
    },
  );

  test('an openlog error keeps its code and message', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 401, {
        'error': {
          'code': 'unauthenticated',
          'message': 'invalid email or password',
        },
      }),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);

    await expectLater(
      client.signIn(email: 'a@b.c', password: 'nope', deviceName: 'phone'),
      throwsA(
        isA<ApiException>()
            .having((e) => e.isUnauthenticated, 'isUnauthenticated', isTrue)
            .having((e) => e.code, 'code', 'unauthenticated')
            .having((e) => e.message, 'message', 'invalid email or password'),
      ),
    );
  });

  test('a failure that did not come from openlog still reads sensibly', () async {
    // A proxy or load balancer answering for the server: HTML, no error object.
    final server = await FakeServer.start(
      (req, _) => req.response
        ..statusCode = 502
        ..headers.contentType = ContentType.html
        ..write('<html><body>Bad Gateway</body></html>'),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);

    await expectLater(
      client.me(),
      throwsA(
        isA<ApiException>()
            .having((e) => e.status, 'status', 502)
            .having((e) => e.code, 'code', isEmpty)
            .having((e) => e.message, 'message', contains('502')),
      ),
    );
  });

  test(
    'an address that answers 200 with something other than JSON says so',
    () async {
      // A captive portal is the common case, and "unexpected token <" would send
      // the person looking for the wrong problem.
      final server = await FakeServer.start(
        (req, _) => req.response
          ..statusCode = 200
          ..headers.contentType = ContentType.html
          ..write('<html>sign in to the wifi</html>'),
      );
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl);
      addTearDown(client.close);

      await expectLater(
        client.authConfig(),
        throwsA(
          isA<ApiException>().having(
            (e) => e.message,
            'message',
            contains('not an openlog server'),
          ),
        ),
      );
    },
  );

  test(
    'an address that nothing answers is a different problem from a wrong password',
    () async {
      // Port 1 on loopback: nothing listens, so this fails without a network.
      final client = OpenlogClient(
        baseUrl: 'http://127.0.0.1:1',
        timeout: const Duration(seconds: 2),
      );
      addTearDown(client.close);

      await expectLater(client.authConfig(), throwsA(isA<ApiUnreachable>()));
    },
  );

  test('signing out forgets the token even when the request fails', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 503, {
        'error': {'code': 'unavailable', 'message': 'try again'},
      }),
    );
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl);
    addTearDown(client.close);
    client.token = 'olm_x';

    await expectLater(client.signOut(), throwsA(isA<ApiException>()));
    // The person asked to be signed out; keeping the token because the network
    // was down would be the opposite of what they asked for.
    expect(client.token, isNull);
    expect(client.orgId, isNull);
  });
}
