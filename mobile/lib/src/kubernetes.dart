// Kubernetes: the cluster everything else on the screen is about, and the
// three lists under it.
//
// The web has four pages behind one nav -- overview, workloads, pods, nodes
// -- and one cluster picker that travels between them. Here they are four
// tabs over one [KubernetesScope], which is what keeps the picker honest: a
// cluster chosen on the nodes tab is still the cluster the workloads tab is
// about.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'list_controller.dart';
import 'session.dart';

/// The cluster the Kubernetes screen is looking at, and the clusters there
/// are to choose from.
///
/// A [ChangeNotifier] of its own rather than a field on each list: the lists
/// have to reload when it changes, and four copies of "which cluster" is how
/// two tabs end up about different ones.
class KubernetesScope extends ChangeNotifier {
  KubernetesScope(this.client);

  final OpenlogClient client;

  List<KubernetesCluster> clusters = const [];

  /// The chosen cluster's uid, or empty for every cluster. The overview can
  /// only be about one, so it picks the first when nothing is chosen.
  String clusterUid = '';

  bool loading = false;
  bool loaded = false;
  SessionFailure? failure;

  /// The chosen cluster, or the first one when nothing is chosen.
  KubernetesCluster? get cluster {
    for (final c in clusters) {
      if (c.clusterUid == clusterUid) return c;
    }
    return clusters.isEmpty ? null : clusters.first;
  }

  /// The namespaces to offer: those of the chosen cluster, or of all of
  /// them when none is chosen. Sorted, because the server returns them per
  /// cluster and two clusters share most of their namespaces.
  List<String> get namespaces {
    final out = <String>{};
    for (final c in clusters) {
      if (clusterUid.isNotEmpty && c.clusterUid != clusterUid) continue;
      out.addAll(c.namespaces);
    }
    return out.toList()..sort();
  }

