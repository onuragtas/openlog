// Kubernetes: the web's four pages, as four tabs.
//
// Genel bakış, İş yükleri, Pod'lar, Düğümler -- the web's own order, behind
// the same cluster picker, which travels between the tabs there and here.
// What changes on a phone is the shape of a row: the web's tables become
// cards, and a filter that is a select there is a searchable sheet here,
// because a namespace list is longer than a phone dropdown.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../kubernetes.dart';
import '../sections.dart';
import '../usage.dart' show formatBytes;
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'pick_sheet.dart';
import 'pod_screen.dart';
import 'sections_screen.dart';
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';

/// What the app bar's refresh button reloads here.
///
/// The section is four tabs over one cluster, so refreshing means the
/// cluster list, the cluster on screen and every list that has already been
/// looked at -- not the four of them, three of which nobody opened.
Future<void> refreshKubernetes(Sections s) async {
  await s.k8s.load();
  final uid = s.k8s.cluster?.clusterUid ?? '';
  await Future.wait([
    if (uid.isNotEmpty) s.k8sCluster.load(uid),
    if (s.k8sNodes.loaded) s.k8sNodes.refresh(),
    if (s.k8sWorkloads.loaded) s.k8sWorkloads.refresh(),
    if (s.pods.loaded) s.pods.refresh(),
    if (s.k8sEvents.loaded) s.k8sEvents.refresh(),
  ]);
}

class KubernetesBody extends StatefulWidget {
  const KubernetesBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  State<KubernetesBody> createState() => _KubernetesBodyState();
}

