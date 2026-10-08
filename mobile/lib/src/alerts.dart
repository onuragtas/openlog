// What is firing, and taking one of them.
import 'api/client.dart';
import 'api/schema.g.dart';
import 'list_controller.dart';
import 'session.dart';

/// The open-and-acknowledged list behind the alerts screen.
///
/// Separate from [SessionController] because its lifetime is the screen's, not
/// the app's: signing out should drop this, and a reload should not touch who
/// is signed in.
class AlertsController extends ListController<AlertIncident> {
  AlertsController(this._client);

  final OpenlogClient _client;

  /// Newest first, open and acknowledged only.
  List<AlertIncident> get incidents => items;

  /// Every state's count for the organization, including the resolved ones
  /// this list deliberately does not show.
  IncidentPageCounts? counts;

  /// The incident whose acknowledge button is busy, so only that row spins.
  String? acknowledging;

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  Future<List<AlertIncident>> fetch() async {
    final page = await _client.incidents();
    counts = page.counts;
    return page.incidents;
  }

  /// Takes [id], then reloads so the row shows who took it rather than this
  /// app's guess at what the server did.
  Future<void> acknowledge(String id) async {
    acknowledging = id;
    failure = null;
    notifyListeners();
    try {
      await _client.acknowledgeIncident(id);
      await refresh();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      // 409 is the race worth naming: it resolved between the list being drawn
      // and the button being pressed, so the person acted on something that is
      // no longer true and should be told, not quietly left with a changed row.
      final reported = e.status == 409
          ? const SessionFailure('alreadyResolved', '')
          : SessionFailure('unexpected', e.message);
      // Reload first and report after: refresh() clears `failure` when it
      // succeeds, so setting it before the reload threw the message away and
      // left the person with a row that silently changed under them.
      if (e.status == 409) {
        await refresh();
      }
      failure = reported;
    } finally {
      acknowledging = null;
      notifyListeners();
    }
  }
}
