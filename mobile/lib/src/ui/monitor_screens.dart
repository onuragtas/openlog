// What an SLO row, a job monitor row and an advisory row open. Read-only:
// writing these stays on the web, and each screen says so.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../infra_detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'host_screen.dart';
import 'infra_sort.dart';
import 'list_paging.dart';
import 'list_scaffold.dart';
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';

/// The web's traffic light for a budget.
(String, SeverityLevel) budgetLabel(L l, BudgetLevel level) => switch (level) {
  BudgetLevel.healthy => (l.sloStatusHealthy, SeverityLevel.good),
  BudgetLevel.warning => (l.sloStatusWarning, SeverityLevel.warning),
  BudgetLevel.exhausted => (l.sloStatusExhausted, SeverityLevel.critical),
  BudgetLevel.none => (l.sloStatusNone, SeverityLevel.unknown),
};

(String, SeverityLevel) jobStateLabel(L l, JobState state) => switch (state) {
  JobState.ok => (l.jobStateOk, SeverityLevel.good),
  JobState.failing => (l.jobStateFailing, SeverityLevel.critical),
  JobState.late => (l.jobStateLate, SeverityLevel.warning),
  JobState.running => (l.jobStateRunning, SeverityLevel.unknown),
  JobState.paused => (l.jobStatePaused, SeverityLevel.unknown),
  JobState.unknown => (l.jobStateUnknown, SeverityLevel.unknown),
};

(String, SeverityLevel) jobRunLabel(L l, JobRunStatus status) =>
    switch (status) {
      JobRunStatus.success => (l.jobRunSuccess, SeverityLevel.good),
      JobRunStatus.failure => (l.jobRunFailure, SeverityLevel.critical),
      JobRunStatus.missed => (l.jobRunMissed, SeverityLevel.critical),
      JobRunStatus.overrun => (l.jobRunOverrun, SeverityLevel.warning),
      JobRunStatus.running => (l.jobRunRunning, SeverityLevel.unknown),
      // A status this build has never heard of reads as what the server calls
      // it, on the web as here; the wire value is all there is of it.
      _ => (status.wire, SeverityLevel.unknown),
    };

(String, SeverityLevel) vulnSeverityLabel(L l, VulnSeverity? s) => switch (s) {
  VulnSeverity.critical => (l.vulnSeverityCritical, SeverityLevel.critical),
  VulnSeverity.high => (l.vulnSeverityHigh, SeverityLevel.critical),
  VulnSeverity.medium => (l.vulnSeverityMedium, SeverityLevel.warning),
  VulnSeverity.low => (l.vulnSeverityLow, SeverityLevel.unknown),
  _ => (l.vulnSeverityNone, SeverityLevel.unknown),
};

/// A detail screen's stats: two columns on a phone, however many the web fits.
class _Tiles extends StatelessWidget {
  const _Tiles(this.tiles);

  final List<(String, String)> tiles;

  @override
  Widget build(BuildContext context) => LayoutBuilder(
    builder: (context, box) => Wrap(
      runSpacing: 12,
      children: [
        for (final (label, value) in tiles)
          SizedBox(
            width: box.maxWidth / 2,
            child: Stat(label: label, value: value),
          ),
      ],
    ),
  );
}

class _EditOnWeb extends StatelessWidget {
  const _EditOnWeb();

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(top: 20),
      child: Text(
        L.of(context).detailEditOnWeb,
        style: theme.textTheme.bodySmall?.copyWith(
          color: theme.colorScheme.onSurfaceVariant,
        ),
      ),
    );
  }
}

/// A controller made when a screen opens and disposed with it, the same way
/// every detail screen here does it.
class _DetailHost<C extends ChangeNotifier> extends StatefulWidget {
  const _DetailHost({
    required this.create,
    required this.load,
    required this.builder,
  });

  final C Function() create;
  final Future<void> Function(C) load;
  final Widget Function(BuildContext, C) builder;

  @override
  State<_DetailHost<C>> createState() => _DetailHostState<C>();
}

