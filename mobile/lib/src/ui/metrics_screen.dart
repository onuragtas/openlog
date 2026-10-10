// The metrics explorer: which metrics exist, and what one of them looks like.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'oql_view.dart';
import 'sections_screen.dart';
import 'trace_screen.dart';
import 'sparkline.dart';
import 'theme.dart';

class MetricsBody extends StatelessWidget {
  const MetricsBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  Widget build(BuildContext context) => SectionBody<MetricInfo>(
    session: session,
    controller: sections.metrics,
    searchKey: 'metrics-search',
    active: active,
    emptyTitle: (l) => l.metricsEmpty,
    card: (context, m) => _MetricCard(
      metric: m,
      onOpen: () => Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) =>
              MetricScreen(session: session, sections: sections, name: m.name),
        ),
      ),
    ),
  );
}

class _MetricCard extends StatelessWidget {
  const _MetricCard({required this.metric, required this.onOpen});

  final MetricInfo metric;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return Card(
      key: Key('metric-${metric.name}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: InkWell(
        onTap: onOpen,
        borderRadius: BorderRadius.circular(Radii.lg),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                metric.name,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: theme.textTheme.titleSmall,
              ),
              if (metric.description.isNotEmpty) ...[
                const SizedBox(height: 4),
                Text(
                  metric.description,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ],
              const SizedBox(height: 8),
              Wrap(
                spacing: 8,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Text(
                    metric.type.wire,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                  if (metric.unit.isNotEmpty)
                    Text(
                      metric.unit,
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  Text(
                    l.metricSeriesCount(metric.series),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                  // Which service sent it is how a person recognises a metric
                  // whose name means nothing to them.
                  if (metric.services.isNotEmpty)
                    Text(
                      metric.services.take(2).join(', '),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.primary,
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

class MetricScreen extends StatefulWidget {
  const MetricScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.name,
  });

  final SessionController session;
  final Sections sections;
  final String name;

  @override
  State<MetricScreen> createState() => _MetricScreenState();
}

class _MetricScreenState extends State<MetricScreen> {
  late final MetricController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.metric(widget.name);
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
      builder: (context, _) => DetailScreen<MetricDetail>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: widget.name,
        subtitle: _c.value?.unit.isNotEmpty == true ? _c.value!.unit : null,
        builder: (context, m) => _body(context, l, m),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, MetricDetail m) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final values = _c.values;
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return [
      if (m.description.isNotEmpty) ...[
        const SizedBox(height: 4),
        Text(m.description, style: theme.textTheme.bodyMedium),
      ],
      const SizedBox(height: 14),
      Wrap(
        spacing: 14,
        runSpacing: 6,
        children: [
          Text(m.type.wire, style: muted),
          Text(m.defaultAggregation.wire, style: muted),
          Text(l.metricSeriesCount(m.series), style: muted),
          for (final s in m.services.take(3))
            Text(
              s,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.primary,
              ),
            ),
        ],
      ),

      if (_c.seriesError != null)
        DetailSection(
          title: m.defaultAggregation.wire,
          children: [
            Text(
              l.metricNoChart(_c.seriesError!),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            ),
          ],
        )
      else if (values.length < 2)
        DetailSection(
          title: m.defaultAggregation.wire,
          children: [Text(l.metricNoPoints, style: muted)],
        )
      else
        DetailSection(
          title: m.defaultAggregation.wire,
          children: [
            Text(
              formatNumber(values.last),
              style: theme.textTheme.headlineSmall,
            ),
            const SizedBox(height: 8),
            SizedBox(
              height: 64,
              child: Sparkline(
                key: const Key('metric-spark'),
                values: values,
                color: colors.primary,
              ),
            ),
            // One line out of many is a choice, and the screen says so rather
            // than letting the person read it as the whole metric.
            if ((_c.series?.series.length ?? 0) > 1)
              Padding(
                padding: const EdgeInsets.only(top: 6),
                child: Text(
                  l.metricOneSeries(_c.series!.series.length),
                  style: muted,
                ),
              ),
            if (_c.series?.truncated == true)
              Text(l.metricTruncated, style: muted),
          ],
        ),

      if (m.attributeKeys.isNotEmpty)
        DetailSection(
          title: l.metricAttributes,
          children: [
            Wrap(
              spacing: 8,
              runSpacing: 6,
              children: [
                for (final k in m.attributeKeys.take(30))
                  Text(k.key, style: theme.textTheme.bodySmall),
              ],
            ),
          ],
        ),
      if (m.resourceKeys.isNotEmpty)
        DetailSection(
          title: l.metricResourceKeys,
          children: [
            Wrap(
              spacing: 8,
              runSpacing: 6,
              children: [
                for (final k in m.resourceKeys.take(30))
                  Text(k.key, style: theme.textTheme.bodySmall),
              ],
            ),
          ],
        ),

      // The traces behind the points: a spike in the chart above, opened
      // as the request that made it. Asked for on demand, because they
      // follow the trace retention and an old window simply has none.
      DetailSection(
        title: l.metricExemplars,
        children: [
          if (_c.exemplarsError != null)
            Text(
              l.metricExemplarsFailed(_c.exemplarsError!),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            )
          else if (_c.exemplars.isEmpty && !_c.exemplarsLoading)
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton(
                key: const Key('metric-exemplars'),
                onPressed: _c.loadExemplars,
                child: Text(l.metricExemplarsLoad),
              ),
            )
          else if (_c.exemplarsLoading)
            const Padding(
              padding: EdgeInsets.symmetric(vertical: 12),
              child: Center(
                child: SizedBox(
                  width: 18,
                  height: 18,
                  child: CircularProgressIndicator(strokeWidth: 2),
                ),
              ),
            )
          else ...[
            for (final e in _c.exemplars)
              ListTile(
                key: Key('exemplar-${e.spanId}-${e.traceId}'),
                contentPadding: EdgeInsets.zero,
                visualDensity: VisualDensity.compact,
                title: Text(
                  // The exemplar's own measurement, not the point's
                  // aggregate: that is the whole reason it was kept.
                  '${_number(e.value)} · ${e.serviceName}',
                  style: theme.textTheme.bodyMedium,
                ),
                subtitle: Text(
                  relativeTimeOf(l, e.timestamp),
                  style: theme.textTheme.bodySmall,
                ),
                trailing: const Icon(Icons.chevron_right, size: 18),
                onTap: () => Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => TraceScreen(
                      session: widget.session,
                      sections: widget.sections,
                      traceId: e.traceId,
                    ),
                  ),
                ),
              ),
            if (_c.exemplarsTruncated)
              Text(
                l.metricExemplarsTruncated,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
          ],
        ],
      ),
    ];
  }
}

/// Enough digits to be useful and not enough to be noise.
String _number(double v) {
  final abs = v.abs();
  final fixed = abs >= 100
      ? v.toStringAsFixed(0)
      : abs >= 1
      ? v.toStringAsFixed(2)
      : v.toStringAsFixed(4);
  if (!fixed.contains('.')) return fixed;
  return fixed
      .replaceFirst(RegExp(r'0+$'), '')
      .replaceFirst(RegExp(r'\.$'), '');
}
