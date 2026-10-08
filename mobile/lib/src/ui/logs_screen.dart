// Recent log records, newest first.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../logs.dart';
import '../session.dart';
import 'list_scaffold.dart';
import 'severity.dart';

/// The levels worth filtering by on a phone. Everything below WARN is noise at
/// ten lines a screen, which is why WARN is where the controller starts.
const logSeverities = ['', 'INFO', 'WARN', 'ERROR'];

class LogsBody extends StatefulWidget {
  const LogsBody({super.key, required this.session, required this.logs});

  final SessionController session;
  final LogsController logs;

  @override
  State<LogsBody> createState() => _LogsBodyState();
}

class _LogsBodyState extends State<LogsBody> {
  final _search = TextEditingController();

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => widget.logs.refresh());
  }

  @override
  void dispose() {
    _search.dispose();
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
        ),
        emptyTitle: l.logsEmpty,
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