class _DetailHostState<C extends ChangeNotifier> extends State<_DetailHost<C>> {
  late final C _c = widget.create();

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => widget.load(_c));
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: _c,
    builder: (context, _) => widget.builder(context, _c),
  );
}

class SloScreen extends StatelessWidget {
  const SloScreen({
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
  Widget build(BuildContext context) => _DetailHost<SloDetailController>(
    create: () => SloDetailController(sections.client, id),
    load: (c) => c.refresh(),
    builder: (context, c) => DetailScreen<SloResults>(
      controller: c,
      baseUrl: session.baseUrl ?? '',
      title: c.value?.slo.name ?? name,
      builder: (context, r) => _body(context, r),
    ),
  );

  List<Widget> _body(BuildContext context, SloResults r) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final budget = r.status.budget;
    final (label, level) = budgetLabel(l, budgetLevel(budget));
    final slo = r.slo;
    final burndown = burndownValues(r.series);

    return [
      Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Tag(label: label, level: level),
          Tag(label: l.sloWindowDays(slo.windowDays)),
          Text(
            sloTarget(
              serviceName: slo.serviceName,
              serviceNamespace: slo.serviceNamespace,
              environment: slo.environment,
            ),
            style: theme.textTheme.bodySmall,
          ),
          Text(
            [
              slo.sliType == SloSliType.latency
                  ? l.sloSliLatency
                  : l.sloSliAvailability,
              if (slo.sliType == SloSliType.latency &&
                  slo.latencyThresholdMs != null)
                '${slo.latencyThresholdMs!.toStringAsFixed(0)} ms',
            ].join(' · '),
            style: muted,
          ),
        ],
      ),
      if (slo.description.isNotEmpty)
        Padding(
          padding: const EdgeInsets.only(top: 8),
          child: Text(slo.description, style: muted),
        ),
      const SizedBox(height: 14),
      _Tiles([
        ('SLI', formatRatio(budget.sli, digits: 3)),
        (l.sloObjective, formatObjective(slo.objective)),
        (l.sloRemaining, formatRatio(budget.remainingRatio)),
        (l.sloBurnRate, formatBurnRate(budget.burnRate)),
        (l.sloRequests, formatRequests(budget.requests)),
        (l.sloBad, formatRequests(budget.bad)),
      ]),
      DetailSection(
        title: l.sloBudgetTitle,
        children: [
          if (burndown.isEmpty)
            Text(l.sloSeriesEmpty, style: muted)
          else ...[
            SizedBox(
              height: 80,
              child: Sparkline(
                key: const Key('slo-burndown'),
                values: burndown,
                color: colorsOf(context).primary,
                minimum: -0.25,
                maximum: 1,
              ),
            ),
            const SizedBox(height: 4),
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              children: [
                Text(l.sloBudgetFull, style: muted),
                Text(l.sloBudgetEmpty, style: muted),
              ],
            ),
          ],
        ],
      ),
      DetailSection(
        title: l.sloBurnTitle,
        children: [
          Text(l.sloBurnHint, style: muted),
          const SizedBox(height: 8),
          for (final b in r.burn) _BurnRow(window: b),
        ],
      ),
      const _EditOnWeb(),
    ];
  }
}

class _BurnRow extends StatelessWidget {
  const _BurnRow({required this.window});

  final SloBurnWindowResult window;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final b = window;
    // The two defaults have translated names; a rule may name its own.
    final name = switch (b.name) {
      'fast' => l.sloBurnFast,
      'slow' => l.sloBurnSlow,
      _ => b.name,
    };
    final (label, level) = b.breaching
        ? (l.sloBurnBreaching, SeverityLevel.critical)
        : b.rate == null
        ? (l.sloStatusNone, SeverityLevel.unknown)
        : (l.sloBurnOk, SeverityLevel.good);

