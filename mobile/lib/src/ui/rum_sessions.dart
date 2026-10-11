// The visits of one browser application, and what one visit did.
//
// A session is a visit, not a person: the id is random, per tab and expires
// with the tab (rum.md §1.1), so nothing on these screens identifies anybody
// unless the application itself called identify().
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../rum.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'oql_view.dart' show formatNumber;
import 'rum_vitals.dart' show formatVital;
import 'sections_screen.dart';
import 'severity.dart';
import 'trace_screen.dart';

class RumSessionsTab extends StatefulWidget {
  const RumSessionsTab({
    super.key,
    required this.session,
    required this.sections,
    required this.app,
  });

  final SessionController session;
  final Sections sections;
  final String app;

  @override
  State<RumSessionsTab> createState() => _RumSessionsTabState();
}

class _RumSessionsTabState extends State<RumSessionsTab> {
  late final RumSessionsController _c = widget.sections.rumSessions(widget.app);

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
      builder: (context, _) => ListScreen<RumSession>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        emptyTitle: l.rumSessionsEmpty,
        // Said on the screen: the list looks like a list of people and is
        // not one -- the same person in two tabs is two rows here.
        search: Align(
          alignment: Alignment.centerLeft,
          child: Text(
            l.rumSessionsNote,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: Theme.of(context).colorScheme.onSurfaceVariant,
            ),
          ),
        ),
        itemBuilder: (context, i) => _SessionCard(
          visit: _c.items[i],
          onOpen: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => RumSessionScreen(
                session: widget.session,
                sections: widget.sections,
                sessionId: _c.items[i].sessionId,
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// What a browser and a device are, in one line: "Chrome 131 · desktop".
String browserLabel(RumSession s) => [
  if (s.browserName.isNotEmpty)
    [s.browserName, s.browserVersion].where((p) => p.isNotEmpty).join(' '),
  if (s.deviceType.isNotEmpty) s.deviceType,
  if (s.osName.isNotEmpty) s.osName,
].join(' · ');

class _SessionCard extends StatelessWidget {
  const _SessionCard({required this.visit, required this.onOpen});

  final RumSession visit;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return SectionCard(
      cardKey: Key('rum-session-${visit.sessionId}'),
      // The first twelve characters: the whole id is 32 random ones and the
      // screen is 400 points wide. It is on the detail in full.
      title: '${visit.sessionId.substring(0, 12)}…',
      subtitle: browserLabel(visit),
      trailing: relativeTimeOf(l, visit.startedAt),
      onOpen: onOpen,
      stats: [
        (
          label: l.rumSessionDuration,
          value: formatVital(visit.durationMs, 'ms'),
          emphasis: null,
        ),
        (
          label: l.rumSessionPageViews,
          value: formatNumber(visit.pageViews),
          emphasis: null,
        ),
        (
          label: l.rumErrors,
          value: formatNumber(visit.errors),
          emphasis: visit.errors > 0
              ? severityTextColor(context, SeverityLevel.critical)
              : null,
        ),
        (
          label: l.rumSessionEntry,
          value: visit.entryRoute.isEmpty ? '-' : visit.entryRoute,
          emphasis: null,
        ),
      ],
    );
  }
}

/// One visit: its summary and the events it produced.
class RumSessionScreen extends StatefulWidget {
  const RumSessionScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.sessionId,
  });

  final SessionController session;
  final Sections sections;
  final String sessionId;

  @override
  State<RumSessionScreen> createState() => _RumSessionScreenState();
}

