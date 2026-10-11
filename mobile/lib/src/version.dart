// What this installation is running, and what the updater is doing about it.
//
// The same screen the web has in Settings: the running version, the release
// available, the updater's state with its notices and steps, the maintenance
// window, and the two requests an operator can make -- check now, update now.
//
// It is here because the answer to "why is my server still on the old
// version" is a thing somebody asks away from their desk.
import 'api/client.dart';
import 'api/schema.g.dart';
import 'detail.dart';
import 'session.dart';

class VersionController extends DetailController<VersionInfo> {
  VersionController(this._client);

  final OpenlogClient _client;

  /// True while a check or an apply this screen asked for is in flight.
  bool busy = false;

  /// What a refused request said, kept apart from the screen's own failure:
  /// the page is still readable when "update now" was refused.
  SessionFailure? requestFailure;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<VersionInfo> fetch() => _client.version();

  /// Runs the release check now. The answer is the fresh version info, so
  /// the screen redraws from the result rather than asking again.
  Future<void> checkNow() => _request(() => _client.checkForUpdate());

  /// Asks the updater to install [targetVersion].
  ///
  /// The reply is the queued request, not the result: the installation takes
  /// minutes and is followed in `updater.state`, so the screen reloads the
  /// version info after queuing rather than pretending to be done.
  Future<void> applyNow(
    String targetVersion, {
    bool ignoreMaintenanceWindow = false,
  }) => _request(() async {
    await _client.applyUpdate(
      targetVersion,
      ignoreMaintenanceWindow: ignoreMaintenanceWindow,
    );
    return _client.version();
  });

  Future<void> _request(Future<VersionInfo> Function() send) async {
    busy = true;
    requestFailure = null;
    notifyListeners();
    try {
      value = await send();
      loaded = true;
    } on ApiUnreachable {
      requestFailure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      requestFailure = switch (e.status) {
        403 => const SessionFailure('updateForbidden', ''),
        // 429 and 409 both mean "not now, and here is why": the server's own
        // sentence says which, and it is more use than a code.
        _ => SessionFailure('unexpected', e.message),
      };
    } finally {
      busy = false;
      notifyListeners();
    }
  }
}

/// Whether this caller may ask for a check or an update at all. False for an
/// API key, a viewer, and a static-auth installation -- where the buttons are
/// not shown rather than shown and refused.
bool canRequestUpdates(VersionInfo? v) =>
    v?.updateRequests?.canRequest ?? false;

/// The release worth offering, or empty: the newest available one, and only
/// while an updater that can install it reports.
String updateTarget(VersionInfo? v) {
  final latest = v?.latestAvailable?.version ?? '';
  if (latest.isEmpty || v?.updater == null) return '';
  return v!.updater!.mode == UpdaterStatusMode.off ? '' : latest;
}