    return Padding(
      key: Key('burn-${b.name}'),
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(child: Text(name, style: theme.textTheme.bodyMedium)),
              Tag(label: label, level: level),
            ],
          ),
          const SizedBox(height: 2),
          Text(
            '${l.sloBurnThreshold} ${formatBurnRate(b.factor)} · '
            '${l.sloBurnLong} ${formatBurnRate(b.long.burnRate)} · '
            '${l.sloBurnShort} ${formatBurnRate(b.short.burnRate)}',
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ],
      ),
    );
  }
}

class JobScreen extends StatelessWidget {
  const JobScreen({
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
  Widget build(BuildContext context) => _DetailHost<JobDetailController>(
    create: () => JobDetailController(sections.client, id),
    load: (c) => c.refresh(),
    builder: (context, c) => DetailScreen<JobRunPage>(
      controller: c,
      baseUrl: session.baseUrl ?? '',
      title: c.value?.monitor.name ?? name,
      builder: (context, r) => _body(context, r),
    ),
  );

  List<Widget> _body(BuildContext context, JobRunPage r) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final m = r.monitor;
    final (label, level) = jobStateLabel(l, jobState(m));
    final schedule = m.kind == 'interval'
        ? l.jobEvery(formatSeconds(m.intervalSeconds))
        : m.timeZone.isNotEmpty
        ? '${m.cron} · ${m.timeZone}'
        : m.cron;

    return [
      Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Tag(label: label, level: level),
          Text(schedule, style: muted),
        ],
      ),
      if (m.description.isNotEmpty)
        Padding(
          padding: const EdgeInsets.only(top: 8),
          child: Text(m.description, style: muted),
        ),
      const SizedBox(height: 14),
      _Tiles([
        (l.jobRuns, '${r.summary.runs}'),
        (l.jobFailures, '${r.summary.failures}'),
        (l.jobMissed, '${r.summary.missed}'),
        (l.jobAvg, formatJobDuration(r.summary.avgMs)),
        (l.jobMax, formatJobDuration(r.summary.maxMs)),
        (
          l.jobNextRun,
          m.enabled ? relativeTimeOf(l, m.state.expectedAt) : l.jobStatePaused,
        ),
      ]),
      DetailSection(
        title: l.jobPingUrl,
        children: [SelectableText(m.pingUrl, style: theme.textTheme.bodySmall)],
      ),
      if (m.state.lastMessage.isNotEmpty)
        DetailSection(
          title: l.jobLastOutput,
          children: [
            SelectableText(
              m.state.lastMessage,
              style: theme.textTheme.bodySmall?.copyWith(
                fontFamily: 'monospace',
              ),
            ),
          ],
        ),
      DetailSection(
        title: l.jobRunsTitle,
        children: [
          if (r.runs.isEmpty) Text(l.jobRunsEmpty, style: muted),
          for (final (i, run) in r.runs.indexed)
            _RunRow(run: run, key: Key('job-run-$i')),
        ],
      ),
      const _EditOnWeb(),
    ];
  }
}

class _RunRow extends StatelessWidget {
  const _RunRow({super.key, required this.run});

  final JobRun run;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final (label, level) = jobRunLabel(l, run.status);

    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Tag(label: label, level: level),
              const SizedBox(width: 8),
              Expanded(
                child: Text(relativeTimeOf(l, run.timestamp), style: muted),
              ),
            ],
          ),
          const SizedBox(height: 2),
          Text(
            '${l.jobDuration} ${formatJobDuration(run.durationMs)} · '
            '${l.jobLate} ${formatLate(run.lateSeconds)} · '
            // Zero is the ordinary exit, and the web prints it as nothing.
            '${l.jobExit} ${run.exitCode == 0 ? '–' : run.exitCode}',
            style: muted,
          ),
          if (run.message.isNotEmpty)
            Text(
              run.message,
              maxLines: 3,
              overflow: TextOverflow.ellipsis,
              style: theme.textTheme.bodySmall?.copyWith(
                fontFamily: 'monospace',
              ),
            ),
        ],
      ),
    );
  }
}