class _KubernetesBodyState extends State<KubernetesBody>
    with SingleTickerProviderStateMixin {
  late final TabController _tabs;

  @override
  void initState() {
    super.initState();
    _tabs = TabController(length: 4, vsync: this)
      ..addListener(() {
        if (_tabs.indexIsChanging) return;
        _loadTab();
      });
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(KubernetesBody old) {
    super.didUpdateWidget(old);
    _loadIfVisible();
  }

  @override
  void dispose() {
    _tabs.dispose();
    super.dispose();
  }

  /// Nothing is asked until this section is the one on screen: the shell
  /// builds every section at sign-in, and four lists per section would be
  /// four requests nobody asked for.
  void _loadIfVisible() {
    final s = widget.sections;
    if (!widget.active || s.k8s.loaded || s.k8s.loading) return;
    WidgetsBinding.instance.addPostFrameCallback((_) async {
      await s.k8s.load();
      if (!mounted) return;
      _applyCluster();
      _loadTab();
    });
  }

  /// The chosen cluster is what every list is about.
  void _applyCluster() {
    final s = widget.sections;
    final uid = s.k8s.clusterUid;
    s.k8sNodes.clusterUid = uid;
    s.k8sWorkloads.clusterUid = uid;
    s.pods.clusterUid = uid;
    s.k8sEvents.clusterUid = uid;
  }

  /// Loads what the open tab shows, the first time it is looked at.
  void _loadTab() {
    final s = widget.sections;
    switch (_tabs.index) {
      case 0:
        final uid = s.k8s.cluster?.clusterUid ?? '';
        if (uid.isNotEmpty && s.k8sCluster.uid != uid) s.k8sCluster.load(uid);
        if (!s.k8sNodes.loaded && !s.k8sNodes.loadingFirst) {
          s.k8sNodes.refresh();
        }
      case 1:
        if (!s.k8sWorkloads.loaded && !s.k8sWorkloads.loadingFirst) {
          s.k8sWorkloads.refresh();
        }
      case 2:
        if (!s.pods.loaded && !s.pods.loadingFirst) s.pods.refresh();
      case 3:
        if (!s.k8sNodes.loaded && !s.k8sNodes.loadingFirst) {
          s.k8sNodes.refresh();
        }
    }
  }

  /// A different cluster is a different question for every tab, so each
  /// list that has already been loaded is asked again.
  void _chooseCluster(String uid) {
    final s = widget.sections;
    if (!s.k8s.choose(uid)) return;
    _applyCluster();
    final cluster = s.k8s.cluster?.clusterUid ?? '';
    if (cluster.isNotEmpty) s.k8sCluster.load(cluster);
    if (s.k8sNodes.loaded) s.k8sNodes.refresh();
    if (s.k8sWorkloads.loaded) s.k8sWorkloads.refresh();
    if (s.pods.loaded) s.pods.refresh();
    if (s.k8sEvents.loaded) s.k8sEvents.refresh();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final s = widget.sections;

    return ListenableBuilder(
      listenable: s.k8s,
      builder: (context, _) => Column(
        children: [
          if (s.k8s.clusters.length > 1)
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 10, 12, 0),
              child: _ClusterPicker(scope: s.k8s, onChoose: _chooseCluster),
            ),
          FailureBanner(
            failure: s.k8s.failure,
            baseUrl: widget.session.baseUrl ?? '',
          ),
          TabBar(
            controller: _tabs,
            isScrollable: true,
            tabAlignment: TabAlignment.start,
            tabs: [
              Tab(key: const Key('k8s-tab-overview'), text: l.k8sTabOverview),
              Tab(key: const Key('k8s-tab-workloads'), text: l.k8sTabWorkloads),
              Tab(key: const Key('k8s-tab-pods'), text: l.k8sTabPods),
              Tab(key: const Key('k8s-tab-nodes'), text: l.k8sTabNodes),
            ],
          ),
          Expanded(
            child: TabBarView(
              controller: _tabs,
              children: [
                _Overview(
                  session: widget.session,
                  sections: s,
                  onAllNodes: () => _tabs.animateTo(3),
                  onKind: (kind, health) {
                    s.k8sWorkloads
                      ..kind = kind
                      ..health = health
                      ..refresh();
                    _tabs.animateTo(1);
                  },
                ),
                _Workloads(session: widget.session, sections: s),
                _Pods(session: widget.session, sections: s),
                _Nodes(session: widget.session, sections: s),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

class _ClusterPicker extends StatelessWidget {
  const _ClusterPicker({required this.scope, required this.onChoose});

  final KubernetesScope scope;
  final ValueChanged<String> onChoose;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return DropdownButtonFormField<String>(
      key: const Key('k8s-cluster'),
      initialValue: scope.clusterUid,
      isExpanded: true,
      decoration: InputDecoration(
        labelText: l.k8sCluster,
        border: const OutlineInputBorder(),
        isDense: true,
      ),
      items: [
        DropdownMenuItem(value: '', child: Text(l.k8sAllClusters)),
        for (final c in scope.clusters)
          DropdownMenuItem(
            value: c.clusterUid,
            child: Text(
              c.clusterName.isEmpty ? c.clusterUid : c.clusterName,
              overflow: TextOverflow.ellipsis,
            ),
          ),
      ],
      onChanged: (v) => onChoose(v ?? ''),
    );
  }
}

/// A filter that is a select on the web and a searchable sheet here.
class _FilterChip extends StatelessWidget {
  const _FilterChip({
    required this.chipKey,
    required this.label,
    required this.value,
    required this.options,
    required this.onPick,
  });

  final Key chipKey;

  /// What the filter is, shown when nothing is chosen ("Ad alanı").
  final String label;
  final String value;
  final List<String> options;
  final ValueChanged<String> onPick;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final chosen = value.isNotEmpty;
    return InputChip(
      key: chipKey,
      label: Text(chosen ? value : label),
      avatar: chosen ? null : const Icon(Icons.filter_alt_outlined, size: 16),
      // Chosen, it clears; empty, it opens the list. One chip, both jobs,
      // because a phone has no room for a select and a clear button.
      onDeleted: chosen ? () => onPick('') : null,
      onPressed: () async {
        final picked = await pickOne(
          context,
          title: label,
          options: options,
          // "All of them" is a row like any other, which is how the web's
          // select offers it too.
          firstLabel: l.k8sAll,
        );
        if (picked != null) onPick(picked);
      },
    );
  }
}

/// The cluster itself: its totals, its workloads by kind, its warnings and
/// its nodes -- the web's overview, in one column.
class _Overview extends StatelessWidget {
  const _Overview({
    required this.session,
    required this.sections,
    required this.onAllNodes,
    required this.onKind,
  });

  final SessionController session;
  final Sections sections;
  final VoidCallback onAllNodes;
  final void Function(String kind, String health) onKind;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = sections.k8sCluster;

    return ListenableBuilder(
      listenable: Listenable.merge([c, sections.k8sNodes, sections.k8s]),
      builder: (context, _) {
        if (sections.k8s.loaded && sections.k8s.clusters.isEmpty) {
          return Center(
            child: Padding(
              padding: const EdgeInsets.all(24),
              child: Text(
                l.k8sEmpty,
                key: const Key('k8s-empty'),
                textAlign: TextAlign.center,
                style: theme.textTheme.bodyMedium?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ),
          );
        }
        final detail = c.detail;
        if (c.loading && detail == null) {
          return const Center(child: CircularProgressIndicator());
        }
        if (detail == null) {
          return ListView(
            padding: const EdgeInsets.all(12),
            children: [
              FailureBanner(failure: c.failure, baseUrl: session.baseUrl ?? ''),
            ],
          );
        }

        final pods = totalDetailPods(detail.pods);
        final nodes = sections.k8sNodes;
        return RefreshIndicator(
          onRefresh: () async {
            await c.load(detail.clusterUid);
            await nodes.refresh();
          },
          child: ListView(
            padding: const EdgeInsets.fromLTRB(12, 12, 12, 32),
            children: [
              Wrap(
                spacing: 10,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Text(
                    detail.clusterName.isEmpty
                        ? detail.clusterUid
                        : detail.clusterName,
                    style: theme.textTheme.titleMedium,
                  ),
                  if (!detail.reporting)
                    Tag(label: l.k8sNotReporting, level: SeverityLevel.warning),
                  if (detail.version.isNotEmpty)
                    Text(
                      detail.version,
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  Text(
                    relativeTimeOf(l, detail.lastSeen),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 12),
              _Tiles(detail: detail, pods: pods),
              const SizedBox(height: 16),
              Text(l.k8sWorkloadsByKind, style: theme.textTheme.titleSmall),
              if (detail.workloadsByKind.isEmpty)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 8),
                  child: Text(
                    l.k8sNoWorkloads,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              else
                for (final k in detail.workloadsByKind)
                  _KindRow(count: k, onOpen: onKind),
              const SizedBox(height: 16),
              Row(
                children: [
                  Expanded(
                    child: Text(
                      l.k8sWarningEvents,
                      style: theme.textTheme.titleSmall,
                    ),
                  ),
                ],
              ),
              if (detail.warningEvents.isEmpty)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 8),
                  child: Text(
                    l.k8sNoWarnings,
                    key: const Key('k8s-no-warnings'),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              else
                for (final e in detail.warningEvents) _EventRow(event: e),
              const SizedBox(height: 16),
              Row(
                children: [
                  Expanded(
                    child: Text(l.k8sNodes, style: theme.textTheme.titleSmall),
                  ),
                  TextButton(
                    key: const Key('k8s-all-nodes'),
                    onPressed: onAllNodes,
                    child: Text(l.k8sAllNodes),
                  ),
                ],
              ),
              if (nodes.loadingFirst)
                const Padding(
                  padding: EdgeInsets.symmetric(vertical: 16),
                  child: Center(child: CircularProgressIndicator()),
                )
              else if (nodes.items.isEmpty)
                Text(
                  l.k8sNoNodes,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                )
              else
                for (final n in nodes.items.take(5))
                  nodeCard(context, n, onOpen: () => _openNodePods(context, n)),
            ],
          ),
        );
      },
    );
  }

  void _openNodePods(BuildContext context, KubernetesNode node) =>
      Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) => ScopedPodsScreen(
            session: session,
            sections: sections,
            title: node.nodeName,
            node: node.nodeName,
            clusterUid: node.clusterUid,
          ),
        ),
      );
}

class _Tiles extends StatelessWidget {
  const _Tiles({required this.detail, required this.pods});

  final KubernetesClusterDetail detail;
  final int pods;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final cpu = usageRatio(detail.cpuUsage, detail.allocatableCpu);
    final memory = usageRatio(
      detail.memoryWorkingSet,
      detail.allocatableMemory,
    );
    final phases = [
      if (detail.pods.pending > 0) 'Pending ${detail.pods.pending}',
      if (detail.pods.failed > 0) 'Failed ${detail.pods.failed}',
      if (detail.pods.succeeded > 0) 'Succeeded ${detail.pods.succeeded}',
      if (detail.pods.unknown > 0) 'Unknown ${detail.pods.unknown}',
    ];

    return GridView.count(
      crossAxisCount: 2,
      shrinkWrap: true,
      physics: const NeverScrollableScrollPhysics(),
      // Tall enough for a number and two lines under it: the pod tile
      // carries every phase that is not Running.
      childAspectRatio: 1.75,
      mainAxisSpacing: 8,
      crossAxisSpacing: 8,
      children: [
        _Tile(
          tileKey: const Key('k8s-tile-nodes'),
          title: l.k8sNodesReady,
          value: '${detail.nodesReady}/${detail.nodes}',
          bad: detail.nodesReady < detail.nodes,
        ),
        _Tile(
          tileKey: const Key('k8s-tile-pods'),
          title: l.k8sPods,
          value: '$pods',
          sub: [l.k8sRunning(detail.pods.running), ...phases].join(' · '),
        ),
        _Tile(
          tileKey: const Key('k8s-tile-not-ready'),
          title: l.k8sPodsNotReady,
          value: '${detail.podsNotReady}',
          sub: l.k8sPodsNotReadyHint,
          bad: detail.podsNotReady > 0,
        ),
        _Tile(
          tileKey: const Key('k8s-tile-workloads'),
          title: l.k8sWorkloadsUnhealthy,
          value: '${detail.workloadsUnhealthy}',
          sub: l.k8sOfTotal(detail.workloads),
          bad: detail.workloadsUnhealthy > 0,
        ),
        _Tile(
          tileKey: const Key('k8s-tile-cpu'),
          title: l.k8sCpu,
          value: formatCores(detail.cpuUsage),
          sub: detail.allocatableCpu == null
              ? null
              : l.k8sOfAllocatable(
                  formatCores(detail.allocatableCpu),
                  ((cpu ?? 0) * 100).round(),
                ),
        ),
        _Tile(
          tileKey: const Key('k8s-tile-memory'),
          title: l.k8sMemory,
          value: detail.memoryWorkingSet == null
              ? '—'
              : formatBytes(detail.memoryWorkingSet!),
          sub: detail.allocatableMemory == null
              ? null
              : l.k8sOfAllocatable(
                  formatBytes(detail.allocatableMemory!),
                  ((memory ?? 0) * 100).round(),
                ),
        ),
      ],
    );
  }
}

class _Tile extends StatelessWidget {
  const _Tile({
    required this.tileKey,
    required this.title,
    required this.value,
    this.sub,
    this.bad = false,
  });

  final Key tileKey;
  final String title;
  final String value;
  final String? sub;
  final bool bad;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Card(
      key: tileKey,
      margin: EdgeInsets.zero,
      child: Padding(
        padding: const EdgeInsets.all(10),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Text(
              title,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            Text(
              value,
              style: theme.textTheme.titleMedium?.copyWith(
                fontFeatures: const [FontFeature.tabularFigures()],
                color: bad ? colorsOf(context).destructiveText : null,
              ),
            ),
            if (sub != null)
              Text(
                sub!,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
          ],
        ),
      ),
    );
  }
}

class _KindRow extends StatelessWidget {
  const _KindRow({required this.count, required this.onOpen});

  final KubernetesWorkloadKindCount count;
  final void Function(String kind, String health) onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final unhealthy = count.degraded + count.unavailable;

    return InkWell(
      key: Key('k8s-kind-${count.kind}'),
      // The number is a link on the web; here the row is, and it opens the
      // workloads tab filtered the same way.
      onTap: () => onOpen(count.kind, unhealthy > 0 ? 'degraded' : ''),
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 8),
        child: Row(
          children: [
            Expanded(
              child: Text(count.kind, style: theme.textTheme.bodyMedium),
            ),
            Text(
              '${count.total}',
              style: theme.textTheme.bodyMedium?.copyWith(
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
            if (unhealthy > 0) ...[
              const SizedBox(width: 10),
              Text(
                l.k8sUnhealthyCount(unhealthy),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: colors.destructiveText,
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }
}

class _EventRow extends StatelessWidget {
  const _EventRow({required this.event});

  final KubernetesEvent event;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Padding(
      key: Key('k8s-event-${event.objectUid}-${event.reason}'),
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Tag(
                label: event.type,
                level: event.type == 'Warning'
                    ? SeverityLevel.warning
                    : SeverityLevel.info,
              ),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  event.count > 1
                      ? '${event.reason} ${l.k8sEventCount(event.count)}'
                      : event.reason,
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              Text(relativeTimeOf(l, event.timestamp), style: muted),
            ],
          ),
          Text(
            '${event.objectKind} ${event.objectName}'
            '${event.namespace.isEmpty ? '' : ' · ${event.namespace}'}',
            style: muted,
          ),
          Text(event.message, style: theme.textTheme.bodySmall),
          const Divider(height: 16),
        ],
      ),
    );
  }
}

class _Workloads extends StatelessWidget {
  const _Workloads({required this.session, required this.sections});

  final SessionController session;
  final Sections sections;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = sections.k8sWorkloads;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListScreen<KubernetesWorkload>(
        controller: c,
        baseUrl: session.baseUrl ?? '',
        search: _Search(
          fieldKey: const Key('k8s-workloads-search'),
          hint: l.k8sWorkloadSearch,
          onSubmitted: (v) {
            c.query = v;
            c.refresh();
          },
        ),
        header: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Wrap(
              spacing: 6,
              runSpacing: 4,
              children: [
                _FilterChip(
                  chipKey: const Key('k8s-workload-namespace'),
                  label: l.k8sNamespace,
                  value: c.namespace,
                  options: sections.k8s.namespaces,
                  onPick: (v) {
                    c.namespace = v;
                    c.refresh();
                  },
                ),
                _FilterChip(
                  chipKey: const Key('k8s-workload-kind'),
                  label: l.k8sKind,
                  value: c.kind,
                  options: workloadKinds,
                  onPick: (v) {
                    c.kind = v;
                    c.refresh();
                  },
                ),
                _FilterChip(
                  chipKey: const Key('k8s-workload-health'),
                  label: l.k8sHealth,
                  value: c.health,
                  options: workloadHealths,
                  onPick: (v) {
                    c.health = v;
                    c.refresh();
                  },
                ),
              ],
            ),
            _Shown(shown: c.items.length, total: c.total),
          ],
        ),
        emptyTitle: l.k8sNoWorkloads,
        itemBuilder: (context, i) => workloadCard(
          context,
          c.items[i],
          onOpen: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => ScopedPodsScreen(
                session: session,
                sections: sections,
                title: c.items[i].name,
                clusterUid: c.items[i].clusterUid,
                namespace: c.items[i].namespace,
                workloadKind: c.items[i].kind,
                workloadName: c.items[i].name,
              ),
            ),
          ),
        ),
      ),
    );
  }
}

