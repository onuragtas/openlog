// One Core Web Vital, the way the browser screen shows it on both ends.
//
// The p75 leads: that is the percentile the assessment is defined on. Around
// it the three things a developer asks next -- where the rest of the
// distribution sits (p50/p95), how the measurements split across the three
// bands, and how far the p75 is from the boundary that decides the verdict.
//
// The thresholds and the rating come from the server (rum.md §2.1): they are
// constants of the programme, not settings of an installation, so nothing
// here recomputes a verdict.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import 'severity.dart';
import 'theme.dart';

/// CLS is a unitless score around 0.1; every other vital is a duration, and a
/// duration past a second reads better in seconds than in four digits of
/// milliseconds.
String formatVital(double? value, String unit) {
  if (value == null) return '-';
  if (unit.isEmpty) return value.toStringAsFixed(2);
  if (value.abs() >= 1000) return '${_trim(value / 1000)} s';
  return '${value.toStringAsFixed(0)} ms';
}

/// Two digits under ten, one above, and no trailing zeros -- the web's
/// `formatNumber`, so that 2500 ms reads as "2.5 s" on both ends rather than
/// as "2.50 s" on one of them.
String _trim(double v) {
  var s = v.toStringAsFixed(v.abs() < 10 ? 2 : 1);
  if (!s.contains('.')) return s;
  s = s.replaceFirst(RegExp(r'0+$'), '');
  return s.endsWith('.') ? s.substring(0, s.length - 1) : s;
}

/// The verdict's label and colour. An empty rating means the server had
/// nothing to rate, which is not the same as a bad rating.
(String, SeverityLevel) vitalRating(L l, String rating) => switch (rating) {
  'good' => (l.rumVitalGood, SeverityLevel.good),
  'needs_improvement' => (l.rumVitalNeedsImprovement, SeverityLevel.warning),
  'poor' => (l.rumVitalPoor, SeverityLevel.critical),
  _ => (l.rumVitalNoData, SeverityLevel.unknown),
};

class VitalTile extends StatelessWidget {
  const VitalTile({super.key, required this.vital});

  final RumVital vital;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final (label, level) = vitalRating(l, vital.rating);
    final measured = vital.count > 0;

    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              SizedBox(
                width: 52,
                child: Text(
                  vital.name.wire.toUpperCase(),
                  style: theme.textTheme.bodyMedium?.copyWith(
                    fontWeight: FontWeight.w600,
                  ),
                ),
              ),
              Expanded(
                child: Text(
                  formatVital(vital.p75, vital.unit),
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              Tag(label: label, level: level),
            ],
          ),
          if (!measured)
            Padding(
              padding: const EdgeInsets.only(left: 52, top: 2),
              child: Text(
                l.rumVitalNoData,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            )
          else
            Padding(
              padding: const EdgeInsets.only(left: 52, top: 4),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  // One number is a verdict, three are a shape: a long tail
                  // shows up here and nowhere else.
                  Text(
                    'p50 ${formatVital(vital.p50, vital.unit)} · '
                    'p95 ${formatVital(vital.p95, vital.unit)} · '
                    '${l.rumVitalCount(vital.count.round())}',
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                  const SizedBox(height: 6),
                  _Bands(vital: vital, colors: colors),
                  const SizedBox(height: 4),
                  Text(
                    _distance(l, vital),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ],
              ),
            ),
        ],
      ),
    );
  }

  /// "LCP is 3.2 s" is half an answer; the other half is "good ends at 2.5 s,
  /// so it is 0.7 s over". A vital already rated good is measured against the
  /// boundary it would lose, which is the same sentence read the other way.
  String _distance(L l, RumVital v) {
    final thresholds = l.rumVitalThresholds(
      formatVital(v.goodThreshold, v.unit),
      formatVital(v.poorThreshold, v.unit),
    );
    final p75 = v.p75;
    if (p75 == null || v.rating.isEmpty) return thresholds;
    final target = v.rating == 'poor' ? v.poorThreshold : v.goodThreshold;
    final amount = formatVital((p75 - target).abs(), v.unit);
    final said = v.rating == 'good'
        ? l.rumVitalHeadroom(amount)
        : l.rumVitalOver(amount);
    return '$thresholds · $said';
  }
}

/// The three bands are shares of one total, so they read as one bar rather
/// than three numbers.
class _Bands extends StatelessWidget {
  const _Bands({required this.vital, required this.colors});

  final RumVital vital;
  final AppColors colors;

  @override
  Widget build(BuildContext context) {
    final parts = <(double, Color)>[
      (vital.good, colors.successText),
      (vital.needsImprovement, colors.warningText),
      (vital.poor, colors.destructiveText),
    ];
    return SizedBox(
      height: 6,
      child: ClipRRect(
        borderRadius: BorderRadius.circular(3),
        // Stretch, or a childless coloured box takes the smallest height it
        // is allowed and the bar disappears.
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            for (final (share, color) in parts)
              if (share > 0)
                Expanded(
                  flex: (share * 1000).round(),
                  child: ColoredBox(color: color),
                ),
          ],
        ),
      ),
    );
  }
}
