// Dashboards, read-only.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'list_controller.dart';
import 'session.dart';

class DashboardsController extends ListController<DashboardSummary> {
  DashboardsController(this._client);

  final OpenlogClient _client;

  String query = '';

  @override
  String get forbiddenKind => 'dashboardsForbidden';

  @override
  Future<List<DashboardSummary>> fetch() async =>
      (await _client.dashboards(q: query.trim())).dashboards;
}

/// One dashboard: its widgets, and what each widget's query answered.
///
/// A widget holds a query, not data, so opening a dashboard is one request for
/// the layout and then one per widget. They run one after another rather than
/// all at once: a dashboard can hold twenty, and twenty concurrent queries from
/// a phone is a load spike the person did not ask for.
class DashboardViewController extends ChangeNotifier {
  DashboardViewController(this._client, this.id);

  final OpenlogClient _client;
  final String id;

  Dashboard? dashboard;

  /// Widget id to its answer, filled in as each query returns, so the screen
  /// shows the first widget while the last is still running.
  final Map<String, OqlResult> results = {};

  /// Widget id to why its query did not answer. One widget failing is not the
  /// dashboard failing: the other nineteen are still worth showing.
  final Map<String, String> widgetErrors = {};

  bool loading = false;
  SessionFailure? failure;

  List<DashboardWidget> get widgets => [
    for (final page in dashboard?.pages ?? const <DashboardPage>[])
      ...page.widgets,
  ];

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      dashboard = await _client.dashboard(id);
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
      loading = false;
      notifyListeners();
      return;
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('dashboardsForbidden', '')
          : SessionFailure('unexpected', e.message);
      loading = false;
      notifyListeners();
      return;
    }
    notifyListeners();

    for (final widget in widgets) {
      if (widget.query.trim().isEmpty) continue;
      try {
        results[widget.id] = await _client.runQuery(widget.query);
      } on ApiUnreachable {
        widgetErrors[widget.id] = '';
      } on ApiException catch (e) {
        widgetErrors[widget.id] = e.message;
      }
      notifyListeners();
    }
    loading = false;
    notifyListeners();
  }
}

/// The first numeric value of a `single` result, which is what a billboard is.
double? singleValue(OqlResult result) {
  if (result.rows.isEmpty || result.rows.first.values.isEmpty) return null;
  final v = result.rows.first.values.first;
  return v is num ? v.toDouble() : null;
}

/// `[facet, value]` pairs of a faceted result, largest first.
///
/// A phone shows this as a ranked list rather than as the pie or bar the
/// dashboard asked for: at this width a ranked list is both readable and more
/// precise, and a five-slice pie on a 390-point screen is neither.
List<(String, double)> facetRows(OqlResult result) {
  final out = <(String, double)>[];
  for (final row in result.rows) {
    if (row.values.isEmpty) continue;
    final v = row.values.first;
    if (v is! num) continue;
    out.add((row.facets.isEmpty ? '' : row.facets.join(' · '), v.toDouble()));
  }
  out.sort((a, b) => b.$2.compareTo(a.$2));
  return out;
}

/// The values of the first series, for a sparkline.
///
/// A null bucket is dropped, which joins its neighbours with a straight
/// segment. That is not free of invention either -- it claims a continuity the
/// data does not have -- but it is a smaller lie than plotting zero, which
/// would draw a cliff where there was simply no measurement. At 48 points tall
/// and with no axis, the line is read as a trend, and a trend survives a
/// bridged gap better than it survives a false dip.
List<double> seriesValues(OqlResult result) {
  if (result.series.isEmpty) return const [];
  return [
    for (final point in result.series.first.points)
      if (point.length > 1 && point[1] != null) point[1]!,
  ];
}
