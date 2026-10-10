// The explorer views somebody kept, shared with the web.
//
// A view is a name over an explorer's state, and the state is the web's own
// JSON: `filters`, `groups`, `q` and the browser table's own keys. The same
// object is read and written here, so a view saved on this phone opens in a
// browser and a view saved in a browser opens here.
//
// Two rules keep that honest. What this app cannot show it keeps rather than
// drops -- which columns the table had, how it was sorted, what time range it
// was looking at -- so saving over a view from a phone does not quietly undo
// the part somebody set in a browser. What it cannot show but that *changes
// which rows are listed* -- OR groups, spans below the root -- is said out
// loud when such a view is applied, because a list that silently answers a
// different question than the view's name is worse than no view at all.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'fields.dart';
import 'session.dart';

/// The views of one signal: the organization's, and this account's own.
class SavedViewsController extends ChangeNotifier {
  SavedViewsController(this.client, {required this.signal});

  final OpenlogClient client;

  /// `logs` or `traces`. Metrics have views too, but a metrics view is a set
  /// of queries and a formula, and this app's metrics screen is a list of
  /// metric names -- there is nothing here to put in one yet.
  final String signal;

  List<SavedView> views = const [];

  /// The view whose state is on screen, so the button can say its name.
  String? activeId;

  bool loading = false;

  /// True while a create, an overwrite or a delete is in flight, so the
  /// sheet can disable its buttons rather than let two run at once.
  bool saving = false;

  /// True when this installation keeps no views at all: the endpoints only
  /// exist in PostgreSQL auth mode and the path 404s otherwise. The button
  /// then hides, exactly as the web's does -- "no views" and "no such
  /// feature" are different answers and must not look the same.
  bool unavailable = false;

  bool loaded = false;

  SessionFailure? failure;

  /// Says which view is on screen now, so the button can name it.
  void activate(String? id) {
    if (activeId == id) return;
    activeId = id;
    notifyListeners();
  }

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      views = (await client.savedViews(signal)).views;
      unavailable = false;
      loaded = true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      if (e.status == 404) {
        unavailable = true;
        views = const [];
        loaded = true;
      } else {
        failure = e.status == 403
            ? const SessionFailure('sectionForbidden', '')
            : SessionFailure('unexpected', e.message);
      }
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  /// Keeps the state on screen under a new name. Returns the new view, or
  /// null when the server refused.
  Future<SavedView?> create({
    required String name,
    required String visibility,
    required Map<String, Object?> state,
  }) => _write(
    () => client.createSavedView(
      signal: signal,
      name: name,
      visibility: visibility,
      state: state,
    ),
  );

  /// Writes the state on screen over [view], keeping its name, its
  /// description and who may see it.
  Future<SavedView?> overwrite(SavedView view, Map<String, Object?> state) =>
      _write(
        () => client.updateSavedView(
          view.id,
          signal: signal,
          name: view.name,
          visibility: view.visibility.wire,
          description: view.description,
          state: state,
        ),
      );

  Future<bool> remove(String id) async {
    saving = true;
    failure = null;
    notifyListeners();
    try {
      await client.deleteSavedView(id);
      if (activeId == id) activeId = null;
      return true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
      return false;
    } on ApiException catch (e) {
      failure = _writeFailure(e);
      return false;
    } finally {
      saving = false;
      notifyListeners();
      if (failure == null) await load();
    }
  }

  /// Sends a write and asks for the list again, which is what the web does
  /// too.
  ///
  /// The order is the server's -- `ORDER BY lower(name)` under its own
  /// collation -- and sorting the list here instead would put a view in a
  /// different place than the browser shows it, because Dart compares
  /// `Ö` against `Z` by code unit and PostgreSQL does not.
  Future<SavedView?> _write(Future<SavedView> Function() send) async {
    saving = true;
    failure = null;
    notifyListeners();
    SavedView? saved;
    try {
      saved = await send();
      activeId = saved.id;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _writeFailure(e);
    } finally {
      saving = false;
      notifyListeners();
      if (saved != null) await load();
    }
    return saved;
  }

  /// A refused write, by what the server actually refused.
  ///
  /// 403 is two different things here -- a viewer who may not write at all,
  /// and a member editing somebody else's org-wide view -- and the server
  /// says which in its message, so that message is what gets shown.
  SessionFailure _writeFailure(ApiException e) => switch (e.status) {
    409 => const SessionFailure('viewsFull', ''),
    404 => const SessionFailure('viewGone', ''),
    _ => SessionFailure('unexpected', e.message),
  };
}

/// What a saved view says, as far as a phone can show it.
class ViewState {
  const ViewState({
    this.filters = const [],
    this.query = '',
    this.severityMin = '',
    this.slowest = false,
    this.hiddenGroups = 0,
    this.allSpans = false,
  });

