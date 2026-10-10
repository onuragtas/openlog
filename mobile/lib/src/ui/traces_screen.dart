// The traces section: recent or slowest requests, each a way into its spans.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'filter_sheet.dart';
import 'list_scaffold.dart';
import 'sections_screen.dart';
import 'severity.dart';
import 'theme.dart';
import 'trace_screen.dart';

class TracesBody extends StatelessWidget {
  const TracesBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  Widget build(BuildContext context) {
    final c = sections.traces;

    return SectionBody<SpanQueryRow>(
      session: session,
      controller: c,
      searchKey: 'traces-search',
      active: active,
      emptyTitle: (l) => l.tracesEmpty,
      header: ListenableBuilder(
        listenable: c,
        builder: (context, _) => Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _SortToggle(
              slowest: c.slowest,
              onChanged: (v) {
                c.slowest = v;
                c.refresh();
              },
            ),
            // The same dictionary the logs use, for the span attributes a
            // trace carries.
            FilterChips(
              filters: c.filters,
              onRemove: (i) {
                c.filters = [...c.filters]..removeAt(i);
                c.refresh();
              },
              onAdd: () async {
                final filter = await pickFilter(
                  context,
                  session: session,
                  fields: sections.fields('traces'),
                );
                if (filter == null) return;
                c.filters = [...c.filters, filter];
                await c.refresh();
              },
            ),
          ],
        ),
      ),
      card: (context, row) => _TraceCard(
        row: row,
        onOpen: () => Navigator.of(context).push(
          MaterialPageRoute<void>(
            builder: (_) => TraceScreen(
              session: session,
              sections: sections,
              traceId: row.traceId,
            ),
          ),
        ),
      ),
    );
  }
}

/// The two questions a traces list answers: what just happened, and what is
/// slow. A switch rather than the web's sort menu, because there are two.
class _SortToggle extends StatelessWidget {
  const _SortToggle({required this.slowest, required this.onChanged});

  final bool slowest;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: SegmentedButton<bool>(
        key: const Key('traces-sort'),
        segments: [
          ButtonSegment(value: false, label: Text(l.tracesNewest)),
          ButtonSegment(value: true, label: Text(l.tracesSlowest)),
        ],
        selected: {slowest},
        showSelectedIcon: false,
        onSelectionChanged: (s) => onChanged(s.first),
      ),
    );
  }
}

class _TraceCard extends StatelessWidget {
  const _TraceCard({required this.row, required this.onOpen});

  final SpanQueryRow row;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return Card(
      key: Key('trace-${row.traceId}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: InkWell(
        onTap: onOpen,
        borderRadius: BorderRadius.circular(Radii.lg),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Text(
                      // The transaction is what the request was for; the span
                      // name is what it was called. Prefer the former and fall
                      // back, because one of them is always empty.
                      row.transactionName.isNotEmpty
                          ? row.transactionName
                          : row.name,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.titleSmall?.copyWith(
                        color: row.isError
                            ? severityTextColor(context, SeverityLevel.critical)
                            : null,
                      ),
                    ),
                  ),
                  const SizedBox(width: 8),
                  Text(
                    '${row.durationMs.toStringAsFixed(row.durationMs >= 100 ? 0 : 1)} ms',
                    style: theme.textTheme.bodyMedium,
                  ),
                ],
              ),
              const SizedBox(height: 6),
              Wrap(
                spacing: 8,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Text(
                    row.serviceName,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                  if (row.httpStatusCode > 0)
                    Text(
                      '${row.httpStatusCode}',
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: row.httpStatusCode >= 500
                            ? severityTextColor(context, SeverityLevel.critical)
                            : theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  if (row.isError)
                    Tag(label: l.tracesError, level: SeverityLevel.critical),
                  Text(
                    relativeTimeOf(l, row.timestamp),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}
