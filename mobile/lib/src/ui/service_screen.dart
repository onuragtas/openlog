// One service's golden signals: where an alert points, and the first screen
// that can say whether the thing it complained about is still happening.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';
import 'trace_screen.dart';

class ServiceScreen extends StatefulWidget {
  const ServiceScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.serviceName,
  });

  final SessionController session;
  final Sections sections;
  final String serviceName;

  @override
  State<ServiceScreen> createState() => _ServiceScreenState();
}

class _ServiceScreenState extends State<ServiceScreen>
    with SingleTickerProviderStateMixin {
  late final ServiceOverviewController _c;
  late final ServiceErrorsController _errors;
  late final TabController _tabs;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.serviceOverview(widget.serviceName);
    _errors = widget.sections.serviceErrors(widget.serviceName);
    _tabs = TabController(length: 2, vsync: this)..addListener(_loadErrors);
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  /// The inbox is asked for the first time the tab is looked at. Both tabs are
  /// built at once by TabBarView, so building is not the signal -- the web does
  /// not fetch the error inbox of a service nobody opened the tab of either.
  void _loadErrors() {
    if (_tabs.index == 1 && !_errors.loaded && !_errors.loadingFirst) {
      _errors.refresh();
    }
  }

  @override
  void dispose() {
    _tabs.removeListener(_loadErrors);
    _tabs.dispose();
    _errors.dispose();
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final baseUrl = widget.session.baseUrl ?? '';

    return Scaffold(
      appBar: AppBar(
        title: Text(widget.serviceName, overflow: TextOverflow.ellipsis),
        bottom: TabBar(
          controller: _tabs,
          tabs: [
            Tab(key: const Key('tab-overview'), text: l.serviceTabOverview),
            Tab(key: const Key('tab-errors'), text: l.serviceTabErrors),
          ],
        ),
      ),
      body: TabBarView(
        controller: _tabs,
        children: [
          ListenableBuilder(
            listenable: _c,
            builder: (context, _) => DetailBody<ApmOverview>(
              controller: _c,
              baseUrl: baseUrl,
              builder: (context, overview) => _body(context, l, overview),
            ),
          ),
          ListenableBuilder(
            listenable: _errors,
            builder: (context, _) => DetailBody<ApmErrorInbox>(
              controller: _errors,
              baseUrl: baseUrl,
              builder: (context, inbox) => _errorList(context, l, inbox),
            ),
          ),
        ],
      ),
    );
  }

  List<Widget> _errorList(BuildContext context, L l, ApmErrorInbox inbox) {
    final groups = _errors.groups;
    if (groups.isEmpty) {
      return [
        const SizedBox(height: 48),
        Text(
          l.errorsEmpty,
          textAlign: TextAlign.center,
          style: Theme.of(context).textTheme.titleMedium,
        ),
      ];
    }
    return [
      for (final g in groups)
        _ErrorCard(
          key: Key('error-${g.groupId}'),
          group: g,
          onOpenTrace: g.lastTraceId.isEmpty
              ? null
              : () => Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => TraceScreen(
                      session: widget.session,
                      sections: widget.sections,
                      traceId: g.lastTraceId,
                    ),
                  ),
                ),
        ),
      if (inbox.truncated)
        Padding(
          padding: const EdgeInsets.only(top: 8),
          child: Text(
            l.errorsTruncated,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: Theme.of(context).colorScheme.onSurfaceVariant,
            ),
          ),
        ),
    ];
  }

  List<Widget> _body(BuildContext context, L l, ApmOverview o) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final t = o.totals;

    // No requests at all is a real answer, not an empty screen: it is what a
    // service that has stopped serving looks like, and saying so beats four
    // zeroes the person has to interpret.
    if (t.requests == 0) {
      return [
        const SizedBox(height: 48),
        Text(
          l.serviceNoData,
          textAlign: TextAlign.center,
          style: theme.textTheme.titleMedium,
        ),
      ];
    }

    final errorPercent = t.errorRate * 100;
    return [
      const SizedBox(height: 8),
      Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Stat(
              label: l.svcThroughput,
              value: t.throughput.toStringAsFixed(t.throughput >= 100 ? 0 : 1),
            ),
          ),
          Expanded(
            child: Stat(
              label: l.serviceErrors,
              value:
                  '${errorPercent.toStringAsFixed(errorPercent >= 10 ? 0 : 1)}%',
              // The web colours an error rate rather than leaving the reader to
              // compare it: anything above a percent is worth looking at.
              color: errorPercent >= 1
                  ? severityTextColor(context, SeverityLevel.critical)
                  : null,
            ),
          ),
          Expanded(
            child: Stat(
              label: l.serviceRequests,
              value: t.requests.toStringAsFixed(0),
            ),
          ),
        ],
      ),
      const SizedBox(height: 18),
      Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(child: _ms(l.serviceLatency, t.p50Ms, 'p50')),
          Expanded(child: _ms('', t.p95Ms, 'p95')),
          Expanded(child: _ms('', t.p99Ms, 'p99')),
          if (t.apdex != null)
            Expanded(
              child: Stat(
                label: l.svcApdex,
                value: t.apdex!.toStringAsFixed(2),
              ),
            ),
        ],
      ),

      if (o.series.length >= 2) ...[
        _chart(context, l.serviceThroughputChart, [
          for (final p in o.series) p.throughput,
        ], colors.primary),
        _chart(
          context,
          l.serviceErrorRateChart,
          [for (final p in o.series) p.errorRate * 100],
          severityTextColor(context, SeverityLevel.critical),
        ),
      ],
    ];
  }

  /// A latency column. [label] is empty for the second and third, so the three
  /// read as one group under one heading rather than as three unrelated numbers.
  Widget _ms(String label, double? value, String percentile) => Stat(
    label: label.isEmpty ? percentile : '$label $percentile',
    value: value == null
        ? '-'
        : '${value.toStringAsFixed(value >= 100 ? 0 : 1)} ms',
  );

  Widget _chart(
    BuildContext context,
    String title,
    List<double> values,
    Color color,
  ) => DetailSection(
    title: title,
    children: [
      SizedBox(
        height: 64,
        child: Sparkline(values: values, color: color),
      ),
    ],
  );
}