  /// Read out of a view's `state`, which is somebody else's JSON.
  factory ViewState.of(Map<String, Object?> state, {required String signal}) {
    // `f`/`g` before `filters`/`groups`: the web's own decoder reads
    // either, because a view's state is untrusted JSON that may have been
    // written by another build, by hand or by a copied URL. Reading only
    // the long form made such a view apply as nothing at all.
    final filters = filtersFromJson(state['f'] ?? state['filters']);
    final groups = state['g'] ?? state['groups'];
    final q = state['q'];
    if (signal == 'traces') {
      return ViewState(
        filters: filters,
        slowest: state['sort'] == 'duration',
        hiddenGroups: groups is List ? groups.length : 0,
        // The web can list spans that are not the root of their trace; this
        // app always asks for root spans only.
        allSpans: state['root_only'] == false,
      );
    }
    // A logs view with no severity of its own is every severity: this app
    // starts at WARN, but a view says what it says.
    final severity = _severityOf(filters);
    return ViewState(
      filters: severity.rest,
      query: q is String ? q : '',
      severityMin: severity.min,
      hiddenGroups: groups is List ? groups.length : 0,
    );
  }

  /// The AND-ed conditions, as chips.
  final List<Filter> filters;

  /// The body search of a logs view. Traces views keep no search text: the
  /// web drops it there too, because a span has no body to search.
  final String query;

  /// `INFO`, `WARN`, `ERROR` or empty for every severity (logs).
  final String severityMin;

  /// Slowest instead of newest (traces).
  final bool slowest;

  /// How many OR groups the view has that this app cannot show. Anything
  /// above zero means the phone's list answers a narrower question than the
  /// view does, and the screen says so.
  final int hiddenGroups;

  /// The view wanted every span, not just the root of each trace.
  final bool allSpans;

  /// True when applying this view here shows something other than what it
  /// was saved as.
  bool get partial => hiddenGroups > 0 || allSpans;

  /// True when the view carries nothing this screen can act on: no
  /// conditions, no search, no severity, no sort. Applying it is a
  /// no-op, and a screen that looks unchanged has to say why.
  bool get empty =>
      filters.isEmpty && query.isEmpty && severityMin.isEmpty && !slowest;
}

/// `severity_number >= 13` is how this app's severity button reaches the
/// server, and it is a condition like any other in a saved view -- so a view
/// made here reads as a severity filter in a browser, and a severity filter
/// made in a browser moves the button here.
const _severityNumbers = {'INFO': 9, 'WARN': 13, 'ERROR': 17};

({String min, List<Filter> rest}) _severityOf(List<Filter> filters) {
  for (var i = 0; i < filters.length; i++) {
    final f = filters[i];
    if (f.key != 'severity_number' || f.op != '>=' || f.values.length != 1) {
      continue;
    }
    for (final e in _severityNumbers.entries) {
      if (f.values.first != '${e.value}') continue;
      return (min: e.key, rest: [...filters]..removeAt(i));
    }
  }
  return (min: '', rest: filters);
}

/// The state to save for a logs explorer, in the web's shape.
///
/// [keep] is the state of the view being written over, so its columns, its
/// sort order and its time range survive a phone overwriting it.
Map<String, Object?> logsViewState({
  required List<Filter> filters,
  required String query,
  required String severityMin,
  String service = '',
  Map<String, Object?> keep = const {},
}) => {
  ..._kept(keep),
  'filters': [
    for (final f in filters) f.toJson(),
    // The service box and the severity button are this app's own controls,
    // and both are plain conditions underneath. Writing them as conditions
    // is what makes the browser show the same rows for the same view.
    if (service.trim().isNotEmpty)
      Filter(key: 'service.name', op: '=', values: [service.trim()]).toJson(),
    if (_severityNumbers[severityMin] != null)
      Filter(
        key: 'severity_number',
        op: '>=',
        values: ['${_severityNumbers[severityMin]}'],
      ).toJson(),
  ],
  // Written empty rather than left out: this screen has no OR groups, so a
  // view saved from it has none, and keeping the old ones would save a
  // question nobody asked.
  'groups': const <Object?>[],
  'q': query.trim(),
};

/// The state to save for a traces explorer, in the web's shape.
Map<String, Object?> tracesViewState({
  required List<Filter> filters,
  required String query,
  required bool slowest,
  Map<String, Object?> keep = const {},
}) => {
  ..._kept(keep),
  'filters': [
    for (final f in filters) f.toJson(),
    // The traces search box is a contains over the service name, which is
    // how this app sends it; the web's traces view keeps no `q` at all.
    if (query.trim().isNotEmpty)
      Filter(
        key: 'service_name',
        op: 'contains',
        values: [query.trim()],
      ).toJson(),
  ],
  'groups': const <Object?>[],
  'sort': slowest ? 'duration' : 'timestamp',
  // What this app asked for, not what the old view asked for: it lists the
  // root span of each trace.
  'root_only': true,
};

/// The parts of a view's state this app does not understand, kept as they
/// were found. The parts it does understand are written fresh.
Map<String, Object?> _kept(Map<String, Object?> state) => {
  for (final e in state.entries)
    if (!_ownKeys.contains(e.key)) e.key: e.value,
};

const _ownKeys = {'filters', 'groups', 'q', 'sort', 'root_only'};
