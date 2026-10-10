// Recent log records, newest first -- and what they say, as patterns.
//
// Two tabs, as the web has: a thousand lines a minute is not something
// anybody reads, and the templates behind them are. They share the search
// box and the filters, because they are two views of one question.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../log_fields.dart';
import '../logs.dart';
import '../saved_views.dart';
import '../session.dart';
import '../volume.dart';
import '../sections.dart';
import 'columns_sheet.dart';
import 'filter_sheet.dart';
import 'log_patterns_body.dart';
import 'saved_views_sheet.dart';
import 'volume_chart.dart';
import 'list_scaffold.dart';
import 'log_detail_screen.dart';
import 'severity.dart';
import 'theme.dart';

/// The levels worth filtering by on a phone. Everything below WARN is noise at
/// ten lines a screen, which is why WARN is where the controller starts.
const logSeverities = ['', 'INFO', 'WARN', 'ERROR'];

class LogsBody extends StatefulWidget {
  const LogsBody({
    super.key,
    required this.session,
    required this.sections,
    required this.logs,
    required this.patterns,
    required this.volume,
    this.scopeLabel,
  });

  final SessionController session;

  /// Needed for the filter builder, which asks the server which keys the
  /// logs in this range actually have.
  final Sections sections;
  final LogsController logs;

  /// The same logs, grouped by what they say.
  final LogPatternsController patterns;

  /// How many arrived and when, above the list.
  final VolumeController volume;

  /// What this list is about, when it is about one thing. Set by the screen
  /// that pushed it -- a request, a pod, a container -- so the reader is not
  /// left wondering why the whole stream is missing.
  final String? scopeLabel;

  @override
  State<LogsBody> createState() => _LogsBodyState();
}

