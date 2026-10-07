// The generated reader is where a contract break either becomes a clear error or
// a confusing one three screens later, so these tests are about its edges rather
// than about field names.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';

/// A realistic POST /api/v1/auth/device 201 body.
Map<String, Object?> deviceBody({Object? csrf, String role = 'owner'}) => {
  'token': 'olm_${'a' * 48}',
  'session_id': '9f1e2d9a-3b4f-4e5a-8b6c-000000000001',
  'expires_at': '2027-01-05T10:00:00.000000000Z',
  'me': {
    'auth': 'device',
    'user': {
      'id': 'af1ed251-8538-45ff-87e3-d6731e5a6e9c',
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
      'role': role,
    },
    'role': role,
    'organizations': [
      {'id': 'o1', 'tenant_id': 't1', 'name': 'Org A', 'role': role},
      {'id': 'o2', 'tenant_id': 't2', 'name': 'Org B', 'role': 'viewer'},
    ],
    'csrf_token': csrf,
  },
};

void main() {
  test('a device sign-in body parses into the types the screens use', () {
    final s = DeviceSession.fromJson(deviceBody());

    expect(s.token.startsWith('olm_'), isTrue);
    expect(s.expiresAt.year, 2027);
    expect(
      s.expiresAt.isUtc,
      isTrue,
      reason: 'timestamps are normalised, so no screen compares across zones',
    );
    expect(s.me.auth, MeAuth.device);
    expect(s.me.user!.email, 'owner@example.com');
    expect(s.me.user!.language, UserLanguage.tr);
    expect(s.me.role, Role.owner);
    // The organization switcher reads this list; two entries means it is shown.
    expect(s.me.organizations.map((o) => o.name), ['Org A', 'Org B']);
    expect(s.me.organizations.last.role, Role.viewer);
  });

  test(
    'a device session carries no CSRF token and the field is simply absent',
    () {
      expect(DeviceSession.fromJson(deviceBody()).me.csrfToken, isNull);
      // `type: [string, "null"]` must read an explicit null as null, not throw.
      expect(
        DeviceSession.fromJson(deviceBody(csrf: null)).me.csrfToken,
        isNull,
      );
      // And a browser session's token still reads.
      expect(
        DeviceSession.fromJson(deviceBody(csrf: 'tok')).me.csrfToken,
        'tok',
      );
    },
  );

  // The guarantee a store build depends on: the server can be newer than the
  // app, and a value added there must not take a screen down with it.
  test(
    'a role this build has never heard of reads as unknown rather than throwing',
    () {
      final s = DeviceSession.fromJson(deviceBody(role: 'auditor'));
      expect(s.me.role, Role.unknown);
      expect(s.me.organizations.first.role, Role.unknown);
      // Known values keep working alongside it.
      expect(s.me.organizations.last.role, Role.viewer);
    },
  );

  test('a missing required field names itself', () {
    final body = deviceBody();
    (body['me']! as Map<String, Object?>).remove('organizations');

    expect(
      () => DeviceSession.fromJson(body),
      throwsA(
        isA<ApiShapeError>()
            .having((e) => e.path, 'path', 'DeviceSession.me.organizations')
            .having((e) => e.detail, 'detail', contains('required')),
      ),
    );
  });

  test('a field of the wrong type says which field and what it got', () {
    final body = deviceBody();
    (body['me']! as Map<String, Object?>)['organizations'] = [
      {'id': 'o1', 'tenant_id': 't1', 'name': 42},
    ];

    expect(
      () => DeviceSession.fromJson(body),
      throwsA(
        isA<ApiShapeError>()
            .having(
              (e) => e.path,
              'path',
              'DeviceSession.me.organizations[0].name',
            )
            .having(
              (e) => e.toString(),
              'message',
              contains('expected a string'),
            ),
      ),
    );
  });

  group('AuthConfig is the first call the app makes against an address', () {
    Map<String, Object?> base() => {
      'mode': 'postgres',
      'signup_enabled': true,
      'password_min_length': 12,
      'email_enabled': true,
      'email_verification_required': false,
      'captcha': null,
      'sso_enabled': true,
    };

    test(
      'no captcha configured means the field is null, not an empty object',
      () {
        final c = AuthConfig.fromJson(base());
        expect(c.captcha, isNull);
        expect(c.mode, AuthConfigMode.postgres);
        expect(c.signupEnabled, isTrue);
        expect(c.passwordMinLength, 12);
        expect(c.ssoEnabled, isTrue);
      },
    );

    test('a configured captcha carries the provider and site key', () {
      final c = AuthConfig.fromJson(
        base()..['captcha'] = {'provider': 'turnstile', 'site_key': '0x4AAA'},
      );
      expect(c.captcha!.provider, AuthConfigCaptchaProvider.turnstile);
      expect(c.captcha!.siteKey, '0x4AAA');
    });

    test(
      'a server that sends no sso_enabled is readable, since it is not required',
      () {
        final c = AuthConfig.fromJson(base()..remove('sso_enabled'));
        expect(c.ssoEnabled, isNull);
      },
    );
  });

  test('a session list tells a phone from a browser', () {
    final phone = Session.fromJson({
      'id': 's-3',
      'created_at': '2026-09-28T09:00:00.000000000Z',
      'last_seen_at': '2026-10-07T08:40:00.000000000Z',
      'expires_at': '2026-12-27T09:00:00.000000000Z',
      'ip': '203.0.113.44',
      'user_agent': 'openlog-mobile/1.0 (iOS 18.2)',
      'current': false,
      'kind': 'device',
      'device_name': "Onur's iPhone",
    });

    expect(phone.kind, SessionKind.device);
    expect(phone.deviceName, "Onur's iPhone");
    expect(phone.current, isFalse);
  });
}
