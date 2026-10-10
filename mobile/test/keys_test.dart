// The three key tabs: what the forms send, and what the server shows once.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/keys.dart';

import 'fake_server.dart';

Map<String, Object?> licenseKey(String id, {String? revokedAt}) => {
  'id': id,
  'name': 'ingest',
  'prefix': 'olk_1a2b',
  'custom': false,
  'created_by_email': 'owner@example.com',
  'created_at': '2026-09-01T00:00:00.000000000Z',
  'last_used_at': null,
  'revoked_at': revokedAt,
};

Map<String, Object?> apiKey(String id) => {
  'id': id,
  'name': 'ci',
  'prefix': 'ola_9z8y',
  'role': 'viewer',
  'scope': 'read',
  'created_by_user_id': 'u1',
  'created_by_email': 'owner@example.com',
  'created_at': '2026-09-01T00:00:00.000000000Z',
  'last_used_at': null,
  'expires_at': null,
  'revoked_at': null,
};

Map<String, Object?> browserKey(String id, String kind) => {
  'id': id,
  'name': 'shop',
  'prefix': 'olb_3c4d',
  'key': 'olb_3c4d5e6f',
  'service_name': 'shop-web',
  'environment': 'prod',
  'kind': kind,
  'origins': kind == 'browser' ? ['https://shop.example.com'] : <String>[],
  'app_ids': kind == 'mobile' ? ['com.example.shop'] : <String>[],
  'rate_limit_per_minute': 6000,
  'sample_rate': 1.0,
  'created_by_email': 'owner@example.com',
  'created_at': '2026-09-01T00:00:00.000000000Z',
  'updated_at': '2026-09-01T00:00:00.000000000Z',
  'last_used_at': null,
  'revoked_at': null,
};

void main() {
  test(
    'a generated license key is shown once; an imported one has nothing',
    () async {
      var createdKey = Object();
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'POST') {
          writeJson(req, 201, {
            'license_key': licenseKey('k1'),
            'key': createdKey == 'null' ? null : 'olk_generated',
          });
          return;
        }
        writeJson(req, 200, {
          'license_keys': [licenseKey('k1')],
        });
      });
      addTearDown(server.stop);
      final c = LicenseKeysController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );
      addTearDown(c.dispose);

      await c.add('ingest');
      expect(c.createdValue, 'olk_generated');
      expect(c.createdName, 'ingest');
      expect(c.items, hasLength(1));

      // An operator-imported value: the server has nothing to reveal, which
      // is a different thing from a key that failed to be made.
      createdKey = 'null';
      await c.add('imported');
      expect(c.createdValue, isNull);
      expect(c.createdName, 'imported');
      expect(c.failure, isNull);
    },
  );

  test('an API key goes out with its role', () async {
    Map<String, Object?>? posted;
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        posted = jsonDecode(seen.body) as Map<String, Object?>;
        writeJson(req, 201, {'api_key': apiKey('a1'), 'key': 'ola_secret'});
        return;
      }
      writeJson(req, 200, {
        'api_keys': [apiKey('a1')],
      });
    });
    addTearDown(server.stop);
    final c = ApiKeysController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.add('ci', 'member');
    expect(posted, {'name': 'ci', 'role': 'member'});
    expect(c.createdValue, 'ola_secret');
  });

  test('a browser key sends the allowlist that belongs to its kind', () async {
    final bodies = <Map<String, Object?>>[];
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        bodies.add(jsonDecode(seen.body) as Map<String, Object?>);
        writeJson(req, 201, {
          'browser_key': browserKey('b1', 'browser'),
          'key': 'olb_value',
        });
        return;
      }
      writeJson(req, 200, {
        'browser_keys': [browserKey('b1', 'browser')],
      });
    });
    addTearDown(server.stop);
    final c = BrowserKeysController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.add(
      name: 'shop',
      serviceName: 'shop-web',
      kind: 'browser',
      allowlist: const ['https://shop.example.com'],
    );
    await c.add(
      name: 'app',
      serviceName: 'shop-app',
      kind: 'mobile',
      allowlist: const ['com.example.shop'],
    );

    // Exactly one allowlist goes with the kind; sending the other, or
    // both, is a 400.
    expect(bodies.first['origins'], ['https://shop.example.com']);
    expect(bodies.first.containsKey('app_ids'), isFalse);
    expect(bodies.last['app_ids'], ['com.example.shop']);
    expect(bodies.last.containsKey('origins'), isFalse);
  });

  test('the allowlist is parsed from lines or commas, blanks dropped', () {
    expect(
      parseAllowlist('https://a.example.com\n\n https://b.example.com ,'),
      ['https://a.example.com', 'https://b.example.com'],
    );
    expect(parseAllowlist('   '), isEmpty);
  });

  test('a revoked key stays in the list', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'DELETE') {
        req.response.statusCode = 204;
        return;
      }
      writeJson(req, 200, {
        'license_keys': [
          licenseKey('k1', revokedAt: '2026-10-09T10:00:00.000000000Z'),
        ],
      });
    });
    addTearDown(server.stop);
    final c = LicenseKeysController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.revoke('k1');

    // "This key existed and was revoked" is part of the answer to "who
    // could write to us", so the row stays.
    expect(c.items.single.revokedAt, isNotNull);
  });
}