class _RumSessionScreenState extends State<RumSessionScreen> {
  late final RumSessionController _c = widget.sections.rumSession(
    widget.sessionId,
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

    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => DetailScreen<RumSessionDetail>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: l.rumSessionTitle,
        subtitle: widget.sessionId,
        builder: (context, d) => _body(context, l, d),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, RumSessionDetail d) {
    final s = d.session;
    final theme = Theme.of(context);
    return [
      const SizedBox(height: 8),
      Wrap(
        spacing: 20,
        runSpacing: 10,
        children: [
          Stat(
            label: l.rumSessionDuration,
            value: formatVital(s.durationMs, 'ms'),
          ),
          Stat(label: l.rumSessionPageViews, value: formatNumber(s.pageViews)),
          Stat(
            label: l.rumErrors,
            value: formatNumber(s.errors),
            color: s.errors > 0
                ? severityTextColor(context, SeverityLevel.critical)
                : null,
          ),
        ],
      ),
      DetailSection(
        title: l.rumSessionTitle,
        children: [
          KeyValue(
            name: l.rumSessionStarted,
            value: relativeTimeOf(l, s.startedAt),
          ),
          KeyValue(name: l.rumSessionDevice, value: browserLabel(s)),
          KeyValue(
            name: l.rumSessionEntry,
            value: s.entryRoute.isEmpty ? '-' : s.entryRoute,
          ),
          KeyValue(
            name: l.rumSessionExit,
            value: s.exitRoute.isEmpty ? '-' : s.exitRoute,
          ),
          // Only when the application declared one: an empty line here would
          // suggest the visit was anonymous by choice rather than by default.
          if (s.userId.isNotEmpty)
            KeyValue(name: l.rumSessionUser, value: s.userId),
          if (s.country.isNotEmpty)
            KeyValue(name: l.rumSessionCountry, value: s.country),
          if (s.traceId.isNotEmpty)
            Align(
              alignment: Alignment.centerLeft,
              child: OutlinedButton.icon(
                key: const Key('rum-session-trace'),
                onPressed: () => Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => TraceScreen(
                      session: widget.session,
                      sections: widget.sections,
                      traceId: s.traceId,
                    ),
                  ),
                ),
                icon: const Icon(Icons.account_tree_outlined, size: 18),
                label: Text(l.rumSessionOpenTrace),
              ),
            ),
        ],
      ),
      DetailSection(
        title: l.rumSessionTimeline,
        children: [
          if (d.events.isEmpty)
            // The timeline comes from the stored spans, so it goes with the
            // trace retention while the summary stays. Saying which is which
            // keeps "nothing happened" apart from "it is no longer kept".
            Text(
              l.rumSessionTimelineEmpty,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            )
          else
            for (final e in d.events)
              _EventRow(
                event: e,
                key: Key(
                  'rum-event-${e.timestamp.toIso8601String()}-${e.name}',
                ),
                onOpenTrace: e.traceId.isEmpty
                    ? null
                    : () => Navigator.of(context).push(
                        MaterialPageRoute<void>(
                          builder: (_) => TraceScreen(
                            session: widget.session,
                            sections: widget.sections,
                            traceId: e.traceId,
                            spanId: e.spanId,
                          ),
                        ),
                      ),
              ),
        ],
      ),
    ];
  }
}

class _EventRow extends StatelessWidget {
  const _EventRow({super.key, required this.event, this.onOpenTrace});

  final RumEvent event;
  final VoidCallback? onOpenTrace;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final (label, level) = switch (event.event) {
      RumEventEvent.pageView => (l.rumEventPageView, SeverityLevel.info),
      RumEventEvent.vital => (l.rumEventVital, SeverityLevel.good),
      RumEventEvent.error => (l.rumEventError, SeverityLevel.critical),
      RumEventEvent.resource => (l.rumEventResource, SeverityLevel.unknown),
      _ => (event.event.wire, SeverityLevel.unknown),
    };

    return InkWell(
      onTap: onOpenTrace,
      child: Padding(
        padding: const EdgeInsets.only(bottom: 10),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Tag(label: label, level: level),
            const SizedBox(width: 8),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    event.name.isEmpty ? event.route : event.name,
                    style: theme.textTheme.bodyMedium,
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                  ),
                  Text(
                    [
                      relativeTimeOf(l, event.timestamp),
                      if (event.durationMs > 0)
                        formatVital(event.durationMs, 'ms'),
                      if (event.statusCode > 0) '${event.statusCode}',
                    ].join(' · '),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ],
              ),
            ),
            if (onOpenTrace != null)
              Icon(
                Icons.chevron_right,
                size: 18,
                color: theme.colorScheme.onSurfaceVariant,
              ),
          ],
        ),
      ),
    );
  }
}
