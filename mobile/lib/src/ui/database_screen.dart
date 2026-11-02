// One database instance, and one statement of it.
//
// The web's three tabs -- Etkinlik, Sorgular, Oturumlar -- and its
// statement page, in the same order and with the same contents. The chart
// of average active sessions per wait type is the one picture that says
// whether a database is busy on CPU, on locks or on disk, so it stays; on
// a phone it is a stack of bars per wait type rather than a wide area
// chart, because twelve stacked series in 360 points is a smudge.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../databases.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'failure_text.dart';
import '../list_controller.dart';
import 'list_paging.dart';
import 'list_scaffold.dart';
import 'sparkline.dart';
import 'theme.dart';

class DatabaseScreen extends StatefulWidget {
  const DatabaseScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.instance,
    required this.dbSystem,
  });

  final SessionController session;
  final Sections sections;

  /// `service.instance.id`, which is what every one of these endpoints
  /// takes.
  final String instance;
  final String dbSystem;

  @override
  State<DatabaseScreen> createState() => _DatabaseScreenState();
}

class _DatabaseScreenState extends State<DatabaseScreen>
    with SingleTickerProviderStateMixin {
  late final DbActivityController _activity;
  late final DbActivityWaitsController _waits;
  late final DbActivityQueriesController _top;
  late final DbQueriesController _queries;
  late final DbSessionsController _sessions;
  late final TabController _tabs;

  @override
  void initState() {
    super.initState();
    _activity = widget.sections.dbActivity(widget.instance);
    _waits = widget.sections.dbActivityWaits(widget.instance);
    _top = widget.sections.dbActivityQueries(widget.instance);
    _queries = widget.sections.dbQueries(widget.instance);
    _sessions = widget.sections.dbSessions(widget.instance);
    _tabs = TabController(length: 3, vsync: this)
      ..addListener(() {
        if (_tabs.indexIsChanging) return;
        _loadTab();
      });
    WidgetsBinding.instance.addPostFrameCallback((_) => _refreshActivity());
  }

  /// The tab's three reads: the chart and its two lists, which are apart so
  /// that sorting a list does not draw the chart again.
  Future<void> _refreshActivity() async {
    await Future.wait([_activity.refresh(), _waits.refresh(), _top.refresh()]);
  }

  void _loadTab() {
    switch (_tabs.index) {
      case 1:
        if (!_queries.loaded && !_queries.loadingFirst) _queries.refresh();
      case 2:
        if (!_sessions.loaded && !_sessions.loadingFirst) _sessions.refresh();
    }
  }

  @override
  void dispose() {
    _tabs.dispose();
    _activity.dispose();
    _waits.dispose();
    _top.dispose();
    _queries.dispose();
    _sessions.dispose();
    super.dispose();
  }

  void _openQuery(String fingerprint) => Navigator.of(context).push(
    MaterialPageRoute<void>(
      builder: (_) => DbQueryScreen(
        session: widget.session,
        sections: widget.sections,
        instance: widget.instance,
        fingerprint: fingerprint,
      ),
    ),
  );

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return Scaffold(
      appBar: AppBar(
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Text(widget.instance, overflow: TextOverflow.ellipsis),
            Text(
              widget.dbSystem,
              overflow: TextOverflow.ellipsis,
              style: theme.textTheme.bodySmall,
            ),
          ],
        ),
        bottom: TabBar(
          controller: _tabs,
          tabs: [
            Tab(key: const Key('db-tab-activity'), text: l.dbTabActivity),
            Tab(key: const Key('db-tab-queries'), text: l.dbTabQueries),
            Tab(key: const Key('db-tab-sessions'), text: l.dbTabSessions),
          ],
        ),
      ),
      body: TabBarView(
        controller: _tabs,
        children: [
          _Activity(
            session: widget.session,
            controller: _activity,
            waits: _waits,
            top: _top,
            onRefresh: _refreshActivity,
            onOpenQuery: _openQuery,
          ),
          _Queries(
            session: widget.session,
            controller: _queries,
            onOpenQuery: _openQuery,
          ),
          _Sessions(
            session: widget.session,
            controller: _sessions,
            onOpenQuery: _openQuery,
          ),
        ],
      ),
    );
  }
}