  Future<void> load() async {
    if (loading) return;
    loading = true;
    failure = null;
    notifyListeners();
    try {
      clusters = (await client.k8sClusters()).clusters;
      loaded = true;
      // A cluster that stopped reporting and fell out of the list must not
      // keep filtering every tab from behind.
      if (clusterUid.isNotEmpty &&
          !clusters.any((c) => c.clusterUid == clusterUid)) {
        clusterUid = '';
      }
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('sectionForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  /// Chooses a cluster. Returns false when it was already the chosen one,
  /// so the caller does not reload three lists for nothing.
  bool choose(String uid) {
    if (uid == clusterUid) return false;
    clusterUid = uid;
    notifyListeners();
    return true;
  }
}

/// One cluster's own screen: totals, workload health by kind and the latest
/// Warning events.
class KubernetesClusterController extends ChangeNotifier {
  KubernetesClusterController(this.client);

  final OpenlogClient client;

  KubernetesClusterDetail? detail;

  /// Which cluster [detail] is about, so a stale answer is never shown
  /// under another cluster's name.
  String uid = '';

  bool loading = false;
  SessionFailure? failure;

  Future<void> load(String clusterUid) async {
    if (clusterUid.isEmpty) {
      detail = null;
      uid = '';
      notifyListeners();
      return;
    }
    loading = true;
    failure = null;
    notifyListeners();
    try {
      final answer = await client.k8sCluster(clusterUid);
      detail = answer;
      uid = clusterUid;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = switch (e.status) {
        403 => const SessionFailure('sectionForbidden', ''),
        404 => const SessionFailure('detailGone', ''),
        _ => SessionFailure('unexpected', e.message),
      };
      detail = null;
      uid = '';
    } finally {
      loading = false;
      notifyListeners();
    }
  }
}

/// The nodes of the window, filtered like the web's page.
class KubernetesNodesController extends ListController<KubernetesNode> {
  KubernetesNodesController(this.client);

  final OpenlogClient client;

  String query = '';
  String clusterUid = '';

  /// How many matched before the server's limit, so a list that was cut
  /// says so instead of reading as the whole truth.
  int total = 0;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<List<KubernetesNode>> fetch() async {
    final page = await client.k8sNodes(clusterUid: clusterUid, q: query.trim());
    total = page.total;
    return page.nodes;
  }
}

/// The workloads of the window: the web's filters, the web's order.
class KubernetesWorkloadsController extends ListController<KubernetesWorkload> {
  KubernetesWorkloadsController(this.client);

  final OpenlogClient client;

  String query = '';
  String clusterUid = '';
  String namespace = '';

  /// `Deployment`, `StatefulSet`… or empty for every kind.
  String kind = '';

  /// `healthy`, `degraded`, `unavailable`, `unknown` or empty.
  String health = '';

  int total = 0;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<List<KubernetesWorkload>> fetch() async {
    final page = await client.k8sWorkloads(
      clusterUid: clusterUid,
      namespace: namespace,
      kind: kind,
      health: health,
      q: query.trim(),
    );
    total = page.total;
    return page.workloads;
  }
}

/// The cluster's events, newest first.
class KubernetesEventsController extends ListController<KubernetesEvent> {
  KubernetesEventsController(this.client);

  final OpenlogClient client;

  String clusterUid = '';
  String namespace = '';

  /// `Warning` by default: a cluster produces hundreds of Normal events an
  /// hour and none of them is why somebody opened this screen.
  String type = 'Warning';

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<List<KubernetesEvent>> fetch() async => (await client.k8sEvents(
    clusterUid: clusterUid,
    namespace: namespace,
    type: type,
  )).events;
}

/// The workload kinds the contract has, in the web's order.
const workloadKinds = [
  'Deployment',
  'StatefulSet',
  'DaemonSet',
  'Job',
  'CronJob',
  'ReplicaSet',
];

const workloadHealths = ['healthy', 'degraded', 'unavailable', 'unknown'];

/// The pod phases, as the web lists them.
const podPhases = ['Pending', 'Running', 'Succeeded', 'Failed', 'Unknown'];

/// CPU in cores, as the web prints it: `250m` below one core, else a
/// number with at most two decimals.
String formatCores(double? v) {
  if (v == null || !v.isFinite) return '—';
  if (v != 0 && v.abs() < 1) {
    final m = (v * 1000).round();
    return m == 0 ? '<1m' : '${m}m';
  }
  // At most two decimals, and no trailing zeros: the web prints 1.5 and
  // 2, not 1.50 and 2.00.
  var s = v.toStringAsFixed(2);
  if (s.contains('.')) {
    s = s.replaceFirst(RegExp(r'0+$'), '').replaceFirst(RegExp(r'\.$'), '');
  }
  return s;
}

/// used / total, or null when either is unknown.
double? usageRatio(double? used, double? total) {
  if (used == null || total == null || !(total > 0)) return null;
  return used / total;
}

/// Every pod of a cluster, whatever phase it is in.
///
/// Twice, because the contract composes the detail out of the list shape
/// and the generator gives each its own class; the alternative would be a
/// hand-written interface over two generated types that are identical.
int totalPods(KubernetesClusterPods p) =>
    p.pending + p.running + p.succeeded + p.failed + p.unknown;

int totalDetailPods(KubernetesClusterDetailPods p) =>
    p.pending + p.running + p.succeeded + p.failed + p.unknown;

/// Pod reasons that are not failures: a finished pod's containers are
/// terminated, and a starting pod is not broken.
const _benignReasons = {'Completed', 'ContainerCreating', 'PodInitializing'};

/// How a pod's state should read, as the web decides it.
({String label, bool notReporting, bool notReady, String level}) podStatus(
  KubernetesPod p,
) {
  final label = p.status.isNotEmpty
      ? p.status
      : p.reason.isNotEmpty
      ? p.reason
      : (p.phase.isNotEmpty ? p.phase : 'Unknown');
  final active =
      p.phase == 'Running' || p.phase == 'Pending' || p.phase.isEmpty;
  if (!p.reporting && active) {
    return (label: label, notReporting: true, notReady: false, level: 'muted');
  }
  final notReady = p.phase == 'Running' && !p.ready;
  if (p.reason.isNotEmpty && !_benignReasons.contains(p.reason)) {
    return (
      label: label,
      notReporting: false,
      notReady: notReady,
      level: 'bad',
    );
  }
  final level = switch (p.phase) {
    'Running' => p.ready ? 'good' : 'warn',
    'Pending' => 'warn',
    'Succeeded' => 'muted',
    'Failed' => 'bad',
    _ => 'muted',
  };
  return (label: label, notReporting: false, notReady: notReady, level: level);
}

/// A node's state: `notReporting`, `ready`, `notReady` or `unknown`.
String nodeStatus(KubernetesNode n) {
  if (!n.reporting) return 'notReporting';
  return switch (n.ready) {
    KubernetesNodeReady.trueValue => 'ready',
    KubernetesNodeReady.falseValue => 'notReady',
    _ => 'unknown',
  };
}

/// The conditions other than Ready that are true right now: the pressures
/// and the unreachable network that explain a node nobody can schedule on.
List<String> nodeProblems(KubernetesNode n) => [
  for (final c in n.conditions)
    if (c.condition != 'Ready' &&
        c.status == KubernetesNodeConditionStatus.trueValue)
      c.condition,
];
