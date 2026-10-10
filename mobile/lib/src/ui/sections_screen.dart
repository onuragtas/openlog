// The eight list sections, and the one card shape they share.
//
// Together in one file because they are one screen with eight sets of fields;
// splitting them would copy the scaffolding eight times and let the eight
// drift apart, which is how two of them would end up with different ideas of
// what "not reporting" looks like.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'list_scaffold.dart';
import 'severity.dart';
import 'theme.dart';

/// A section: a search box over a list of cards.
class SectionBody<T> extends StatefulWidget {
  const SectionBody({
    super.key,
    required this.session,
    required this.controller,
    required this.searchKey,
    required this.emptyTitle,
    required this.card,
    required this.active,
    this.onSearch,
    this.header,
    this.searchable = true,
    this.searchHint,
  });

  final SessionController session;
  final SectionController<T> controller;
  final String searchKey;
  final String Function(L) emptyTitle;
  final Widget Function(BuildContext, T) card;

  /// Anything this section needs above its rows and below its search box --
  /// the traces list puts its newest/slowest switch here. Optional, because
  /// most sections are a search and a list and nothing else.
  final Widget? header;

  /// Whether this section has a search box. The RUM list does not: its
  /// endpoint takes no query, and a box that does nothing is worse than none.
  final bool searchable;

  /// What the search box is actually searching, when "Search" is too vague.
  /// Inventory searches a key, not the row, and the web says so.
  final String Function(L)? searchHint;

  /// Whether this section is the one on screen. An IndexedStack builds every
  /// child, so without this the app would fire one request per section the
  /// moment somebody signs in -- thirteen at once, twelve of them for screens
  /// nobody has opened.
  final bool active;

  /// Told after the search box changed what the list is about, for the
  /// things beside the list that have to agree with it -- the volume chart
  /// above the traces, for one.
  final VoidCallback? onSearch;

  @override
  State<SectionBody<T>> createState() => _SectionBodyState<T>();
}

class _SectionBodyState<T> extends State<SectionBody<T>> {
  final _search = TextEditingController();

  @override
  void initState() {
    super.initState();
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(SectionBody<T> old) {
    super.didUpdateWidget(old);
    _loadIfVisible();
  }

  /// Loads once, the first time this section is looked at.
  void _loadIfVisible() {
    final c = widget.controller;
    if (!widget.active || c.loaded || c.loadingFirst) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      c.refresh();
      widget.onSearch?.call();
    });
  }

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListScreen<T>(
        controller: c,
        baseUrl: widget.session.baseUrl ?? '',
        search: widget.searchable
            ? SearchField(
                fieldKey: Key(widget.searchKey),
                controller: _search,
                hint: widget.searchHint?.call(l) ?? l.sectionSearch,
                onSubmitted: (value) {
                  c.query = value;
                  c.refresh();
                  widget.onSearch?.call();
                },
              )
            : null,
        emptyTitle: widget.emptyTitle(l),
        header: widget.header,
        itemBuilder: (context, i) => widget.card(context, c.items[i]),
      ),
    );
  }
}

/// A card: a title, a subtitle, optional tags and a row of small numbers.
class SectionCard extends StatelessWidget {
  const SectionCard({
    super.key,
    required this.cardKey,
    required this.title,
    this.subtitle,
    this.trailing,
    this.tags = const [],
    this.stats = const [],
    this.onOpen,
  });

  final Key cardKey;
  final String title;
  final String? subtitle;
  final String? trailing;
  final List<Widget> tags;
  final List<({String label, String value, Color? emphasis})> stats;

  /// What the card opens, when there is something behind it. Sections whose
  /// rows have no detail yet pass nothing and stay untappable, rather than
  /// offering a tap that does nothing.
  final VoidCallback? onOpen;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final c = colorsOf(context);