class _Pods extends StatelessWidget {
  const _Pods({required this.session, required this.sections});

  final SessionController session;
  final Sections sections;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = sections.pods;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListScreen<KubernetesPod>(
        controller: c,
        baseUrl: session.baseUrl ?? '',
        search: _Search(
          fieldKey: const Key('pods-search'),
          hint: l.k8sPodSearch,
          onSubmitted: (v) {
            c.query = v;
            c.refresh();
          },
        ),
        header: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Wrap(
              spacing: 6,
              runSpacing: 4,
              children: [
                _FilterChip(
                  chipKey: const Key('k8s-pod-namespace'),
                  label: l.k8sNamespace,
                  value: c.namespace,
                  options: sections.k8s.namespaces,
                  onPick: (v) {
                    c.namespace = v;
                    c.refresh();
                  },
                ),
                _FilterChip(
                  chipKey: const Key('k8s-pod-phase'),
                  label: l.k8sPhase,
                  value: c.phase,
                  options: podPhases,
                  onPick: (v) {
                    c.phase = v;
                    c.refresh();
                  },
                ),
              ],
            ),
            _Shown(shown: c.items.length, total: c.total),
          ],
        ),
        emptyTitle: l.podsEmpty,
        itemBuilder: (context, i) => podCard(
          context,
          c.items[i],
          onOpen: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => PodScreen(
                session: session,
                sections: sections,
                podUid: c.items[i].podUid,
                podName: c.items[i].podName,
              ),
            ),
          ),
        ),
      ),
    );
  }
}

