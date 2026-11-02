// One transaction: its numbers, how its latency is spread and the slowest
// traces of it. The web opens the same thing above the transactions table; on
// a phone there is no room above a list, so it is a screen.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../apm_format.dart';
import '../sections.dart';
import '../services.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'logs_screen.dart';
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';
import 'trace_screen.dart';

class TransactionScreen extends StatefulWidget {
  const TransactionScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.serviceName,
    required this.name,
    this.onShowTraces,
  });

  final SessionController session;
  final Sections sections;
  final String serviceName;
  final String name;

  /// Goes to the traces tab, narrowed to this transaction.
  final VoidCallback? onShowTraces;

  @override
  State<TransactionScreen> createState() => _TransactionScreenState();
}

class _TransactionScreenState extends State<TransactionScreen> {
  late final ApmTransactionController _c = widget.sections.apmTransaction(
    widget.serviceName,
    widget.name,
  );

  @override
  void initState() {
    super.initState();
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
      builder: (context, _) => DetailScreen<ApmTransactionDetail>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: widget.name,
        subtitle: widget.serviceName,
        builder: (context, d) => _body(context, l, d),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, ApmTransactionDetail d) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final t = d.totals;
    final throughput = [for (final p in d.series) p.throughput];

    return [
      SelectableText(widget.name, style: theme.textTheme.titleSmall),
      if (d.transactionType.isNotEmpty) Text(d.transactionType, style: muted),
      const SizedBox(height: 8),
      Wrap(
        spacing: 8,
        runSpacing: 4,
        children: [
          OutlinedButton.icon(
            key: const Key('transaction-logs'),
            onPressed: () => Navigator.of(context).push(
              MaterialPageRoute<void>(
                builder: (_) => LogsScreen(
                  session: widget.session,
                  sections: widget.sections,
                  logs: widget.sections.transactionLogs(
                    widget.serviceName,
                    widget.name,
                  ),
                  title: l.txRelatedLogs,
                  scopeLabel: l.txRelatedLogs,
                ),
              ),
            ),
            icon: const Icon(Icons.article_outlined, size: 18),
            label: Text(l.txRelatedLogs),
          ),
          if (widget.onShowTraces != null)
            OutlinedButton.icon(
              key: const Key('transaction-traces'),
              onPressed: () {
                Navigator.of(context).pop();
                widget.onShowTraces!();
              },
              icon: const Icon(Icons.timeline, size: 18),
              label: Text(l.txTraces),
            ),
        ],
      ),
      const SizedBox(height: 12),
      Wrap(
        key: const Key('transaction-totals'),
        spacing: 20,
        runSpacing: 10,
        children: [
          Stat(label: l.apmSortThroughput, value: formatApmRpm(t.throughput)),
          Stat(
            label: l.apmSortErrorRate,
            value: formatApmRate(t.errorRate),
            color: t.errorRate > 0
                ? severityTextColor(context, SeverityLevel.critical)
                : null,
          ),
          Stat(label: l.dbAvg, value: formatApmMs(t.avgMs)),
          Stat(label: 'p95', value: formatApmMs(t.p95Ms)),
          Stat(label: 'p99', value: formatApmMs(t.p99Ms)),
          Stat(label: l.svcApdex, value: formatApmApdex(t.apdex)),
        ],
      ),
      DetailSection(
        title: l.txHistogram,
        children: [_Histogram(bins: d.histogram)],
      ),
      DetailSection(
        title: l.txLatency,
        children: [
          // The web draws three lines on one chart. A phone's sparkline has no
          // legend to tell them apart, so each has its own.
          for (final (label, values) in [
            ('p50', [for (final p in d.series) p.p50Ms]),
            ('p95', [for (final p in d.series) p.p95Ms]),
            ('p99', [for (final p in d.series) p.p99Ms]),
          ])
            _Line(
              label: label,
              // A point nobody measured is a gap on the web. A sparkline has
              // no gaps, so it is left out rather than drawn as a zero.
              values: values.nonNulls.toList(),
              color: colors.primary,
            ),
        ],
      ),
      DetailSection(
        title: l.apmSortThroughput,
        children: [
          _Line(label: 'rpm', values: throughput, color: colors.primary),
        ],
      ),
      DetailSection(
        title: l.txSlowest,
        children: [
          if (d.slowest.isEmpty)
            Text(
              l.txNoTraces,
              key: const Key('transaction-no-traces'),
              style: muted,
            )
          else
            for (final s in d.slowest)
              _Sample(
                sample: s,
                onOpen: () => Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => TraceScreen(
                      session: widget.session,
                      sections: widget.sections,
                      traceId: s.traceId,
                    ),
                  ),
                ),
              ),
        ],
      ),
    ];
  }
}

