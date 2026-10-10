// What one discovered integration needs to be told: an endpoint, a user,
// a password, a database.
//
// A port of `web/src/lib/integration-settings.ts`: which fields an
// integration uses, what a sensible endpoint looks like, and the body a
// save sends. The two have to agree, because the same setting is edited
// from both and the server replaces every non-secret field.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

/// The fields each integration actually uses. An integration that uses
/// none (docker, iis) is configured by the agent itself.
const configFields = <String, List<String>>{
  'nginx': ['endpoint'],
  'apache': ['endpoint'],
  'redis': ['endpoint', 'username', 'password'],
  'memcached': ['endpoint'],
  'mysql': ['endpoint', 'username', 'password'],
  'postgresql': ['endpoint', 'username', 'password', 'database'],
  'mongodb': ['endpoint', 'username', 'password', 'database'],
  'docker': <String>[],
  'mssql': ['endpoint', 'username', 'password'],
  'iis': <String>[],
  'haproxy': ['endpoint'],
  'rabbitmq': ['endpoint', 'username', 'password'],
  'elasticsearch': ['endpoint', 'username', 'password'],
  'jvm': ['endpoint', 'username', 'password'],
  'kafka': ['endpoint', 'username', 'password'],
  'php-fpm': ['endpoint'],
};

/// What an endpoint looks like for each one, as the web hints it.
const endpointPlaceholders = <String, String>{
  'nginx': 'http://127.0.0.1:8080/nginx_status',
  'apache': 'http://127.0.0.1/server-status?auto',
  'redis': '127.0.0.1:6379',
  'memcached': '127.0.0.1:11211',
  'mysql': '127.0.0.1:3306',
  'postgresql': '127.0.0.1:5432',
  'mongodb': '127.0.0.1:27017',
  'mssql': '127.0.0.1:1433',
  'haproxy': 'http://127.0.0.1:8404/stats',
  'rabbitmq': 'http://127.0.0.1:15672',
  'elasticsearch': 'http://127.0.0.1:9200',
  'jvm': 'http://127.0.0.1:9010',
  'kafka': 'http://127.0.0.1:9404',
  'php-fpm': '127.0.0.1:9000/status',
};

/// Whether this integration can be configured at all from here.
bool isConfigurable(String integration) =>
    (configFields[integration] ?? const []).isNotEmpty;

/// How far a saved change has got: the agent has to fetch it and report
/// back, which takes a sync.
enum ApplyPhase {
  /// Nothing was saved in this session, or the agent already applied it.
  idle,

  /// Saved; the host has a newer revision than the one it reported.
  sent,

  /// The agent has the revision and has not reported its status yet.
  awaitingStatus,

  /// The agent says remote configuration is off, so nothing will be
  /// applied however many times it is saved.
  disabled,
}

ApplyPhase applyPhase(IntegrationSettingsHost? host, bool saved) {
  if (host == null) return ApplyPhase.idle;
  if (host.remoteConfigDisabled) return ApplyPhase.disabled;
  if (host.appliedRevision != host.revision) return ApplyPhase.sent;
  if (saved) return ApplyPhase.awaitingStatus;
  return ApplyPhase.idle;
}

/// The settings of one host, and the saving of one of them.
class IntegrationSettingsController extends ChangeNotifier {
  IntegrationSettingsController(this.client, {required this.hostId});

  final OpenlogClient client;
  final String hostId;

  List<IntegrationSetting> items = const [];
  IntegrationSettingsHost? host;

  bool loading = false;
  bool saving = false;

  /// True once something was saved here, so the screen can say the change
  /// is on its way rather than looking like nothing happened.
  bool saved = false;

  SessionFailure? failure;

  ApplyPhase get phase => applyPhase(host, saved);

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      final page = await client.integrationSettings(hostId: hostId);
      items = page.items;
      host = page.host;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('sectionForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  /// The setting of one discovered instance on this host: the web matches
  /// it the same way -- the instance, and no port, container or endpoint
  /// match, because those are the rules somebody wrote by hand.
  IntegrationSetting? settingFor(String integration, String instance) {
    for (final s in items) {
      if (s.hostId == hostId &&
          s.integration.wire == integration &&
          s.match.instance == instance &&
          s.match.port == null &&
          s.match.container.isEmpty &&
          s.match.endpoint.isEmpty) {
        return s;
      }
    }
    return null;
  }

  /// Saves one instance's settings: a PUT when it exists, a POST when it
  /// does not.
  Future<bool> save({
    required String integration,
    required String instance,
    required bool enabled,
    String endpoint = '',
    String username = '',
    String database = '',
    String? password,
    bool clearPassword = false,
  }) async {
    final existing = settingFor(integration, instance);
    final fields = configFields[integration] ?? const [];
    final body = <String, Object?>{
      'host_id': hostId,
      'integration': integration,
      'match': {'instance': instance},
      'enabled': enabled,
      if (fields.contains('endpoint')) 'endpoint': endpoint.trim(),
      if (fields.contains('username')) 'username': username.trim(),
      if (fields.contains('database')) ...{
        'database': database.trim(),
        // The list of databases is the web's field; this screen does not
        // edit it, so it goes back as it was rather than emptied.
        'databases': existing?.databases ?? const <String>[],
      },
      // Write-only, as the contract has it: "" clears, null keeps, and a
      // value replaces.
      if (fields.contains('password'))
        'password': clearPassword
            ? ''
            : (password?.isEmpty ?? true)
            ? null
            : password,
    };

    saving = true;
    failure = null;
    notifyListeners();
    try {
      if (existing == null) {
        await client.createIntegrationSetting(body);
      } else {
        await client.updateIntegrationSetting(existing.id, body);
      }
      saved = true;
      return true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
      return false;
    } on ApiException catch (e) {
      failure = switch (e.status) {
        403 => const SessionFailure('fleetForbidden', ''),
        409 => SessionFailure('integrationConflict', e.message),
        _ => SessionFailure('unexpected', e.message),
      };
      return false;
    } finally {
      saving = false;
      notifyListeners();
      if (failure == null) await load();
    }
  }
}
