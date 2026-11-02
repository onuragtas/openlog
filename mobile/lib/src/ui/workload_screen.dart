// One workload: how many of it are ready, what it is using, its pods and what
// happened to it. A row of the workloads list opens this, as on the web.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../kubernetes.dart';
import '../sections.dart';
import '../session.dart';
import 'container_screen.dart' show formatBytes;
import 'detail_scaffold.dart';
import 'kubernetes_screen.dart';
import 'pod_screen.dart';
import 'sections_screen.dart';
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';

class WorkloadScreen extends StatefulWidget {
  const WorkloadScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.clusterUid,
    required this.namespace,
    required this.kind,
    required this.name,
  });

  final SessionController session;
  final Sections sections;
  final String clusterUid;
  final String namespace;
  final String kind;
  final String name;

  @override
  State<WorkloadScreen> createState() => _WorkloadScreenState();
}

class _WorkloadScreenState extends State<WorkloadScreen> {
  late final _c = KubernetesWorkloadController(
    widget.sections.client,
    clusterUid: widget.clusterUid,
    namespace: widget.namespace,
    kind: widget.kind,
    name: widget.name,
  );

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: _c,
    builder: (context, _) => DetailScreen<KubernetesWorkloadDetail>(
      controller: _c,
      baseUrl: widget.session.baseUrl ?? '',
      title: widget.name,
      subtitle: '${widget.kind} · ${widget.namespace}',
      builder: (context, w) => _body(context, w),
    ),
  );

  List<Widget> _body(BuildContext context, KubernetesWorkloadDetail w) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final level = switch (w.health) {
      KubernetesWorkloadHealth.healthy => SeverityLevel.good,
      KubernetesWorkloadHealth.degraded => SeverityLevel.warning,
      KubernetesWorkloadHealth.unavailable => SeverityLevel.critical,
      _ => SeverityLevel.unknown,
    };
    final isJob = w.kind == 'Job';
    final s = _c.series?.series;
    final hpa = w.hpa;
    // A blank number on an autoscaler is "not known yet", not zero.
    String n(double? v) => v == null ? '–' : v.toStringAsFixed(0);

    return [
      Wrap(
        spacing: 8,
        runSpacing: 4,
        children: [
          Tag(label: w.health.wire, level: level),
          if (!w.reporting)
            Tag(label: l.k8sNotReporting, level: SeverityLevel.warning),
          if (w.clusterName.isNotEmpty) Text(w.clusterName, style: muted),
        ],
      ),
      const SizedBox(height: 12),
      KeyValue(
        name: l.workloadReplicas,
        value: replicasText(w.ready, w.desired),
      ),
      KeyValue(
        name: isJob ? l.workloadSucceeded : l.workloadAvailable,
        value: '${w.available}',
      ),
      KeyValue(
        name: isJob ? l.workloadFailed : l.workloadUpdated,
        value: '${w.updated}',
      ),
      if (w.restarts > 0)
        KeyValue(name: l.statRestarts, value: '${w.restarts}'),
      if (hpa != null)
        KeyValue(
          name: l.workloadHpa,
          value: l.workloadHpaValue(
            hpa.name,
            n(hpa.currentReplicas),
            n(hpa.desiredReplicas),
            n(hpa.minReplicas),
            n(hpa.maxReplicas),
          ),
        ),
      if (_c.seriesError != null)
        DetailSection(
          title: l.podCpu,
          children: [
            Text(
              _c.seriesError!,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            ),
          ],
        )
      else if (s != null) ...[
        _spark(
          context,
          l.podCpu,
          pointValues(s.cpuUsage),
          (v) => formatCores(v),
          colors.primary,
        ),
        _spark(
          context,
          l.podMemory,
          pointValues(s.memoryWorkingSet),
          formatBytes,
          colors.primary,
        ),
      ],
      DetailSection(
        title: l.workloadPods,
        children: [
          if (w.podList.isEmpty) Text(l.workloadPodsEmpty, style: muted),
          for (final p in w.podList)
            podCard(
              context,
              p,
              onOpen: () => Navigator.of(context).push(
                MaterialPageRoute<void>(
                  builder: (_) => PodScreen(
                    session: widget.session,
                    sections: widget.sections,
                    podUid: p.podUid,
                    podName: p.podName,
                  ),
                ),
              ),
            ),
          Align(
            alignment: Alignment.centerLeft,
            child: TextButton(
              key: const Key('workload-all-pods'),
              onPressed: () => Navigator.of(context).push(
                MaterialPageRoute<void>(
                  builder: (_) => ScopedPodsScreen(
                    session: widget.session,
                    sections: widget.sections,
                    title: w.name,
                    clusterUid: w.clusterUid,
                    namespace: w.namespace,
                    workloadKind: w.kind,
                    workloadName: w.name,
                  ),
                ),
              ),
              child: Text(l.workloadAllPods),
            ),
          ),
        ],
      ),
      DetailSection(
        title: l.workloadEvents,
        children: [
          if (_c.eventsError != null)
            Text(
              _c.eventsError!,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            )
          else if (_c.events.isEmpty)
            Text(l.workloadEventsEmpty, style: muted),
          for (final e in _c.events) K8sEventRow(event: e),
        ],
      ),
      if (w.attributes.isNotEmpty)
        DetailSection(
          title: l.hostAttributes,
          children: [
            for (final e in w.attributes.entries)
              KeyValue(name: e.key, value: e.value),
          ],
        ),
    ];
  }

  Widget _spark(
    BuildContext context,
    String title,
    List<double> values,
    String Function(double) format,
    Color color,
  ) {
    final theme = Theme.of(context);
    return DetailSection(
      title: title,
      children: [
        if (values.length < 2)
          Text(
            L.of(context).containerNoSeries,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          )
        else ...[
          Text(format(values.last), style: theme.textTheme.titleMedium),
          const SizedBox(height: 8),
          SizedBox(
            height: 44,
            child: Sparkline(values: values, color: color),
          ),
        ],
      ],
    );
  }
}
