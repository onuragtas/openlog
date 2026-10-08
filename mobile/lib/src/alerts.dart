// What is firing, and taking one of them.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

/// The open-and-acknowledged list behind the alerts screen.
///
/// Separate from [SessionController] because its lifetime is the screen's, not
/// the app's: signing out should drop this, and a reload should not touch who
/// is signed in.
class AlertsController extends ChangeNotifier {
  AlertsController(this._client);

  final OpenlogClient _client;

  /// Newest first, open and acknowledged only.
  List<AlertIncident> incidents = const [];

  /// Every state's count for the organization, including the resolved ones
  /// this list deliberately does not show.
  IncidentPageCounts? counts;

  /// True only for the first load, so a pull-to-refresh does not blank the
  /// list that is already on screen.
  bool loadingFirst = false;
  bool loaded = false;
  SessionFailure? failure;

  /// The incident whose acknowledge button is busy, so only that row spins.
  String? acknowledging;

  Future<void> refresh() async {
    if (!loaded) {
      loadingFirst = true;
      notifyListeners();
    }
    try {
      final page = await _client.incidents();
      incidents = page.incidents;
      counts = page.counts;
      failure = null;
      loaded = true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('alertsForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      loadingFirst = false;
      notifyListeners();
    }
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
