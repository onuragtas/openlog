// What one host costs: the card on the host screen, and what a row of the cost
// list opens. An estimate from a price table, and it says so next to the figure.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../infra_detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'severity.dart';

/// The web's HostCostCard: the bill, the run rate, the idle share and what the
/// machine is. A host openlog cannot price says so rather than showing a zero.
class HostCostSummary extends StatelessWidget {
  const HostCostSummary({super.key, required this.cost});

  final CostHostDetail cost;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final h = cost.host;
    final currency = cost.pricing.currency;

    if (!h.priced) return Text(l.costHostNotPriced, style: muted);
    return Wrap(
      spacing: 10,
      runSpacing: 4,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [
        Text(
          formatMoney(h.total, currency),
          style: theme.textTheme.titleMedium,
        ),
        Text(
          l.costHostPerHour(formatMoney(h.price.usdPerHour, currency)),
          style: muted,
        ),
        Text(l.costHostIdleShare((h.idleShare * 100).round()), style: muted),
        if (h.instanceType.isNotEmpty)
          Text(
            [
              h.instanceType,
              if (h.region.isNotEmpty) h.region,
              if (h.lifecycle.isNotEmpty && h.lifecycle != 'on-demand')
                h.lifecycle,
            ].join(' · '),
            style: muted,
          ),
        if (h.price.source == CostSource.fallback)
          Text(l.costHostFallback, style: muted),
      ],
    );
  }
}

class CostHostScreen extends StatefulWidget {
  const CostHostScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.hostId,
    required this.hostName,
  });

  final SessionController session;
  final Sections sections;
  final String hostId;
  final String hostName;

  @override
  State<CostHostScreen> createState() => _CostHostScreenState();
}

class _CostHostScreenState extends State<CostHostScreen> {
  late final _c = CostHostController(widget.sections.client, widget.hostId);

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
      builder: (context, _) => DetailScreen<CostHostDetail>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: widget.hostName,
        subtitle: l.costHostTitle,
        builder: (context, d) => _body(context, d),
      ),
    );
  }

  List<Widget> _body(BuildContext context, CostHostDetail d) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final currency = d.pricing.currency;

    return [
      HostCostSummary(cost: d),
      const SizedBox(height: 10),
      Text(l.costsEstimate(d.pricing.updated, d.pricing.note), style: muted),
      if (d.host.oversubscribed)
        Padding(
          padding: const EdgeInsets.only(top: 4),
          child: Text(
            l.costHostOversubscribed,
            style: theme.textTheme.bodySmall?.copyWith(
              color: severityTextColor(context, SeverityLevel.warning),
            ),
          ),
        ),
      if (d.services.isNotEmpty)
        DetailSection(
          title: l.costHostServices,
          children: [
            for (final s in d.services)
              KeyValue(
                key: Key('cost-host-service-${s.serviceName}'),
                name: s.serviceName,
                value: formatMoney(s.total, currency),
              ),
          ],
        ),
      if (d.containers.isNotEmpty)
        DetailSection(
          title: l.costHostContainers,
          children: [
            for (final c in d.containers)
              Padding(
                key: Key('cost-host-container-${c.containerId}'),
                padding: const EdgeInsets.only(bottom: 8),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      c.containerName.isEmpty ? c.containerId : c.containerName,
                      style: theme.textTheme.bodyMedium,
                    ),
                    Text(
                      [
                        formatMoney(c.total, currency),
                        l.costHostShare((c.share * 100).round()),
                        if (c.serviceName.isNotEmpty) c.serviceName,
                      ].join(' · '),
                      style: muted,
                    ),
                  ],
                ),
              ),
          ],
        ),
    ];
  }
}