class _Activity extends StatelessWidget {
  const _Activity({
    required this.session,
    required this.controller,
    required this.waits,
    required this.top,
    required this.onRefresh,
    required this.onOpenQuery,
  });

  final SessionController session;
  final DbActivityController controller;
  final DbActivityWaitsController waits;
  final DbActivityQueriesController top;
  final Future<void> Function() onRefresh;
  final ValueChanged<String> onOpenQuery;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return ListenableBuilder(
      listenable: controller,
      builder: (context, _) => DetailBody<DbActivity>(
        controller: controller,
        baseUrl: session.baseUrl ?? '',
        onRefresh: onRefresh,
        builder: (context, a) => [
          Text(l.dbActivityTitle, style: theme.textTheme.titleSmall),
          Text(l.dbActivityAbout, style: muted),
          const SizedBox(height: 8),
          if (a.series.isEmpty)
            Text(l.dbNoSamples, key: const Key('db-no-samples'), style: muted)
          else
            for (final s in a.series) _WaitSeries(series: s),
          DetailSection(
            title: l.dbWaits,
            children: [
              _ActivityList<DbWait>(
                controller: waits,
                baseUrl: session.baseUrl ?? '',
                sorts: dbWaitSorts,
                sortLabel: (s) => switch (s) {
                  'type' => l.apmSortWaitType,
                  'event' => l.apmSortWaitEvent,
                  _ => l.apmSortShare,
                },
                // What an older server has: the top ones, unsortable.
                fallback: a.waits,
                row: (w) => _WaitRow(wait: w),
              ),
            ],
          ),
          DetailSection(
            title: l.dbTopQueries,
            children: [
              _ActivityList<DbActivityQuery>(
                controller: top,
                baseUrl: session.baseUrl ?? '',
                sorts: dbActivityQuerySorts,
                sortLabel: (s) => switch (s) {
                  'statement' => l.apmSortStatement,
                  'wait' => l.apmSortTopWait,
                  _ => l.apmSortAas,
                },
                fallback: a.topQueries,
                row: (q) => _TopQueryRow(query: q, onOpen: onOpenQuery),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

/// One of the activity tab's two lists, inside its scrolling body: the sort,
/// the rows, and the foot that fetches the next page.
///
/// A server older than these lists still sends the short ones on the activity
/// answer, so it keeps those and says why they cannot be sorted.
class _ActivityList<T> extends StatelessWidget {
  const _ActivityList({
    required this.controller,
    required this.baseUrl,
    required this.sorts,
    required this.sortLabel,
    required this.fallback,
    required this.row,
  });

  final PagedListController<T> controller;
  final String baseUrl;
  final List<String> sorts;
  final String Function(String) sortLabel;
  final List<T> fallback;
  final Widget Function(T) row;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final c = controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.loadingFirst) {
          return const Padding(
            padding: EdgeInsets.symmetric(vertical: 16),
            child: Center(child: CircularProgressIndicator()),
          );
        }
        if (notOfferedNow) {
          return Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const NotOfferedNote(padding: EdgeInsets.only(bottom: 8)),
              if (fallback.isEmpty)
                Text(l.dbNoSamples, style: muted)
              else
                for (final item in fallback) row(item),
            ],
          );
        }
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            FailureBanner(failure: c.failure, baseUrl: baseUrl),
            if (c.items.isEmpty && c.failure == null)
              Text(l.dbNoSamples, style: muted)
            else if (c.items.isNotEmpty) ...[
              ListSortBar(controller: c, columns: sorts, label: sortLabel),
              for (final item in c.items) row(item),
              LoadMoreTile(controller: c),
            ],
          ],
        );
      },
    );
  }

  bool get notOfferedNow {
    final c = controller;
    return c is OlderServerList<T> && c.notOffered;
  }
}

class _WaitRow extends StatelessWidget {
  const _WaitRow({required this.wait});

  final DbWait wait;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        children: [
          Expanded(
            child: Text(
              wait.event.isEmpty ? wait.type : '${wait.type} · ${wait.event}',
              style: theme.textTheme.bodyMedium,
            ),
          ),
          Text(
            '%${(wait.share * 100).toStringAsFixed(1)}',
            style: theme.textTheme.bodyMedium?.copyWith(
              fontFeatures: const [FontFeature.tabularFigures()],
            ),
          ),
        ],
      ),
    );
  }
}

