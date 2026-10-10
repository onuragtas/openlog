// What the fleet costs. An estimate, and the screen says so next to every
// figure -- the server sends the provenance for exactly that reason.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'sparkline.dart';
import 'theme.dart';
import 'severity.dart';

class CostsBody extends StatefulWidget {
  const CostsBody({
    super.key,
    required this.session,
    required this.costs,
    required this.active,
  });

  final SessionController session;
  final CostsController costs;
  final bool active;

  @override
  State<CostsBody> createState() => _CostsBodyState();
}

class _CostsBodyState extends State<CostsBody> {
  @override
  void initState() {
    super.initState();
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(CostsBody old) {
    super.didUpdateWidget(old);
    _loadIfVisible();
  }

  void _loadIfVisible() {
    final c = widget.costs;
    if (!widget.active || c.loaded || c.loadingFirst) return;
    WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);

    return ListenableBuilder(
      listenable: widget.costs,
      builder: (context, _) => DetailBody<CostHostPage>(
        controller: widget.costs,
        baseUrl: widget.session.baseUrl ?? '',
        builder: (context, page) => _body(context, l, page),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, CostHostPage page) {
    final theme = Theme.of(context);
    final s = page.summary;
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    // The currency rides on the label, not on every figure: "413 USD" in a
    // quarter of a 390-point screen wraps, and four wrapped stats are harder
    // to read than one word saying which money this is.
    //
    // Cents below a hundred, none above: a run rate of 1.72 an hour
    // rounded to "2" says something else entirely.
    String money(double v) =>
        v >= 100 ? v.toStringAsFixed(0) : v.toStringAsFixed(2);

    return [
      const SizedBox(height: 4),
      Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Stat(
              label: '${l.costsTotal} (${s.currency})',
              value: money(s.total),
            ),
          ),
          Expanded(
            child: Stat(
              label: '${l.costsPerHour} (${s.currency})',
              value: money(s.perHour),
            ),
          ),
          Expanded(
            child: Stat(
              label: l.costsIdle,
              value: '${(s.idleShare * 100).round()}%',
              // Idle capacity is the number somebody can act on, and past a
              // third of the bill it is worth looking at.
              color: s.idleShare >= 0.33
                  ? severityTextColor(context, SeverityLevel.warning)
                  : null,
            ),
          ),
          Expanded(
            child: Stat(
              // What the containers of a known service cost. The web's
              // fourth tile too -- the host count is in the list heading.
              label: l.costsServices,
              value: money(s.services),
            ),
          ),
        ],
      ),

      const SizedBox(height: 16),
      // Never let an estimate be mistaken for a bill: the server sends this
      // provenance with every answer and it belongs next to the figures.
      Text(
        l.costsEstimate(page.pricing.updated, page.pricing.note),
        style: muted,
      ),
      if (s.unpricedHosts > 0)
        Padding(
          padding: const EdgeInsets.only(top: 4),
          child: Text(
            l.costsUnpriced(s.unpricedHosts),
            style: theme.textTheme.bodySmall?.copyWith(
              color: severityTextColor(context, SeverityLevel.warning),
            ),
          ),
        ),

      // The four buckets add up to the total exactly (cost.md §3), so the
      // bar is a decomposition rather than an illustration.
      if (s.total > 0)
        DetailSection(
          title: l.costsBuckets,
          children: [_Buckets(summary: s, money: money)],
        ),

      if ((widget.costs.trend?.points.length ?? 0) >= 2)
        DetailSection(
          title: '${l.costsTrend} (${s.currency})',
          children: [_Trend(trend: widget.costs.trend!)],
        ),

      if (widget.costs.services.isNotEmpty)
        DetailSection(
          title: '${l.costsByService} (${s.currency})',
          children: [
            for (final svc in widget.costs.services)
              Padding(
                key: Key('cost-service-${svc.serviceName}-${svc.environment}'),
                padding: const EdgeInsets.symmetric(vertical: 5),
                child: Row(
                  children: [
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            svc.serviceName,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: theme.textTheme.bodyMedium,
                          ),
                          Text(
                            [
                              if (svc.environment.isNotEmpty) svc.environment,
                              l.costsContainers(svc.containers),
                            ].join(' · '),
                            style: muted,
                          ),
                        ],
                      ),
                    ),
                    Text(
                      // The list's own decimals, from its biggest row.
                      widget.costs.services.first.total >= 100
                          ? svc.total.toStringAsFixed(0)
                          : svc.total.toStringAsFixed(2),
                      style: theme.textTheme.bodyMedium?.copyWith(
                        fontFeatures: const [FontFeature.tabularFigures()],
                      ),
                    ),
                  ],
                ),
              ),
          ],
        ),

      DetailSection(
        title: '${l.costsHostsTitle} (${s.currency})',
        children: [
          for (final h in page.hosts)
            _HostRow(host: h, key: Key('cost-${h.hostId}')),
          if (page.total > page.hosts.length)
            Padding(
              padding: const EdgeInsets.only(top: 6),
              child: Text(
                l.costsMoreHosts(page.total - page.hosts.length),
                style: muted,
              ),
            ),
        ],
      ),
    ];
  }
}

