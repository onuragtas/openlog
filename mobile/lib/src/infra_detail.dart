// What a list row opens: one SLO, one job monitor, one advisory, the parts of
// a host that are their own screens, one host's bill and one alert rule.
//
// Read-only: writing these stays on the web (parity-exceptions.txt). The rules
// here are the web's, ported from `web/src/lib/slo.ts`, `jobs.ts`,
// `vulnerabilities.ts`, `inventory.ts` and `alert-history.ts`, because what
// counts as "budget low" or "late" must not depend on which console is asking.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'detail.dart';
import 'list_controller.dart';

// ---- SLOs ----

enum BudgetLevel { healthy, warning, exhausted, none }

/// More than a quarter left is healthy, anything above zero is a warning, and
/// an exhausted or negative budget means the objective is missed. Without
/// requests there is no level.
BudgetLevel budgetLevel(SloBudget? budget) {
  final remaining = budget?.remainingRatio;
  if (remaining == null || !remaining.isFinite) return BudgetLevel.none;
  if (remaining > 0.25) return BudgetLevel.healthy;
  if (remaining > 0) return BudgetLevel.warning;
  return BudgetLevel.exhausted;
}

/// [v] with at most [digits] decimals and no trailing zeros, the way the web's
/// number formatting writes it.
String _trimmed(double v, int digits) {
  var s = v.toStringAsFixed(digits);
  if (s.contains('.')) {
    s = s.replaceFirst(RegExp(r'0+$'), '').replaceFirst(RegExp(r'\.$'), '');
  }
  return s;
}

/// A 0..1 ratio as a percentage. Budgets need more digits than a rounded
/// percent: 99.9% is not 100%.
String formatRatio(double? v, {int digits = 2}) =>
    v == null || !v.isFinite ? '–' : '${_trimmed(v * 100, digits)}%';

/// A burn rate as a multiple; "–" without requests.
String formatBurnRate(double? v) =>
    v == null || !v.isFinite ? '–' : '${_trimmed(v, v < 10 ? 2 : 1)}×';

/// Weighted counts are estimates of unsampled traffic, so they are rounded.
String formatRequests(double? v) =>
    v == null || !v.isFinite ? '–' : v.toStringAsFixed(0);

String formatObjective(double objective) => '${_trimmed(objective, 3)}%';

/// "checkout · shop (prod)": namespace and environment only when fixed.
String sloTarget({
  required String serviceName,
  String? serviceNamespace,
  String? environment,
}) {
  final name = [
    serviceName,
    if (serviceNamespace != null && serviceNamespace.isNotEmpty)
      serviceNamespace,
  ].join(' · ');
  return environment != null && environment.isNotEmpty
      ? '$name ($environment)'
      : name;
}

/// The share of the window's budget left after each bucket. A bucket without a
/// value repeats the last known one, so the line has no holes, and the scale
/// stops a quarter below exhausted. Empty when no bucket has a value at all.
List<double> burndownValues(List<SloPoint> points) {
  if (!points.any((p) => p.remainingRatio != null)) return const [];
  var last = 1.0;
  return [
    for (final p in points)
      () {
        last = p.remainingRatio ?? last;
        return last.clamp(-0.25, 1.0);
      }(),
  ];
}

/// One SLO. `/results` carries the SLO itself, as the web reads it.
class SloDetailController extends DetailController<SloResults> {
  SloDetailController(this._client, this.id);

  final OpenlogClient _client;
  final String id;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  bool get endpointMayBeMissing => true;

  @override
  Future<SloResults> fetch() => _client.sloResults(id);
}

// ---- Job monitors ----

enum JobState { ok, late, failing, running, paused, unknown }

/// A disabled monitor is paused whatever its history; one past its grace period
/// is late even before the sweeper concludes it, because that is what the
/// person needs to see; otherwise the last concluded run decides.
JobState jobState(JobMonitor m) {
  if (!m.enabled) return JobState.paused;
  if (m.state.late) return JobState.late;
  return switch (m.state.status) {
    'success' => JobState.ok,
    'failure' || 'missed' || 'overrun' => JobState.failing,
    'running' => JobState.running,
    _ => JobState.unknown,
  };
}

/// A duration in seconds as the largest whole unit that fits.
String formatSeconds(num seconds) {
  if (seconds <= 0) return '–';
  const units = [('d', 86400), ('h', 3600), ('m', 60)];
  for (final (suffix, size) in units) {
    if (seconds % size == 0 && seconds >= size) {
      return '${seconds ~/ size}$suffix';
    }
  }
  return '${seconds}s';
}