/// One error group: what threw, how often, and the way into the request it
/// happened in -- which is where the chain that started with an alert ends.
class _ErrorCard extends StatelessWidget {
  const _ErrorCard({super.key, required this.group, required this.onOpenTrace});

  final ApmErrorGroup group;
  final VoidCallback? onOpenTrace;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final (label, level) = switch (group.status) {
      ApmErrorStatus.unresolved => (
        l.errorStatusUnresolved,
        SeverityLevel.critical,
      ),
      ApmErrorStatus.resolved => (l.errorStatusResolved, SeverityLevel.info),
      ApmErrorStatus.ignored => (l.errorStatusIgnored, SeverityLevel.unknown),
      ApmErrorStatus.unknown => (l.errorStatusUnknown, SeverityLevel.unknown),
    };

    return Card(
      margin: const EdgeInsets.symmetric(vertical: 6),
      child: InkWell(
        onTap: onOpenTrace,
        borderRadius: BorderRadius.circular(Radii.lg),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Wrap(
                spacing: 8,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Tag(label: label, level: level),
                  Text(
                    l.errorsOccurrences(group.count.round()),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                  if (group.lastSeen != null)
                    Text(
                      l.errorsLastSeen(relativeTimeOf(l, group.lastSeen!)),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                ],
              ),
              const SizedBox(height: 8),
              Text(group.errorType, style: theme.textTheme.titleSmall),
              if (group.message.isNotEmpty) ...[
                const SizedBox(height: 4),
                Text(
                  group.message,
                  maxLines: 3,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.bodyMedium,
                ),
              ],
              const SizedBox(height: 6),
              Text(
                // A group whose sample has aged out of retention has no trace
                // to open, and a card that does nothing when tapped is worse
                // than one that says why.
                onOpenTrace == null ? l.errorsNoTrace : group.lastSpanName,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: onOpenTrace == null
                      ? theme.colorScheme.onSurfaceVariant
                      : theme.colorScheme.primary,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
