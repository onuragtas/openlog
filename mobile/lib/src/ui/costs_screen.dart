// What the fleet costs. An estimate, and the screen says so next to every
// figure -- the server sends the provenance for exactly that reason.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../session.dart';
import 'detail_scaffold.dart';
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
            child: Stat(label: l.costsHosts, value: '${s.hosts}'),
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