class _LogsBodyState extends State<LogsBody>
    with SingleTickerProviderStateMixin {
  final _search = TextEditingController();
  final _service = TextEditingController();
  late final TabController _tabs;

  @override
  void initState() {
    super.initState();
    _tabs = TabController(length: 2, vsync: this)
      ..addListener(() {
        if (_tabs.indexIsChanging) return;
        final p = widget.patterns;
        if (_tabs.index == 1 && !p.loaded && !p.loadingFirst) _reloadPatterns();
      });
    WidgetsBinding.instance.addPostFrameCallback((_) {
      widget.logs.refresh();
      // The chart is of the rows underneath it, so it is asked with the
      // same conditions and reloaded with them.
      if (widget.scopeLabel == null) _reloadVolume();
    });
  }

  Future<void> _reloadVolume() =>
      widget.volume.load(q: widget.logs.query, filters: widget.logs.filters);

  @override
  void dispose() {
    _tabs.dispose();
    _search.dispose();
    _service.dispose();
    super.dispose();
  }

  /// Shows what a saved view says. Everything the view does not mention is
  /// reset rather than left as it was: a view is a whole question, and half
  /// of it under somebody else's name is not that question.
  void _apply(SavedView view) {
    final state = ViewState.of(view.state, signal: 'logs');
    final c = widget.logs;
    c.filters = state.filters;
    c.query = state.query;
    c.severityMin = state.severityMin;
    // The table is part of the view too: a view saved with six columns
    // that opened here with four was a different view wearing its name.
    c.columns = state.columns.isEmpty ? [...defaultLogColumns] : state.columns;
    c.oldestFirst = state.oldestFirst;
    // The service box is saved as a `service.name` condition, so a view's
    // service arrives as a chip and the box has nothing left to say.
    c.service = '';
    _search.text = state.query;
    _service.clear();
    c.refresh();
    _reloadVolume();
    if (widget.patterns.loaded) _reloadPatterns();
    reportAppliedView(context, view, state);
  }

  /// The patterns answer the same question as the list, so they are asked
  /// with the same search box and the same filters.
  Future<void> _reloadPatterns() {
    final p = widget.patterns
      ..query = widget.logs.query
      ..filters = widget.logs.filters;
    return p.refresh();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    // A scoped list is about one request, pod or container; patterns of
    // six lines are not a view worth a tab.
    if (widget.scopeLabel != null) return _records(context);
    return Column(
      children: [
        TabBar(
          controller: _tabs,
          tabs: [
            Tab(key: const Key('logs-tab-records'), text: l.logsTabRecords),
            Tab(key: const Key('logs-tab-patterns'), text: l.logsTabPatterns),
          ],
        ),
        Expanded(
          child: TabBarView(
            controller: _tabs,
            children: [
              _records(context),
              LogPatternsBody(
                session: widget.session,
                controller: widget.patterns,
                onOpenPattern: (pattern) {
                  // One pattern's records are the same list with one more
                  // condition: `pattern_id` is a filter key.
                  widget.logs.filters = [
                    ...widget.logs.filters,
                    patternFilter(pattern),
                  ];
                  widget.logs.refresh();
                  _tabs.animateTo(0);
                },
              ),
            ],
          ),
        ),
      ],
    );
  }

  Widget _records(BuildContext context) {
    final l = L.of(context);
    final c = widget.logs;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListScreen(
        controller: c,
        baseUrl: widget.session.baseUrl ?? '',
        search: Column(
          children: [
            Builder(
              builder: (context) {
                final search = SearchField(
                  fieldKey: const Key('logs-search'),
                  controller: _search,
                  hint: l.logsSearch,
                  onSubmitted: (value) {
                    c.query = value;
                    c.refresh();
                    if (widget.scopeLabel == null) _reloadVolume();
                    if (widget.patterns.loaded) _reloadPatterns();
                  },
                );
                // Beside the search box, not under it: two full-width
                // boxes are two of the ten lines this screen has.
                if (widget.scopeLabel != null) return search;
                return Row(
                  children: [
                    Expanded(flex: 3, child: search),
                    const SizedBox(width: 8),
                    Expanded(
                      flex: 2,
                      child: TextField(
                        key: const Key('logs-service'),
                        controller: _service,
                        textInputAction: TextInputAction.search,
                        autocorrect: false,
                        decoration: InputDecoration(
                          labelText: l.logsService,
                          border: const OutlineInputBorder(),
                          isDense: true,
                        ),
                        onSubmitted: (value) {
                          c.service = value;
                          c.refresh();
                        },
                      ),
                    ),
                  ],
                );
              },
            ),
            // A scoped list already answers about one thing; a service box
            // and a severity filter on top of it would be three ways of
            // narrowing the same handful of lines.
            if (widget.scopeLabel != null) ...[
              const SizedBox(height: 8),
              Align(
                alignment: Alignment.centerLeft,
                child: Text(
                  '${widget.scopeLabel!} · ${l.logsScopedAll}',
                  style: Theme.of(context).textTheme.bodySmall?.copyWith(
                    color: Theme.of(context).colorScheme.onSurfaceVariant,
                  ),
                ),
              ),
            ] else ...[
              const SizedBox(height: 8),
              // One wrapping row for everything that is a button: the
              // views, the table options and the conditions. They were a
              // stack of four single-button rows, which on a phone is a
              // third of the screen spent on controls above ten log
              // lines. The web's toolbar is one row too.
              //
              // Picked, not typed: a key somebody has to remember is a key
              // they will get wrong, and the dictionary knows which ones
              // the range actually has.
              FilterChips(
                leading: [
                  // The views the organization kept, in the web's own
                  // shape: the same list a browser shows, and applying
                  // one here puts the browser's filters in the chips
                  // beside it.
                  SavedViewsBar(
                    session: widget.session,
                    controller: widget.sections.logViews,
                    active: true,
                    state: ({keep = const {}}) => logsViewState(
                      filters: c.filters,
                      query: c.query,
                      severityMin: c.severityMin,
                      service: c.service,
                      columns: c.columns,
                      oldestFirst: c.oldestFirst,
                      keep: keep,
                    ),
                    onApply: _apply,
                  ),
                  ActionChip(
                    key: const Key('logs-order'),
                    avatar: Icon(
                      c.oldestFirst ? Icons.arrow_upward : Icons.arrow_downward,
                      size: 16,
                    ),
                    label: Text(
                      c.oldestFirst ? l.logOrderOldest : l.logOrderNewest,
                    ),
                    onPressed: () {
                      c.oldestFirst = !c.oldestFirst;
                      c.refresh();
                    },
                  ),
                  ActionChip(
                    key: const Key('logs-columns'),
                    avatar: const Icon(Icons.view_column_outlined, size: 16),
                    label: Text(
                      isDefaultColumns(c.columns)
                          ? l.logColumns
                          : '${l.logColumns} · ${c.columns.length}',
                    ),
                    onPressed: () async {
                      final picked = await pickColumns(
                        context,
                        session: widget.session,
                        fields: widget.sections.fields('logs'),
                        columns: c.columns,
                      );
                      if (picked == null) return;
                      c.columns = picked;
                      // A column is a key the server has to be asked for,
                      // so this is a new request, not a redraw.
                      await c.refresh();
                    },
                  ),
                ],
                filters: c.filters,
                onRemove: (i) {
                  c.filters = [...c.filters]..removeAt(i);
                  c.refresh();
                  _reloadVolume();
                },
                onAdd: () async {
                  final filter = await pickFilter(
                    context,
                    session: widget.session,
                    fields: widget.sections.fields('logs'),
                  );
                  if (filter == null) return;
                  c.filters = [...c.filters, filter];
                  await c.refresh();
                  await _reloadVolume();
                  if (widget.patterns.loaded) await _reloadPatterns();
                },
              ),
              VolumeChart(session: widget.session, controller: widget.volume),
              const SizedBox(height: 8),
              Align(
                alignment: Alignment.centerLeft,
                child: SegmentedButton<String>(
                  key: const Key('logs-severity'),
                  showSelectedIcon: false,
                  segments: [
                    for (final s in logSeverities)
                      ButtonSegment(
                        value: s,
                        label: Text(s.isEmpty ? l.logsSeverityAll : s),
                      ),
                  ],
                  selected: {c.severityMin},
                  onSelectionChanged: (set) {
                    c.severityMin = set.first;
                    c.refresh();
                  },
                ),
              ),
            ],
          ],
        ),
        emptyTitle: c.scoped ? l.logsEmptyScoped : l.logsEmpty,
        itemBuilder: (context, i) => _LogTile(
          record: c.items[i],
          extras: extraColumns(c.columns),
          onOpen: () => openLogDetail(
            context,
            session: widget.session,
            sections: widget.sections,
            record: c.items[i],
            onFilter: (filter) {
              c.filters = [...c.filters, filter];
              c.refresh();
              if (widget.scopeLabel == null) _reloadVolume();
              if (widget.patterns.loaded) _reloadPatterns();
            },
          ),
        ),
      ),
    );
  }
}

