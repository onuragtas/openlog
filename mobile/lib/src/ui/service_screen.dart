// One service's golden signals: where an alert points, and the first screen
// that can say whether the thing it complained about is still happening.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../sections.dart';
import '../services.dart';
import '../session.dart';
import 'deployments_card.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'service_lists_tabs.dart';
import 'service_map_tab.dart';
import 'service_traces_tab.dart';
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
  late final ServiceTransactionsController _transactions;
  late final ServiceErrorsController _errors;
  late final ServiceDatabasesController _databases;
  late final ServiceTracesController _traces;
  late final ServiceMapController _map;
  late final ServiceDeploymentsController _deployments;
  late final TabController _tabs;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.serviceOverview(widget.serviceName);
    _transactions = widget.sections.serviceTransactions(widget.serviceName);
    _errors = widget.sections.serviceErrors(widget.serviceName);
    _databases = widget.sections.serviceDatabases(widget.serviceName);
    _traces = widget.sections.serviceTraces(widget.serviceName);
    _map = widget.sections.serviceMap(widget.serviceName);
    _deployments = widget.sections.serviceDeployments(widget.serviceName);
    _tabs = TabController(length: 6, vsync: this)..addListener(_loadTab);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _c.refresh();
      // The deployments are part of the overview, as on the web: a version
      // that went out ten minutes ago is the first thing to suspect.
      _deployments.refresh();
      // The top transactions are on the overview too, so the list the tab
      // uses is loaded with it rather than when the tab is opened.
      _transactions.refresh();
    });
  }

  /// A tab is asked for the first time it is looked at. Every tab is built at
  /// once by TabBarView, so building is not the signal -- the web does not
  /// fetch the error inbox of a service nobody opened the tab of either.
  void _loadTab() {
    // The web's order: overview, transactions, errors, databases, map,
    // traces. Each one is asked for the first time somebody looks at it.
    // Written out rather than switched over a common type, because these
    // controllers have no supertype that carries `refresh` with it.
    switch (_tabs.index) {
      case 1:
        // Already asked for with the overview, which shows the top of it.
        break;
      case 2:
        if (!_errors.loaded && !_errors.loadingFirst) _errors.refresh();
      case 3:
        if (!_databases.loaded && !_databases.loadingFirst) {
          _databases.refresh();
        }
      case 4:
        if (!_map.loaded && !_map.loadingFirst) _map.refresh();
      case 5:
        if (!_traces.loaded && !_traces.loadingFirst) _traces.refresh();
    }
  }

  @override
  void dispose() {
    _tabs.removeListener(_loadTab);
    _tabs.dispose();
    _deployments.dispose();
    _map.dispose();
    _traces.dispose();
    _databases.dispose();
    _errors.dispose();
    _transactions.dispose();
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
          isScrollable: true,
          tabAlignment: TabAlignment.start,
          tabs: [
            Tab(key: const Key('tab-overview'), text: l.serviceTabOverview),
            Tab(
              key: const Key('tab-transactions'),
              text: l.serviceTabTransactions,
            ),
            Tab(key: const Key('tab-errors'), text: l.serviceTabErrors),
            Tab(key: const Key('tab-databases'), text: l.serviceTabDatabases),
            Tab(key: const Key('tab-map'), text: l.serviceTabMap),
            Tab(key: const Key('tab-traces'), text: l.serviceTabTraces),
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
          ServiceTransactionsTab(
            session: widget.session,
            controller: _transactions,
          ),
          ListenableBuilder(
            listenable: _errors,
            builder: (context, _) => DetailBody<ApmErrorInbox>(
              controller: _errors,
              baseUrl: baseUrl,
              builder: (context, inbox) => _errorList(context, l, inbox),
            ),
          ),
          ServiceDatabasesTab(session: widget.session, controller: _databases),
          ServiceMapTab(
            session: widget.session,
            sections: widget.sections,
            controller: _map,
          ),
          ServiceTracesTab(
            session: widget.session,
            sections: widget.sections,
            controller: _traces,
            // The map answers "where does this transaction go"; the traces
            // tab is where a transaction name is in front of somebody.
            onShowPath: (transaction) {
              _map.loadPath(transaction);
              if (!_map.loaded && !_map.loadingFirst) _map.refresh();
              _tabs.animateTo(4);
            },
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
        // The web draws four: throughput, latency, error rate and Apdex.
        if (o.series.any((p) => p.p95Ms != null))
          _chart(context, l.serviceLatencyChart, [
            for (final p in o.series) p.p95Ms ?? 0,
          ], colors.primary),
        _chart(
          context,
          l.serviceErrorRateChart,
          [for (final p in o.series) p.errorRate * 100],
          severityTextColor(context, SeverityLevel.critical),
        ),
        if (o.series.any((p) => p.apdex != null))
          _chart(context, l.serviceApdexChart, [
            for (final p in o.series) p.apdex ?? 0,
          ], colors.primary),
      ],
      DeploymentsCard(session: widget.session, controller: _deployments),
      _topTransactions(context, l),
    ];
  }

  /// The handful of transactions the service spends most of its time on,
  /// with a way to the whole list -- the web's overview ends the same way.
  Widget _topTransactions(BuildContext context, L l) => ListenableBuilder(
    listenable: _transactions,
    builder: (context, _) {
      final theme = Theme.of(context);
      final top = _transactions.items.take(5).toList();
      if (top.isEmpty) return const SizedBox.shrink();
      return Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const SizedBox(height: 18),
          Row(
            children: [
              Expanded(
                child: Text(
                  l.serviceTopTransactions,
                  style: theme.textTheme.titleSmall,
                ),
              ),
              TextButton(
                key: const Key('service-all-transactions'),
                onPressed: () => _tabs.animateTo(1),
                child: Text(l.serviceAllTransactions),
              ),
            ],
          ),
          for (final t in top)
            Padding(
              key: Key('top-transaction-${t.transactionName}'),
              padding: const EdgeInsets.symmetric(vertical: 4),
              child: Row(
                children: [
                  Expanded(
                    child: Text(
                      t.transactionName,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.bodyMedium,
                    ),
                  ),
                  const SizedBox(width: 8),
                  Text(
                    l.transactionsShare(_share(t.timeShare)),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ],
              ),
            ),
        ],
      );
    },
  );

  static String _share(double ratio) {
    final v = ratio * 100;
    if (v == 0) return '0';
    return v >= 10 ? v.toStringAsFixed(0) : v.toStringAsFixed(1);
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