/// A run openlog saw both ends of. Zero means it did not, so it reads "–".
String formatJobDuration(double? ms) {
  if (ms == null || !ms.isFinite || ms <= 0) return '–';
  if (ms < 1000) return '${ms.round()} ms';
  final seconds = ms / 1000;
  if (seconds < 60) return '${_trimmed(seconds, 1)} s';
  final minutes = seconds ~/ 60;
  final rest = (seconds % 60).round();
  return minutes < 60
      ? '${minutes}m ${rest}s'
      : '${minutes ~/ 60}h ${minutes % 60}m';
}

/// How late a run was, signed.
String formatLate(double? seconds) {
  if (seconds == null || !seconds.isFinite) return '–';
  final rounded = seconds.round();
  if (rounded == 0) return '0s';
  return '${rounded > 0 ? '+' : '−'}${formatSeconds(rounded.abs())}';
}

/// The monitor, its concluded runs and their summary, in one answer.
class JobDetailController extends DetailController<JobRunPage> {
  JobDetailController(this._client, this.id);

  final OpenlogClient _client;
  final String id;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  bool get endpointMayBeMissing => true;

  @override
  Future<JobRunPage> fetch() => _client.jobRuns(id);
}

// ---- Vulnerabilities ----

/// "1.2.3-4 → 1.2.3-5", or the installed version alone when no fix is known.
String upgradeText(VulnFinding f) =>
    f.fixedIn.isEmpty ? f.version : '${f.version} → ${f.fixedIn}';

/// "–" when the feed carries no score: zero is not a score of zero.
String formatScore(double? score) =>
    score == null || !score.isFinite || score <= 0
    ? '–'
    : score.toStringAsFixed(1);

/// The advisory's own id and the CVE, without repeating one that is both.
String advisoryLabel(String vulnId, String cve) =>
    cve.isEmpty || cve == vulnId ? vulnId : '$vulnId · $cve';

/// The severities in the web's order, most serious first.
const vulnSeverities = ['critical', 'high', 'medium', 'low', 'none'];

/// A findings list on a screen of its own, for an endpoint older servers have
/// not got. [notOffered] then says so instead of a failure banner.
abstract class FindingsController extends PagedListController<VulnFinding> {
  FindingsController({required Set<String> ascending})
    : super(sort: 'severity', dir: 'asc', ascendingSorts: ascending);

  bool notOffered = false;

  @override
  String get forbiddenKind => 'sectionForbidden';

  Future<ListPage<VulnFinding>> read(int offset);

  @override
  Future<ListPage<VulnFinding>> fetchPage(int offset) async {
    try {
      final page = await read(offset);
      notOffered = false;
      return page;
    } on ApiException catch (e) {
      if (!e.noSuchEndpoint) rethrow;
      notOffered = true;
      return const ListPage([]);
    }
  }
}

/// One advisory: the hosts it was found on and what upgrading each means.
class VulnerabilityController extends FindingsController {
  VulnerabilityController(this._client, this.id)
    : super(ascending: const {'host', 'package', 'version', 'ecosystem'});

  final OpenlogClient _client;
  final String id;

  /// About the advisory rather than one page of it. A server older than this
  /// field leaves it null, and the rows are all there is to go by.
  VulnAdvisorySummary? advisory;

  VulnFinding? get _first => items.isEmpty ? null : items.first;

  VulnSeverity? get severity => advisory?.severity ?? _first?.severity;
  double? get score => advisory?.score ?? _first?.score;
  String get cve => advisory?.cve ?? _first?.cve ?? '';
  String get summary => advisory?.summary ?? _first?.summary ?? '';

  /// How many hosts run a version with no fix yet, which is the case a person
  /// cannot act on by upgrading.
  int get withoutFix =>
      advisory?.withoutFix ?? items.where((f) => f.fixedIn.isEmpty).length;

  @override
  Future<ListPage<VulnFinding>> read(int offset) async {
    final page = await _client.vulnerability(
      id,
      sort: sort,
      dir: dir,
      limit: limit,
      offset: offset,
    );
    advisory = page.advisory ?? advisory;
    return ListPage(page.findings, hasMore: page.hasMore ?? false);
  }
}

/// The open findings of one host.
class HostVulnerabilitiesController extends FindingsController {
  HostVulnerabilitiesController(this._client, this.hostId)
    : super(ascending: const {'advisory', 'severity', 'package', 'version'});

  final OpenlogClient _client;
  final String hostId;

  @override
  Future<ListPage<VulnFinding>> read(int offset) async {
    final page = await _client.hostVulnerabilities(
      hostId,
      sort: sort,
      dir: dir,
      limit: limit,
      offset: offset,
    );
    return ListPage(page.findings, hasMore: page.hasMore ?? false);
  }
}

