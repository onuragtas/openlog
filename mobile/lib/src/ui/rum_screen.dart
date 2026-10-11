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
import 'rum_pages.dart';
import 'rum_releases.dart';
import 'rum_sessions.dart';
import 'rum_vitals.dart';
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

class _RumAppScreenState extends State<RumAppScreen>
    with SingleTickerProviderStateMixin {
  late final RumOverviewController _c;
  late final TabController _tabs = TabController(length: 5, vsync: this);

  @override
  void initState() {
    super.initState();
    _c = widget.sections.rumOverview(widget.app);
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void dispose() {
    _tabs.dispose();
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);

    // The web's five tabs, in its order. Each one is its own endpoint, so
    // each loads when it is first looked at rather than all at once.
    return Scaffold(
      appBar: AppBar(
        title: Text(widget.app, overflow: TextOverflow.ellipsis),
        bottom: TabBar(
          controller: _tabs,
          isScrollable: true,
          tabAlignment: TabAlignment.start,
          tabs: [
            Tab(key: const Key('rum-tab-overview'), text: l.rumTabOverview),
            Tab(key: const Key('rum-tab-pages'), text: l.rumTabPages),
            Tab(key: const Key('rum-tab-sessions'), text: l.rumTabSessions),
            Tab(key: const Key('rum-tab-releases'), text: l.rumTabReleases),
            Tab(key: const Key('rum-tab-funnel'), text: l.rumTabFunnel),
          ],
        ),
      ),
      body: TabBarView(
        controller: _tabs,
        children: [
          ListenableBuilder(
            listenable: _c,
            builder: (context, _) => DetailBody<RumOverview>(
              controller: _c,
              baseUrl: widget.session.baseUrl ?? '',
              builder: (context, o) => _body(context, l, o),
            ),
          ),
          RumPagesTab(
            session: widget.session,
            sections: widget.sections,
            app: widget.app,
          ),
          RumSessionsTab(
            session: widget.session,
            sections: widget.sections,
            app: widget.app,
          ),
          RumReleasesTab(
            session: widget.session,
            sections: widget.sections,
            app: widget.app,
          ),
          RumFunnelTab(
            session: widget.session,
            sections: widget.sections,
            app: widget.app,
          ),
        ],
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
            VitalTile(vital: v, key: Key('vital-${v.name.wire}')),
        ],
      ),
    ];
  }
}