class VulnerabilityScreen extends StatelessWidget {
  const VulnerabilityScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.id,
  });

  final SessionController session;
  final Sections sections;
  final String id;

  @override
  Widget build(BuildContext context) => _DetailHost<VulnerabilityController>(
    create: () => VulnerabilityController(sections.client, id),
    load: (c) => c.refresh(),
    builder: (context, c) {
      final l = L.of(context);
      return Scaffold(
        appBar: AppBar(title: Text(id, overflow: TextOverflow.ellipsis)),
        body: ListScreen<VulnFinding>(
          controller: c,
          baseUrl: session.baseUrl ?? '',
          emptyTitle: c.notOffered ? l.serverNotOffered : l.vulnNoFindings,
          search: Align(
            alignment: Alignment.centerLeft,
            child: ListSortBar(
              controller: c,
              columns: advisoryFindingSorts,
              label: (s) => infraSortLabel(l, s),
            ),
          ),
          header: c.notOffered ? null : _advisoryHeader(context, c),
          footer: LoadMoreTile(controller: c),
          itemBuilder: (context, i) => FindingCard(
            finding: c.items[i],
            showHost: true,
            onOpen: () => Navigator.of(context).push(
              MaterialPageRoute<void>(
                builder: (_) => HostScreen(
                  session: session,
                  sections: sections,
                  hostId: c.items[i].hostId,
                  hostName: c.items[i].hostName.isEmpty
                      ? c.items[i].hostId
                      : c.items[i].hostName,
                ),
              ),
            ),
          ),
        ),
      );
    },
  );

  Widget _advisoryHeader(BuildContext context, VulnerabilityController c) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final severity = c.severity;
    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (severity != null)
            Wrap(
              spacing: 8,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                () {
                  final (label, level) = vulnSeverityLabel(l, severity);
                  return Tag(label: label, level: level);
                }(),
                Text('CVSS ${formatScore(c.score)}', style: muted),
                if (c.cve.isNotEmpty && c.cve != id) Text(c.cve, style: muted),
              ],
            ),
          if (c.summary.isNotEmpty)
            Padding(
              padding: const EdgeInsets.only(top: 6),
              child: Text(c.summary, style: muted),
            ),
          // The case a person cannot act on by upgrading is worth saying out
          // loud rather than leaving it to a row with no arrow in it.
          if (c.withoutFix > 0)
            Padding(
              padding: const EdgeInsets.only(top: 6),
              child: Text(
                l.vulnNoFix(c.withoutFix),
                key: const Key('vuln-no-fix'),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: severityTextColor(context, SeverityLevel.warning),
                ),
              ),
            ),
        ],
      ),
    );
  }
}

/// One package on one host that an advisory is about.
class FindingCard extends StatelessWidget {
  const FindingCard({
    super.key,
    required this.finding,
    required this.showHost,
    required this.onOpen,
  });

  final VulnFinding finding;

  /// The host on an advisory's screen, the advisory on a host's.
  final bool showHost;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final f = finding;
    final (label, level) = vulnSeverityLabel(l, f.severity);

    return Card(
      key: Key('finding-${f.hostId}-${f.vulnId}-${f.package}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: InkWell(
        onTap: onOpen,
        borderRadius: BorderRadius.circular(Radii.lg),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                showHost
                    ? (f.hostName.isEmpty ? f.hostId : f.hostName)
                    : advisoryLabel(f.vulnId, f.cve),
                style: theme.textTheme.titleSmall,
              ),
              if (!showHost && f.summary.isNotEmpty)
                Padding(
                  padding: const EdgeInsets.only(top: 3),
                  child: Text(
                    f.summary,
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                    style: muted,
                  ),
                ),
              const SizedBox(height: 6),
              Wrap(
                spacing: 8,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  if (!showHost) ...[
                    Tag(label: label, level: level),
                    Text('CVSS ${formatScore(f.score)}', style: muted),
                  ],
                  Text(f.package, style: theme.textTheme.bodySmall),
                  Text(upgradeText(f), style: muted),
                  if (showHost && f.ecosystem.isNotEmpty)
                    Text(f.ecosystem, style: muted),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}