class _Nodes extends StatelessWidget {
  const _Nodes({required this.session, required this.sections});

  final SessionController session;
  final Sections sections;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = sections.k8sNodes;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListScreen<KubernetesNode>(
        controller: c,
        baseUrl: session.baseUrl ?? '',
        search: _Search(
          fieldKey: const Key('k8s-nodes-search'),
          hint: l.k8sNodeSearch,
          onSubmitted: (v) {
            c.query = v;
            c.refresh();
          },
        ),
        header: _Shown(shown: c.items.length, total: c.total),
        emptyTitle: l.k8sNoNodes,
        itemBuilder: (context, i) => nodeCard(
          context,
          c.items[i],
          onOpen: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => ScopedPodsScreen(
                session: session,
                sections: sections,
                title: c.items[i].nodeName,
                node: c.items[i].nodeName,
                clusterUid: c.items[i].clusterUid,
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// What was cut off by the server's limit. Nothing when the list is all
/// of it, because "12 of 12" is noise.
class _Shown extends StatelessWidget {
  const _Shown({required this.shown, required this.total});

  final int shown;
  final int total;

  @override
  Widget build(BuildContext context) {
    if (total <= shown) return const SizedBox.shrink();
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(top: 6),
      child: Text(
        L.of(context).k8sShown(shown, total),
        key: const Key('k8s-shown'),
        style: theme.textTheme.bodySmall?.copyWith(
          color: theme.colorScheme.onSurfaceVariant,
        ),
      ),
    );
  }
}

class _Search extends StatefulWidget {
  const _Search({
    required this.fieldKey,
    required this.hint,
    required this.onSubmitted,
  });

  final Key fieldKey;
  final String hint;
  final ValueChanged<String> onSubmitted;

  @override
  State<_Search> createState() => _SearchState();
}

class _SearchState extends State<_Search> {
  final _controller = TextEditingController();

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => SearchField(
    fieldKey: widget.fieldKey,
    controller: _controller,
    hint: widget.hint,
    onSubmitted: widget.onSubmitted,
  );
}

/// The pods of one node or one workload, on a screen of its own.
///
/// A controller of its own, not the tab's: the tab is about the cluster and
/// this is about one thing in it, and sharing one would make going back
/// show the wrong list.
class ScopedPodsScreen extends StatefulWidget {
  const ScopedPodsScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.title,
    this.clusterUid = '',
    this.namespace = '',
    this.node = '',
    this.workloadKind = '',
    this.workloadName = '',
  });

  final SessionController session;
  final Sections sections;
  final String title;
  final String clusterUid;
  final String namespace;
  final String node;
  final String workloadKind;
  final String workloadName;

  @override
  State<ScopedPodsScreen> createState() => _ScopedPodsScreenState();
}

class _ScopedPodsScreenState extends State<ScopedPodsScreen> {
  late final PodsController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.scopedPods()
      ..clusterUid = widget.clusterUid
      ..namespace = widget.namespace
      ..node = widget.node
      ..workloadKind = widget.workloadKind
      ..workloadName = widget.workloadName;
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return Scaffold(
      appBar: AppBar(
        title: Text(widget.title, overflow: TextOverflow.ellipsis),
      ),
      body: ListenableBuilder(
        listenable: _c,
        builder: (context, _) => ListScreen<KubernetesPod>(
          controller: _c,
          baseUrl: widget.session.baseUrl ?? '',
          emptyTitle: l.podsEmpty,
          itemBuilder: (context, i) => podCard(
            context,
            _c.items[i],
            onOpen: () => Navigator.of(context).push(
              MaterialPageRoute<void>(
                builder: (_) => PodScreen(
                  session: widget.session,
                  sections: widget.sections,
                  podUid: _c.items[i].podUid,
                  podName: _c.items[i].podName,
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// One workload: what it is, how healthy, and what it is using.
Widget workloadCard(
  BuildContext context,
  KubernetesWorkload w, {
  required VoidCallback onOpen,
}) {
  final l = L.of(context);
  final colors = colorsOf(context);
  final level = switch (w.health) {
    KubernetesWorkloadHealth.healthy => SeverityLevel.info,
    KubernetesWorkloadHealth.degraded => SeverityLevel.warning,
    KubernetesWorkloadHealth.unavailable => SeverityLevel.critical,
    _ => SeverityLevel.info,
  };

  return SectionCard(
    cardKey: Key('k8s-workload-${w.clusterUid}-${w.namespace}-${w.name}'),
    onOpen: onOpen,
    title: w.name,
    subtitle: '${w.namespace} · ${w.kind}',
    trailing: relativeTimeOf(l, w.lastSeen),
    tags: [
      Tag(label: _healthLabel(l, w.health), level: level),
      if (!w.reporting)
        Tag(label: l.k8sNotReporting, level: SeverityLevel.warning),
    ],
    stats: [
      if (w.kind != 'CronJob')
        (
          label: l.k8sReplicas,
          value: '${w.ready}/${w.desired}',
          emphasis: null,
        ),
      (
        label: l.k8sRestarts,
        value: '${w.restarts}',
        emphasis: w.restarts > 0 ? colors.warningText : null,
      ),
      (label: l.k8sCpu, value: formatCores(w.cpuUsage), emphasis: null),
      (
        label: l.k8sMemory,
        value: w.memoryWorkingSet == null
            ? '—'
            : formatBytes(w.memoryWorkingSet!),
        emphasis: null,
      ),
    ],
    extra: w.cpuSparkline.length < 2
        ? null
        : Row(
            children: [
              // Labelled, because a line between a health chip and a row
              // of numbers could be either of the two numbers.
              Text(
                l.k8sCpuTrend,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: Theme.of(context).colorScheme.onSurfaceVariant,
                ),
              ),
              const SizedBox(width: 8),
              Expanded(
                child: SizedBox(
                  height: 26,
                  child: Sparkline(
                    key: Key('k8s-spark-${w.name}'),
                    values: [for (final p in w.cpuSparkline) p[1]],
                    color: colors.primary,
                  ),
                ),
              ),
            ],
          ),
  );
}

/// One node: whether it is ready, what runs on it and what it is using.
Widget nodeCard(
  BuildContext context,
  KubernetesNode n, {
  required VoidCallback onOpen,
}) {
  final l = L.of(context);
  final colors = colorsOf(context);
  final status = nodeStatus(n);
  final problems = nodeProblems(n);
  final cpu = usageRatio(n.cpuUsage, n.allocatableCpu);
  final memory = usageRatio(n.memoryWorkingSet, n.allocatableMemory);

  return SectionCard(
    cardKey: Key('k8s-node-${n.nodeUid}'),
    onOpen: onOpen,
    title: n.nodeName,
    subtitle: [
      if (n.roles.isNotEmpty) n.roles.join(', '),
      if (n.internalIp.isNotEmpty) n.internalIp,
      if (n.kubeletVersion.isNotEmpty) n.kubeletVersion,
    ].join(' · '),
    trailing: relativeTimeOf(l, n.lastSeen),
    tags: [
      Tag(
        label: _nodeStatusLabel(l, status),
        level: switch (status) {
          'ready' => SeverityLevel.info,
          'notReady' => SeverityLevel.critical,
          _ => SeverityLevel.warning,
        },
      ),
      if (n.unschedulable)
        Tag(label: l.k8sUnschedulable, level: SeverityLevel.warning),
      // The pressures are why a node nobody can schedule on is in that
      // state, and they are not in the Ready condition.
      for (final p in problems) Tag(label: p, level: SeverityLevel.warning),
    ],
    stats: [
      (
        label: l.k8sPods,
        value: n.allocatablePods == null
            ? '${n.pods}'
            : '${n.pods}/${n.allocatablePods!.round()}',
        emphasis: null,
      ),
      (
        label: l.k8sCpu,
        value: cpu == null
            ? formatCores(n.cpuUsage)
            : '${formatCores(n.cpuUsage)} · ${(cpu * 100).round()}%',
        emphasis: cpu != null && cpu >= 0.9 ? colors.destructiveText : null,
      ),
      (
        label: l.k8sMemory,
        value: n.memoryWorkingSet == null
            ? '—'
            : memory == null
            ? formatBytes(n.memoryWorkingSet!)
            : '${formatBytes(n.memoryWorkingSet!)} · '
                  '${(memory * 100).round()}%',
        emphasis: memory != null && memory >= 0.9
            ? colors.destructiveText
            : null,
      ),
    ],
  );
}

String _healthLabel(L l, KubernetesWorkloadHealth h) => switch (h) {
  KubernetesWorkloadHealth.healthy => l.k8sHealthy,
  KubernetesWorkloadHealth.degraded => l.k8sDegraded,
  KubernetesWorkloadHealth.unavailable => l.k8sUnavailable,
  _ => l.k8sUnknown,
};

String _nodeStatusLabel(L l, String status) => switch (status) {
  'ready' => l.k8sReady,
  'notReady' => l.k8sNotReady,
  'notReporting' => l.k8sNotReporting,
  _ => l.k8sUnknown,
};
