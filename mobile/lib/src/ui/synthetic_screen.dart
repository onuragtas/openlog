// One synthetic check: the same four questions the web's detail answers
// (web/src/components/synthetics/SyntheticDetail.tsx) -- how often it passed,
// how slow it was, what each location is doing right now, and what the runs
// that failed said -- plus the shortcut that turns the check into an alert.
//
// Read-only on purpose. Editing a check means headers, assertions and expected
// status codes, which is a form for a desk; a phone is where a red check is
// looked at, and where an alert for it gets set up so it does not have to be.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import '../synthetics.dart';
import '../templates.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart' show relativeTimeOf;
import 'severity.dart';
import 'sparkline.dart';
import 'templates_screen.dart';
import 'theme.dart';

/// The target as the list shows it: a method and a URL, or a host:port.
String syntheticTarget(SyntheticCheck c) {
  if (c.type == 'http') return '${c.method} ${c.url}';
  if (c.type == 'dns') return '${c.dnsRecordType} ${c.target}';
  return c.target;
}

/// A location's current state, which is the last run's outcome.
(String, SeverityLevel) locationLook(L l, SyntheticLocationStatus s) =>
    switch (s.lastSuccess) {
      null => (l.syntheticStateUnknown, SeverityLevel.unknown),
      true => (l.syntheticStateUp, SeverityLevel.good),
      false => (l.syntheticStateDown, SeverityLevel.critical),
    };

class SyntheticScreen extends StatefulWidget {
  const SyntheticScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.id,
    required this.name,
  });

  final SessionController session;
  final Sections sections;
  final String id;
  final String name;

  @override
  State<SyntheticScreen> createState() => _SyntheticScreenState();
}

class _SyntheticScreenState extends State<SyntheticScreen> {
  late final SyntheticDetailController _controller;

  @override
  void initState() {
    super.initState();
    _controller = widget.sections.syntheticDetail(widget.id);
    WidgetsBinding.instance.addPostFrameCallback((_) => _controller.refresh());
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return ListenableBuilder(
      listenable: _controller,
      builder: (context, _) => Scaffold(
        appBar: AppBar(
          title: Text(widget.name, overflow: TextOverflow.ellipsis),
          actions: [
            // The check is the one thing the templates need, and this is the
            // screen that knows which check it is.
            IconButton(
              key: const Key('synthetic-create-alert'),
              icon: const Icon(Icons.add_alert_outlined),
              tooltip: l.syntheticCreateAlert,
              onPressed: () => Navigator.of(context).push(
                MaterialPageRoute<void>(
                  builder: (_) => SyntheticAlertScreen(
                    session: widget.session,
                    sections: widget.sections,
                    checkId: widget.id,
                    checkName: widget.name,
                  ),
                ),
              ),
            ),
          ],
        ),
        body: DetailBody<SyntheticResults>(
          controller: _controller,
          baseUrl: widget.session.baseUrl ?? '',
          builder: (context, v) => _body(context, l, v),
        ),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, SyntheticResults v) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final check = v.check;
    final s = v.summary;
    // p95 per bucket, which is the line the web draws; a bucket without a run
    // has no latency and is left out rather than drawn as a zero.
    final latency = [
      for (final p in s.points)
        if (p.p95Ms != null) p.p95Ms!,
    ];

    return [
      DetailSection(
        title: l.syntheticSummaryTitle,
        children: [
          Wrap(
            spacing: 8,
            runSpacing: 6,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              if (!check.enabled) Tag(label: l.stateDisabled),
              Text(syntheticTarget(check), style: mono(muted)),
            ],
          ),
          const SizedBox(height: 8),
          if (latency.length < 2)
            Text(l.syntheticSeriesEmpty, style: muted)
          else
            SizedBox(
              height: 56,
              child: Sparkline(
                key: const Key('synthetic-latency'),
                values: latency,
                color: colors.primary,
              ),
            ),
          const SizedBox(height: 8),
          KeyValue(name: l.syntheticUptime, value: _pct(s.uptime)),
          KeyValue(name: l.syntheticRuns, value: '${s.runs}'),
          KeyValue(name: l.syntheticFailureCount, value: '${s.failures}'),
          KeyValue(name: l.syntheticAvg, value: _ms(s.avgMs)),
          KeyValue(name: l.syntheticP95, value: _ms(s.p95Ms)),
          KeyValue(name: l.syntheticP99, value: _ms(s.p99Ms)),
          KeyValue(
            name: l.syntheticInterval,
            value: _secs(check.intervalSeconds),
          ),
          KeyValue(
            name: l.syntheticTimeout,
            value: _ms(check.timeoutMs.toDouble()),
          ),
        ],
      ),
      DetailSection(
        title: l.syntheticLocationsTitle,
        children: [
          for (final st in check.status)
            _Location(
              status: st,
              key: Key('synthetic-location-${st.location}'),
            ),
        ],
      ),
      DetailSection(
        title: l.syntheticFailuresTitle,
        children: [
          if (v.failures.isEmpty)
            Text(l.syntheticFailuresEmpty, style: muted)
          else
            for (final f in v.failures)
              _Failure(
                failure: f,
                key: Key('synthetic-failure-${f.timestamp}'),
              ),
        ],
      ),
    ];
  }
}

