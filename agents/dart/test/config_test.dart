import 'package:openlog/src/config.dart';
import 'package:test/test.dart';

OpenlogOptions valid({String? key, String? endpoint, String? appId, double? sampleRate}) => OpenlogOptions(
      key: key ?? 'olb_1a2b3c4d5e6f708192a3b4c5d6e7f809',
      endpoint: endpoint ?? 'https://ingest.example.com:4318',
      appId: appId ?? 'com.example.shop',
      sampleRate: sampleRate ?? 1,
    );

void main() {
  test('derives the two URLs once, without a trailing slash', () {
    final c = resolveConfig(valid(endpoint: 'https://ingest.example.com:4318///'));
    expect(c.rumUrl, 'https://ingest.example.com:4318/v1/rum');
    expect(c.configUrl, 'https://ingest.example.com:4318/v1/rum/config');
  });

  test('refuses options the server would refuse', () {
    // Thrown rather than logged: an SDK that silently does nothing is found out weeks later.
    expect(() => resolveConfig(valid(key: 'olk_an_ingest_license_key')), throwsA(isA<ConfigError>()));
    expect(() => resolveConfig(valid(endpoint: 'ingest.example.com')), throwsA(isA<ConfigError>()));
    expect(() => resolveConfig(valid(appId: '   ')), throwsA(isA<ConfigError>()));
    expect(() => resolveConfig(valid(sampleRate: 0)), throwsA(isA<ConfigError>()));
    expect(() => resolveConfig(valid(sampleRate: 1.5)), throwsA(isA<ConfigError>()));
  });

  test('an application id is required, because a blank one matches no allowlist', () {
    expect(() => resolveConfig(valid(appId: '')), throwsA(isA<ConfigError>()));
  });
}