class _TopQueryRow extends StatelessWidget {
  const _TopQueryRow({required this.query, required this.onOpen});

  final DbActivityQuery query;
  final ValueChanged<String> onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    return InkWell(
      key: Key('db-top-${query.fingerprint}'),
      onTap: () => onOpen(query.fingerprint),
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 8),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              query.text,
              maxLines: 3,
              overflow: TextOverflow.ellipsis,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.primary,
              ),
            ),
            const SizedBox(height: 2),
            Text(
              '${l.dbAas} '
              '${query.avgActiveSessions.toStringAsFixed(2)} · '
              '${query.topWait}',
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

/// One wait type over the window: the sparkline and what it averaged.
///
/// The web stacks every type into one chart. A phone cannot carry twelve
/// stacked series, so each type gets its own line and its own number --
/// the same question ("busy on what?") answered one row at a time.
class _WaitSeries extends StatelessWidget {
  const _WaitSeries({required this.series});

  final DbActivitySeriesItem series;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final values = [for (final p in series.points) p[1]];
    final average = values.isEmpty
        ? 0.0
        : values.reduce((a, b) => a + b) / values.length;

    return Padding(
      key: Key('db-wait-${series.waitType}'),
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        children: [
          SizedBox(
            width: 96,
            child: Text(
              series.waitType,
              overflow: TextOverflow.ellipsis,
              style: theme.textTheme.bodySmall,
            ),
          ),
          Expanded(
            child: SizedBox(
              height: 24,
              child: values.length < 2
                  ? const SizedBox.shrink()
                  : Sparkline(values: values, color: colors.primary),
            ),
          ),
          const SizedBox(width: 8),
          Text(
            average.toStringAsFixed(2),
            style: theme.textTheme.bodySmall?.copyWith(
              fontFeatures: const [FontFeature.tabularFigures()],
            ),
          ),
        ],
      ),
    );
  }
}

class _Queries extends StatelessWidget {
  const _Queries({
    required this.session,
    required this.controller,
    required this.onOpenQuery,
  });

  final SessionController session;
  final DbQueriesController controller;
  final ValueChanged<String> onOpenQuery;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListScreen<DbQuery>(
        controller: c,
        baseUrl: session.baseUrl ?? '',
        search: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _QuerySearch(controller: c),
            ListSortBar(
              controller: c,
              columns: dbQuerySorts,
              label: (s) => _sortLabel(l, s),
            ),
          ],
        ),
        emptyTitle: l.dbNoQueries,
        footer: LoadMoreTile(controller: c),
        itemBuilder: (context, i) => _QueryRow(
          query: c.items[i],
          onOpen: () => onOpenQuery(c.items[i].fingerprint),
        ),
      ),
    );
  }
}

String _sortLabel(L l, String sort) => switch (sort) {
  'time' => l.dbSortTime,
  'statement' => l.apmSortStatement,
  'calls' => l.dbSortCalls,
  'avg' => l.dbSortAvg,
  'rows_per_call' => l.dbRowsPerCall,
  'rows' => l.dbSortRows,
  'errors' => l.dbSortErrors,
  _ => l.dbSortReads,
};

class _QuerySearch extends StatefulWidget {
  const _QuerySearch({required this.controller});

  final DbQueriesController controller;

  @override
  State<_QuerySearch> createState() => _QuerySearchState();
}

class _QuerySearchState extends State<_QuerySearch> {
  final _text = TextEditingController();

  @override
  void dispose() {
    _text.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => SearchField(
    fieldKey: const Key('db-query-search'),
    controller: _text,
    hint: L.of(context).dbQuerySearch,
    onSubmitted: (v) {
      widget.controller.query = v;
      widget.controller.refresh();
    },
  );
}

class _QueryRow extends StatelessWidget {
  const _QueryRow({required this.query, required this.onOpen});

