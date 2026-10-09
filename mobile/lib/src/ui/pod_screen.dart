// One pod: what it is, what it has been doing, and why it is not.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../sections.dart';
import '../session.dart';
import 'container_screen.dart' show formatBytes;
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';

class PodScreen extends StatefulWidget {
  const PodScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.podUid,
    required this.podName,
  });

  final SessionController session;
  final Sections sections;
  final String podUid;
  final String podName;

  @override
  State<PodScreen> createState() => _PodScreenState();
}

class _PodScreenState extends State<PodScreen> {
  late final PodController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.pod(widget.podUid);
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

    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => DetailScreen<KubernetesPodDetail>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: _c.value?.podName ?? widget.podName,
        subtitle: _c.value == null
            ? null
            : '${_c.value!.namespace} · ${_c.value!.clusterName}',
        builder: (context, pod) => _body(context, l, pod),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, KubernetesPodDetail pod) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final s = _c.series?.series;

    return [
      const SizedBox(height: 4),
      Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Tag(
            label: pod.phase,
            level: switch (pod.phase) {
              'Running' || 'Succeeded' => SeverityLevel.good,
              'Failed' => SeverityLevel.critical,
              'Pending' => SeverityLevel.warning,
              _ => SeverityLevel.unknown,
            },
          ),
          Tag(
            label: pod.ready ? l.podReady : l.podNotReady,
            level: pod.ready ? SeverityLevel.good : SeverityLevel.warning,
          ),
          // The reason is the short form of what the events say at length,
          // and it is the first thing worth reading on a pod that is stuck.
          if (pod.reason.isNotEmpty)
            Text(
              pod.reason,
              style: theme.textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, SeverityLevel.critical),
              ),
            ),
          if (pod.restarts > 0)
            Text(
              '${l.podRestartsLabel}: ${pod.restarts}',
              style: theme.textTheme.bodySmall?.copyWith(
                color: pod.restarts >= 5
                    ? severityTextColor(context, SeverityLevel.warning)
                    : theme.colorScheme.onSurfaceVariant,
              ),
            ),
        ],
      ),
      const SizedBox(height: 10),
      Wrap(
        spacing: 12,
        runSpacing: 4,
        children: [
          if (pod.workloadName.isNotEmpty)
            Text('${pod.workloadKind}/${pod.workloadName}', style: muted),
          if (pod.nodeName.isNotEmpty)
            Text('${l.podNode}: ${pod.nodeName}', style: muted),
          if (pod.podIp.isNotEmpty) Text(pod.podIp, style: muted),
          if (pod.qosClass.isNotEmpty) Text(pod.qosClass, style: muted),
        ],
      ),

      // Events first, above the charts: a pod nobody opened for fun is a pod
      // that is wrong, and the answer is here rather than in a line graph.
      DetailSection(
        title: l.podEvents,
        children: [
          if (_c.eventsError != null)
            Text(
              l.podEventsFailed(_c.eventsError!),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            )
          else if (_c.sortedEvents.isEmpty)
            Text(l.podNoEvents, style: muted)
          else
            for (final e in _c.sortedEvents.take(20))
              _Event(event: e, key: Key('podevent-${e.timestamp}-${e.reason}')),
        ],
      ),

      DetailSection(
        title: l.podContainers,
        children: [
          for (final c in pod.containers)
            _Container(container: c, key: Key('podc-${c.name}')),
        ],
      ),

      if (s != null) ...[
        if (ContainerController.values(s.cpuUsage).length >= 2)
          DetailSection(
            title: l.podCpu,
            children: [
              SizedBox(
                height: 44,
                child: Sparkline(
                  values: ContainerController.values(s.cpuUsage),
                  color: colors.primary,
                ),
              ),
            ],
          ),
        if (ContainerController.values(s.memoryWorkingSet).length >= 2)
          DetailSection(
            title: l.podMemory,
            children: [
              Text(
                formatBytes(
                  ContainerController.values(s.memoryWorkingSet).last,
                ),
                style: theme.textTheme.titleMedium,
              ),
              const SizedBox(height: 8),
              SizedBox(
                height: 44,
                child: Sparkline(
                  values: ContainerController.values(s.memoryWorkingSet),
                  color: colors.primary,
                ),
              ),
            ],
          ),
      ],

      if (pod.services.isNotEmpty)
        DetailSection(
          title: l.podServices,
          children: [
            for (final svc in pod.services)
              Text(
                svc.serviceName,
                style: theme.textTheme.bodyMedium?.copyWith(
                  color: theme.colorScheme.primary,
                ),
              ),
          ],
        ),

      if (pod.labels.isNotEmpty)
        DetailSection(
          title: l.podLabels,
          children: [
            for (final e in pod.labels.entries)
              KeyValue(name: e.key, value: e.value),
          ],
        ),
    ];
  }
}

class _Event extends StatelessWidget {
  const _Event({super.key, required this.event});

  final KubernetesEvent event;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final warning = event.type == 'Warning';

    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Wrap(
            spacing: 8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Text(
                event.reason,
                style: theme.textTheme.bodyMedium?.copyWith(
                  fontWeight: FontWeight.w600,
                  color: warning
                      ? severityTextColor(context, SeverityLevel.critical)
                      : null,
                ),
              ),
              // A reason that happened forty times is a different problem
              // from one that happened once.
              if (event.count > 1)
                Text(
                  l.podEventCount(event.count),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              Text(
                relativeTimeOf(l, event.timestamp),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ],
          ),
          if (event.message.isNotEmpty)
            Text(event.message, style: theme.textTheme.bodySmall),
        ],
      ),
    );
  }
}

class _Container extends StatelessWidget {
  const _Container({super.key, required this.container});

  final KubernetesPodContainer container;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  container.name,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              const SizedBox(width: 8),
              Tag(
                label: container.ready ? l.podReady : container.state,
                level: container.ready
                    ? SeverityLevel.good
                    : SeverityLevel.warning,
              ),
            ],
          ),
          Text(
            container.image,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: muted,
          ),
          Wrap(
            spacing: 8,
            children: [
              if (container.restarts > 0)
                Text(
                  '${l.podRestartsLabel}: ${container.restarts}',
                  style: muted,
                ),
              if (container.reason.isNotEmpty)
                Text(
                  container.reason,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: severityTextColor(context, SeverityLevel.critical),
                  ),
                ),
            ],
          ),
        ],
      ),
    );
  }
}
