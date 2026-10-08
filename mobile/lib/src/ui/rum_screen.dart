// What the browser saw: which applications report, and how one of them feels
// to the people using it.
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
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';

class RumBody extends StatelessWidget {
  const RumBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  Widget build(BuildContext context) => SectionBody<RumApp>(
    session: session,
    controller: sections.rum,
    searchKey: 'rum-search',
    // The endpoint takes no query, and a box that does nothing is worse than
    // no box at all.
    searchable: false,
    active: active,
    emptyTitle: (l) => l.rumEmpty,
    card: (context, app) => _AppCard(
      app: app,
      onOpen: () => Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) =>
              RumAppScreen(session: session, sections: sections, app: app.app),
        ),
      ),
    ),
  );
}

class _AppCard extends StatelessWidget {
  const _AppCard({required this.app, required this.onOpen});

  final RumApp app;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Card(
      key: Key('rum-${app.app}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: InkWell(
        onTap: onOpen,
        borderRadius: BorderRadius.circular(Radii.lg),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Text(
                      app.app,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.titleSmall,
                    ),
                  ),
                  if (app.environment.isNotEmpty)
                    Text(app.environment, style: muted),
                ],
              ),
              const SizedBox(height: 8),
              Wrap(
                spacing: 16,
                runSpacing: 6,
                children: [
                  _Pair(label: l.rumViews, value: formatNumber(app.views)),
                  _Pair(
                    label: l.rumSessions,
                    value: formatNumber(app.sessions.toDouble()),
                  ),
                  _Pair(
                    label: l.rumErrors,
                    value: formatNumber(app.errors),
                    // Red only when there are errors: colouring the column
                    // because of its name makes the colour mean nothing on the
                    // row that actually has them.
                    color: app.errors > 0
                        ? severityTextColor(context, SeverityLevel.critical)
                        : null,
                  ),
                  _Pair(label: '', value: relativeTimeOf(l, app.lastSeen)),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _Pair extends StatelessWidget {
  const _Pair({required this.label, required this.value, this.color});

  final String label;
  final String value;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        if (label.isNotEmpty)
          Text(
            label,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        Text(
          value,
          style: theme.textTheme.titleSmall?.copyWith(
            fontWeight: FontWeight.w600,
            color: color,
          ),
        ),
      ],
    );
  }
}

class RumAppScreen extends StatefulWidget {
  const RumAppScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.app,
  });

  final SessionController session;
  final Sections sections;
  final String app;

  @override
  State<RumAppScreen> createState() => _RumAppScreenState();
}

class _RumAppScreenState extends State<RumAppScreen> {
  late final RumOverviewController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.rumOverview(widget.app);
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
      builder: (context, _) => DetailScreen<RumOverview>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: widget.app,
        builder: (context, o) => _body(context, l, o),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, RumOverview o) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final views = _c.views;

    return [
      const SizedBox(height: 8),
      Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Stat(label: l.rumViews, value: formatNumber(o.totals.views)),
          ),
          Expanded(
            child: Stat(
              label: l.rumSessions,
              value: formatNumber(o.totals.sessions.toDouble()),
            ),
          ),
          Expanded(
            child: Stat(
              label: l.rumErrors,
              value: formatNumber(o.totals.errors),
              color: o.totals.errors > 0
                  ? severityTextColor(context, SeverityLevel.critical)
                  : null,
            ),
          ),
          Expanded(
            child: Stat(
              label: l.rumAvgLoad,
              value: o.totals.avgMs == null
                  ? '-'
                  : '${o.totals.avgMs!.toStringAsFixed(0)} ms',
            ),
          ),
        ],
      ),

      if (views.length >= 2)
        DetailSection(
          title: l.rumViews,
          children: [
            SizedBox(
              height: 56,
              child: Sparkline(
                key: const Key('rum-spark'),
                values: views,
                color: colors.primary,
              ),
            ),
          ],
        )
      else
        DetailSection(
          title: l.rumViews,
          children: [
            Text(
              l.rumNoPoints,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ],
        ),

      DetailSection(
        title: l.rumVitals,
        children: [
          for (final v in o.vitals)
            _VitalRow(vital: v, key: Key('vital-${v.name.wire}')),
        ],
      ),
    ];
  }
}

/// One Core Web Vital: its p75, the verdict on it, and how much of the traffic
/// was rated good. The thresholds are published constants rather than settings
/// of this installation, so the rating comes from the server unchanged.
class _VitalRow extends StatelessWidget {
  const _VitalRow({super.key, required this.vital});

  final RumVital vital;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final (label, level) = switch (vital.rating) {
      'good' => (l.rumVitalGood, SeverityLevel.good),
      'needs_improvement' => (
        l.rumVitalNeedsImprovement,
        SeverityLevel.warning,
      ),
      'poor' => (l.rumVitalPoor, SeverityLevel.critical),
      // "" means the server had no measurements to rate, which is not the same
      // as a bad rating and must not look like one.
      _ => (l.rumVitalNoData, SeverityLevel.unknown),
    };

    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Row(
        children: [
          SizedBox(
            width: 48,
            child: Text(
              vital.name.wire.toUpperCase(),
              style: theme.textTheme.bodyMedium?.copyWith(
                fontWeight: FontWeight.w600,
              ),
            ),
          ),
          Expanded(
            child: Text(
              vital.p75 == null
                  ? '-'
                  // CLS is unitless; everything else is milliseconds, and a
                  // layout shift of 0.08 must not read as 0 ms.
                  : vital.unit.isEmpty
                  ? vital.p75!.toStringAsFixed(2)
                  : '${vital.p75!.toStringAsFixed(0)} ${vital.unit}',
              style: theme.textTheme.bodyMedium,
            ),
          ),
          // Only where there was something to rate: "0% good" next to "no
          // measurements" reads as a share that was measured and came out
          // zero, which is the opposite of what it means.
          if (vital.rating.isNotEmpty)
            Text(
              l.rumVitalShare((vital.good * 100).round()),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          const SizedBox(width: 10),
          Tag(label: label, level: level),
        ],
      ),
    );
  }
}
