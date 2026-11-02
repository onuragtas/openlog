// One alert rule, read-only: what it watches, how each of its series is doing
// and what its evaluations found. Editing it stays on the web; turning it off
// is the switch on the list row.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../infra_detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';
import 'sparkline.dart';
import 'theme.dart';

String ruleTypeLabel(L l, AlertRuleType type) => switch (type) {
  AlertRuleType.metricThreshold => l.ruleTypeMetricThreshold,
  AlertRuleType.logMatch => l.ruleTypeLogMatch,
  AlertRuleType.noData => l.ruleTypeNoData,
  AlertRuleType.discovery => l.ruleTypeDiscovery,
  AlertRuleType.apm => l.ruleTypeApm,
  AlertRuleType.apmNoData => l.ruleTypeApmNoData,
  AlertRuleType.apmError => l.ruleTypeApmError,
  AlertRuleType.oql => l.ruleTypeOql,
  AlertRuleType.sloBurn => l.ruleTypeSloBurn,
  AlertRuleType.anomaly => l.ruleTypeAnomaly,
  AlertRuleType.unknown => type.wire,
};

class RuleScreen extends StatefulWidget {
  const RuleScreen({
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
  State<RuleScreen> createState() => _RuleScreenState();
}

class _RuleScreenState extends State<RuleScreen> {
  late final _c = AlertRuleController(widget.sections.client, widget.id);
  late final _history = RuleEvaluationsController(
    widget.sections.client,
    widget.id,
  );

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _c.refresh();
      _history.refresh();
    });
  }

  @override
  void dispose() {
    _c.dispose();
    _history.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: Listenable.merge([_c, _history]),
    builder: (context, _) => DetailScreen<AlertRuleDetail>(
      controller: _c,
      baseUrl: widget.session.baseUrl ?? '',
      title: _c.value?.name ?? widget.name,
      builder: (context, rule) => _body(context, rule),
    ),
  );

  List<Widget> _body(BuildContext context, AlertRuleDetail r) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final (stateLabel, level) = switch (r.status.state) {
      AlertRuleStatusState.firing => (l.ruleFiring, SeverityLevel.critical),
      AlertRuleStatusState.pending => (l.rulePending, SeverityLevel.warning),
      AlertRuleStatusState.error => (l.ruleError, SeverityLevel.critical),
      AlertRuleStatusState.ok => (l.ruleOk, SeverityLevel.good),
      AlertRuleStatusState.disabled => (l.ruleDisabled, SeverityLevel.unknown),
      _ => (l.ruleOk, SeverityLevel.unknown),
    };
    final evaluated = r.status.lastEvaluatedAt;
    final next = r.status.nextEvaluationAt;
    // The condition as the contract spells it: a dozen rule types each have
    // their own fields, and the point here is to read what a rule is watching
    // rather than to explain the form that made it.
    final condition = r.condition.toJson().entries.where(
      (e) => e.value != null && e.value is! List && e.value is! Map,
    );
    final channels = {
      for (final c in widget.sections.channels.items) c.id: c.name,
    };

    return [
      Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Tag(label: stateLabel, level: level),
          SeverityChip(severity: r.severity),
          Text(ruleTypeLabel(l, r.type), style: muted),
          Text(l.ruleEvery(r.intervalSeconds), style: muted),
        ],
      ),
      if (r.description.isNotEmpty)
        Padding(
          padding: const EdgeInsets.only(top: 8),
          child: Text(r.description, style: muted),
        ),
      if (r.status.lastError.isNotEmpty)
        Padding(
          padding: const EdgeInsets.only(top: 8),
          child: Text(
            r.status.lastError,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.error,
            ),
          ),
        ),
      const SizedBox(height: 10),
      KeyValue(
        name: l.ruleLastEvaluated,
        value: evaluated == null
            ? l.ruleNotEvaluated
            : relativeTimeOf(l, evaluated),
      ),
      if (next != null)
        KeyValue(name: l.ruleNextEvaluation, value: relativeTimeOf(l, next)),
      if (r.status.openIncidents > 0)
        Text(
          l.ruleOpenIncidents(r.status.openIncidents),
          style: theme.textTheme.bodySmall?.copyWith(
            color: severityTextColor(context, SeverityLevel.critical),
          ),
        ),
      DetailSection(
        title: l.ruleConditionTitle,
        children: [
          for (final e in condition) KeyValue(name: e.key, value: '${e.value}'),
          for (final g in r.condition.groupBy ?? const <String>[])
            KeyValue(name: 'group_by', value: g),
        ],
      ),
      DetailSection(
        title: l.ruleSeriesTitle,
        children: [
          if (r.series.isEmpty) Text(l.ruleSeriesEmpty, style: muted),
          for (final s in r.series) _SeriesRow(series: s),
        ],
      ),
      _historySection(context),
      if (r.channelIds.isNotEmpty)
        DetailSection(
          title: l.ruleChannels,
          children: [
            Text(
              [for (final id in r.channelIds) channels[id] ?? id].join(', '),
              style: theme.textTheme.bodySmall,
            ),
          ],
        ),
      if (r.labels.isNotEmpty)
        DetailSection(
          title: l.ruleLabels,
          children: [
            for (final e in r.labels.entries)
              KeyValue(name: e.key, value: e.value),
          ],
        ),
      if (r.runbookUrl.isNotEmpty)
        DetailSection(
          title: l.ruleRunbook,
          children: [
            SelectableText(r.runbookUrl, style: theme.textTheme.bodySmall),
          ],
        ),
      Padding(
        padding: const EdgeInsets.only(top: 20),
        child: Text(l.ruleEditOnWeb, style: muted),
      ),
    ];
  }

  Widget _historySection(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final h = _history.value;
    final totals = h == null ? null : evaluationTotals(h);
    final first = h != null && h.series.isNotEmpty ? h.series.first : null;
    final values = first == null ? const <double>[] : evaluationValues(first);

    return DetailSection(
      key: const Key('rule-history'),
      title: l.ruleHistoryTitle,
      children: [
        Wrap(
          spacing: 6,
          children: [
            for (final hours in evaluationRanges)
              ChoiceChip(
                key: Key('rule-history-$hours'),
                label: Text(l.ruleHistoryHours(hours)),
                selected: _history.hours == hours,
                onSelected: (_) => _history.setHours(hours),
              ),
          ],
        ),
        const SizedBox(height: 8),
        if (_history.notOffered)
          const NotOfferedNote(padding: EdgeInsets.zero)
        else if (_history.failure != null)
          FailureBanner(
            failure: _history.failure,
            baseUrl: widget.session.baseUrl ?? '',
          )
        else if (_history.loadingFirst)
          const LinearProgressIndicator()
        else if (totals == null || totals.evaluations == 0)
          Text(l.ruleHistoryEmpty, style: muted)
        else ...[
          Text(
            l.ruleHistorySummary(
              totals.evaluations,
              totals.errors,
              totals.lastDurationMs ?? 0,
            ),
            style: muted,
          ),
          if (values.length >= 2) ...[
            const SizedBox(height: 8),
            SizedBox(
              height: 56,
              child: Sparkline(
                key: const Key('rule-history-line'),
                values: values,
                color: colorsOf(context).primary,
              ),
            ),
            Text(
              labelsText(first!.labels).isEmpty
                  ? first.seriesKey
                  : labelsText(first.labels),
              style: muted,
            ),
          ],
          if (h!.truncated || h.series.length > 1)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text(
                l.ruleHistoryTruncated,
                style: theme.textTheme.bodySmall,
              ),
            ),
        ],
      ],
    );
  }
}

class _SeriesRow extends StatelessWidget {
  const _SeriesRow({required this.series});

  final AlertSeriesState series;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final (label, level) = switch (series.state) {
      AlertSeriesStateName.firing => (l.ruleFiring, SeverityLevel.critical),
      AlertSeriesStateName.pending => (l.rulePending, SeverityLevel.warning),
      _ => (l.ruleOk, SeverityLevel.good),
    };
    final name = labelsText(series.labels);

    return Padding(
      key: Key('rule-series-${series.seriesKey}'),
      padding: const EdgeInsets.only(bottom: 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Tag(label: label, level: level),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              name.isEmpty ? series.seriesKey : name,
              style: theme.textTheme.bodySmall,
            ),
          ),
          // Null is "nothing to compare yet", and zero is a reading.
          Text(
            series.value == null ? '–' : '${series.value}',
            style: theme.textTheme.bodySmall,
          ),
        ],
      ),
    );
  }
}