// ---- A host's inventory ----

/// Display order of the known categories; unknown ones follow by name.
const knownInventoryCategories = [
  'os',
  'hardware',
  'package',
  'process',
  'listening_port',
  'systemd_unit',
  'launchd_service',
  'windows_service',
  'kernel_module',
  'network_interface',
  'mount',
  'user',
  'discovered_service',
];

/// Each category of [items] with its count, in display order.
List<({String category, int count})> countByCategory(
  List<InventoryItem> items,
) {
  final counts = <String, int>{};
  for (final it in items) {
    counts[it.category] = (counts[it.category] ?? 0) + 1;
  }
  int rank(String c) {
    final i = knownInventoryCategories.indexOf(c);
    return i < 0 ? knownInventoryCategories.length : i;
  }

  final names = counts.keys.toList()
    ..sort((a, b) {
      final d = rank(a) - rank(b);
      return d != 0 ? d : a.compareTo(b);
    });
  return [for (final c in names) (category: c, count: counts[c]!)];
}

/// One display format for a listening port: `tcp 0.0.0.0:80`, IPv6 bracketed.
String inventoryKey(InventoryItem item) {
  if (item.category != 'listening_port') return item.key;
  final m = RegExp(r'^([a-z0-9]+):(.*):(\d+)$').firstMatch(item.key);
  if (m == null) return item.key;
  var host = m.group(2)!;
  if (host.startsWith('[') && host.endsWith(']')) {
    host = host.substring(1, host.length - 1);
  }
  if (host.isEmpty) {
    host = '*';
  } else if (host.contains(':')) {
    host = '[$host]';
  }
  return '${m.group(1)} $host:${m.group(3)}';
}

/// The key and the body, lower-cased, for the filter.
String _inventoryText(InventoryItem item) {
  final data = item.data;
  return '${item.key}\n${inventoryKey(item)}\n$data'.toLowerCase();
}

/// By category (empty is all) and a query, whose every word has to occur.
List<InventoryItem> filterInventory(
  List<InventoryItem> items,
  String category,
  String query,
) {
  final terms = query
      .toLowerCase()
      .split(RegExp(r'\s+'))
      .where((t) => t.isNotEmpty)
      .toList();
  return [
    for (final it in items)
      if ((category.isEmpty || it.category == category) &&
          terms.every(_inventoryText(it).contains))
        it,
  ];
}

/// One line of what an item's body says, the web's fields in the web's order.
String summarizeInventory(Object? data) {
  if (data == null) return '';
  if (data is! Map) return '$data';
  const pick = [
    'version',
    'description',
    'process_name',
    'exe',
    'state',
    'enabled_state',
    'fs_type',
    'shell',
    'model',
    'pretty_name',
    'operstate',
  ];
  final parts = <String>[];
  for (final k in pick) {
    final v = data[k];
    if (v != null && v != '') parts.add('$k=$v');
    if (parts.length >= 3) break;
  }
  return parts.join('  ');
}

class HostInventoryController extends DetailController<InventoryResponse> {
  HostInventoryController(this._client, this.hostId);

  final OpenlogClient _client;
  final String hostId;

  /// Filtered here, as on the web: the whole snapshot came in one answer.
  String category = '';
  String query = '';

  void filter({String? category, String? query}) {
    this.category = category ?? this.category;
    this.query = query ?? this.query;
    notifyListeners();
  }

  List<InventoryItem> get filtered =>
      filterInventory(value?.items ?? const [], category, query);

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  bool get endpointMayBeMissing => true;

  @override
  Future<InventoryResponse> fetch() => _client.hostInventory(hostId);
}

// ---- A host's metrics ----

enum HostChartUnit { percent, number, bytes, bytesPerSecond }

/// One line of a host chart.
class HostChartLine {
  const HostChartLine(this.label, this.values);

  final String label;
  final List<double> values;

  double? get latest => values.isEmpty ? null : values.last;
}

/// One chart: the lines drawn and listed, or why there are none.
class HostChart {
  const HostChart(this.id, this.unit, this.lines, {this.error});

  /// `cpu`, `load`, `memory`, `filesystem`, `disk` or `network`, as the web
  /// names its overview charts.
  final String id;
  final HostChartUnit unit;
  final List<HostChartLine> lines;
  final String? error;
}

/// One metric of a chart, the web's own definitions.
class _MetricSpec {
  const _MetricSpec(this.name, this.agg, {this.groupBy = '', this.label});

  final String name;
  final String agg;
  final String groupBy;