  final DbQuery query;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return InkWell(
      key: Key('db-query-${query.fingerprint}'),
      onTap: onOpen,
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 10, horizontal: 4),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              query.text,
              maxLines: 3,
              overflow: TextOverflow.ellipsis,
              style: theme.textTheme.bodySmall,
            ),
            if (query.dbNames.isNotEmpty)
              Text(query.dbNames.join(', '), style: muted),
            const SizedBox(height: 6),
            // The share bar: where the instance's time goes, which is the
            // whole reason this list is sorted the way it is.
            ClipRRect(
              borderRadius: BorderRadius.circular(3),
              child: LinearProgressIndicator(
                value: query.timeShare.clamp(0.0, 1.0),
                minHeight: 4,
                backgroundColor: theme.colorScheme.surfaceContainerHighest,
                color: colors.primary,
              ),
            ),
            const SizedBox(height: 6),
            Wrap(
              spacing: 14,
              runSpacing: 4,
              children: [
                _Stat(
                  label: l.dbShare,
                  value: '%${(query.timeShare * 100).toStringAsFixed(1)}',
                ),
                _Stat(
                  label: l.dbThroughput,
                  value: l.dbPerSecond(query.throughput.toStringAsFixed(2)),
                ),
                _Stat(
                  label: l.dbAvg,
                  value: query.avgMs == null
                      ? '—'
                      : '${query.avgMs!.toStringAsFixed(1)} ms',
                ),
                _Stat(
                  label: l.dbRowsPerCall,
                  value: query.rowsPerCall == null
                      ? '—'
                      : query.rowsPerCall!.toStringAsFixed(1),
                ),
                _Stat(label: l.dbRows, value: '${query.rows}'),
                _Stat(label: l.dbSortReads, value: '${query.blocksRead}'),
                if (query.errors > 0)
                  _Stat(
                    label: l.dbErrors,
                    value: '${query.errors}',
                    emphasis: colors.destructiveText,
                  ),
              ],
            ),
            const Divider(height: 18),
          ],
        ),
      ),
    );
  }
}

class _Stat extends StatelessWidget {
  const _Stat({required this.label, required this.value, this.emphasis});

  final String label;
  final String value;
  final Color? emphasis;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        Text(
          label,
          style: theme.textTheme.bodySmall?.copyWith(
            color: theme.colorScheme.onSurfaceVariant,
          ),
        ),
        Text(
          value,
          style: theme.textTheme.bodyMedium?.copyWith(
            fontFeatures: const [FontFeature.tabularFigures()],
            color: emphasis,
          ),
        ),
      ],
    );
  }
}

class _Sessions extends StatelessWidget {
  const _Sessions({
    required this.session,
    required this.controller,
    required this.onOpenQuery,
  });

  final SessionController session;
  final DbSessionsController controller;
  final ValueChanged<String> onOpenQuery;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.loadingFirst) {
          return const Center(child: CircularProgressIndicator());
        }
        final forest = blockingForest(c.items);
        return RefreshIndicator(
          onRefresh: c.refresh,
          child: ListView(
            padding: const EdgeInsets.fromLTRB(16, 12, 16, 32),
            children: [
              FailureBanner(failure: c.failure, baseUrl: session.baseUrl ?? ''),
              if (c.sampledAt != null)
                Text(
                  // When the photograph was taken. A session list without
                  // its time reads as "now", and it is not.
                  l.dbSampledAt(relativeTimeOf(l, c.sampledAt!)),
                  key: const Key('db-sampled-at'),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              if (c.items.isEmpty)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 24),
                  child: Text(
                    l.dbNoSessions,
                    key: const Key('db-no-sessions'),
                    textAlign: TextAlign.center,
                    style: theme.textTheme.bodyMedium?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ),
              if (forest.roots.isNotEmpty) ...[
                const SizedBox(height: 8),
                Text(l.dbBlocking, style: theme.textTheme.titleSmall),
                for (final r in forest.roots)
                  _SessionTree(node: r, depth: 0, onOpenQuery: onOpenQuery),
                const SizedBox(height: 8),
              ],
              if (forest.others.isNotEmpty) ...[
                const SizedBox(height: 8),
                Text(l.dbSessions, style: theme.textTheme.titleSmall),
                for (final s in forest.others)
                  _SessionRow(session: s, onOpenQuery: onOpenQuery),
              ],
            ],
          ),
        );
      },
    );
  }
}

class _SessionTree extends StatelessWidget {
  const _SessionTree({
    required this.node,
    required this.depth,
    required this.onOpenQuery,
  });