class _Line extends StatelessWidget {
  const _Line({required this.label, required this.values, required this.color});

  final String label;
  final List<double> values;
  final Color color;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        children: [
          SizedBox(
            width: 40,
            child: Text(label, style: theme.textTheme.bodySmall),
          ),
          Expanded(
            child: SizedBox(
              height: 32,
              child: values.length < 2
                  ? const SizedBox.shrink()
                  : Sparkline(values: values, color: color),
            ),
          ),
        ],
      ),
    );
  }
}

/// The latency distribution, on the web's log scale: a bar per bucket, with the
/// lowest, the middle and the highest bound under them.
class _Histogram extends StatelessWidget {
  const _Histogram({required this.bins});

  final List<ApmHistogramBin> bins;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final shaped = shapeHistogram(bins);
    if (shaped.isEmpty) return Text(l.txNoTraces, style: muted);
    final max = shaped.map((b) => b.count).reduce((a, b) => a > b ? a : b);

    return Column(
      key: const Key('transaction-histogram'),
      children: [
        SizedBox(
          height: 96,
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              for (final b in shaped)
                Expanded(
                  child: Padding(
                    padding: const EdgeInsets.symmetric(horizontal: 0.5),
                    child: FractionallySizedBox(
                      alignment: Alignment.bottomCenter,
                      // A bucket with anything in it stays visible next to
                      // one with a thousand times more, as on the web.
                      heightFactor: b.count <= 0
                          ? 0
                          : (b.count / (max <= 0 ? 1 : max)).clamp(0.02, 1.0),
                      child: ColoredBox(
                        color: colorsOf(context).primary.withValues(alpha: 0.7),
                      ),
                    ),
                  ),
                ),
            ],
          ),
        ),
        const SizedBox(height: 4),
        Row(
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            Text(formatApmMs(shaped.first.fromMs), style: muted),
            Text(formatApmMs(shaped[shaped.length ~/ 2].toMs), style: muted),
            Text(formatApmMs(shaped.last.toMs), style: muted),
          ],
        ),
      ],
    );
  }
}

class _Sample extends StatelessWidget {
  const _Sample({required this.sample, required this.onOpen});

  final ApmTraceSample sample;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final id = sample.traceId;

    return InkWell(
      key: Key('transaction-sample-${sample.spanId}'),
      onTap: onOpen,
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 8),
        child: Row(
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    id.length > 16 ? '${id.substring(0, 16)}…' : id,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.primary,
                      fontFamily: 'monospace',
                    ),
                  ),
                  Text(relativeTimeOf(l, sample.timestamp), style: muted),
                ],
              ),
            ),
            if (sample.isError) ...[
              Tag(
                label: sample.httpStatusCode > 0
                    ? '${sample.httpStatusCode}'
                    : l.tracesError,
                level: SeverityLevel.critical,
              ),
              const SizedBox(width: 8),
            ],
            Text(
              formatApmMs(sample.durationMs),
              style: theme.textTheme.bodyMedium?.copyWith(
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
          ],
        ),
      ),
    );
  }
}