  /// The fixed label of a metric that is one line of a combined chart.
  final String? label;
}

class _ChartSpec {
  const _ChartSpec(this.id, this.unit, this.metrics, {this.order = const []});

  final String id;
  final HostChartUnit unit;
  final List<_MetricSpec> metrics;
  final List<String> order;
}

const _hostCharts = [
  _ChartSpec(
    'cpu',
    HostChartUnit.percent,
    [_MetricSpec('system.cpu.utilization', 'avg', groupBy: 'cpu.mode')],
    order: [
      'user',
      'system',
      'iowait',
      'nice',
      'irq',
      'interrupt',
      'softirq',
      'steal',
    ],
  ),
  _ChartSpec('load', HostChartUnit.number, [
    _MetricSpec('system.cpu.load_average.1m', 'avg', label: '1m'),
    _MetricSpec('system.cpu.load_average.5m', 'avg', label: '5m'),
    _MetricSpec('system.cpu.load_average.15m', 'avg', label: '15m'),
  ]),
  _ChartSpec(
    'memory',
    HostChartUnit.bytes,
    [_MetricSpec('system.memory.usage', 'avg', groupBy: 'system.memory.state')],
    order: ['used', 'buffers', 'cached', 'free'],
  ),
  _ChartSpec('filesystem', HostChartUnit.percent, [
    _MetricSpec(
      'system.filesystem.utilization',
      'avg',
      groupBy: 'system.filesystem.mountpoint',
    ),
  ]),
  _ChartSpec(
    'disk',
    HostChartUnit.bytesPerSecond,
    [_MetricSpec('system.disk.io', 'rate', groupBy: 'disk.io.direction')],
    order: ['read', 'write'],
  ),
  _ChartSpec(
    'network',
    HostChartUnit.bytesPerSecond,
    [_MetricSpec('system.network.io', 'rate', groupBy: 'network.io.direction')],
    order: ['receive', 'transmit'],
  ),
];

/// The busy modes summed bucket by bucket. The web stacks the modes and hides
/// idle by default, so the top of its stack is this line: a stacked area is
/// not something a phone's width reads well, but the total is.
List<double> _busy(Iterable<MetricSeries> modes) {
  final byTime = <double, double>{};
  for (final s in modes) {
    if (s.attributes['cpu.mode'] == 'idle') continue;
    for (final p in s.points) {
      if (p.length > 1) byTime[p[0]] = (byTime[p[0]] ?? 0) + p[1];
    }
  }
  final times = byTime.keys.toList()..sort();
  return [for (final t in times) byTime[t]!.clamp(0.0, 1.0)];
}

/// Six charts over the window, each asked and allowed to fail on its own, as
/// the web's cards do. Asked when the screen is opened rather than with the
/// host: that is eight requests on a phone network, for one tap's worth of
/// interest.
class HostMetricsController extends DetailController<List<HostChart>> {
  HostMetricsController(this._client, this.hostId);

  final OpenlogClient _client;
  final String hostId;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  bool get endpointMayBeMissing => true;

  @override
  Future<List<HostChart>> fetch() =>
      Future.wait([for (final spec in _hostCharts) _chart(spec)]);

  Future<HostChart> _chart(_ChartSpec spec) async {
    final lines = <HostChartLine>[];
    try {
      for (final m in spec.metrics) {
        final answer = await _client.hostMetric(
          hostId,
          name: m.name,
          agg: m.agg,
          groupBy: m.groupBy,
        );
        if (spec.id == 'cpu') {
          final busy = _busy(answer.series);
          if (busy.isNotEmpty) lines.add(HostChartLine('', busy));
        }
        for (final s in answer.series) {
          final label =
              m.label ??
              (m.groupBy.isEmpty ? m.name : s.attributes[m.groupBy] ?? m.name);
          // Idle hides everything else, on the web as here.
          if (spec.id == 'cpu' && label == 'idle') continue;
          lines.add(
            HostChartLine(label, [
              for (final p in s.points)
                if (p.length > 1) p[1],
            ]),
          );
        }
      }
    } on ApiException catch (e) {
      // The first chart to learn this says it for all of them.
      if (e.noSuchEndpoint) rethrow;
      return HostChart(spec.id, spec.unit, const [], error: e.message);
    } on ApiShapeError catch (e) {
      return HostChart(spec.id, spec.unit, const [], error: e.toString());
    }
    int rank(HostChartLine l) {
      if (l.label.isEmpty) return -1;
      final i = spec.order.indexOf(l.label);
      return i < 0 ? spec.order.length : i;
    }

    lines.sort((a, b) {
      final d = rank(a) - rank(b);
      return d != 0 ? d : a.label.compareTo(b.label);
    });
    return HostChart(spec.id, spec.unit, lines);
  }
}

