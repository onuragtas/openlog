// Service health: where to look once an alert says something is wrong.
import 'dart:math' as math;

import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'detail.dart';
import 'list_controller.dart';
import 'session.dart';

class ServicesController extends PagedListController<ApmService> {
  ServicesController(this._client)
    : super(
        sort: 'name',
        dir: 'asc',
        ascendingSorts: const {'name', 'environment'},
      );

  final OpenlogClient _client;

  /// The search box. Empty means every service with spans in the range.
  String query = '';

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<ListPage<ApmService>> fetchPage(int offset) async {
    final page = await _client.services(
      q: query.trim(),
      sort: sort,
      dir: dir,
      limit: limit,
      offset: offset,
    );
    return ListPage(page.services, hasMore: page.hasMore ?? false);
  }
}

/// The sorts of the services list, in the web's order.
const serviceSorts = [
  'name',
  'environment',
  'throughput',
  'error_rate',
  'p95',
  'apdex',
  'last_seen',
];

/// The entry spans of one service, with the filters the web's traces tab has.
///
/// A controller per service screen, like the overview and the inbox: the
/// filters belong to that visit.
class ServiceTracesController extends PagedListController<ApmTraceResult> {
  ServiceTracesController(this.client, this.serviceName)
    : super(sort: 'timestamp');

  final OpenlogClient client;
  final String serviceName;

  String transaction = '';
  String minDurationMs = '';
  String maxDurationMs = '';
  bool errorsOnly = false;

  /// `attr.<key>=<value>` filters, as typed. Parsed rather than sent raw so
  /// a typo becomes an empty filter here instead of a 400 there.
  Map<String, String> attributes = const {};

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<ListPage<ApmTraceResult>> fetchPage(int offset) async {
    final page = await client.apmTraces(
      service: serviceName,
      transaction: transaction,
      minDurationMs: minDurationMs,
      maxDurationMs: maxDurationMs,
      errorsOnly: errorsOnly,
      sort: sort,
      dir: dir,
      attributes: attributes,
      limit: limit,
      offset: offset,
    );
    return ListPage(page.traces, hasMore: page.hasMore ?? false);
  }

  /// "Traces of this transaction": its slowest ones, as the web opens them.
  Future<void> showTransaction(String name) {
    transaction = name;
    sort = 'duration';
    dir = 'desc';
    return refresh();
  }
}

/// Timestamp and duration are the only two the server sorts traces by.
const serviceTraceSorts = ['timestamp', 'duration'];

/// `k=v k2=v2` as typed into one field, as the server's `attr.` parameters.
///
/// Pairs without a key or without a value are dropped rather than sent: the
/// server answers 400 for `attr.=x`, and a half-typed filter is not a filter
/// anybody meant. At most ten, which is the server's limit.
Map<String, String> parseAttributeFilter(String text) {
  final out = <String, String>{};
  for (final part in text.split(RegExp(r'[\s,]+'))) {
    final i = part.indexOf('=');
    if (i <= 0 || i == part.length - 1) continue;
    out[part.substring(0, i)] = part.substring(i + 1);
    if (out.length == 10) break;
  }
  return out;
}

/// What calls this service, and what it calls.
///
/// The web draws a graph; a phone draws two lists. The same edges, the same
/// numbers -- a node-link picture of forty services on a 390-point screen is
/// a picture of nothing.
class ServiceMapController extends ListController<ApmMapEdge> {
  ServiceMapController(this.client, this.serviceName);

  final OpenlogClient client;
  final String serviceName;

  /// The nodes by id, so an edge can be shown with the name of the thing at
  /// its other end rather than with its id.
  Map<String, ApmMapNode> nodes = const {};

  /// Which edge ids one transaction's traces use, once somebody asked.
  /// Empty when nobody did.
  Set<String> pathEdges = const {};
  String pathTransaction = '';
  int pathTraces = 0;

  @override
  String get forbiddenKind => 'servicesForbidden';

  /// The id of this service's own node, which both lists are relative to.
  String? get selfId {
    for (final n in nodes.values) {
      if (n.type == ApmMapNodeType.service && n.name == serviceName) {
        return n.id;
      }
    }
    return null;
  }

  List<ApmMapEdge> get incoming => [
    for (final e in items)
      if (e.target == selfId) e,
  ];

  List<ApmMapEdge> get outgoing => [
    for (final e in items)
      if (e.source == selfId) e,
  ];

  @override
  Future<List<ApmMapEdge>> fetch() async {
    final map = await client.apmMap(service: serviceName);
    nodes = {for (final n in map.nodes) n.id: n};
    // Busiest first: the dependency carrying the most calls is the one an
    // incident is most likely about.
    return [...map.edges]..sort((a, b) => b.calls.compareTo(a.calls));
  }