class _HostRow extends StatelessWidget {
  const _HostRow({super.key, required this.host});

  final CostHost host;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  host.hostName.isEmpty ? host.hostId : host.hostName,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              const SizedBox(width: 8),
              Text(
                // An unpriced host is not a free host. Showing it as 0
                // would put it at the bottom of the list looking cheap.
                !host.priced
                    ? '-'
                    : host.total >= 100
                    ? host.total.toStringAsFixed(0)
                    : host.total.toStringAsFixed(2),
                style: theme.textTheme.bodyMedium?.copyWith(
                  fontWeight: FontWeight.w600,
                ),
              ),
            ],
          ),
          const SizedBox(height: 2),
          Wrap(
            spacing: 8,
            runSpacing: 2,
            children: [
              if (host.instanceType.isNotEmpty)
                Text(host.instanceType, style: muted),
              if (host.region.isNotEmpty) Text(host.region, style: muted),
              Text(l.costsUsed((host.usedShare * 100).round()), style: muted),
              // An unpriced host is not a free host, and the list would
              // otherwise show it at the bottom as if it cost nothing.
              if (!host.priced)
                Text(
                  l.costsNoPrice,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: severityTextColor(context, SeverityLevel.warning),
                  ),
                ),
            ],
          ),
        ],
      ),
    );
  }
}

/// The bill, decomposed: services, unallocated, unattributed, idle.
class _Buckets extends StatelessWidget {
  const _Buckets({required this.summary, required this.money});

  final CostSummary summary;

  /// Unused for the pieces themselves: a list decides its own decimals
  /// from its largest value, so "210" and "64.00" never sit in one column.
  final String Function(double) money;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final s = summary;
    final parts = [
      (
        label: l.costsBucketServices,
        help: l.costsBucketServicesHelp,
        value: s.services,
        color: colors.primary,
      ),
      (
        label: l.costsBucketUnallocated,
        help: l.costsBucketUnallocatedHelp,
        value: s.unallocated,
        color: colors.primary.withValues(alpha: 0.6),
      ),
      (
        label: l.costsBucketUnattributed,
        help: l.costsBucketUnattributedHelp,
        value: s.unattributed,
        color: colors.primary.withValues(alpha: 0.3),
      ),
      (
        label: l.costsBucketIdle,
        help: l.costsBucketIdleHelp,
        value: s.idle,
        color: theme.colorScheme.surfaceContainerHighest,
      ),
    ].where((p) => p.value > 0).toList();
    final biggest = parts.fold<double>(0, (m, p) => p.value > m ? p.value : m);
    String amount(double v) =>
        biggest >= 100 ? v.toStringAsFixed(0) : v.toStringAsFixed(2);

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        ClipRRect(
          borderRadius: BorderRadius.circular(4),
          child: SizedBox(
            height: 10,
            child: Row(
              key: const Key('cost-buckets'),
              // Stretch, so each piece is told to be the bar's full
              // height: a box with no child takes the smallest height it
              // is allowed, which is none at all.
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                for (final p in parts)
                  Expanded(
                    flex: (p.value / s.total * 1000).round().clamp(1, 1000),
                    // No child: a ColoredBox with one sizes to it, and an
                    // empty SizedBox is zero by zero -- which is how this
                    // bar was invisible.
                    child: ColoredBox(color: p.color),
                  ),
              ],
            ),
          ),
        ),
        const SizedBox(height: 10),
        for (final p in parts)
          Padding(
            padding: const EdgeInsets.only(bottom: 6),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Padding(
                  padding: const EdgeInsets.only(top: 4, right: 8),
                  child: SizedBox(
                    width: 10,
                    height: 10,
                    child: DecoratedBox(
                      decoration: BoxDecoration(
                        color: p.color,
                        shape: BoxShape.circle,
                      ),
                    ),
                  ),
                ),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        '${p.label} · ${amount(p.value)}',
                        style: theme.textTheme.bodyMedium,
                      ),
                      Text(
                        p.help,
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),
      ],
    );
  }
}

/// The bill over time, with the idle part under it.
class _Trend extends StatelessWidget {
  const _Trend({required this.trend});

  final CostTrend trend;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final total = [for (final p in trend.points) p.total];
    final idle = [for (final p in trend.points) p.idle];
    // One scale for both lines: idle is part of the total, and drawing
    // them on two scales would make a tenth of the bill look like half.
    final top = total.reduce((a, b) => a > b ? a : b);

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        SizedBox(
          height: 64,
          child: Stack(
            children: [
              Sparkline(
                key: const Key('cost-trend-total'),
                values: total,
                color: colors.primary,
                minimum: 0,
                maximum: top,
              ),
              Sparkline(
                key: const Key('cost-trend-idle'),
                values: idle,
                color: theme.colorScheme.onSurfaceVariant,
                minimum: 0,
                maximum: top,
              ),
            ],
          ),
        ),
        const SizedBox(height: 4),
        Text(
          '${l.costsTrendTotal} · ${l.costsTrendIdle}',
          style: theme.textTheme.bodySmall?.copyWith(
            color: theme.colorScheme.onSurfaceVariant,
          ),
        ),
      ],
    );
  }
}