// ---- A host's bill ----

/// Who on one host the bill goes to.
class CostHostController extends DetailController<CostHostDetail> {
  CostHostController(this._client, this.hostId);

  final OpenlogClient _client;
  final String hostId;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  bool get endpointMayBeMissing => true;

  @override
  Future<CostHostDetail> fetch() => _client.costHost(hostId);
}

/// Cents below a hundred, none above: a run rate of 1.72 an hour rounded to
/// "2" says something else entirely.
String formatMoney(double v, String currency) {
  final code = RegExp(r'^[A-Z]{3}$').hasMatch(currency) ? currency : 'USD';
  return '${v.abs() < 100 ? v.toStringAsFixed(2) : v.toStringAsFixed(0)} $code';
}

// ---- One alert rule ----

/// A series' labels as the web names one: its host when it has one.
String labelsText(Map<String, String> labels) {
  final host = labels['host.name'];
  if (host != null && host.isNotEmpty) return host;
  final entries = labels.entries.where((e) => e.key != 'host.id');
  return [for (final e in entries) '${e.key}=${e.value}'].join(', ');
}

/// One rule and the state of each series it is watching.
class AlertRuleController extends DetailController<AlertRuleDetail> {
  AlertRuleController(this._client, this.id);

  final OpenlogClient _client;
  final String id;

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  bool get endpointMayBeMissing => true;

  @override
  Future<AlertRuleDetail> fetch() => _client.alertRule(id);
}

/// The ranges the web offers, in hours.
const evaluationRanges = [1, 6, 24, 168];

/// How many evaluations a window held, how many failed, how long the last took.
({int evaluations, int errors, int? lastDurationMs}) evaluationTotals(
  AlertRuleEvaluations h,
) {
  var evaluations = 0;
  var errors = 0;
  for (final e in h.evaluations) {
    evaluations += e.evaluations;
    errors += e.errors;
  }
  return (
    evaluations: evaluations,
    errors: errors,
    lastDurationMs: h.evaluations.isEmpty
        ? null
        : h.evaluations.last.durationMs,
  );
}

/// The series values of one evaluation series, without the buckets that have
/// none: the web drops them too.
List<double> evaluationValues(AlertRuleEvaluationsSeriesItem s) => [
  for (final p in s.points)
    if (p.$2 != null) p.$2!,
];

/// What a rule's evaluations found over the last [hours].
class RuleEvaluationsController extends DetailController<AlertRuleEvaluations> {
  RuleEvaluationsController(this._client, this.id);

  final OpenlogClient _client;
  final String id;

  /// A day, the web's default.
  int hours = 24;

  void setHours(int value) {
    if (value == hours) return;
    hours = value;
    refresh();
  }

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  bool get endpointMayBeMissing => true;

  @override
  Future<AlertRuleEvaluations> fetch() {
    final now = DateTime.now();
    return _client.alertRuleEvaluations(
      id,
      from: now.subtract(Duration(hours: hours)),
      to: now,
    );
  }
}

// ---- A mute's preview ----

/// When a schedule nobody saved yet would next silence alerting.
///
/// A plain [ChangeNotifier]: nothing is on screen until a schedule is whole,
/// and a failure here is about the preview and not about the form under it.
class MutePreviewController extends ChangeNotifier {
  MutePreviewController(this._client);

  final OpenlogClient _client;

  List<AlertMuteOccurrence>? occurrences;
  bool loading = false;
  bool notOffered = false;
  String? error;

  /// Bumped per question, so a slow answer about an older draft is dropped.
  int _asked = 0;

  void clear() {
    _asked++;
    occurrences = null;
    error = null;
    loading = false;
    notifyListeners();
  }

  Future<void> run(Map<String, Object?> schedule) async {
    final asked = ++_asked;
    loading = true;
    error = null;
    notifyListeners();
    try {
      final page = await _client.previewAlertMute(schedule);
      if (asked != _asked) return;
      occurrences = page.occurrences;
      notOffered = false;
    } on ApiUnreachable catch (e) {
      if (asked == _asked) error = '$e';
    } on ApiException catch (e) {
      if (asked != _asked) return;
      occurrences = null;
      if (e.noSuchEndpoint) {
        notOffered = true;
      } else {
        error = e.message;
      }
    } on ApiShapeError catch (e) {
      if (asked == _asked) error = '$e';
    } finally {
      if (asked == _asked) {
        loading = false;
        notifyListeners();
      }
    }
  }
}
