// Cloud connections: how a connection's state is read.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/cloud.dart';

import 'fake_server.dart';

Map<String, Object?> scope(
  String name, {
  String status = 'ok',
  String error = '',
  int metrics = 120,
  String? lastRun = '2026-10-10T08:55:00.000000000Z',
  int consecutiveErrors = 0,
}) => {
  'scope': name,
  'next_run_at': '2026-10-10T09:05:00.000000000Z',
  'last_run_at': lastRun,
  'last_status': status,
  'last_error': error,
  'last_metrics': metrics,
  'last_api_calls': 40,
  'last_duration_ms': 1800.0,
  'consecutive_errors': consecutiveErrors,
};

Map<String, Object?> connection({
  bool enabled = true,
  List<Map<String, Object?>>? status,
}) => {
  'id': '11111111-1111-4111-8111-111111111111',
  'name': 'prod aws',
  'provider': 'aws',
  'ingest_mode': 'poll',
  'enabled': enabled,
  'scopes': ['eu-central-1', 'us-east-1'],
  'services': ['rds', 's3'],
  'poll_interval_seconds': 300,
  'max_metrics_per_poll': 5000,
  'max_api_calls_per_poll': 200,
  'credentials_set': true,
  'credentials_key_id': 'k1',
  'created_by_email': 'owner@example.com',
  'updated_by_email': 'owner@example.com',
  'created_at': '2026-10-01T09:00:00.000000000Z',
  'updated_at': '2026-10-01T09:00:00.000000000Z',
  'status': status ?? [scope('eu-central-1')],
};

CloudConnection read(Map<String, Object?> json) =>
    CloudConnection.fromJson(json);

void main() {
  group('the state of a connection', () {
    test('off is paused, whatever its last polls said', () {
      expect(read(connection(enabled: false)).let(cloudState), 'paused');
    });

    test('before the first poll there is nothing to report', () {
      final c = read(
        connection(status: [scope('eu-central-1', status: '', lastRun: null)]),
      );
      expect(cloudState(c), 'unknown');
      expect(cloudLastRun(c.status), isNull);
    });

    test('the worst scope decides, and its error is the one shown', () {
      final c = read(
        connection(
          status: [
            scope('eu-central-1'),
            scope('us-east-1', status: 'partial'),
            scope('eu-west-1', status: 'error', error: 'AccessDenied'),
          ],
        ),
      );
      expect(cloudState(c), 'error');
      expect(cloudFirstError(c.status), 'AccessDenied');
      // Every scope's last poll counts towards what was collected.
      expect(cloudLastMetrics(c.status), 360);
    });

    test('partial is not an error, and not ok either', () {
      final c = read(
        connection(status: [scope('eu-central-1', status: 'partial')]),
      );
      expect(cloudState(c), 'partial');
    });

    test('the latest poll of any scope is the connection\'s', () {
      final c = read(
        connection(
          status: [
            scope('a', lastRun: '2026-10-10T07:00:00.000000000Z'),
            scope('b', lastRun: '2026-10-10T08:55:00.000000000Z'),
            scope('c', lastRun: null),
          ],
        ),
      );
      expect(cloudLastRun(c.status), DateTime.utc(2026, 10, 10, 8, 55));
    });

    test('a scope is called what its provider calls it', () {
      expect(cloudScopeKind(CloudProviderName.aws), 'region');
      expect(cloudScopeKind(CloudProviderName.azure), 'subscription');
      expect(cloudScopeKind(CloudProviderName.gcp), 'project');
      expect(cloudScopeKind(CloudProviderName.unknown), 'scope');
    });
  });

  test('an installation that cannot keep secrets says so', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(req, 200, {
        'connections': <Object>[],
        'secrets_configured': false,
        'test_supported': true,
      }),
    );
    addTearDown(server.stop);
    final c = CloudConnectionsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    // No key, no stored credentials: an empty list here is a different
    // thing from "nobody has connected an account yet".
    expect(c.secretsConfigured, isFalse);
    expect(c.items, isEmpty);
  });
}

extension<T> on T {
  R let<R>(R Function(T) f) => f(this);
}
