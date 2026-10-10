// Service health: where to look once an alert says something is wrong.
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