class _LogTile extends StatelessWidget {
  const _LogTile({
    required this.record,
    required this.extras,
    required this.onOpen,
  });

  final LogQueryRow record;

  /// The chosen columns that the card does not already show, in the order
  /// they were chosen -- the web puts the same ones under its card.
  final List<String> extras;

  /// The whole record, with its attributes and what can be filtered by.
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;
    final color = severityTextColor(
      context,
      severityOfNumber(record.severityNumber),
    );

    return InkWell(
      key: Key('log-${record.id}'),
      onTap: onOpen,
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 8, horizontal: 4),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text(
                  record.severityText.isEmpty
                      ? '${record.severityNumber}'
                      : record.severityText,
                  style: text.labelSmall?.copyWith(
                    color: color,
                    fontWeight: FontWeight.w700,
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    record.serviceName.isEmpty
                        ? l.logsNoService
                        : record.serviceName,
                    style: text.labelSmall?.copyWith(
                      color: scheme.onSurfaceVariant,
                    ),
                  ),
                ),
                Text(
                  relativeTimeOf(l, record.timestamp),
                  style: text.labelSmall?.copyWith(
                    color: scheme.onSurfaceVariant,
                  ),
                ),
              ],
            ),
            const SizedBox(height: 2),
            // Four lines, then cut: a log body can be a whole stack trace, and
            // one of them must not push the next nine records off the screen.
            Text(
              record.body,
              maxLines: 4,
              overflow: TextOverflow.ellipsis,
              style: text.bodyMedium,
            ),
            if (extras.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Wrap(
                  spacing: 12,
                  runSpacing: 2,
                  children: [
                    for (final key in extras)
                      Text(
                        '$key=${cellValue(record, key) ?? '–'}',
                        style: mono(
                          text.labelSmall,
                        )?.copyWith(color: scheme.onSurfaceVariant),
                      ),
                  ],
                ),
              ),
            const Divider(height: 16),
          ],
        ),
      ),
    );
  }
}

/// The logs of one thing, on a screen of its own.
///
/// Pushed from wherever that thing is: a trace, a pod, a container. Typing a
/// trace id into a search box is the part a phone is worst at, and this is
/// how it never has to be typed.
class LogsScreen extends StatefulWidget {
  const LogsScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.logs,
    required this.title,
    required this.scopeLabel,
  });

  final SessionController session;
  final Sections sections;
  final LogsController logs;
  final String title;
  final String scopeLabel;

  @override
  State<LogsScreen> createState() => _LogsScreenState();
}

class _LogsScreenState extends State<LogsScreen> {
  @override
  void dispose() {
    widget.logs.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => Scaffold(
    appBar: AppBar(title: Text(widget.title, overflow: TextOverflow.ellipsis)),
    body: LogsBody(
      session: widget.session,
      sections: widget.sections,
      logs: widget.logs,
      // A scoped list has neither tab nor chart, so these are never read;
      // they are here because the body asks for them.
      patterns: widget.sections.logPatterns,
      volume: widget.sections.logVolume,
      scopeLabel: widget.scopeLabel,
    ),
  );
}
