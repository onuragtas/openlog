// Cloud connections: the accounts openlog polls for managed-service
// metrics, and how the last poll of each went.
//
// Read-only here. Creating one means typing an access key and a secret on
// a phone keyboard, which is both miserable and a bad idea; the web's form
// stays the place for that, and this screen says so.
import 'api/client.dart';
import 'api/schema.g.dart';
import 'detail.dart';
import 'list_controller.dart';

class CloudConnectionsController extends ListController<CloudConnection> {
  CloudConnectionsController(this.client);

  final OpenlogClient client;

  /// False when this installation has no `OPENLOG_SECRETS_KEY`: without it
  /// no credentials can be stored, so there can be no connections and the
  /// screen says that rather than showing an empty list.
  bool secretsConfigured = true;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<List<CloudConnection>> fetch() async {
    final page = await client.cloudConnections();
    secretsConfigured = page.secretsConfigured;
    return page.connections;
  }
}

/// One connection's recent polls.
class CloudRunsController extends DetailController<CloudRunList> {
  CloudRunsController(this._client, this.id);

  final OpenlogClient _client;
  final String id;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<CloudRunList> fetch() => _client.cloudRuns(id);
}

/// How a connection as a whole is doing, as the web decides it: paused
/// when it is off, otherwise the worst of its scopes' last polls.
String cloudState(CloudConnection c) {
  if (!c.enabled) return 'paused';
  final outcomes = [
    for (final s in c.status)
      if (s.lastStatus != CloudScopeStatusLastStatus.empty) s.lastStatus,
  ];
  if (outcomes.isEmpty) return 'unknown';
  if (outcomes.contains(CloudScopeStatusLastStatus.error)) return 'error';
  if (outcomes.contains(CloudScopeStatusLastStatus.partial)) return 'partial';
  return 'ok';
}

/// The most recent poll of any scope, or null before the first one.
DateTime? cloudLastRun(List<CloudScopeStatus> status) {
  DateTime? latest;
  for (final s in status) {
    final at = s.lastRunAt;
    if (at == null) continue;
    if (latest == null || at.isAfter(latest)) latest = at;
  }
  return latest;
}

/// The first error any scope reported, so a list row can say what is wrong
/// without being opened.
String cloudFirstError(List<CloudScopeStatus> status) {
  for (final s in status) {
    if (s.lastError.isNotEmpty) return s.lastError;
  }
  return '';
}

/// Data points the last poll of every scope collected.
int cloudLastMetrics(List<CloudScopeStatus> status) =>
    status.fold(0, (n, s) => n + s.lastMetrics);

/// What one scope is called: a region on AWS, a subscription on Azure, a
/// project on GCP.
String cloudScopeKind(CloudProviderName provider) => switch (provider) {
  CloudProviderName.aws => 'region',
  CloudProviderName.azure => 'subscription',
  CloudProviderName.gcp => 'project',
  _ => 'scope',
};