String _pct(double? v) => v == null ? '—' : '${(v * 100).toStringAsFixed(1)}%';

String _ms(double? v) => v == null ? '—' : '${v.toStringAsFixed(0)} ms';

/// Seconds as they were asked for: a 5 s interval is "5 s", not "0.1 min".
String _secs(int v) => v % 60 == 0 && v >= 60 ? '${v ~/ 60} min' : '$v s';

class _Location extends StatelessWidget {
  const _Location({super.key, required this.status});

  final SyntheticLocationStatus status;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final (label, level) = locationLook(l, status);
    final last = status.lastRunAt;

    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(status.location, style: theme.textTheme.bodyMedium),
              ),
              Tag(label: label, level: level),
            ],
          ),
          const SizedBox(height: 4),
          Text(
            '${l.syntheticLastRun}: ${last == null ? l.syntheticNever : relativeTimeOf(l, last)}'
            ' · ${l.syntheticNextRun}: ${relativeTimeOf(l, status.nextRunAt)}',
            style: muted,
          ),
          // The reason the last run gave, which is what a red location is
          // opened for.
          if (status.lastError.isNotEmpty) Text(status.lastError, style: muted),
        ],
      ),
    );
  }
}

class _Failure extends StatelessWidget {
  const _Failure({super.key, required this.failure});

  final SyntheticFailure failure;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final f = failure;
    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            f.error.isEmpty ? f.errorKind : f.error,
            style: theme.textTheme.bodyMedium,
          ),
          Text(
            [
              relativeTimeOf(l, f.timestamp),
              f.location,
              if (f.statusCode != 0) 'HTTP ${f.statusCode}',
              _ms(f.durationMs),
            ].join(' · '),
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ],
      ),
    );
  }
}

/// "Alarm kur": the synthetics templates with this check already filled in.
///
/// A catalog of its own rather than the alerts tab's, so taking this shortcut
/// does not move the filter the person left on that tab.
class SyntheticAlertScreen extends StatefulWidget {
  const SyntheticAlertScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.checkId,
    required this.checkName,
  });

  final SessionController session;
  final Sections sections;
  final String checkId;
  final String checkName;

  @override
  State<SyntheticAlertScreen> createState() => _SyntheticAlertScreenState();
}

class _SyntheticAlertScreenState extends State<SyntheticAlertScreen> {
  late final TemplatesController _templates;

  @override
  void initState() {
    super.initState();
    _templates = widget.sections.templatesFor('synthetics');
  }

  @override
  void dispose() {
    _templates.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return Scaffold(
      appBar: AppBar(
        title: Text(l.syntheticCreateAlert, overflow: TextOverflow.ellipsis),
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(22),
          child: Padding(
            padding: const EdgeInsets.only(left: 16, bottom: 8),
            child: Align(
              alignment: Alignment.centerLeft,
              child: Text(
                widget.checkName,
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
          ),
        ),
      ),
      body: TemplatesBody(
        session: widget.session,
        sections: widget.sections,
        active: true,
        controller: _templates,
        target: {'check_id': widget.checkId, 'check_name': widget.checkName},
      ),
    );
  }
}
