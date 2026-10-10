// One request end to end: every span, nested, with a bar for where its time
// went. The last link of the chain an alert starts -- a rule fired, a service
// is unhealthy, this error is why, and this is the request it happened in.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'logs_screen.dart';
import 'severity.dart';
import 'theme.dart';

class TraceScreen extends StatefulWidget {
  const TraceScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.traceId,
  });

  final SessionController session;
  final Sections sections;
  final String traceId;

  @override
  State<TraceScreen> createState() => _TraceScreenState();
}

class _TraceScreenState extends State<TraceScreen> {
  late final TraceController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.trace(widget.traceId);
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
      builder: (context, _) => DetailScreen<Trace>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: l.traceTitle,
        subtitle: widget.traceId,
        builder: (context, _) {
          final rows = _c.rows;
          if (rows.isEmpty) {
            return [
              const SizedBox(height: 48),
              Text(
                l.traceEmpty,
                textAlign: TextAlign.center,
                style: Theme.of(context).textTheme.titleMedium,
              ),
            ];
          }
          return [
            Align(
              alignment: Alignment.centerLeft,
              child: OutlinedButton.icon(
                key: const Key('trace-logs'),
                onPressed: () => Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => LogsScreen(
                      session: widget.session,
                      sections: widget.sections,
                      logs: widget.sections.scopedLogs(traceId: widget.traceId),
                      title: l.logsOpenForTrace,
                      scopeLabel: l.logsScopedTrace,
                    ),
                  ),
                ),
                icon: const Icon(Icons.article_outlined, size: 18),
                label: Text(l.logsScopedTrace),
              ),
            ),
            const SizedBox(height: 12),
            Text(
              l.traceSpans(rows.length),
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: Theme.of(context).colorScheme.onSurfaceVariant,
              ),
            ),
            const SizedBox(height: 10),
            for (final r in rows)
              _SpanTile(row: r, key: Key('span-${r.span.spanId}')),
          ];
        },
      ),
    );
  }
}

class _SpanTile extends StatelessWidget {
  const _SpanTile({super.key, required this.row});

  final SpanRow row;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final span = row.span;
    final failed = span.statusCode == SpanStatusCode.error;
    final bar = failed
        ? severityTextColor(context, SeverityLevel.critical)
        : colors.primary;

    // The indent is the tree: a phone has no room for connector lines, and
    // eight points per level stays readable past the depth a trace reaches.
    // It applies to the text only -- the bars share one full-width track, or a
    // deep span's bar would be measured against a shorter line and sit to the
    // right of where its time actually was, which is the one thing a timeline
    // must not do.
    final indent = EdgeInsets.only(left: row.depth * 8.0);

    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: indent,
            child: Row(
              children: [
                Expanded(
                  child: Text(
                    span.name,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.bodyMedium?.copyWith(
                      fontWeight: row.depth == 0 ? FontWeight.w600 : null,
                      color: failed ? bar : null,
                    ),
                  ),
                ),
                const SizedBox(width: 8),
                Text(
                  _duration(span.durationNs),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ],
            ),
          ),
          const SizedBox(height: 3),
          // Where this span sat inside the request. A bar rather than a
          // timestamp: "it started late and took most of it" is the thing a
          // person is looking for, and a number does not say that.
          LayoutBuilder(
            builder: (context, c) => Stack(
              children: [
                Container(
                  height: 4,
                  width: c.maxWidth,
                  decoration: BoxDecoration(
                    color: colors.muted,
                    borderRadius: BorderRadius.circular(2),
                  ),
                ),
                Padding(
                  padding: EdgeInsets.only(left: c.maxWidth * row.offset),
                  child: Container(
                    height: 4,
                    // A span too short to see still has to be visible, or the
                    // fast ones vanish and the bar lies about what is there.
                    width: (c.maxWidth * row.width).clamp(2.0, c.maxWidth),
                    decoration: BoxDecoration(
                      color: bar,
                      borderRadius: BorderRadius.circular(2),
                    ),
                  ),
                ),
              ],
            ),
          ),
          const SizedBox(height: 3),
          Padding(
            padding: indent,
            child: Wrap(
              spacing: 8,
              children: [
                Text(
                  span.serviceName,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                if (row.depth == 0)
                  Text(
                    l.traceRoot,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                if (span.statusMessage.isNotEmpty)
                  Text(
                    span.statusMessage,
                    style: theme.textTheme.bodySmall?.copyWith(color: bar),
                  ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

/// Microseconds up to a millisecond, then milliseconds, then seconds: the unit
/// a person would use for that length out loud.
String _duration(int ns) {
  if (ns < 1000) {
    return '${ns}ns';
  }
  if (ns < 1000000) {
    return '${(ns / 1000).toStringAsFixed(0)}µs';
  }
  if (ns < 1000000000) {
    return '${(ns / 1000000).toStringAsFixed(ns < 10000000 ? 1 : 0)}ms';
  }
  return '${(ns / 1000000000).toStringAsFixed(2)}s';
}
