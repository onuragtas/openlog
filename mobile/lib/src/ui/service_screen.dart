// One service's golden signals: where an alert points, and the first screen
// that can say whether the thing it complained about is still happening.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';

class ServiceScreen extends StatefulWidget {
  const ServiceScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.serviceName,
  });

  final SessionController session;
  final Sections sections;
  final String serviceName;

  @override
  State<ServiceScreen> createState() => _ServiceScreenState();
}

class _ServiceScreenState extends State<ServiceScreen> {
  late final ServiceOverviewController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.serviceOverview(widget.serviceName);
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
      builder: (context, _) => DetailScreen<ApmOverview>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: widget.serviceName,
        builder: (context, overview) => _body(context, l, overview),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, ApmOverview o) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final t = o.totals;

    // No requests at all is a real answer, not an empty screen: it is what a
    // service that has stopped serving looks like, and saying so beats four
    // zeroes the person has to interpret.
    if (t.requests == 0) {
      return [
        const SizedBox(height: 48),
        Text(
          l.serviceNoData,
          textAlign: TextAlign.center,
          style: theme.textTheme.titleMedium,
        ),
      ];
    }

    final errorPercent = t.errorRate * 100;
    return [
      const SizedBox(height: 8),
      Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Stat(
              label: l.svcThroughput,
              value: t.throughput.toStringAsFixed(t.throughput >= 100 ? 0 : 1),
            ),
          ),
          Expanded(
            child: Stat(
              label: l.serviceErrors,
              value:
                  '${errorPercent.toStringAsFixed(errorPercent >= 10 ? 0 : 1)}%',
              // The web colours an error rate rather than leaving the reader to
              // compare it: anything above a percent is worth looking at.
              color: errorPercent >= 1
                  ? severityTextColor(context, SeverityLevel.critical)
                  : null,
            ),
          ),
          Expanded(
            child: Stat(
              label: l.serviceRequests,
              value: t.requests.toStringAsFixed(0),
            ),
          ),
        ],
      ),
      const SizedBox(height: 18),
      Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(child: _ms(l.serviceLatency, t.p50Ms, 'p50')),
          Expanded(child: _ms('', t.p95Ms, 'p95')),
          Expanded(child: _ms('', t.p99Ms, 'p99')),
          if (t.apdex != null)
            Expanded(
              child: Stat(
                label: l.svcApdex,
                value: t.apdex!.toStringAsFixed(2),
              ),
            ),
        ],
      ),

      if (o.series.length >= 2) ...[
        _chart(context, l.serviceThroughputChart, [
          for (final p in o.series) p.throughput,
        ], colors.primary),
        _chart(
          context,
          l.serviceErrorRateChart,
          [for (final p in o.series) p.errorRate * 100],
          severityTextColor(context, SeverityLevel.critical),
        ),
      ],
    ];
  }

  /// A latency column. [label] is empty for the second and third, so the three
  /// read as one group under one heading rather than as three unrelated numbers.
  Widget _ms(String label, double? value, String percentile) => Stat(
    label: label.isEmpty ? percentile : '$label $percentile',
    value: value == null
        ? '-'
        : '${value.toStringAsFixed(value >= 100 ? 0 : 1)} ms',
  );

  Widget _chart(
    BuildContext context,
    String title,
    List<double> values,
    Color color,
  ) => DetailSection(
    title: title,
    children: [
      SizedBox(
        height: 64,
        child: Sparkline(values: values, color: color),
      ),
    ],
  );
}
