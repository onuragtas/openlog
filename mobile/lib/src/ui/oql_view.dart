// How an OQL answer is drawn on a phone.
//
// One place, because a dashboard widget and the query console have to agree:
// the same query run from either must look like the same answer, and before
// this lived here the console would have been a second opinion about what a
// facet or a series looks like.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../dashboards.dart';
import 'sparkline.dart';

class OqlResultView extends StatelessWidget {
  const OqlResultView({
    super.key,
    required this.result,
    required this.id,
    this.facetLimit = 5,
  });

  final OqlResult result;

  /// Suffix for the keys of the parts, so a screen with several of these can
  /// be asserted on part by part.
  final String id;

  /// How many facets to draw. A dashboard widget shows the top few; the query
  /// console shows more, because that is the screen the person came to read.
  final int facetLimit;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;

    switch (result.kind) {
      case OqlResultKind.single:
        final v = singleValue(result);
        return Text(
          v == null ? l.dashboardNoData : formatNumber(v),
          key: Key('single-$id'),
          style: text.displaySmall?.copyWith(fontWeight: FontWeight.w600),
        );
      case OqlResultKind.timeseries:
        final values = seriesValues(result);
        if (values.isEmpty) {
          return Text(l.dashboardNoData, style: text.bodySmall);
        }
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(formatNumber(values.last), style: text.headlineSmall),
            const SizedBox(height: 8),
            SizedBox(
              height: 48,
              child: Sparkline(
                key: Key('spark-$id'),
                values: values,
                color: scheme.primary,
              ),
            ),
          ],
        );
      case OqlResultKind.facets:
        final rows = facetRows(result);
        if (rows.isEmpty) return Text(l.dashboardNoData, style: text.bodySmall);
        final max = rows.first.$2 == 0 ? 1.0 : rows.first.$2;
        return Column(
          key: Key('facets-$id'),
          children: [
            // Not all of them: a facet can have hundreds and the point on a
            // phone is which few are on top.
            for (final row in rows.take(facetLimit))
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 3),
                child: Row(
                  children: [
                    Expanded(
                      child: Text(
                        row.$1.isEmpty ? '—' : row.$1,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: text.bodyMedium,
                      ),
                    ),
                    SizedBox(
                      width: 70,
                      child: LinearProgressIndicator(
                        value: (row.$2 / max).clamp(0.0, 1.0),
                        backgroundColor: scheme.surfaceContainerHighest,
                      ),
                    ),
                    const SizedBox(width: 10),
                    Text(formatNumber(row.$2), style: text.bodySmall),
                  ],
                ),
              ),
          ],
        );
      case OqlResultKind.histogram:
      case OqlResultKind.unknown:
        // A histogram wants width this screen does not have, and a kind this
        // build does not know cannot be drawn at all. Saying so is better than
        // an empty card that looks broken.
        return Text(l.dashboardOnWeb, style: text.bodySmall);
    }
  }
}

/// Compact numbers: a dashboard value is read at a glance, and 1234567 is not.
String formatNumber(double v) {
  final abs = v.abs();
  if (abs >= 1e9) return '${(v / 1e9).toStringAsFixed(1)}B';
  if (abs >= 1e6) return '${(v / 1e6).toStringAsFixed(1)}M';
  if (abs >= 1e3) return '${(v / 1e3).toStringAsFixed(1)}k';
  if (abs >= 10) return v.toStringAsFixed(0);
  if (abs >= 1) return v.toStringAsFixed(1);
  return v.toStringAsFixed(2);
}
