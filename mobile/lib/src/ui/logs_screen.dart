// Recent log records, newest first -- and what they say, as patterns.
//
// Two tabs, as the web has: a thousand lines a minute is not something
// anybody reads, and the templates behind them are. They share the search
// box and the filters, because they are two views of one question.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../logs.dart';
import '../session.dart';
import '../sections.dart';
import 'filter_sheet.dart';
import 'log_patterns_body.dart';
import 'list_scaffold.dart';
import 'severity.dart';

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
    this.scopeLabel,
  });

  final SessionController session;

  /// Needed for the filter builder, which asks the server which keys the
  /// logs in this range actually have.
  final Sections sections;
  final LogsController logs;

  /// The same logs, grouped by what they say.
  final LogPatternsController patterns;

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
    WidgetsBinding.instance.addPostFrameCallback((_) => widget.logs.refresh());
  }

  @override
  void dispose() {
    _tabs.dispose();
    _search.dispose();
    _service.dispose();
    super.dispose();
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
            SearchField(
              fieldKey: const Key('logs-search'),
              controller: _search,
              hint: l.logsSearch,
              onSubmitted: (value) {
                c.query = value;
                c.refresh();
                if (widget.patterns.loaded) _reloadPatterns();
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
              TextField(
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
              const SizedBox(height: 8),
              // Picked, not typed: a key somebody has to remember is a key
              // they will get wrong, and the dictionary knows which ones
              // the range actually has.
              FilterChips(
                filters: c.filters,
                onRemove: (i) {
                  c.filters = [...c.filters]..removeAt(i);
                  c.refresh();
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
                  if (widget.patterns.loaded) await _reloadPatterns();
                },
              ),
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
        itemBuilder: (context, i) => _LogTile(record: c.items[i]),
      ),
    );
  }
}

class _LogTile extends StatelessWidget {
  const _LogTile({required this.record});

  final LogRecord record;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;
    final color = severityTextColor(
      context,
      severityOfNumber(record.severityNumber),
    );

    return Padding(
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
          const Divider(height: 16),
        ],
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
      // A scoped list has no patterns tab, so this one is never read; it
      // is here because the body asks for it.
      patterns: widget.sections.logPatterns,
      scopeLabel: widget.scopeLabel,
    ),
  );
}
