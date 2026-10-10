// Which language agent each service runs, and how far behind it is.
//
// One row per service x agent x version, like the web's table
// (`web/src/lib/agent-versions.ts`): a service can run two versions at once
// while a deploy rolls out, and collapsing them would hide exactly the thing
// this screen is for.
import 'api/client.dart';
import 'api/schema.g.dart';
import 'list_controller.dart';

/// The openlog agents' own product names. Anything else is named by its SDK,
/// because a third-party OpenTelemetry distribution has no openlog package.
const _products = <ApmServiceAgentKind, String>{
  ApmServiceAgentKind.go: 'openlog-go',
  ApmServiceAgentKind.node: 'openlog-node',
  ApmServiceAgentKind.python: 'openlog-agent',
  ApmServiceAgentKind.java: 'openlog-javaagent',
  ApmServiceAgentKind.dotnet: 'OpenLog.Agent',
  ApmServiceAgentKind.php: 'openlog-php',
};

String agentProduct(ApmServiceAgent a) {
  final known = _products[a.kind];
  if (known != null) return known;
  final parts = [
    if (a.sdkName.isNotEmpty) a.sdkName else 'OpenTelemetry',
    if (a.sdkLanguage.isNotEmpty) a.sdkLanguage,
  ];
  return parts.isEmpty ? a.kind.wire : parts.join(' · ');
}

/// Outdated and unsupported are the two that mean somebody has to do
/// something; unknown means the release catalog could not say.
bool agentNeedsAttention(ApmAgentStatus s) =>
    s == ApmAgentStatus.outdated || s == ApmAgentStatus.unsupported;

/// One line of the list: a version of one agent of one service.
class AgentRow {
  const AgentRow({
    required this.serviceName,
    required this.environment,
    required this.product,
    required this.version,
    required this.status,
    required this.instances,
    required this.spans,
    required this.lastSeen,
  });

  final String serviceName;
  final String environment;
  final String product;
  final String version;
  final ApmAgentStatus status;
  final int instances;
  final int spans;
  final DateTime lastSeen;

  String get searchText =>
      '$serviceName $environment $product $version'.toLowerCase();
}

List<AgentRow> agentRows(List<ApmServiceAgents> services) => [
  for (final s in services)
    for (final a in s.agents)
      for (final v in a.versions)
        AgentRow(
          serviceName: s.serviceName,
          environment: s.environment,
          product: agentProduct(a),
          version: v.version,
          status: v.status,
          instances: v.instances,
          spans: v.spans,
          lastSeen: v.lastSeen,
        ),
];

/// The agent list.
///
/// Filtered here rather than by the server: the endpoint returns every
/// service in one answer and has no search of its own, so asking again for
/// each keystroke would re-read the same rows.
class ApmAgentsController extends ListController<AgentRow> {
  ApmAgentsController(this.client);

  final OpenlogClient client;

  String query = '';

  /// Only the rows somebody has to act on, which is why anyone opens this.
  bool attentionOnly = false;

  /// The newest release to compare with, and whether the catalog could be
  /// read at all -- without it every openlog agent's status is `unknown`
  /// and the list cannot say anything about being behind.
  ApmAgentRelease? release;

  List<AgentRow> _all = const [];

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<List<AgentRow>> fetch() async {
    final answer = await client.apmAgents();
    release = answer.release;
    _all = agentRows(answer.services)
      // Behind first, then by service: the rows worth acting on are the
      // reason the screen is open.
      ..sort((a, b) {
        final byAttention =
            (agentNeedsAttention(b.status) ? 1 : 0) -
            (agentNeedsAttention(a.status) ? 1 : 0);
        if (byAttention != 0) return byAttention;
        final byService = a.serviceName.compareTo(b.serviceName);
        return byService != 0 ? byService : a.version.compareTo(b.version);
      });
    return filtered;
  }

  /// Re-filters what is already loaded, without asking the server again.
  void refilter() {
    items = filtered;
    notifyListeners();
  }

  List<AgentRow> get filtered {
    final terms = query.toLowerCase().split(RegExp(r'\s+'))
      ..removeWhere((t) => t.isEmpty);
    return [
      for (final r in _all)
        if (!(attentionOnly && !agentNeedsAttention(r.status)) &&
            terms.every(r.searchText.contains))
          r,
    ];
  }
}
