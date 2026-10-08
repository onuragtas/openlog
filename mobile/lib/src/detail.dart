// One record, loaded because someone tapped a row.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

/// A single record behind a detail screen.
///
/// `ListController`'s sibling, and it differs where detail screens differ. This
/// screen was reached by a tap, so a 404 is its own failure rather than an empty
/// page: the row that was tapped can describe something the server has since
/// purged, and "it is gone" is the one thing the person needs to hear.
abstract class DetailController<T> extends ChangeNotifier {
  T? value;

  /// True only for the first load, so a pull-to-refresh keeps what is on screen.
  bool loadingFirst = false;
  bool loaded = false;
  SessionFailure? failure;

  Future<T> fetch();

  /// What a 403 means here, named so the person knows it is a permission and
  /// not an outage.
  String get forbiddenKind;

  Future<void> refresh() async {
    if (!loaded) {
      loadingFirst = true;
      notifyListeners();
    }
    try {
      value = await fetch();
      failure = null;
      loaded = true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = switch (e.status) {
        403 => SessionFailure(forbiddenKind, ''),
        404 => const SessionFailure('detailGone', ''),
        _ => SessionFailure('unexpected', e.message),
      };
    } finally {
      loadingFirst = false;
      notifyListeners();
    }
  }
}

/// One incident: its timeline, what was delivered, and what an on-call person
/// can do about it from a phone.
class IncidentController extends DetailController<AlertIncidentDetail> {
  IncidentController(this._client, this.id);

  final OpenlogClient _client;
  final String id;

  /// Which action is in flight, so the screen disables only that button.
  String? busy;

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  Future<AlertIncidentDetail> fetch() => _client.incident(id);

  /// The service this incident is about, when the rule was about a service.
  ///
  /// APM conditions label their series with `service.name`
  /// (internal/alert/cond_apm.go), and that label is the whole reason this app
  /// can get from "what is firing" to "what is wrong" in one tap.
  String? get serviceName {
    final name = value?.labels['service.name'];
    return name == null || name.isEmpty ? null : name;
  }

  /// Everything except the `alert.` bookkeeping the rule engine adds, which is
  /// what the web shows too -- the person wants the labels they chose.
  Map<String, String> get labels {
    final all = value?.labels ?? const <String, String>{};
    return {
      for (final e in all.entries)
        if (!e.key.startsWith('alert.')) e.key: e.value,
    };
  }

  Future<void> acknowledge() =>
      _act('acknowledge', () => _client.acknowledgeIncident(id));

  Future<void> resolve({String note = ''}) =>
      _act('resolve', () => _client.resolveIncident(id, note: note));

  Future<void> addNote(String text) =>
      _act('note', () => _client.addIncidentNote(id, text));

  /// Runs [action] then reloads, so the timeline shows what the server did
  /// rather than this app's guess at it.
  Future<void> _act(String which, Future<void> Function() action) async {
    busy = which;
    failure = null;
    notifyListeners();
    try {
      await action();
      await refresh();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      // 409 is the race worth naming: it resolved between this screen being
      // drawn and the button being pressed. Reload first and report after,
      // because a successful refresh() clears `failure` and would throw the
      // message away, leaving a screen that silently changed under them.
      final reported = e.status == 409
          ? const SessionFailure('alreadyResolved', '')
          : SessionFailure('unexpected', e.message);
      if (e.status == 409) await refresh();
      failure = reported;
    } finally {
      busy = null;
      notifyListeners();
    }
  }
}

/// One service's golden signals: the numbers that say whether the alert is
/// still true, and the series that says how it got there.
class ServiceOverviewController extends DetailController<ApmOverview> {
  ServiceOverviewController(this._client, this.serviceName);

  final OpenlogClient _client;
  final String serviceName;

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<ApmOverview> fetch() => _client.serviceOverview(serviceName);
}