    final body = Padding(
      padding: const EdgeInsets.all(14),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(child: Text(title, style: text.titleMedium)),
              if (trailing != null)
                Text(
                  trailing!,
                  style: text.bodySmall?.copyWith(color: c.mutedForeground),
                ),
            ],
          ),
          if (subtitle != null && subtitle!.isNotEmpty) ...[
            const SizedBox(height: 3),
            Text(
              subtitle!,
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              style: text.bodySmall?.copyWith(color: c.mutedForeground),
            ),
          ],
          if (tags.isNotEmpty) ...[
            const SizedBox(height: 8),
            Wrap(spacing: 6, runSpacing: 4, children: tags),
          ],
          if (stats.isNotEmpty) ...[
            const SizedBox(height: 10),
            Wrap(
              spacing: 18,
              runSpacing: 6,
              children: [
                for (final s in stats)
                  Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Text(
                        s.label,
                        style: text.bodySmall?.copyWith(
                          color: c.mutedForeground,
                        ),
                      ),
                      Text(
                        s.value,
                        style: text.titleSmall?.copyWith(
                          fontWeight: FontWeight.w600,
                          color: s.emphasis,
                        ),
                      ),
                    ],
                  ),
              ],
            ),
          ],
        ],
      ),
    );

    return Card(
      key: cardKey,
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: onOpen == null
          ? body
          : InkWell(
              onTap: onOpen,
              borderRadius: BorderRadius.circular(Radii.lg),
              child: body,
            ),
    );
  }
}

String _pct(double? v) => v == null ? '—' : '${(v * 100).toStringAsFixed(0)}%';
String _ms(double? v) => v == null ? '—' : '${v.toStringAsFixed(0)} ms';

/// A usage number is only worth colouring when it is high: colouring every row
/// makes the colour mean nothing on the row that matters.
Color? _hot(BuildContext context, double? ratio) =>
    ratio != null && ratio >= 0.9 ? colorsOf(context).destructiveText : null;

Widget hostCard(BuildContext context, Host h, {VoidCallback? onOpen}) {
  final l = L.of(context);
  final u = h.usage;
  return SectionCard(
    cardKey: Key('host-${h.hostId}'),
    onOpen: onOpen,
    title: h.hostName.isEmpty ? h.hostId : h.hostName,
    subtitle: h.osDescription,
    trailing: relativeTimeOf(l, h.lastSeen),
    stats: [
      (label: l.statCpu, value: _pct(u?.cpu), emphasis: _hot(context, u?.cpu)),
      (
        label: l.statMemory,
        value: _pct(u?.memory),
        emphasis: _hot(context, u?.memory),
      ),
      (
        label: l.statDisk,
        value: _pct(u?.disk),
        emphasis: _hot(context, u?.disk),
      ),
    ],
  );
}

Widget containerCard(
  BuildContext context,
  ApiContainer x, {
  VoidCallback? onOpen,
}) {
  final l = L.of(context);
  final memory =
      x.memoryLimit != null && x.memoryLimit! > 0 && x.memoryUsage != null
      ? x.memoryUsage! / x.memoryLimit!
      : null;
  return SectionCard(
    cardKey: Key('container-${x.containerId}'),
    onOpen: onOpen,
    title: x.name.isEmpty ? x.containerId : x.name,
    subtitle: x.imageName,
    trailing: x.hostName,
    tags: [
      if (!x.reporting)
        Tag(label: l.stateNotReporting, level: SeverityLevel.warning),
      if (x.composeService.isNotEmpty) Tag(label: x.composeService),
      if (x.k8sNamespaceName.isNotEmpty) Tag(label: x.k8sNamespaceName),
    ],
    stats: [
      (
        label: l.statCpu,
        value: _pct(x.cpuUtilization),
        emphasis: _hot(context, x.cpuUtilization),
      ),
      (
        label: l.statMemory,
        value: _pct(memory),
        emphasis: _hot(context, memory),
      ),
      if (x.restartCount > 0)
        (
          label: l.statRestarts,
          value: '${x.restartCount}',
          emphasis: colorsOf(context).warningText,
        ),
    ],
  );
}

Widget podCard(BuildContext context, KubernetesPod p, {VoidCallback? onOpen}) {
  final l = L.of(context);
  // Running-and-ready is the only state that is not worth a colour.
  final healthy = p.ready && p.phase.toLowerCase() == 'running';
  return SectionCard(
    cardKey: Key('pod-${p.podUid}'),
    onOpen: onOpen,
    title: p.podName,
    subtitle: '${p.namespace} · ${p.workloadKind} ${p.workloadName}'.trim(),
    trailing: p.nodeName,
    tags: [
      Tag(
        label: p.phase,
        level: healthy ? SeverityLevel.info : SeverityLevel.warning,
      ),
      if (p.ready) Tag(label: l.stateReady),
      if (p.reason.isNotEmpty)
        Tag(label: p.reason, level: SeverityLevel.critical),
    ],
  );
}

