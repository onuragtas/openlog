// The statements of every service, as the applications measured them, and one
// statement in full.
//
// Shown beside the server's view and not merged into it, because the two
// durations measure different things: this one includes the network and
// waiting for a pooled connection.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../apm_format.dart';
import '../databases.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'failure_text.dart';
import 'list_paging.dart';
import 'list_scaffold.dart';
import 'sections_screen.dart';
import 'server_view_link.dart';
import 'service_screen.dart';
import 'severity.dart';

/// A statement's database as the web's badge reads it.
String appDbLabel(ApmDbQuery q) =>
    q.dbName.isEmpty ? q.dbSystem : '${q.dbSystem}/${q.dbName}';

String appDbSortLabel(L l, String sort) => switch (sort) {
  'statement' => l.apmSortStatement,
  'database' => l.apmSortDatabase,
  'calls' => l.apmSortThroughput,
  'slowest' => l.dbAvg,
  'errors' || 'error_rate' => l.apmSortErrorRate,
  _ => l.sortTimeConsumed,
};

class AppDbQueriesList extends StatefulWidget {
  const AppDbQueriesList({
    super.key,
    required this.session,
    required this.sections,
    required this.sourcePicker,
  });

  final SessionController session;
  final Sections sections;

  /// The choice between this list and the servers', above the search.
  final Widget sourcePicker;

  @override
  State<AppDbQueriesList> createState() => _AppDbQueriesListState();
}

class _AppDbQueriesListState extends State<AppDbQueriesList> {
  late final _search = TextEditingController(
    text: widget.sections.appDbQueries.query,
  );

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.sections.appDbQueries;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListScreen<ApmDbQuery>(
        controller: c,
        baseUrl: widget.session.baseUrl ?? '',
        // An older server's answer is a plain sentence where the rows would
        // be, which is what an empty list already draws.
        emptyTitle: c.notOffered ? l.serverNotOffered : l.dbAppEmpty,
        search: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            widget.sourcePicker,
            if (!c.notOffered) ...[
              const SizedBox(height: 8),
              SearchField(
                fieldKey: const Key('app-db-search'),
                controller: _search,
                hint: l.dbAppSearch,
                onSubmitted: (v) {
                  c.query = v;
                  c.refresh();
                },
              ),
              ListSortBar(
                controller: c,
                columns: appDbQuerySorts,
                label: (s) => appDbSortLabel(l, s),
              ),
            ],
          ],
        ),
        header: c.notOffered
            ? null
            : Padding(
                padding: const EdgeInsets.only(bottom: 4),
                child: Text(
                  l.dbAppHint,
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: Theme.of(context).colorScheme.onSurfaceVariant,
                  ),
                ),
              ),
        footer: LoadMoreTile(controller: c),
        itemBuilder: (context, i) => _AppDbCard(
          index: i,
          query: c.items[i],
          onOpen: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => AppDbQueryScreen(
                session: widget.session,
                sections: widget.sections,
                query: c.items[i],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

class _AppDbCard extends StatelessWidget {
  const _AppDbCard({
    required this.index,
    required this.query,
    required this.onOpen,
  });

  final int index;
  final ApmDbQuery query;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final q = query;
    return SectionCard(
      cardKey: Key('app-db-query-$index'),
      title: q.statement,
      subtitle: [
        appDbLabel(q),
        if ((q.services ?? const []).isNotEmpty) q.services!.join(', '),
      ].join(' · '),
      onOpen: onOpen,
      stats: appDbStats(context, l, q),
    );
  }
}

/// The numbers of a statement, in the order of the web's columns.
List<({String label, String value, Color? emphasis})> appDbStats(
  BuildContext context,
  L l,
  ApmDbQuery q,
) => [
  (
    label: l.apmSortThroughput,
    value: formatApmRpm(q.throughput),
    emphasis: null,
  ),
  (label: l.dbAvg, value: formatApmMs(q.avgMs), emphasis: null),
  (label: 'p95', value: formatApmMs(q.p95Ms), emphasis: null),
  (
    label: l.apmSortErrorRate,
    value: formatApmRate(q.errorRate),
    emphasis: q.errorRate > 0
        ? severityTextColor(context, SeverityLevel.critical)
        : null,
  ),
  (label: l.dbShare, value: formatApmRate(q.timeShare), emphasis: null),
];

/// One statement: its whole text, who runs it, the way to the server's view of
/// it and the plans agents captured.
///
/// Not a [DetailScreen]: what it shows came with the list, and only the plans
/// are read here, so an older server without them still leaves the rest.
class AppDbQueryScreen extends StatefulWidget {
  const AppDbQueryScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.query,
  });

  final SessionController session;
  final Sections sections;
  final ApmDbQuery query;

  @override
  State<AppDbQueryScreen> createState() => _AppDbQueryScreenState();
}

