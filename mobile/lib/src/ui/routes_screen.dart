// Where a page goes, and why it went there.
//
// Routes are tried in order and the first match wins, so the order *is* the
// answer. The list shows them in that order and nothing else sorts or filters
// it -- a search box that hid a route would hide the reason a page went
// somewhere. Dragging to reorder is the one thing a phone does better than
// the web here.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'failure_text.dart';
import 'severity.dart';

class RoutesBody extends StatefulWidget {
  const RoutesBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  State<RoutesBody> createState() => _RoutesBodyState();
}

class _RoutesBodyState extends State<RoutesBody> {
  @override
  void initState() {
    super.initState();
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(RoutesBody old) {
    super.didUpdateWidget(old);
    _loadIfVisible();
  }

  void _loadIfVisible() {
    final c = widget.sections.routes;
    if (!widget.active || c.loaded || c.loadingFirst) return;
    WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.sections.routes;
    final theme = Theme.of(context);

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.loadingFirst) {
          return const Center(child: CircularProgressIndicator());
        }
        return RefreshIndicator(
          onRefresh: c.refresh,
          child: Column(
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      c.items.isEmpty ? l.routesEmpty : l.routesOrder,
                      key: const Key('routes-order-note'),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                    FailureBanner(
                      failure: c.failure,
                      baseUrl: widget.session.baseUrl ?? '',
                    ),
                  ],
                ),
              ),
              Expanded(
                child: ReorderableListView.builder(
                  padding: const EdgeInsets.fromLTRB(12, 4, 12, 24),
                  itemCount: c.items.length,
                  onReorder: c.move,
                  itemBuilder: (context, i) {
                    final rule = c.items[i];
                    return _RouteCard(
                      key: ValueKey(rule.id),
                      rule: rule,
                      index: i,
                      busy: c.busy == rule.id,
                      onToggle: () =>
                          c.setEnabled(rule, enabled: !rule.enabled),
                    );
                  },
                ),
              ),
            ],
          ),
        );
      },
    );
  }
}

class _RouteCard extends StatelessWidget {
  const _RouteCard({
    super.key,
    required this.rule,
    required this.index,
    required this.busy,
    required this.onToggle,
  });

  final AlertRoutingRule rule;
  final int index;
  final bool busy;
  final VoidCallback onToggle;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Card(
      margin: const EdgeInsets.symmetric(vertical: 5),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 10, 4, 10),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // The number is the point of the screen: it is the order the
            // server tries them in.
            Padding(
              padding: const EdgeInsets.only(top: 2, right: 10),
              child: Text(
                '${index + 1}',
                style: theme.textTheme.titleMedium?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Flexible(
                        child: Text(
                          rule.name,
                          maxLines: 2,
                          overflow: TextOverflow.ellipsis,
                          style: theme.textTheme.titleSmall,
                        ),
                      ),
                      if (rule.isDefault) ...[
                        const SizedBox(width: 8),
                        Tag(label: l.routesDefault, level: SeverityLevel.info),
                      ],
                      if (!rule.enabled) ...[
                        const SizedBox(width: 8),
                        Tag(label: l.routesOff, level: SeverityLevel.unknown),
                      ],
                    ],
                  ),
                  const SizedBox(height: 4),
                  Text(describeMatch(l, rule.match), style: muted),
                  Text(l.routesChannels(rule.channelIds.length), style: muted),
                ],
              ),
            ),
            busy
                ? const Padding(
                    padding: EdgeInsets.all(12),
                    child: SizedBox(
                      width: 16,
                      height: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    ),
                  )
                : Switch(
                    key: Key('route-switch-${rule.id}'),
                    value: rule.enabled,
                    onChanged: (_) => onToggle(),
                  ),
            ReorderableDragStartListener(
              index: index,
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: 4),
                child: Icon(
                  Icons.drag_handle,
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// What a route matches, in one line.
///
/// The parts are AND-ed and an empty part matches everything, which is why a
/// match with nothing in it reads as "matches everything" rather than as an
/// empty line: a route that catches every incident is the one worth seeing.
String describeMatch(L l, AlertRouteMatch m) {
  final parts = <String>[
    if (m.severities != null && m.severities!.isNotEmpty)
      l.routesSeverities([for (final s in m.severities!) s.wire].join(', ')),
    if (m.services != null && m.services!.isNotEmpty)
      l.routesServices(m.services!.join(', ')),
    if (m.ruleTypes != null && m.ruleTypes!.isNotEmpty)
      l.routesTypes([for (final t in m.ruleTypes!) t.wire].join(', ')),
    if (m.labels != null && m.labels!.isNotEmpty)
      l.routesLabels(m.labels!.length),
    if (m.timeWindow != null)
      l.routesWindow(
        m.timeWindow!.startTime,
        m.timeWindow!.endTime,
        m.timeWindow!.timezone,
      ),
  ];
  return parts.isEmpty ? l.routesMatchAll : parts.join(' · ');
}
