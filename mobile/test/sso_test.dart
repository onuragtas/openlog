// Single sign-on: the few writes a phone makes, and what they must not
// quietly change.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/sso.dart';

import 'fake_server.dart';

Map<String, Object?> connection({
  bool enforce = false,
  List<String> breakGlass = const ['u7'],
}) => {
  'id': 'c1',
  'protocol': 'oidc',
  'name': 'Okta',
  'enabled': true,
  'default': true,
  'oidc': {
    'issuer': 'https://idp.example.com',
    'client_id': 'abc',
    'scopes': ['openid'],
    'require_email_verified': true,
    'client_secret_set': true,
  },
  'saml': null,
  'email_attribute': 'email',
  'name_attribute': 'name',
  'groups_attribute': 'groups',
  'jit_enabled': true,
  'default_role': 'member',
  'session_max_age_seconds': 36000,
  'logout_redirect_allowlist': <String>[],
  'allow_external_invitations': false,
  'enforce': enforce,
  'break_glass_user_ids': breakGlass,
  'config_version': 3,
  'tested': true,
  'last_test': null,
  'health': {
    'status': 'ok',
    'message': '',
    'checked_at': null,
    'next_at': null,
    'failures': 0,
    'metadata_valid_until': null,
  },
  'created_at': '2026-09-01T00:00:00.000000000Z',
  'updated_at': '2026-09-01T00:00:00.000000000Z',
};

Map<String, Object?> state({
  bool enforce = false,
  List<String> breakGlass = const ['u7'],
  bool scim = false,
}) => {
  'available': true,
  'secrets_encrypted': true,
  'scim_enabled': scim,
  'email_verification_available': true,
  'domain_email_local_parts': ['admin'],
  'service_provider': {
    'oidc_redirect_uri': 'https://openlog.example.com/cb',
    'oidc_post_logout_redirect_uri': 'https://openlog.example.com/',
    'oidc_backchannel_logout_uri': null,
    'oidc_frontchannel_logout_uri': null,
    'scim_base_url': 'https://openlog.example.com/scim',
    'saml_entity_id': null,
    'saml_acs_url': null,
    'saml_slo_url': null,
    'saml_slo_soap_url': null,
    'saml_metadata_url': null,
    'saml_certificate_pem': null,
  },
  'connection': connection(enforce: enforce, breakGlass: breakGlass),
  'connections': [connection(enforce: enforce, breakGlass: breakGlass)],
};

void main() {
  test('turning enforcement on keeps the break-glass list', () async {
    Map<String, Object?>? body;
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'PUT') {
        body = jsonDecode(seen.body) as Map<String, Object?>;
        writeJson(req, 200, state(enforce: true));
        return;
      }
      if (seen.path.endsWith('/domains')) {
        writeJson(req, 200, {'domains': <Object>[]});
      } else if (seen.path.endsWith('/role-mappings')) {
        writeJson(req, 200, {'mappings': <Object>[]});
      } else {
        writeJson(req, 200, state());
      }
    });
    addTearDown(server.stop);
    final c = SsoController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.load();
    await c.setEnforce(enforce: true);

    // The endpoint replaces both fields. Sending the toggle alone would
    // empty the list and lock everybody into the identity provider.
    expect(body?['enforce'], isTrue);
    expect(body?['break_glass_user_ids'], ['u7']);
  });

  test('a mapping is added by sending the whole set', () async {
    final bodies = <Map<String, Object?>>[];
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'PUT') {
        bodies.add(jsonDecode(seen.body) as Map<String, Object?>);
        req.response.statusCode = 204;
        return;
      }
      if (seen.path.endsWith('/domains')) {
        writeJson(req, 200, {'domains': <Object>[]});
      } else if (seen.path.endsWith('/role-mappings')) {
        writeJson(req, 200, {
          'mappings': [
            {'group': 'sre', 'role': 'admin'},
          ],
        });
      } else {
        writeJson(req, 200, state());
      }
    });
    addTearDown(server.stop);
    final c = SsoController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.load();
    await c.setMapping('devs', Role.viewer);

    final sent = (bodies.single['mappings']! as List).cast<Map>();
    // The existing one is kept: the endpoint replaces the set, so leaving
    // it out would delete it.
    expect(sent.map((m) => m['group']), ['sre', 'devs']);
    expect(sent.last['role'], 'viewer');
  });

  test('owner never comes from a group', () async {
    final bodies = <Map<String, Object?>>[];
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'PUT') {
        bodies.add(jsonDecode(seen.body) as Map<String, Object?>);
        req.response.statusCode = 204;
        return;
      }
      if (seen.path.endsWith('/domains')) {
        writeJson(req, 200, {'domains': <Object>[]});
      } else if (seen.path.endsWith('/role-mappings')) {
        writeJson(req, 200, {'mappings': <Object>[]});
      } else {
        writeJson(req, 200, state());
      }
    });
    addTearDown(server.stop);
    final c = SsoController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.setMapping('owners', Role.owner);

    // The contract's mapping role has three values; owner is given by a
    // person, not by an identity provider.
    final sent = (bodies.single['mappings']! as List).cast<Map>();
    expect(sent.single['role'], 'member');
  });

  test('SCIM tokens are only asked for when the server has SCIM', () async {
    final paths = <String>[];
    var scim = false;
    final server = await FakeServer.start((req, seen) {
      paths.add(seen.path);
      if (seen.path.endsWith('/domains')) {
        writeJson(req, 200, {'domains': <Object>[]});
      } else if (seen.path.endsWith('/role-mappings')) {
        writeJson(req, 200, {'mappings': <Object>[]});
      } else if (seen.path.endsWith('/scim/tokens')) {
        writeJson(req, 200, {
          'tokens': <Object>[],
          'base_url': 'https://openlog.example.com/scim/v2',
          'enabled': true,
        });
      } else {
        writeJson(req, 200, state(scim: scim));
      }
    });
    addTearDown(server.stop);
    final c = SsoController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.load();
    expect(paths.where((p) => p.endsWith('/scim/tokens')), isEmpty);

    scim = true;
    await c.load();
    expect(paths.where((p) => p.endsWith('/scim/tokens')), hasLength(1));
  });
}
