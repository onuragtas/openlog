// Recent log records, newest first.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../logs.dart';
import '../session.dart';
import '../sections.dart';
import 'filter_sheet.dart';
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
    this.scopeLabel,
  });

  final SessionController session;

  /// Needed for the filter builder, which asks the server which keys the
  /// logs in this range actually have.
  final Sections sections;
  final LogsController logs;

  /// What this list is about, when it is about one thing. Set by the screen
  /// that pushed it -- a request, a pod, a container -- so the reader is not
  /// left wondering why the whole stream is missing.
  final String? scopeLabel;

  @override
  State<LogsBody> createState() => _LogsBodyState();
}

class _LogsBodyState extends State<LogsBody> {
  final _search = TextEditingController();
  final _service = TextEditingController();

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => widget.logs.refresh());
  }

  @override
  void dispose() {
    _search.dispose();
    _service.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
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
      scopeLabel: widget.scopeLabel,
    ),
  );
}
