// Service health: where to look once an alert says something is wrong.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'list_controller.dart';
import 'session.dart';

class ServicesController extends ListController<ApmService> {
  ServicesController(this._client);

  final OpenlogClient _client;

  /// The search box. Empty means every service with spans in the range.
  String query = '';

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<List<ApmService>> fetch() async {
    final page = await _client.services(q: query.trim());
    // Worst first: on a phone the list is read from the top and rarely
    // scrolled, so the service that is actually broken has to be there.
    final list = [...page.services]
      ..sort((a, b) {
        final byError = b.errorRate.compareTo(a.errorRate);
        if (byError != 0) return byError;
        return b.throughput.compareTo(a.throughput);
      });
    return list;
  }
}

/// The entry spans of one service, with the filters the web's traces tab has.
///
/// A controller per service screen, like the overview and the inbox: the
/// filters belong to that visit.
class ServiceTracesController extends ListController<ApmTraceResult> {
  ServiceTracesController(this.client, this.serviceName);

  final OpenlogClient client;
  final String serviceName;

  String transaction = '';
  String minDurationMs = '';
  String maxDurationMs = '';
  bool errorsOnly = false;

  /// 'timestamp' (newest first) or 'duration' (slowest first).
  String sort = 'timestamp';

  /// `attr.<key>=<value>` filters, as typed. Parsed rather than sent raw so
  /// a typo becomes an empty filter here instead of a 400 there.
  Map<String, String> attributes = const {};

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<List<ApmTraceResult>> fetch() async => (await client.apmTraces(
    service: serviceName,
    transaction: transaction,
    minDurationMs: minDurationMs,
    maxDurationMs: maxDurationMs,
    errorsOnly: errorsOnly,
    sort: sort,
    attributes: attributes,
  )).traces;
}

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
class ServiceTransactionsController extends ListController<ApmTransaction> {
  ServiceTransactionsController(this.client, this.serviceName);

  final OpenlogClient client;
  final String serviceName;

  /// 'time', 'throughput', 'slowest' or 'errors'.
  String sort = 'time';

  /// The Apdex threshold the server used, which the rows are judged against.
  double apdexTMs = 0;

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<List<ApmTransaction>> fetch() async {
    final page = await client.apmTransactions(service: serviceName, sort: sort);
    apdexTMs = page.apdexTMs;
    return page.transactions;
  }
}

/// The database statements one service runs.
class ServiceDatabasesController extends ListController<ApmDbQuery> {
  ServiceDatabasesController(this.client, this.serviceName);

  final OpenlogClient client;
  final String serviceName;

  /// 'time', 'calls', 'slowest' or 'errors'.
  String sort = 'time';

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<List<ApmDbQuery>> fetch() async => (await client.apmServiceDatabases(
    service: serviceName,
    sort: sort,
  )).queries;
}

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

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<List<ApmDeployment>> fetch() async {
    final page = await client.apmDeployments(service: serviceName);
    return [...page.deployments]..sort((a, b) => b.t.compareTo(a.t));
  }

  /// Asks what the deployment at [atMillis] did. Tapping the open one closes
  /// it, because a comparison nobody is looking at is a panel in the way.
  Future<void> toggleCompare(int atMillis) async {
    if (comparing == atMillis) {
      comparing = null;
      compare = null;
      notifyListeners();
      return;
    }
    comparing = atMillis;
    compare = null;
    comparingBusy = true;
    failure = null;
    notifyListeners();
    try {
      compare = await client.apmDeploymentCompare(
        service: serviceName,
        atMillis: atMillis,
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

/// How a number moved across a deployment, as a ratio, or null when there is
/// nothing to compare with.
///
/// Null before, null after, or a zero before all mean "no comparison", not
/// "no change": dividing by the zero would say infinity and reading it as
/// 0% would say the deployment did nothing.
double? deltaRatio(double? before, double? after) {
  if (before == null || after == null || before == 0) return null;
  return (after - before) / before;
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
