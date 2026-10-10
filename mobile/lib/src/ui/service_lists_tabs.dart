// Two tabs of the service screen that are the same shape: a sort, then a
// list of rows with the same four numbers on each.
//
// İşlemler is what the service spends its time on; Veritabanları is what it
// asks the database. The web has both as tables with sortable columns; a
// phone sorts with a chip row, because a table of eight columns on a 390
// point screen is a table nobody reads.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../services.dart';
import '../session.dart';
import 'failure_text.dart';
import 'severity.dart';

/// The sorts the transactions endpoint takes, in the web's order.
const transactionSorts = <String>['time', 'throughput', 'slowest', 'errors'];

/// The sorts the databases endpoint takes. `calls` where transactions have
/// `throughput`: the server names them differently and this is not the place
/// to invent a common word for them.
const databaseSorts = <String>['time', 'calls', 'slowest', 'errors'];

class ServiceTransactionsTab extends StatelessWidget {
  const ServiceTransactionsTab({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final ServiceTransactionsController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);

    return ListenableBuilder(
      listenable: controller,
      builder: (context, _) => _SortedList(
        baseUrl: session.baseUrl ?? '',
        failure: controller.failure,
        loading: controller.loadingFirst,
        empty: controller.items.isEmpty,
        emptyText: l.transactionsEmpty,
        emptyKey: const Key('transactions-empty'),
        onRefresh: controller.refresh,
        sorts: transactionSorts,
        selected: controller.sort,
        keyPrefix: 'transactions-sort',
        label: (s) => switch (s) {
          'throughput' => l.sortThroughput,
          'slowest' => l.sortSlowest,
          'errors' => l.sortErrors,
          _ => l.sortTimeConsumed,
        },
        onSort: (s) {
          controller.sort = s;
          controller.refresh();
        },
        count: controller.items.length,
        row: (context, i) {
          final t = controller.items[i];
          return _Row(
            rowKey: Key('transaction-${t.transactionName}'),
            title: t.transactionName,
            subtitle: t.transactionType,
            // The share is why this list is sorted by time consumed: one
            // transaction at 60% is the service's whole story.
            lead: l.transactionsShare(_showPercent(t.timeShare)),
            requests: t.requests,
            errorRate: t.errorRate,
            p95Ms: t.p95Ms,
            avgMs: t.avgMs,
          );
        },
      ),
    );
  }
}

class ServiceDatabasesTab extends StatelessWidget {
  const ServiceDatabasesTab({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final ServiceDatabasesController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);

    return ListenableBuilder(
      listenable: controller,
      builder: (context, _) => _SortedList(
        baseUrl: session.baseUrl ?? '',
        failure: controller.failure,
        loading: controller.loadingFirst,
        empty: controller.items.isEmpty,
        emptyText: l.serviceDatabasesEmpty,
        emptyKey: const Key('service-databases-empty'),
        onRefresh: controller.refresh,
        sorts: databaseSorts,
        selected: controller.sort,
        keyPrefix: 'service-databases-sort',
        label: (s) => switch (s) {
          'calls' => l.sortCalls,
          'slowest' => l.sortSlowest,
          'errors' => l.sortErrors,
          _ => l.sortTimeConsumed,
        },
        onSort: (s) {
          controller.sort = s;
          controller.refresh();
        },
        count: controller.items.length,
        row: (context, i) {
          final q = controller.items[i];
          return _Row(
            rowKey: Key('db-query-$i'),
            // The normalized statement is the identity of the row; the
            // server strips the values out of it, so it is safe to show.
            title: q.statement,
            subtitle: [
              q.dbSystem,
              if (q.dbName.isNotEmpty) q.dbName,
              if (q.dbOperation.isNotEmpty) q.dbOperation,
            ].join(' · '),
            lead: l.transactionsShare(_showPercent(q.timeShare)),
            requests: q.calls,
            errorRate: q.errorRate,
            p95Ms: q.p95Ms,
            avgMs: q.avgMs,
            titleMaxLines: 3,
          );
        },
      ),
    );
  }
}

