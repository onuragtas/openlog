// The routes of one browser application, and one route in full.
//
// The application's vitals answer "is this site fast"; the next question is
// always "which page, and which vital" — and that is a second query, because
// a route's p75 is not derivable from the application's. It is asked when a
// route is opened rather than for every row: the list runs to hundreds and
// five vitals each would be hundreds of requests on a phone network.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../rum.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'oql_view.dart' show formatNumber;
import 'rum_vitals.dart';
import 'sections_screen.dart';
import 'severity.dart';

/// `views`, `slowest` and `avg`, in the web's order.
const rumPageSorts = ['views', 'slowest', 'avg'];

class RumPagesTab extends StatefulWidget {
  const RumPagesTab({
    super.key,
    required this.session,
    required this.sections,
    required this.app,
  });

  final SessionController session;
  final Sections sections;
  final String app;

  @override
  State<RumPagesTab> createState() => _RumPagesTabState();
}

class _RumPagesTabState extends State<RumPagesTab> {
  late final RumPagesController _c = widget.sections.rumPages(widget.app);

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

  String _sortLabel(L l, String sort) => switch (sort) {
    'slowest' => l.rumPageSortSlowest,
    'avg' => l.rumPageSortAvg,
    _ => l.rumPageSortViews,
  };

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);

    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => ListScreen<RumPage>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        emptyTitle: l.rumPagesEmpty,
        search: Align(
          alignment: Alignment.centerLeft,
          child: DropdownButton<String>(
            key: const Key('rum-page-sort'),
            value: _c.sort,
            underline: const SizedBox.shrink(),
            items: [
              for (final s in rumPageSorts)
                DropdownMenuItem(value: s, child: Text(_sortLabel(l, s))),
            ],
            onChanged: (v) {
              if (v == null) return;
              _c.sort = v;
              _c.refresh();
            },
          ),
        ),
        itemBuilder: (context, i) => _PageCard(
          page: _c.items[i],
          onOpen: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => RumPageScreen(
                session: widget.session,
                sections: widget.sections,
                app: widget.app,
                page: _c.items[i],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

class _PageCard extends StatelessWidget {
  const _PageCard({required this.page, required this.onOpen});

  final RumPage page;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return SectionCard(
      cardKey: Key('rum-page-${page.route}'),
      title: page.route,
      onOpen: onOpen,
      stats: [
        (
          label: l.rumPageViews,
          value: formatNumber(page.views),
          emphasis: null,
        ),
        (label: 'p75', value: formatVital(page.p75Ms, 'ms'), emphasis: null),
        (label: 'LCP', value: formatVital(page.lcpP75, 'ms'), emphasis: null),
        (
          label: l.rumErrors,
          value: formatNumber(page.errors),
          // Red only where there are errors: colouring the column because of
          // its name makes the colour mean nothing on the row that has them.
          emphasis: page.errors > 0
              ? severityTextColor(context, SeverityLevel.critical)
              : null,
        ),
      ],
    );
  }
}

/// One route: the numbers the list already had, and the five vitals of that
/// route alone.
class RumPageScreen extends StatefulWidget {
  const RumPageScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.app,
    required this.page,
  });

  final SessionController session;
  final Sections sections;
  final String app;
  final RumPage page;

  @override
  State<RumPageScreen> createState() => _RumPageScreenState();
}

class _RumPageScreenState extends State<RumPageScreen> {
  late final RumRouteVitalsController _c = widget.sections.rumRouteVitals(
    widget.app,
    widget.page.route,
  );

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
    final p = widget.page;

    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => DetailScreen<RumVitalList>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: l.rumPageTitle,
        subtitle: p.route,
        builder: (context, v) => [
          DetailSection(
            title: l.rumPageLoad,
            children: [
              Wrap(
                spacing: 20,
                runSpacing: 10,
                children: [
                  Stat(label: l.rumPageViews, value: formatNumber(p.views)),
                  Stat(label: l.rumAvgLoad, value: formatVital(p.avgMs, 'ms')),
                  Stat(label: 'p50', value: formatVital(p.p50Ms, 'ms')),
                  Stat(label: 'p75', value: formatVital(p.p75Ms, 'ms')),
                  Stat(label: 'p95', value: formatVital(p.p95Ms, 'ms')),
                  // The worst single load of the range: a p95 of 1.2 s next
                  // to a max of 40 s is a different page from one whose max
                  // is 1.4 s, and only the second is fast for everybody.
                  Stat(label: l.rumPageMax, value: formatVital(p.maxMs, 'ms')),
                ],
              ),
              const SizedBox(height: 12),
              KeyValue(
                name: l.rumPageTtfb,
                value: formatVital(p.ttfbAvgMs, 'ms'),
              ),
              KeyValue(name: l.rumErrors, value: formatNumber(p.errors)),
            ],
          ),
          DetailSection(
            title: l.rumPageVitals,
            children: [
              for (final vital in v.vitals)
                VitalTile(
                  vital: vital,
                  key: Key('route-vital-${vital.name.wire}'),
                ),
            ],
          ),
        ],
      ),
    );
  }
}