Widget sloCard(BuildContext context, SloListItem s) {
  final l = L.of(context);
  final budget = s.status?.budget;
  final remaining = budget?.remainingRatio;
  return SectionCard(
    cardKey: Key('slo-${s.id}'),
    title: s.name,
    subtitle: s.serviceName,
    tags: [
      if (budget != null)
        Tag(
          label: budget.met ? l.sloMet : l.sloBreached,
          level: budget.met ? SeverityLevel.info : SeverityLevel.critical,
        ),
    ],
    stats: [
      (
        label: l.statObjective,
        value: '${s.objective.toStringAsFixed(2)}%',
        emphasis: null,
      ),
      (
        label: l.statBudget,
        value: _pct(remaining),
        // Under a tenth left is the number the on-call person is looking for.
        emphasis: remaining != null && remaining < 0.1
            ? colorsOf(context).destructiveText
            : null,
      ),
    ],
  );
}

Widget syntheticCard(BuildContext context, SyntheticCheckListItem s) {
  final l = L.of(context);
  final summary = s.summary;
  final failing = (summary?.failures ?? 0) > 0;
  return SectionCard(
    cardKey: Key('synthetic-${s.id}'),
    title: s.name,
    subtitle: s.url,
    trailing: s.type,
    tags: [
      if (!s.enabled) Tag(label: l.stateDisabled),
      if (failing) Tag(label: l.statFailures, level: SeverityLevel.critical),
    ],
    stats: [
      if (summary != null) ...[
        (label: l.statUptime, value: _pct(summary.uptime), emphasis: null),
        (
          label: l.statFailures,
          value: '${summary.failures}',
          emphasis: failing ? colorsOf(context).destructiveText : null,
        ),
      ],
    ],
  );
}

Widget jobCard(BuildContext context, JobMonitor j) {
  final l = L.of(context);
  final summary = j.summary;
  // Late counts as bad even before a run is missed: that is the whole
  // point of monitoring a job that should already have reported.
  final bad =
      j.state.late || (summary?.failures ?? 0) + (summary?.missed ?? 0) > 0;
  return SectionCard(
    cardKey: Key('job-${j.id}'),
    title: j.name,
    subtitle: j.cron.isNotEmpty ? j.cron : '${j.intervalSeconds}s',
    trailing: summary?.lastAt == null
        ? null
        : relativeTimeOf(l, summary!.lastAt!),
    tags: [
      if (!j.enabled) Tag(label: l.stateDisabled),
      Tag(
        label: j.state.status,
        level: bad ? SeverityLevel.critical : SeverityLevel.info,
      ),
    ],
    stats: [
      if (summary != null) ...[
        (label: l.statRuns, value: '${summary.runs}', emphasis: null),
        (
          label: l.statFailures,
          value: '${summary.failures + summary.missed}',
          emphasis: bad ? colorsOf(context).destructiveText : null,
        ),
      ],
    ],
  );
}

Widget vulnCard(BuildContext context, VulnGroup v) {
  final l = L.of(context);
  final level = switch (v.severity) {
    VulnSeverity.critical || VulnSeverity.high => SeverityLevel.critical,
    VulnSeverity.medium => SeverityLevel.warning,
    _ => SeverityLevel.info,
  };
  return SectionCard(
    cardKey: Key('vuln-${v.vulnId}'),
    title: v.cve.isEmpty ? v.vulnId : v.cve,
    subtitle: v.summary,
    tags: [
      Tag(label: v.severity.wire, level: level),
      for (final p in v.packages.take(2)) Tag(label: p),
    ],
    stats: [
      (label: l.statScore, value: v.score.toStringAsFixed(1), emphasis: null),
      (label: l.statHosts, value: '${v.hosts}', emphasis: null),
    ],
  );
}

Widget dbCard(BuildContext context, DbInstance d) {
  final l = L.of(context);
  return SectionCard(
    cardKey: Key('db-${d.instance}-${d.hostId}'),
    title: d.instance.isEmpty ? d.dbSystem : d.instance,
    subtitle: '${d.dbSystem} · ${d.serverAddress}:${d.serverPort}',
    trailing: d.hostName,
    stats: [
      (label: l.statCalls, value: '${d.calls}', emphasis: null),
      (label: l.svcP95, value: _ms(d.avgMs), emphasis: null),
    ],
  );
}
