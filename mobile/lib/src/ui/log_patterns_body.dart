// What is being said in the logs, rather than what was said at 10:04.
//
// The web's Patterns view. A thousand lines a minute is not something
// anybody reads; the templates behind them are. Tapping one filters the log
// list by its id, which is the move the whole view exists for.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../fields.dart';
import '../logs.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';

class LogPatternsBody extends StatelessWidget {
  const LogPatternsBody({
    super.key,
    required this.session,
    required this.controller,
    required this.onOpenPattern,
  });

  final SessionController session;
  final LogPatternsController controller;

  /// Lists the records of one pattern. `pattern_id` is a filter key, so
  /// this is the same log list with one more condition.
  final void Function(LogPattern pattern) onOpenPattern;

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
        return RefreshIndicator(
          onRefresh: c.refresh,
          child: ListView(
            padding: const EdgeInsets.fromLTRB(12, 8, 12, 24),
            children: [
              FailureBanner(failure: c.failure, baseUrl: session.baseUrl ?? ''),
              if (c.items.isEmpty)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 36),
                  child: Center(
                    child: Text(
                      l.patternsEmpty,
                      key: const Key('patterns-empty'),
                      style: theme.textTheme.bodyMedium?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  ),
                )
              else ...[
                Text(
                  [
                    l.patternsTotal(c.total),
                    // Records with no pattern at all are part of the
                    // count: leaving them out would make the patterns look
                    // like the whole story.
                    if (c.unclassified > 0)
                      l.patternsUnclassified(c.unclassified),
                    // Whole hours instead of the exact range, which is why
                    // the numbers can differ from the log list's.
                    if (c.rollup) l.patternsRollup,
                  ].join(' · '),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                const SizedBox(height: 6),
                for (final p in c.items)
                  _PatternCard(pattern: p, onOpen: () => onOpenPattern(p)),
                if (c.truncated)
                  Padding(
                    padding: const EdgeInsets.all(10),
                    child: Text(
                      l.patternsTruncated,
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  ),
              ],
            ],
          ),
        );
      },
    );
  }
}

class _PatternCard extends StatelessWidget {
  const _PatternCard({required this.pattern, required this.onOpen});

  final LogPattern pattern;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final s = pattern.severity;
    // The worst level in the mix decides the colour: a template that is
    // mostly INFO but sometimes FATAL is a FATAL problem.
    final level = s.fatal > 0 || s.error > 0
        ? SeverityLevel.critical
        : s.warn > 0
        ? SeverityLevel.warning
        : SeverityLevel.unknown;

    return Card(
      key: Key('pattern-${pattern.patternId}'),
      margin: const EdgeInsets.symmetric(vertical: 4),
      child: InkWell(
        onTap: onOpen,
        borderRadius: BorderRadius.circular(12),
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Expanded(
                    child: Text(
                      pattern.template,
                      maxLines: 3,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.bodyMedium?.copyWith(
                        color: severityTextColor(context, level),
                      ),
                    ),
                  ),
                  const SizedBox(width: 8),
                  Text('${pattern.count}', style: theme.textTheme.titleSmall),
                ],
              ),
              const SizedBox(height: 4),
              Wrap(
                spacing: 8,
                runSpacing: 2,
                children: [
                  for (final service in pattern.services.take(3))
                    Text(service, style: muted),
                  Text(relativeTimeOf(l, pattern.lastSeen), style: muted),
                  if (s.error + s.fatal > 0)
                    Text(
                      l.patternsErrors(s.error + s.fatal),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: severityTextColor(
                          context,
                          SeverityLevel.critical,
                        ),
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

/// The filter that lists one pattern's records.
Filter patternFilter(LogPattern pattern) =>
    Filter(key: 'pattern_id', op: 'eq', values: [pattern.patternId]);