  /// Marks the edges one transaction's traces go through.
  Future<void> loadPath(String transaction) async {
    pathTransaction = transaction;
    failure = null;
    notifyListeners();
    try {
      final path = await client.apmMapPath(
        service: serviceName,
        transaction: transaction,
      );
      pathEdges = path.edges.toSet();
      pathTraces = path.traceCount;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('servicesForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      notifyListeners();
    }
  }

  /// Forgets the transaction, which puts every edge back to plain.
  void clearPath() {
    pathTransaction = '';
    pathEdges = const {};
    pathTraces = 0;
    notifyListeners();
  }
}

/// What one service spends its time on.
///
/// Sorted by the server, because "by time consumed" is a product of the
/// calls and their durations over the whole range -- not something the
/// hundred rows on this page could be re-sorted into.
class ServiceTransactionsController
    extends PagedListController<ApmTransaction> {
  ServiceTransactionsController(this.client, this.serviceName)
    : super(sort: 'time', ascendingSorts: const {'name'});

  final OpenlogClient client;
  final String serviceName;

  /// The Apdex threshold the server used, which the rows are judged against.
  double apdexTMs = 0;

  /// What the overview shows: the first few by time consumed, whichever way
  /// the list on the transactions tab was sorted afterwards.
  List<ApmTransaction> topByTime = const [];

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<ListPage<ApmTransaction>> fetchPage(int offset) async {
    final page = await client.apmTransactions(
      service: serviceName,
      sort: sort,
      dir: dir,
      limit: limit,
      offset: offset,
    );
    apdexTMs = page.apdexTMs;
    if (offset == 0 && sort == 'time' && dir == 'desc') {
      topByTime = page.transactions.take(5).toList();
    }
    return ListPage(page.transactions, hasMore: page.hasMore ?? false);
  }
}

/// The sorts of the transactions list, in the web's order, starting on the one
/// it opens on.
const transactionSorts = [
  'time',
  'name',
  'throughput',
  'avg',
  'slowest',
  'error_rate',
  'apdex',
];

/// One transaction: what the web opens above the list when a row is clicked.
class ApmTransactionController extends DetailController<ApmTransactionDetail> {
  ApmTransactionController(this.client, this.serviceName, this.name);

  final OpenlogClient client;
  final String serviceName;
  final String name;

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  bool get endpointMayBeMissing => true;

  @override
  Future<ApmTransactionDetail> fetch() =>
      client.apmTransaction(service: serviceName, name: name);
}

/// A latency distribution ready to draw: the empty buckets between the first
/// and the last filled in, and neighbours merged down to at most [maxBins]
/// bars. A port of `shapeHistogram` in `web/src/lib/apm.ts`, because the server
/// sends only the buckets that have something in them and a gap in the bars is
/// part of the shape.
List<ApmHistogramBin> shapeHistogram(
  List<ApmHistogramBin> bins, {
  int maxBins = 32,
}) {
  if (bins.isEmpty) return const [];
  const perOctave = 8;
  double upper(int index) => math.pow(2, index / perOctave).toDouble();
  final byIndex = <int, double>{};
  for (final b in bins) {
    final i = (perOctave * math.log(b.toMs) / math.ln2).round();
    byIndex[i] = (byIndex[i] ?? 0) + b.count;
  }
  final indexes = byIndex.keys.toList()..sort();
  final first = indexes.first;
  final last = indexes.last;
  final group = math.max(1, ((last - first + 1) / maxBins).ceil());
  return [
    for (var start = first; start <= last; start += group)
      ApmHistogramBin(
        fromMs: start <= -80 ? 0 : upper(start - 1),
        toMs: upper(math.min(last, start + group - 1)),
        count: [
          for (var i = start; i <= math.min(last, start + group - 1); i++)
            byIndex[i] ?? 0,
        ].reduce((a, b) => a + b),
      ),
  ];
}

/// The database statements one service runs.
class ServiceDatabasesController extends PagedListController<ApmDbQuery> {
  ServiceDatabasesController(this.client, this.serviceName)
    : super(sort: 'time', ascendingSorts: const {'statement', 'database'});

  final OpenlogClient client;
  final String serviceName;

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<ListPage<ApmDbQuery>> fetchPage(int offset) async {
    final page = await client.apmServiceDatabases(
      service: serviceName,
      sort: sort,
      dir: dir,
      limit: limit,
      offset: offset,
    );
    return ListPage(page.queries, hasMore: page.hasMore ?? false);
  }
}

/// The sorts of a service's database list, in the web's order. The server says
/// `calls` where transactions say `throughput`, and `error_rate` is the column
/// the web shows.
const serviceDatabaseSorts = [
  'time',
  'statement',
  'database',
  'calls',
  'slowest',
  'error_rate',
];

/// When each version of a service first appeared, and what one of them did.
///
/// Newest first, which is the order somebody reads them in: the question is
/// almost always about the last deployment.
class ServiceDeploymentsController extends ListController<ApmDeployment> {
  ServiceDeploymentsController(this.client, this.serviceName);

  final OpenlogClient client;
  final String serviceName;

  /// The comparison of one deployment, by its unix-millisecond timestamp.
  /// Null until somebody asks for one.
  ApmDeploymentCompare? compare;
  int? comparing;
  bool comparingBusy = false;

  /// How far either side of the deployment the comparison looks.
  String compareWindow = '30m';