class _AppDbQueryScreenState extends State<AppDbQueryScreen> {
  late final AppDbPlansController _plans = widget.sections.appDbPlans(
    widget.query.dbSystem,
    widget.query.statement,
  );

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _plans.refresh());
  }

  @override
  void dispose() {
    _plans.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final q = widget.query;

    return Scaffold(
      appBar: AppBar(
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Text(l.dbQueryTitle, overflow: TextOverflow.ellipsis),
            Text(
              appDbLabel(q),
              overflow: TextOverflow.ellipsis,
              style: theme.textTheme.bodySmall,
            ),
          ],
        ),
      ),
      body: RefreshIndicator(
        onRefresh: _plans.refresh,
        child: ListView(
          padding: const EdgeInsets.fromLTRB(16, 12, 16, 32),
          children: [
            // Selectable: a statement is a thing somebody copies into a
            // console, and on a phone that is the only way to get it there.
            SelectableText(
              q.statement,
              key: const Key('app-db-statement'),
              style: theme.textTheme.bodySmall?.copyWith(
                fontFamily: 'monospace',
              ),
            ),
            const SizedBox(height: 12),
            Wrap(
              spacing: 18,
              runSpacing: 8,
              children: [
                for (final s in appDbStats(context, l, q))
                  Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Text(s.label, style: muted),
                      Text(
                        s.value,
                        style: theme.textTheme.bodyMedium?.copyWith(
                          color: s.emphasis,
                        ),
                      ),
                    ],
                  ),
              ],
            ),
            Align(
              alignment: Alignment.centerLeft,
              child: ServerViewLink(
                session: widget.session,
                sections: widget.sections,
                dbSystem: q.dbSystem,
                statement: q.statement,
              ),
            ),
            if ((q.services ?? const []).isNotEmpty)
              DetailSection(
                title: l.dbAppServices,
                children: [
                  Wrap(
                    spacing: 4,
                    children: [
                      for (final s in q.services!)
                        TextButton(
                          key: Key('app-db-service-$s'),
                          onPressed: () => Navigator.of(context).push(
                            MaterialPageRoute<void>(
                              builder: (_) => ServiceScreen(
                                session: widget.session,
                                sections: widget.sections,
                                serviceName: s,
                              ),
                            ),
                          ),
                          child: Text(s),
                        ),
                    ],
                  ),
                ],
              ),
            DetailSection(
              title: l.dbPlans,
              children: [
                ListenableBuilder(
                  listenable: _plans,
                  builder: (context, _) => _Plans(
                    controller: _plans,
                    baseUrl: widget.session.baseUrl ?? '',
                  ),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

class _Plans extends StatelessWidget {
  const _Plans({required this.controller, required this.baseUrl});

  final AppDbPlansController controller;
  final String baseUrl;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final c = controller;

    if (c.notOffered) return const NotOfferedNote(padding: EdgeInsets.zero);
    if (c.loadingFirst) {
      return const Padding(
        padding: EdgeInsets.symmetric(vertical: 16),
        child: Center(child: CircularProgressIndicator()),
      );
    }
    if (c.failure != null) {
      return FailureBanner(failure: c.failure, baseUrl: baseUrl);
    }
    final plans = c.value ?? const <ApmDbQueryPlan>[];
    if (plans.isEmpty) {
      // Not "no plan exists": only an agent with explaining on sends any.
      return Text(
        l.dbAppNoPlan,
        key: const Key('app-db-no-plan'),
        style: muted,
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final p in plans)
          Padding(
            key: Key('app-db-plan-${p.traceId}'),
            padding: const EdgeInsets.only(bottom: 14),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  '${l.dbAppPlan} · ${p.service} · '
                  '${relativeTimeOf(l, p.capturedAt)}',
                  style: muted,
                ),
                const SizedBox(height: 4),
                // Scrolls sideways: a plan's indentation is its meaning, and
                // wrapping its long lines would take that away.
                SingleChildScrollView(
                  scrollDirection: Axis.horizontal,
                  child: SelectableText(
                    p.plan,
                    style: theme.textTheme.bodySmall?.copyWith(
                      fontFamily: 'monospace',
                    ),
                  ),
                ),
                if (p.generic) ...[
                  const SizedBox(height: 4),
                  Text(
                    l.dbAppGeneric,
                    key: const Key('app-db-generic'),
                    style: muted,
                  ),
                ],
              ],
            ),
          ),
      ],
    );
  }
}
