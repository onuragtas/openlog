// One container: what it is, and what it has been doing.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'logs_screen.dart';
import 'oql_view.dart';
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';

class ContainerScreen extends StatefulWidget {
  const ContainerScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.containerId,
    required this.name,
  });

  final SessionController session;
  final Sections sections;
  final String containerId;
  final String name;

  @override
  State<ContainerScreen> createState() => _ContainerScreenState();
}

class _ContainerScreenState extends State<ContainerScreen> {
  late final ContainerController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.container(widget.containerId);
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
      builder: (context, _) => DetailScreen<ContainerDetail>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: _c.value?.name.isNotEmpty == true ? _c.value!.name : widget.name,
        subtitle: _c.value == null
            ? null
            : l.containerOn(
                _c.value!.hostName.isEmpty
                    ? _c.value!.hostId
                    : _c.value!.hostName,
              ),
        builder: (context, x) => _body(context, l, x),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, ContainerDetail x) {
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
            label: x.state,
            level: x.state == 'running'
                ? SeverityLevel.good
                : SeverityLevel.unknown,
          ),
          if (x.health.isNotEmpty)
            Tag(
              label: x.health,
              level: x.health == 'healthy'
                  ? SeverityLevel.good
                  : SeverityLevel.critical,
            ),
          // A container that keeps restarting is the thing worth noticing,
          // and the count is the only place it shows.
          if (x.restartCount > 0)
            Text(
              l.containerRestarts(x.restartCount),
              style: theme.textTheme.bodySmall?.copyWith(
                color: x.restartCount >= 5
                    ? severityTextColor(context, SeverityLevel.warning)
                    : theme.colorScheme.onSurfaceVariant,
              ),
            ),
          Text(relativeTimeOf(l, x.lastSeen), style: muted),
        ],
      ),

      const SizedBox(height: 12),
      Align(
        alignment: Alignment.centerLeft,
        child: OutlinedButton.icon(
          key: const Key('container-logs'),
          onPressed: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => LogsScreen(
                session: widget.session,
                sections: widget.sections,
                logs: widget.sections.scopedLogs(
                  containerId: widget.containerId,
                ),
                title: l.logsOpenForTrace,
                scopeLabel: l.logsScopedContainer,
              ),
            ),
          ),
          icon: const Icon(Icons.article_outlined, size: 18),
          label: Text(l.logsScopedContainer),
        ),
      ),

      DetailSection(
        title: l.containerImage,
        children: [
          Text(x.imageName, style: theme.textTheme.bodyMedium),
          if (x.imageTags.isNotEmpty)
            Text(x.imageTags.join(', '), style: muted),
          if (x.composeProject.isNotEmpty)
            Text('${x.composeProject} / ${x.composeService}', style: muted),
          if (x.k8sPodName.isNotEmpty)
            Text('${x.k8sNamespaceName} / ${x.k8sPodName}', style: muted),
        ],
      ),

      if (_c.seriesError != null)
        DetailSection(
          title: l.containerCpu,
          children: [
            Text(
              l.containerSeriesFailed(_c.seriesError!),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            ),
          ],
        )
      else if (s == null)
        const SizedBox.shrink()
      else ...[
        _chart(
          context,
          l.containerCpu,
          ContainerController.values(s.cpuUtilization),
          colors.primary,
          percent: true,
        ),
        DetailSection(
          title: l.containerMemory,
          children: [
            if (_c.memoryShare.length >= 2) ...[
              Text(
                '${(_c.memoryShare.last * 100).toStringAsFixed(0)}%',
                style: theme.textTheme.headlineSmall,
              ),
              const SizedBox(height: 8),
              SizedBox(
                height: 48,
                child: Sparkline(
                  key: const Key('container-memory'),
                  values: _c.memoryShare,
                  color: colors.primary,
                ),
              ),
            ] else ...[
              // No limit means no ceiling to be a share of; bytes against
              // nothing would invent one.
              Text(l.containerNoLimit, style: muted),
              if (ContainerController.values(s.memoryUsage).isNotEmpty)
                Text(
                  formatBytes(ContainerController.values(s.memoryUsage).last),
                  style: theme.textTheme.titleMedium,
                ),
            ],
          ],
        ),
        _pair(
          context,
          l.containerNetwork,
          ContainerController.values(s.networkReceive),
          ContainerController.values(s.networkTransmit),
          legend: l.containerRxTx,
        ),
        _pair(
          context,
          l.containerDisk,
          ContainerController.values(s.blockioRead),
          ContainerController.values(s.blockioWrite),
          // Block I/O is read and write, not in and out: the same two lines
          // mean a different pair of things here.
          legend: l.containerReadWrite,
        ),
      ],

      if (x.attributes.isNotEmpty)
        DetailSection(
          title: l.containerAttributes,
          children: [
            for (final e in x.attributes.entries)
              KeyValue(name: e.key, value: e.value),
          ],
        ),
    ];
  }

  Widget _chart(
    BuildContext context,
    String title,
    List<double> values,
    Color color, {
    bool percent = false,
  }) {
    final l = L.of(context);
    final theme = Theme.of(context);
    if (values.length < 2) {
      return DetailSection(
        title: title,
        children: [
          Text(
            l.containerNoSeries,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ],
      );
    }
    return DetailSection(
      title: title,
      children: [
        Text(
          percent
              ? '${(values.last * 100).toStringAsFixed(0)}%'
              : formatNumber(values.last),
          style: theme.textTheme.headlineSmall,
        ),
        const SizedBox(height: 8),
        SizedBox(
          height: 48,
          child: Sparkline(values: values, color: color),
        ),
      ],
    );
  }

  /// Two directions on one line: in and out are read together or not at all.
  Widget _pair(
    BuildContext context,
    String title,
    List<double> inValues,
    List<double> outValues, {
    required String legend,
  }) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    if (inValues.length < 2 && outValues.length < 2) {
      return DetailSection(
        title: title,
        children: [
          Text(
            l.containerNoSeries,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ],
      );
    }
    return DetailSection(
      title: title,
      children: [
        Text(
          '${formatBytes(inValues.isEmpty ? 0 : inValues.last)}/s'
          '  ·  '
          '${formatBytes(outValues.isEmpty ? 0 : outValues.last)}/s',
          style: theme.textTheme.titleMedium,
        ),
        Text(
          legend,
          style: theme.textTheme.bodySmall?.copyWith(
            color: theme.colorScheme.onSurfaceVariant,
          ),
        ),
        const SizedBox(height: 8),
        if (inValues.length >= 2)
          SizedBox(
            height: 36,
            child: Sparkline(values: inValues, color: colors.primary),
          ),
        if (outValues.length >= 2)
          SizedBox(
            height: 36,
            child: Sparkline(values: outValues, color: colors.mutedForeground),
          ),
      ],
    );
  }
}

/// Bytes in the unit a person would say out loud.
String formatBytes(double v) {
  if (v >= 1 << 30) return '${(v / (1 << 30)).toStringAsFixed(2)} GiB';
  if (v >= 1 << 20) return '${(v / (1 << 20)).toStringAsFixed(1)} MiB';
  if (v >= 1 << 10) return '${(v / (1 << 10)).toStringAsFixed(0)} KiB';
  return '${v.toStringAsFixed(0)} B';
}