  final SessionNode node;
  final int depth;
  final ValueChanged<String> onOpenQuery;

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Padding(
        // Indented by depth, which is what makes "A is waiting for B"
        // readable without drawing a tree.
        padding: EdgeInsets.only(left: depth * 16.0),
        child: _SessionRow(
          session: node.session,
          onOpenQuery: onOpenQuery,
          blocking: depth == 0 && node.children.isNotEmpty,
        ),
      ),
      for (final c in node.children)
        _SessionTree(node: c, depth: depth + 1, onOpenQuery: onOpenQuery),
    ],
  );
}

class _SessionRow extends StatelessWidget {
  const _SessionRow({
    required this.session,
    required this.onOpenQuery,
    this.blocking = false,
  });

  final DbSession session;
  final ValueChanged<String> onOpenQuery;

  /// The head of a blocking chain: the session everybody else is waiting
  /// for, which is the one to look at.
  final bool blocking;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Padding(
      key: Key('db-session-${session.sessionId}'),
      padding: const EdgeInsets.symmetric(vertical: 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  [
                    session.sessionId,
                    if (session.user.isNotEmpty) session.user,
                    if (session.dbName.isNotEmpty) session.dbName,
                  ].join(' · '),
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              Text(
                '${(session.durationMs / 1000).toStringAsFixed(1)} s',
                style: theme.textTheme.bodySmall?.copyWith(
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
            ],
          ),
          Wrap(
            spacing: 6,
            runSpacing: 4,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Text(session.state, style: muted),
              if (session.waitType.isNotEmpty)
                Text(
                  session.waitEvent.isEmpty
                      ? session.waitType
                      : '${session.waitType} · ${session.waitEvent}',
                  style: muted,
                ),
              if (blocking)
                Text(
                  l.dbBlocks(session.blocks),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: colors.destructiveText,
                  ),
                ),
            ],
          ),
          if (session.text.isNotEmpty)
            InkWell(
              onTap: session.fingerprint.isEmpty
                  ? null
                  : () => onOpenQuery(session.fingerprint),
              child: Text(
                session.text,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: session.fingerprint.isEmpty
                      ? null
                      : theme.colorScheme.primary,
                ),
              ),
            ),
          const Divider(height: 14),
        ],
      ),
    );
  }
}

/// One statement: what it costs, how it has been running, its plans, its
/// waits and the services that run it.
class DbQueryScreen extends StatefulWidget {
  const DbQueryScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.instance,
    required this.fingerprint,
  });

  final SessionController session;
  final Sections sections;
  final String instance;
  final String fingerprint;

  @override
  State<DbQueryScreen> createState() => _DbQueryScreenState();
}

