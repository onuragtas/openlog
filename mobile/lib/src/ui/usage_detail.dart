// The rest of the usage screen: the shape of the period, who is storing the
// most, where the organization stands against its quota, and the limits its
// queries run under.
//
// The overview above these says how much was used; these four say *when*,
// *by whom*, *how close to the ceiling* and *what a query may do* — which
// are the questions asked right after the first number is read.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../session.dart';
import '../usage.dart';
import 'detail_scaffold.dart';
import 'failure_text.dart';
import 'list_scaffold.dart' show relativeTimeOf;
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';

class UsageDetailSections extends StatelessWidget {
  const UsageDetailSections({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final UsageDetailController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.loading && !c.loaded) {
          return const Padding(
            padding: EdgeInsets.symmetric(vertical: 24),
            child: Center(child: CircularProgressIndicator()),
          );
        }
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            FailureBanner(failure: c.failure, baseUrl: session.baseUrl ?? ''),
            if (c.status != null) _quota(context, l, c.status!),
            if (c.daily != null) _daily(context, l, c.daily!),
            if (c.top != null) _top(context, l, c),
            if (c.limits != null) _limits(context, l, c.limits!),
          ],
        );
      },
    );
  }

  /// Where the organization stands right now: the level, whether ingest is
  /// blocked, and each metric against its limit.
  Widget _quota(BuildContext context, L l, UsageStatus s) {
    final theme = Theme.of(context);
    final level = switch (s.level) {
      QuotaLevel.exceeded => SeverityLevel.critical,
      QuotaLevel.warning => SeverityLevel.warning,
      _ => SeverityLevel.good,
    };
    return DetailSection(
      title: l.usageQuotaTitle,
      children: [
        Wrap(
          spacing: 8,
          runSpacing: 6,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Tag(label: s.level.wire, level: level),
            if (s.evaluatedAt != null)
              Text(
                l.usageQuotaEvaluated(relativeTimeOf(l, s.evaluatedAt!)),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              )
            else
              Text(l.usageQuotaNotEvaluated, style: theme.textTheme.bodySmall),
          ],
        ),
        // The one line that changes what somebody does next.
        if (s.ingestBlocked)
          Padding(
            padding: const EdgeInsets.only(top: 6),
            child: Text(
              l.usageQuotaBlocked,
              style: theme.textTheme.bodyMedium?.copyWith(
                color: severityTextColor(context, SeverityLevel.critical),
                fontWeight: FontWeight.w600,
              ),
            ),
          ),
        const SizedBox(height: 6),
        for (final m in s.metrics)
          KeyValue(
            name: m.metric.wire,
            value: '${m.percent.toStringAsFixed(0)}% · ${_amount(m)}',
          ),
      ],
    );
  }

  /// Bytes for a byte metric, plain counts for the rest: "12 GB / 50 GB" and
  /// "3 / 5 hosts" are both right, and the same formatter cannot say both.
  String _amount(QuotaMetric m) {
    final bytes =
        m.metric.wire.contains('bytes') || m.metric.wire.contains('ingest');
    return bytes
        ? '${formatBytes(m.used)} / ${formatBytes(m.limit)}'
        : '${m.used.toStringAsFixed(0)} / ${m.limit.toStringAsFixed(0)}';
  }

  /// The period's shape. A sparkline rather than the web's stacked bars: on
  /// a phone the question is "is it steady or did one day do it", and a line
  /// answers that in the width available.
  Widget _daily(BuildContext context, L l, UsageDaily d) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final days = d.days;
    if (days.isEmpty) {
      return DetailSection(
        title: l.usageDailyTitle,
        children: [Text(l.usageDailyEmpty, style: theme.textTheme.bodySmall)],
      );
    }
    final values = [for (final day in days) day.ingestBytes];
    final peak = days.reduce((a, b) => a.ingestBytes >= b.ingestBytes ? a : b);
    final average = values.reduce((a, b) => a + b) / values.length;
    return DetailSection(
      title: l.usageDailyTitle,
      children: [
        if (values.length >= 2)
          SizedBox(
            height: 56,
            child: Sparkline(
              key: const Key('usage-daily-spark'),
              values: values,
              color: colors.primary,
            ),
          ),
        const SizedBox(height: 8),
        KeyValue(
          name: l.usageDailyPeak,
          value: '${peak.day} · ${formatBytes(peak.ingestBytes)}',
        ),
        KeyValue(name: l.usageDailyAverage, value: formatBytes(average)),
      ],
    );
  }

  Widget _top(BuildContext context, L l, UsageDetailController c) {
    final theme = Theme.of(context);
    final entries = c.top!.entries;
    return DetailSection(
      title: l.usageTopTitle,
      children: [
        SegmentedButton<String>(
          key: const Key('usage-top-by'),
          showSelectedIcon: false,
          segments: [
            ButtonSegment(value: 'service', label: Text(l.usageTopByService)),
            ButtonSegment(value: 'host', label: Text(l.usageTopByHost)),
          ],
          selected: {c.by},
          onSelectionChanged: (s) {
            c.by = s.first;
            c.load();
          },
        ),
        const SizedBox(height: 10),
        if (entries.isEmpty)
          Text(l.usageTopEmpty, style: theme.textTheme.bodySmall)
        else
          for (final e in entries)
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: Row(
                children: [
                  Expanded(
                    child: Text(
                      e.key.isEmpty ? '-' : e.key,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.bodyMedium,
                    ),
                  ),
                  const SizedBox(width: 8),
                  Text(
                    formatBytes(e.bytes),
                    style: theme.textTheme.bodyMedium?.copyWith(
                      fontWeight: FontWeight.w600,
                    ),
                  ),
                ],
              ),
            ),
      ],
    );
  }

  /// What a query of this organization may do, and which layer decided it.
  Widget _limits(BuildContext context, L l, OrgQueryLimits q) {
    final e = q.effective;
    final s = q.sources;
    return DetailSection(
      title: l.usageLimitsTitle,
      // The three the contract has; a query is cut off by whichever it
      // reaches first.
      children: [
        _limit(
          l,
          l.usageLimitsMemory,
          formatBytes(e.maxMemoryUsage),
          s.maxMemoryUsage,
        ),
        _limit(l, l.usageLimitsRows, _count(e.maxRowsToRead), s.maxRowsToRead),
        _limit(
          l,
          l.usageLimitsBytes,
          formatBytes(e.maxBytesToRead),
          s.maxBytesToRead,
        ),
      ],
    );
  }

  Widget _limit(L l, String name, String value, QueryLimitSource source) =>
      KeyValue(
        name: name,
        // Which layer set it, because "why is my query cut off at 10 GB" is
        // answered by the layer, not by the number.
        value: source.wire.isEmpty
            ? value
            : '$value · ${l.usageLimitsFrom(source.wire)}',
      );

  String _count(int v) {
    if (v >= 1000000000) return '${(v / 1000000000).toStringAsFixed(1)}B';
    if (v >= 1000000) return '${(v / 1000000).toStringAsFixed(0)}M';
    if (v >= 1000) return '${(v / 1000).toStringAsFixed(0)}k';
    return '$v';
  }
}