/// The shape both tabs share: a sort chip row above a list.
class _SortedList extends StatelessWidget {
  const _SortedList({
    required this.baseUrl,
    required this.failure,
    required this.loading,
    required this.empty,
    required this.emptyText,
    required this.emptyKey,
    required this.onRefresh,
    required this.sorts,
    required this.selected,
    required this.keyPrefix,
    required this.label,
    required this.onSort,
    required this.count,
    required this.row,
  });

  final String baseUrl;
  final SessionFailure? failure;
  final bool loading;
  final bool empty;
  final String emptyText;
  final Key emptyKey;
  final Future<void> Function() onRefresh;
  final List<String> sorts;
  final String selected;
  final String keyPrefix;
  final String Function(String) label;
  final ValueChanged<String> onSort;
  final int count;
  final Widget Function(BuildContext, int) row;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Column(
      children: [
        SingleChildScrollView(
          scrollDirection: Axis.horizontal,
          padding: const EdgeInsets.fromLTRB(12, 10, 12, 2),
          child: Row(
            children: [
              for (final s in sorts)
                Padding(
                  padding: const EdgeInsets.only(right: 8),
                  child: ChoiceChip(
                    key: Key('$keyPrefix-$s'),
                    label: Text(label(s)),
                    selected: selected == s,
                    // Sorted by the server: "by time consumed" is a product
                    // of the calls and their durations over the whole range,
                    // not something the rows on this page could be
                    // rearranged into.
                    onSelected: (_) {
                      if (selected == s) return;
                      onSort(s);
                    },
                  ),
                ),
            ],
          ),
        ),
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: 16),
          child: FailureBanner(failure: failure, baseUrl: baseUrl),
        ),
        Expanded(
          child: loading
              ? const Center(child: CircularProgressIndicator())
              : empty
              ? Center(
                  child: Text(
                    emptyText,
                    key: emptyKey,
                    style: theme.textTheme.bodyMedium?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              : RefreshIndicator(
                  onRefresh: onRefresh,
                  child: ListView.separated(
                    padding: const EdgeInsets.fromLTRB(16, 6, 16, 24),
                    itemCount: count,
                    separatorBuilder: (context, _) => const Divider(height: 1),
                    itemBuilder: row,
                  ),
                ),
        ),
      ],
    );
  }
}

class _Row extends StatelessWidget {
  const _Row({
    required this.rowKey,
    required this.title,
    required this.subtitle,
    required this.lead,
    required this.requests,
    required this.errorRate,
    required this.p95Ms,
    required this.avgMs,
    this.titleMaxLines = 2,
  });

  final Key rowKey;
  final String title;
  final String subtitle;
  final String lead;
  final double requests;
  final double errorRate;
  final double? p95Ms;
  final double? avgMs;
  final int titleMaxLines;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Padding(
      key: rowKey,
      padding: const EdgeInsets.symmetric(vertical: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(
                child: Text(
                  title,
                  maxLines: titleMaxLines,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.titleSmall,
                ),
              ),
              const SizedBox(width: 8),
              Text(lead, style: muted),
            ],
          ),
          if (subtitle.isNotEmpty) Text(subtitle, style: muted),
          const SizedBox(height: 2),
          Wrap(
            spacing: 10,
            runSpacing: 2,
            children: [
              Text(l.mapCalls(requests.round()), style: muted),
              Text(
                l.mapErrorRate(_showPercent(errorRate)),
                style: errorRate > 0
                    ? theme.textTheme.bodySmall?.copyWith(
                        color: severityTextColor(
                          context,
                          SeverityLevel.critical,
                        ),
                      )
                    : muted,
              ),
              if (p95Ms != null) Text(l.mapP95(_showMs(p95Ms!)), style: muted),
              if (avgMs != null) Text(l.mapAvg(_showMs(avgMs!)), style: muted),
            ],
          ),
        ],
      ),
    );
  }
}

String _showPercent(double ratio) {
  final v = ratio * 100;
  if (v == 0) return '0';
  return v >= 10 ? v.toStringAsFixed(0) : v.toStringAsFixed(1);
}

String _showMs(double v) {
  if (v >= 100 || v == v.roundToDouble()) return v.toStringAsFixed(0);
  return v.toStringAsFixed(1);
}