  /// The deployment's own namespace and environment. A service in staging and
  /// in production has one version line each, and without these the numbers
  /// would be of both together.
  String? _namespace;
  String? _environment;

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<List<ApmDeployment>> fetch() async {
    final page = await client.apmDeployments(service: serviceName);
    return [...page.deployments]..sort((a, b) => b.t.compareTo(a.t));
  }

  /// Asks what the deployment at [atMillis] did. Tapping the open one closes
  /// it, because a comparison nobody is looking at is a panel in the way.
  Future<void> toggleCompare(
    int atMillis, {
    String? namespace,
    String? environment,
  }) async {
    if (comparing == atMillis) {
      comparing = null;
      compare = null;
      notifyListeners();
      return;
    }
    comparing = atMillis;
    _namespace = namespace;
    _environment = environment;
    await _readCompare(atMillis);
  }

  /// Compares the open deployment over another window.
  Future<void> setCompareWindow(String window) async {
    compareWindow = window;
    final at = comparing;
    if (at != null) await _readCompare(at);
  }

  Future<void> _readCompare(int atMillis) async {
    compare = null;
    comparingBusy = true;
    failure = null;
    notifyListeners();
    try {
      compare = await client.apmDeploymentCompare(
        service: serviceName,
        atMillis: atMillis,
        window: compareWindow,
        namespace: _namespace,
        environment: _environment,
      );
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('servicesForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      comparingBusy = false;
      notifyListeners();
    }
  }
}

/// The windows a comparison can look over, in the web's order.
const compareWindows = ['15m', '30m', '1h', '3h'];

/// How a number moved across a deployment. A port of `compareDelta` in
/// `web/src/lib/deployments.ts`.
class CompareDelta {
  const CompareDelta({
    this.delta,
    this.ratio,
    this.direction = 'flat',
    this.verdict = 'neutral',
  });

  /// After minus before, or null when either is missing.
  final double? delta;

  /// The change relative to before, or null when before is zero or missing:
  /// dividing by that zero would say infinity, and reading it as 0% would say
  /// the deployment did nothing.
  final double? ratio;

  /// `up`, `down` or `flat`.
  final String direction;

  /// `good`, `bad` or `neutral`. Throughput moving is neither.
  final String verdict;
}

/// [metric] is `throughput`, `error_rate`, `avg_ms`, `p95_ms`, `p99_ms` or
/// `apdex`.
CompareDelta compareDelta(String metric, double? before, double? after) {
  if (before == null || after == null || !before.isFinite || !after.isFinite) {
    return const CompareDelta();
  }
  final delta = after - before;
  final ratio = before != 0 ? delta / before.abs() : null;
  // An absolute tolerance for what lives between 0 and 1, a relative one for
  // the rest: below these a change is noise and reads as no change.
  final small = metric == 'error_rate' || metric == 'apdex'
      ? delta.abs() < 0.001
      : ratio != null
      ? ratio.abs() < 0.02
      : delta == 0;
  if (delta == 0 || small) return CompareDelta(delta: delta, ratio: ratio);
  final direction = delta > 0 ? 'up' : 'down';
  if (metric == 'throughput') {
    return CompareDelta(delta: delta, ratio: ratio, direction: direction);
  }
  final good = (direction == 'up') == (metric == 'apdex');
  return CompareDelta(
    delta: delta,
    ratio: ratio,
    direction: direction,
    verdict: good ? 'good' : 'bad',
  );
}

/// Where one service runs, and what it is judged against.
///
/// Four questions the web's service header answers above the tabs: which
/// language and version, which hosts, which containers, which pods -- and
/// the Apdex threshold, which is the only one of them anybody can change.
class ServiceAboutController extends ChangeNotifier {
  ServiceAboutController(this.client, this.serviceName);

  final OpenlogClient client;
  final String serviceName;

  ApmServiceDetail? detail;
  ApmSettings? settings;
  List<ApmServiceContainer> containers = const [];
  List<KubernetesServicePod> pods = const [];

  bool loading = false;
  bool saving = false;
  SessionFailure? failure;

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      detail = await client.apmService(serviceName);
      settings = await client.apmServiceSettings(serviceName);
      containers = (await client.apmServiceContainers(serviceName)).containers;
      pods = (await client.apmServicePods(serviceName)).pods;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  /// Changes the Apdex threshold, in milliseconds.
  ///
  /// Everything else on this panel is something the server observed; this is
  /// the one judgement call, and it changes what every Apdex number on the
  /// service means -- so it is saved explicitly, never as a side effect.
  Future<bool> setApdex(int apdexTMs) async {
    saving = true;
    failure = null;
    notifyListeners();
    try {
      settings = await client.putApmServiceSettings(
        serviceName,
        apdexTMs: apdexTMs,
      );
      return true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
      return false;
    } on ApiException catch (e) {
      failure = _failureOf(e);
      return false;
    } finally {
      saving = false;
      notifyListeners();
    }
  }

  SessionFailure _failureOf(ApiException e) => switch (e.status) {
    403 => const SessionFailure('servicesForbidden', ''),
    // Static auth mode: there is no store to put settings in.
    404 => const SessionFailure('apdexUnavailable', ''),
    _ => SessionFailure('unexpected', e.message),
  };
}
