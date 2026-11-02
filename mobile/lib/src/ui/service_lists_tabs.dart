// Two tabs of the service screen that are the same shape: a sort, then a
// list of rows with the same four numbers on each.
//
// İşlemler is what the service spends its time on; Veritabanları is what it
// asks the database. The web has both as tables with sortable columns; a
// phone sorts with a menu, because a table of eight columns on a 390 point
// screen is a table nobody reads.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../apm_format.dart';
import '../list_controller.dart';
import '../sections.dart';
import '../services.dart';
import '../session.dart';
import 'app_db_queries.dart' show appDbSortLabel;
import 'failure_text.dart';
import 'list_paging.dart';
import 'server_view_link.dart';
import 'severity.dart';

class ServiceTransactionsTab extends StatelessWidget {
  const ServiceTransactionsTab({
    super.key,
    required this.session,
    required this.controller,
    required this.onOpen,
  });

  final SessionController session;
  final ServiceTransactionsController controller;

  /// Opens one transaction.
  final ValueChanged<String> onOpen;

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
        controller: controller,
        sorts: transactionSorts,
        label: (s) => switch (s) {
          'name' => l.apmSortTransaction,
          'throughput' => l.apmSortThroughput,
          'avg' => l.dbAvg,
          // The server's `slowest` is the p95, which is the web's heading.
          'slowest' => 'p95',
          'error_rate' => l.apmSortErrorRate,
          'apdex' => l.svcApdex,
          _ => l.sortTimeConsumed,
        },
        count: controller.items.length,
        row: (context, i) {
          final t = controller.items[i];
          return _Row(
            rowKey: Key('transaction-${t.transactionName}'),
            onOpen: () => onOpen(t.transactionName),
            apdex: t.apdex,
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
    required this.sections,
    required this.controller,
  });

  final SessionController session;
  final Sections sections;
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
        controller: controller,
        sorts: serviceDatabaseSorts,
        label: (s) => appDbSortLabel(l, s),
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
            foot: ServerViewLink(
              // A statement per row, and the link remembers what it was told
              // about this one: a re-sort must not hand it another's answer.
              key: ValueKey('${q.dbSystem}|${q.statement}'),
              session: session,
              sections: sections,
              dbSystem: q.dbSystem,
              statement: q.statement,
            ),
          );
        },
      ),
    );
  }
}

/// The shape both tabs share: a sort above a list, and its next page under it.
class _SortedList extends StatelessWidget {
  const _SortedList({
    required this.baseUrl,
    required this.failure,
    required this.loading,
    required this.empty,
    required this.emptyText,
    required this.emptyKey,
    required this.onRefresh,
    required this.controller,
    required this.sorts,
    required this.label,
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
  final PagedListController<dynamic> controller;
  final List<String> sorts;
  final String Function(String) label;
  final int count;
  final Widget Function(BuildContext, int) row;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 6, 16, 0),
          child: Align(
            alignment: Alignment.centerLeft,
            // Sorted by the server: "by time consumed" is a product of the
            // calls and their durations over the whole range, not something
            // the rows on this page could be rearranged into.
            child: ListSortBar(
              controller: controller,
              columns: sorts,
              label: label,
            ),
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
                    itemCount: count + 1,
                    separatorBuilder: (context, _) => const Divider(height: 1),
                    itemBuilder: (context, i) => i == count
                        ? LoadMoreTile(controller: controller)
                        : row(context, i),
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
    this.onOpen,
    this.apdex,
    this.foot,
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

  /// What the row opens, when there is something behind it.
  final VoidCallback? onOpen;
  final double? apdex;

  /// Anything under the numbers.
  final Widget? foot;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    final body = Padding(
      key: onOpen == null ? rowKey : null,
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
              if (apdex != null)
                Text('${l.svcApdex} ${formatApmApdex(apdex)}', style: muted),
            ],
          ),
          ?foot,
        ],
      ),
    );
    return onOpen == null
        ? body
        : InkWell(key: rowKey, onTap: onOpen, child: body);
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
