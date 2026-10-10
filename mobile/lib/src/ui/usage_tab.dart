// What the organization used this period, against its plan.
//
// The web's Kullanım ve plan tab: the plan, a meter per quota, the counts
// behind them, what is stored per signal, and the projection for the end of
// the period. Same numbers; on a phone they are rows rather than a table.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../session.dart';
import '../usage.dart';
import 'failure_text.dart';
import 'severity.dart';

class UsageTab extends StatelessWidget {
  const UsageTab({super.key, required this.session, required this.controller});

  final SessionController session;
  final UsageController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.loading) {
          return const Center(child: CircularProgressIndicator());
        }
        final o = c.overview;
        return RefreshIndicator(
          onRefresh: c.load,
          child: ListView(
            padding: const EdgeInsets.all(16),
            children: [
              SegmentedButton<String>(
                key: const Key('usage-period'),
                showSelectedIcon: false,
                segments: [
                  ButtonSegment(value: 'current', label: Text(l.usageCurrent)),
                  ButtonSegment(
                    value: 'previous',
                    label: Text(l.usagePrevious),
                  ),
                ],
                selected: {c.period},
                onSelectionChanged: (s) {
                  c.period = s.first;
                  c.load();
                },
              ),
              FailureBanner(failure: c.failure, baseUrl: session.baseUrl ?? ''),
              if (o != null) ...[
                const SizedBox(height: 12),
                Card(
                  child: Padding(
                    padding: const EdgeInsets.all(14),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(
                          children: [
                            Expanded(
                              child: Text(
                                o.plan.name,
                                style: theme.textTheme.titleMedium,
                              ),
                            ),
                            Text(
                              o.saasMode ? l.usageSaas : l.usageSelfHosted,
                              style: theme.textTheme.bodySmall?.copyWith(
                                color: theme.colorScheme.onSurfaceVariant,
                              ),
                            ),
                          ],
                        ),
                        if (!o.planAssigned)
                          Text(
                            // Nobody chose this plan; it is the catalog's
                            // default, which is a different thing from one
                            // somebody picked.
                            l.usagePlanDefault,
                            style: theme.textTheme.bodySmall?.copyWith(
                              color: theme.colorScheme.onSurfaceVariant,
                            ),
                          ),
                        if (o.ingestBlocked)
                          Padding(
                            padding: const EdgeInsets.only(top: 6),
                            child: Text(
                              l.usageBlocked,
                              key: const Key('usage-blocked'),
                              style: theme.textTheme.bodyMedium?.copyWith(
                                color: severityTextColor(
                                  context,
                                  SeverityLevel.critical,
                                ),
                              ),
                            ),
                          ),
                        const SizedBox(height: 10),
                        for (final m in o.limits) _Meter(metric: m),
                        if (o.projection.ingestBytes > 0)
                          Padding(
                            padding: const EdgeInsets.only(top: 6),
                            child: Text(
                              o.projection.ingestPercent == null
                                  ? l.usageProjected(
                                      formatBytes(o.projection.ingestBytes),
                                    )
                                  : l.usageProjectedPercent(
                                      formatBytes(o.projection.ingestBytes),
                                      o.projection.ingestPercent!.round(),
                                    ),
                              style: theme.textTheme.bodySmall?.copyWith(
                                color: theme.colorScheme.onSurfaceVariant,
                              ),
                            ),
                          ),
                      ],
                    ),
                  ),
                ),
                const SizedBox(height: 12),
                Card(
                  child: Padding(
                    padding: const EdgeInsets.all(14),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        _Stat(
                          label: l.usageHosts,
                          value: formatCount(o.usage.hosts),
                        ),
                        _Stat(
                          label: l.usageContainers,
                          value: formatCount(o.usage.containers),
                        ),
                        _Stat(
                          label: l.usageServices,
                          value: formatCount(o.usage.services),
                        ),
                        _Stat(
                          label: l.usageQueries,
                          value: formatCount(o.usage.query.queries),
                        ),
                        _Stat(
                          label: l.usageStored,
                          value: formatBytes(
                            o.stored.fold<double>(
                              0,
                              (sum, s) => sum + s.compressedBytes,
                            ),
                          ),
                        ),
                      ],
                    ),
                  ),
                ),
                const SizedBox(height: 12),
                Card(
                  child: Padding(
                    padding: const EdgeInsets.all(14),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          l.usageBySignal,
                          style: theme.textTheme.titleSmall,
                        ),
                        const SizedBox(height: 6),
                        for (final s in o.usage.signals)
                          _Stat(
                            label: s.signal.wire,
                            value:
                                '${formatCount(s.items)} · '
                                '${formatBytes(s.ingestBytes)}',
                          ),
                        const Divider(height: 18),
                        Text(
                          l.usageRetention,
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: theme.colorScheme.onSurfaceVariant,
                          ),
                        ),
                        for (final s in o.stored)
                          _Stat(
                            label:
                                '${s.signal.wire} · '
                                '${l.usageDays(s.retentionDays)}',
                            value: formatBytes(s.compressedBytes),
                          ),
                      ],
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

/// One quota: how much of it is used, and how close that is to the limit.
class _Meter extends StatelessWidget {
  const _Meter({required this.metric});

  final QuotaMetric metric;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final label = switch (metric.metric) {
      QuotaMetricMetric.ingestBytes => l.usageIngest,
      QuotaMetricMetric.hosts => l.usageHosts,
      QuotaMetricMetric.users => l.usageUsers,
      _ => metric.metric.wire,
    };
    final level = switch (metric.level) {
      QuotaLevel.exceeded => SeverityLevel.critical,
      QuotaLevel.warning => SeverityLevel.warning,
      _ => SeverityLevel.good,
    };

    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(child: Text(label, style: theme.textTheme.bodyMedium)),
              Text(
                quotaValue(metric),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ],
          ),
          const SizedBox(height: 4),
          if (metric.limit == 0)
            Text(
              // No bar for an unlimited quota: a bar needs an end, and
              // drawing one at an arbitrary place would invent a limit.
              l.usageUnlimited,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            )
          else
            ClipRRect(
              borderRadius: BorderRadius.circular(4),
              child: LinearProgressIndicator(
                value: (metric.percent / 100).clamp(0, 1),
                minHeight: 6,
                backgroundColor: theme.colorScheme.surfaceContainerHighest,
                color: severityTextColor(context, level),
              ),
            ),
        ],
      ),
    );
  }
}

class _Stat extends StatelessWidget {
  const _Stat({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 3),
      child: Row(
        children: [
          Expanded(
            child: Text(
              label,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ),
          Text(value, style: theme.textTheme.bodyMedium),
        ],
      ),
    );
  }
}