class _DbQueryScreenState extends State<DbQueryScreen> {
  late final DbQueryController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.dbQuery(
      instance: widget.instance,
      fingerprint: widget.fingerprint,
    );
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
      builder: (context, _) => DetailScreen<DbQueryDetail>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: l.dbQueryTitle,
        subtitle: widget.instance,
        builder: (context, d) => _body(context, l, d),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, DbQueryDetail d) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final q = d.query;
    final avg = [for (final p in d.points) p.avgMs ?? 0];

    return [
      SelectableText(
        // Selectable: a statement is a thing somebody copies into a
        // console, and on a phone that is the only way to get it there.
        q.text,
        style: theme.textTheme.bodySmall,
      ),
      if (q.dbNames.isNotEmpty) ...[
        const SizedBox(height: 4),
        Text(q.dbNames.join(', '), style: muted),
      ],
      DetailSection(
        title: l.dbQueryCost,
        children: [
          Wrap(
            spacing: 18,
            runSpacing: 8,
            children: [
              _Stat(label: l.dbCalls, value: '${q.calls}'),
              _Stat(
                label: l.dbAvg,
                value: q.avgMs == null
                    ? '—'
                    : '${q.avgMs!.toStringAsFixed(1)} ms',
              ),
              _Stat(
                label: l.dbTotalTime,
                value: '${(q.totalTimeMs / 1000).toStringAsFixed(1)} s',
              ),
              _Stat(
                label: l.dbShare,
                value: '%${(q.timeShare * 100).toStringAsFixed(1)}',
              ),
              _Stat(label: l.dbRows, value: '${q.rows}'),
              if (q.errors > 0)
                _Stat(
                  label: l.dbErrors,
                  value: '${q.errors}',
                  emphasis: colors.destructiveText,
                ),
              if (q.noIndexUsed > 0)
                _Stat(
                  label: l.dbNoIndex,
                  value: '${q.noIndexUsed}',
                  emphasis: colors.warningText,
                ),
              if (q.cacheHitRatio != null)
                _Stat(
                  label: l.dbCacheHit,
                  value: '%${(q.cacheHitRatio! * 100).toStringAsFixed(1)}',
                ),
            ],
          ),
          if (avg.length >= 2) ...[
            const SizedBox(height: 12),
            Text(l.dbAvgTrend, style: muted),
            SizedBox(
              height: 48,
              child: Sparkline(
                key: const Key('db-query-spark'),
                values: avg,
                color: colors.primary,
              ),
            ),
          ],
        ],
      ),
      if (d.waits.isNotEmpty)
        DetailSection(
          title: l.dbWaits,
          children: [
            for (final w in d.waits)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 3),
                child: Row(
                  children: [
                    Expanded(
                      child: Text(
                        w.event.isEmpty ? w.type : '${w.type} · ${w.event}',
                        style: theme.textTheme.bodyMedium,
                      ),
                    ),
                    Text(
                      '%${(w.share * 100).toStringAsFixed(1)}',
                      style: theme.textTheme.bodyMedium?.copyWith(
                        fontFeatures: const [FontFeature.tabularFigures()],
                      ),
                    ),
                  ],
                ),
              ),
          ],
        ),
      if (d.plans.isNotEmpty)
        DetailSection(
          title: l.dbPlans,
          children: [for (final p in d.plans) _PlanRow(plan: p)],
        ),
      if (d.callers.isNotEmpty)
        DetailSection(
          title: l.dbCallers,
          children: [
            for (final c in d.callers)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 4),
                child: Row(
                  children: [
                    Expanded(
                      child: Text(
                        c.environment.isEmpty
                            ? c.serviceName
                            : '${c.serviceName} · ${c.environment}',
                        style: theme.textTheme.bodyMedium,
                      ),
                    ),
                    Text(
                      '${c.calls.round()} · '
                      '${c.avgMs == null ? '—' : '${c.avgMs!.toStringAsFixed(1)} ms'}',
                      style: theme.textTheme.bodySmall?.copyWith(
                        fontFeatures: const [FontFeature.tabularFigures()],
                      ),
                    ),
                  ],
                ),
              ),
          ],
        ),
    ];
  }
}

class _PlanRow extends StatefulWidget {
  const _PlanRow({required this.plan});

  final DbPlan plan;

  @override
  State<_PlanRow> createState() => _PlanRowState();
}

class _PlanRowState extends State<_PlanRow> {
  bool _open = false;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final p = widget.plan;

    return Padding(
      key: Key('db-plan-${p.planHash}'),
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          InkWell(
            onTap: () => setState(() => _open = !_open),
            child: Row(
              children: [
                Expanded(
                  child: Text(
                    p.planHash,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.bodyMedium,
                  ),
                ),
                if (p.isCurrent)
                  Padding(
                    padding: const EdgeInsets.only(left: 6),
                    child: Text(
                      l.dbCurrentPlan,
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.primary,
                      ),
                    ),
                  ),
                if (p.planChange)
                  Padding(
                    padding: const EdgeInsets.only(left: 6),
                    child: Text(
                      // A plan that replaced an earlier one is the usual
                      // answer to "it was fast yesterday".
                      l.dbPlanChanged,
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: colors.warningText,
                      ),
                    ),
                  ),
                Icon(_open ? Icons.expand_less : Icons.expand_more, size: 20),
              ],
            ),
          ),
          Text(
            '${l.dbPlanCost} ${p.totalCost.toStringAsFixed(1)} · '
            '${p.captures}×'
            '${p.dbName.isEmpty ? '' : ' · ${p.dbName}'}',
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          if (_open)
            Padding(
              padding: const EdgeInsets.only(top: 6),
              child: SelectableText(
                p.plan,
                style: theme.textTheme.bodySmall?.copyWith(
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
            ),
        ],
      ),
    );
  }
}
