// Who uses the application and whether they come back (analytics.md §6).
//
// An application that sends no visitor id gets a sentence saying what to turn
// on rather than empty charts, because an empty retention table reads as
// "nobody came back". The web's wide tables are cards here, except retention,
// which is a grid by nature and scrolls sideways.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../analytics.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'failure_text.dart';
import 'list_paging.dart';
import 'sparkline.dart';
import 'theme.dart';

/// The web's first three chart hues, so a line is the same colour in both
/// consoles. The dark set takes the steps that hold their lightness against
/// this app's dark surface. Amber and green sit close for a red-blind reader,
/// so each line is named beside its colour in the legend.
const _seriesLight = [Color(0xFF2563EB), Color(0xFF16A34A), Color(0xFFD97706)];
const _seriesDark = [Color(0xFF3B82F6), Color(0xFF16A34A), Color(0xFFD97706)];

class RumAnalyticsTab extends StatefulWidget {
  const RumAnalyticsTab({
    super.key,
    required this.session,
    required this.sections,
    required this.app,
  });

  final SessionController session;
  final Sections sections;
  final String app;

  @override
  State<RumAnalyticsTab> createState() => _RumAnalyticsTabState();
}

class _RumAnalyticsTabState extends State<RumAnalyticsTab> {
  late final AnalyticsController _c = widget.sections.analytics(widget.app);

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final l = L.of(context);
    _c.setLabels(
      language: Localizations.localeOf(context).languageCode,
      direct: l.analyticsDirect,
      unknown: l.analyticsUnknown,
    );
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final baseUrl = widget.session.baseUrl ?? '';

    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => RefreshIndicator(
        onRefresh: _c.refresh,
        child: _c.activity.pending
            ? const Center(child: CircularProgressIndicator())
            : ListView(
                padding: const EdgeInsets.fromLTRB(12, 12, 12, 32),
                children: _body(context, l, baseUrl),
              ),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, String baseUrl) {
    final activity = _c.activity;
    if (activity.tooOld) {
      return [_Plain(l.rumServerTooOld, key: const Key('analytics-too-old'))];
    }
    final value = activity.value;
    if (value == null) {
      return [FailureBanner(failure: activity.failure, baseUrl: baseUrl)];
    }
    if (!value.collecting) return [const _NotCollecting()];

    final compare = _c.compare.value;
    return [
      FailureBanner(failure: activity.failure, baseUrl: baseUrl),
      _Picker<int>(
        pickerKey: const Key('analytics-period'),
        label: l.analyticsPeriodLabel,
        value: _c.days,
        options: analyticsPeriods,
        name: l.analyticsLastDays,
        onChanged: _c.setDays,
      ),
      if (compare != null)
        Padding(
          padding: const EdgeInsets.only(top: 4),
          child: _Note(
            l.analyticsComparedWith(compare.previous.from, compare.previous.to),
            key: const Key('analytics-compare-range'),
          ),
        ),
      if (compare?.previousPartial ?? false)
        Padding(
          padding: const EdgeInsets.only(top: 4),
          child: _Note(
            l.analyticsPreviousPartial,
            key: const Key('analytics-previous-partial'),
          ),
        ),
      if (_c.compare.tooOld)
        Padding(
          padding: const EdgeInsets.only(top: 4),
          child: _Note(
            l.rumServerTooOld,
            key: const Key('analytics-compare-too-old'),
          ),
        ),
      FailureBanner(failure: _c.compare.failure, baseUrl: baseUrl),
      const SizedBox(height: 10),
      _Totals(controller: _c, key: const Key('analytics-totals')),
      DetailSection(
        title: l.analyticsActivityChart,
        children: [_ActivityChart(days: value.days)],
      ),
      _RetentionSection(controller: _c, baseUrl: baseUrl),
      _BreakdownSection<AnalyticsEventList>(
        title: l.analyticsEvents,
        testId: 'analytics-events',
        nameLabel: l.analyticsEventName,
        columns: [l.analyticsCount, l.analyticsSessions, l.analyticsUsers],
        part: _c.events,
        list: _c.eventRows,
        baseUrl: baseUrl,
      ),
      _BreakdownSection<AnalyticsAcquisitionResponse>(
        title: l.analyticsAcquisition,
        testId: 'analytics-sources',
        nameLabel: _sourceName(l, _c.sourceBy),
        columns: [l.analyticsSessions, l.analyticsUsers],
        part: _c.acquisition,
        list: _c.sourceRows,
        baseUrl: baseUrl,
        picker: _Picker<String>(
          pickerKey: const Key('analytics-source-by'),
          label: l.analyticsBy,
          value: _c.sourceBy,
          options: analyticsSourceBys,
          name: (by) => _sourceName(l, by),
          onChanged: _c.setSourceBy,
        ),
      ),
      _BreakdownSection<AnalyticsAudienceResponse>(
        title: l.analyticsAudience,
        testId: 'analytics-audience',
        nameLabel: _audienceName(l, _c.audienceBy),
        columns: [l.analyticsSessions, l.analyticsUsers],
        part: _c.audience,
        list: _c.audienceRows,
        baseUrl: baseUrl,
        picker: _Picker<String>(
          pickerKey: const Key('analytics-audience-by'),
          label: l.analyticsBy,
          value: _c.audienceBy,
          options: analyticsAudienceBys,
          name: (by) => _audienceName(l, by),
          onChanged: _c.setAudienceBy,
        ),
        note: _c.noCountryRecorded ? l.analyticsNoCountry : null,
      ),
    ];
  }
}

String _sourceName(L l, String by) => switch (by) {
  'source' => l.analyticsSourceSource,
  'medium' => l.analyticsSourceMedium,
  'campaign' => l.analyticsSourceCampaign,
  _ => l.analyticsSourceReferrer,
};

String _audienceName(L l, String by) => switch (by) {
  'device' => l.analyticsAudienceDevice,
  'model' => l.analyticsAudienceModel,
  'os' => l.analyticsAudienceOs,
  'browser' => l.analyticsAudienceBrowser,
  'version' => l.analyticsAudienceVersion,
  _ => l.analyticsAudienceCountry,
};

/// A small sentence under a heading.
class _Note extends StatelessWidget {
  const _Note(this.text, {super.key});

  final String text;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Text(
      text,
      style: theme.textTheme.bodySmall?.copyWith(
        color: theme.colorScheme.onSurfaceVariant,
      ),
    );
  }
}

/// A sentence in place of content that is not there. Not a failure, so not in
/// a failure's colours.
class _Plain extends StatelessWidget {
  const _Plain(this.text, {super.key});

  final String text;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Container(
      padding: const EdgeInsets.all(18),
      margin: const EdgeInsets.symmetric(vertical: 4),
      decoration: BoxDecoration(
        border: Border.all(color: colorsOf(context).border),
        borderRadius: BorderRadius.circular(Radii.lg),
      ),
      child: Text(
        text,
        textAlign: TextAlign.center,
        style: theme.textTheme.bodyMedium?.copyWith(
          color: theme.colorScheme.onSurfaceVariant,
        ),
      ),
    );
  }
}

class _NotCollecting extends StatelessWidget {
  const _NotCollecting();

  @override
  Widget build(BuildContext context) => _Plain(
    L.of(context).analyticsNotCollecting,
    key: const Key('analytics-not-collecting'),
  );
}

class _Picker<T> extends StatelessWidget {
  const _Picker({
    super.key,
    required this.pickerKey,
    required this.label,
    required this.value,
    required this.options,
    required this.name,
    required this.onChanged,
  });

  final Key pickerKey;
  final String label;
  final T value;
  final List<T> options;
  final String Function(T) name;
  final void Function(T) onChanged;

  @override
  Widget build(BuildContext context) {
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        _Note(label),
        const SizedBox(width: 8),
        DropdownButton<T>(
          key: pickerKey,
          value: value,
          underline: const SizedBox.shrink(),
          items: [
            for (final o in options)
              DropdownMenuItem(value: o, child: Text(name(o))),
          ],
          onChanged: (v) {
            if (v != null) onChanged(v);
          },
        ),
      ],
    );
  }
}

/// How far a number moved. An arrow and a sign carry the direction, so it never
/// rests on colour alone.
class _Delta extends StatelessWidget {
  const _Delta({required this.change, required this.rate, this.format});

  final double? change;
  final double? rate;
  final String Function(double)? format;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final value = AnalyticsChange.of(
      change,
      rate,
      locale: Localizations.localeOf(context).languageCode,
      noChange: l.analyticsNoChange,
      fromNothing: l.analyticsFromNothing,
      format: format,
    );
    final (icon, color) = switch (value.direction) {
      ChangeDirection.up => (Icons.arrow_upward, colors.successText),
      ChangeDirection.down => (Icons.arrow_downward, colors.destructiveText),
      _ => (null, theme.colorScheme.onSurfaceVariant),
    };
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (icon != null) Icon(icon, size: 12, color: color),
        Flexible(
          child: Text(
            value.text,
            style: theme.textTheme.bodySmall?.copyWith(color: color),
          ),
        ),
      ],
    );
  }
}

class _Total extends StatelessWidget {
  const _Total({
    required this.label,
    required this.value,
    this.metric,
    this.format,
  });

  final String label;
  final String value;
  final AnalyticsMetric? metric;
  final String Function(double)? format;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final m = metric;
    return Card(
      margin: EdgeInsets.zero,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _Note(label),
            Text(value, style: theme.textTheme.titleLarge),
            if (m != null)
              _Delta(change: m.change, rate: m.changeRate, format: format),
          ],
        ),
      ),
    );
  }
}

/// The totals, two to a row.
///
/// Without a comparison, because it is on its way or this server has none, the
/// three active-user counts and the stickiness still come from the newest day.
/// The rest are a dash: the web can show a passing 0 where it knows its server
/// has them, but here a 0 could stay on screen as a false answer.
class _Totals extends StatelessWidget {
  const _Totals({super.key, required this.controller});

  final AnalyticsController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final language = Localizations.localeOf(context).languageCode;
    final m = controller.compare.value?.metrics;
    final days = controller.activity.value?.days ?? const <AnalyticsDay>[];
    final latest = days.isEmpty ? null : days.last;

    String n(AnalyticsMetric? metric, [double? fallback]) {
      final v = metric?.current ?? fallback;
      return v == null ? '–' : formatCount(v, language);
    }

    final stickiness = m?.stickiness.current ?? latest?.stickiness;
    final tiles = <Widget>[
      _Total(label: l.analyticsUsers, value: n(m?.users), metric: m?.users),
      _Total(
        label: l.analyticsNewUsers,
        value: n(m?.newUsers),
        metric: m?.newUsers,
      ),
      _Total(
        label: l.analyticsSessions,
        value: n(m?.sessions),
        metric: m?.sessions,
      ),
      _Total(
        label: l.analyticsPageViews,
        value: n(m?.pageViews),
        metric: m?.pageViews,
      ),
      _Total(label: l.analyticsEvents, value: n(m?.events), metric: m?.events),
      _Total(
        label: l.analyticsDau,
        value: n(m?.dau, latest?.dau ?? 0),
        metric: m?.dau,
      ),
      _Total(
        label: l.analyticsWau,
        value: n(m?.wau, latest?.wau ?? 0),
        metric: m?.wau,
      ),
      _Total(
        label: l.analyticsMau,
        value: n(m?.mau, latest?.mau ?? 0),
        metric: m?.mau,
      ),
      _Total(
        label: l.analyticsAvgDailyUsers,
        value: n(m?.avgDailyUsers),
        metric: m?.avgDailyUsers,
      ),
      _Total(
        label: l.analyticsStickiness,
        value: formatShare(stickiness ?? 0),
        metric: m?.stickiness,
        format: formatShare,
      ),
    ];

    return Column(
      children: [
        for (var i = 0; i < tiles.length; i += 2)
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: IntrinsicHeight(
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Expanded(child: tiles[i]),
                  const SizedBox(width: 8),
                  Expanded(child: tiles[i + 1]),
                ],
              ),
            ),
          ),
      ],
    );
  }
}

/// DAU, WAU and MAU on one scale from zero: drawn on three, a hundred daily
/// people would look the size of a thousand monthly ones.
class _ActivityChart extends StatelessWidget {
  const _ActivityChart({required this.days});

  final List<AnalyticsDay> days;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final language = Localizations.localeOf(context).languageCode;
    final palette = Theme.of(context).brightness == Brightness.dark
        ? _seriesDark
        : _seriesLight;
    final series = [
      (
        key: 'dau',
        label: l.analyticsDau,
        values: [for (final d in days) d.dau],
      ),
      (
        key: 'wau',
        label: l.analyticsWau,
        values: [for (final d in days) d.wau],
      ),
      (
        key: 'mau',
        label: l.analyticsMau,
        values: [for (final d in days) d.mau],
      ),
    ];
    var top = 0.0;
    for (final s in series) {
      for (final v in s.values) {
        if (v > top) top = v;
      }
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (days.length >= 2) ...[
          SizedBox(
            height: 120,
            child: Stack(
              children: [
                for (var i = 0; i < series.length; i++)
                  Sparkline(
                    key: Key('analytics-chart-${series[i].key}'),
                    values: series[i].values,
                    color: palette[i],
                    minimum: 0,
                    maximum: top,
                  ),
              ],
            ),
          ),
          const SizedBox(height: 4),
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [_Note(days.first.date), _Note(days.last.date)],
          ),
          const SizedBox(height: 6),
        ],
        Wrap(
          spacing: 14,
          runSpacing: 4,
          children: [
            for (var i = 0; i < series.length; i++)
              Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Container(
                    width: 10,
                    height: 10,
                    decoration: BoxDecoration(
                      color: palette[i],
                      shape: BoxShape.circle,
                    ),
                  ),
                  const SizedBox(width: 4),
                  _Note(
                    series[i].values.isEmpty
                        ? series[i].label
                        : '${series[i].label} '
                              '${formatCount(series[i].values.last, language)}',
                  ),
                ],
              ),
          ],
        ),
      ],
    );
  }
}

class _RetentionSection extends StatelessWidget {
  const _RetentionSection({required this.controller, required this.baseUrl});

  final AnalyticsController controller;
  final String baseUrl;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final part = controller.retention;
    final value = part.value;
    return DetailSection(
      title: l.analyticsRetention,
      children: [
        _Picker<String>(
          pickerKey: const Key('analytics-grain'),
          label: l.analyticsGrainLabel,
          value: controller.grain,
          options: analyticsGrains,
          name: (g) => switch (g) {
            'day' => l.analyticsGrainDay,
            'month' => l.analyticsGrainMonth,
            _ => l.analyticsGrainWeek,
          },
          onChanged: controller.setGrain,
        ),
        FailureBanner(failure: part.failure, baseUrl: baseUrl),
        if (part.tooOld)
          _Plain(
            l.rumServerTooOld,
            key: const Key('analytics-retention-too-old'),
          )
        else if (part.pending)
          const _Loading()
        else if (value != null && value.cohorts.isEmpty)
          const _NotCollecting()
        else if (value != null) ...[
          _RetentionGrid(
            cohorts: value.cohorts,
            grain: controller.grain,
            columns: controller.retentionColumns,
          ),
          if (value.samplingChanged)
            Padding(
              padding: const EdgeInsets.only(top: 6),
              child: _Note(l.analyticsSamplingChanged),
            ),
        ],
      ],
    );
  }
}

class _Loading extends StatelessWidget {
  const _Loading();

  @override
  Widget build(BuildContext context) => const Padding(
    padding: EdgeInsets.all(16),
    child: Center(child: CircularProgressIndicator()),
  );
}

/// The one table left on this screen: its point is reading down a column and
/// along a row at once, so it scrolls sideways rather than becoming cards.
class _RetentionGrid extends StatelessWidget {
  const _RetentionGrid({
    required this.cohorts,
    required this.grain,
    required this.columns,
  });

  final List<AnalyticsCohort> cohorts;
  final String grain;
  final int columns;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final language = Localizations.localeOf(context).languageCode;
    final small = theme.textTheme.bodySmall;
    final muted = small?.copyWith(color: theme.colorScheme.onSurfaceVariant);
    final primary = colorsOf(context).primary;

    String periodName(int n) => switch (grain) {
      'day' => l.analyticsPeriodDay(n),
      'month' => l.analyticsPeriodMonth(n),
      _ => l.analyticsPeriodWeek(n),
    };

    Widget cell(String text, {TextStyle? style, Alignment? align}) => Padding(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
      child: Align(
        alignment: align ?? Alignment.center,
        widthFactor: 1,
        child: Text(text, style: style ?? small),
      ),
    );

    return SingleChildScrollView(
      key: const Key('analytics-retention'),
      scrollDirection: Axis.horizontal,
      child: Table(
        defaultColumnWidth: const IntrinsicColumnWidth(),
        defaultVerticalAlignment: TableCellVerticalAlignment.middle,
        children: [
          TableRow(
            children: [
              cell(
                l.analyticsCohort,
                style: muted,
                align: Alignment.centerLeft,
              ),
              cell(l.analyticsSize, style: muted, align: Alignment.centerRight),
              for (var i = 0; i < columns; i++)
                cell(periodName(i), style: muted),
            ],
          ),
          for (final c in cohorts)
            TableRow(
              children: [
                cell(c.cohort, align: Alignment.centerLeft),
                cell(
                  formatCount(c.size, language),
                  align: Alignment.centerRight,
                ),
                for (var i = 0; i < columns; i++)
                  _retentionCell(
                    c.periods.where((p) => p.period == i).firstOrNull,
                    primary,
                    language,
                    cell,
                  ),
              ],
            ),
        ],
      ),
    );
  }

  /// One cohort's share in one period. The number is always written, so the
  /// tint only makes the table quicker to scan. A period nobody came back in
  /// is a real 0%, unlike one that has not happened yet, which cannot be told
  /// apart from it here: both stay blank instead of claiming a number.
  Widget _retentionCell(
    AnalyticsCohortPeriodsItem? p,
    Color primary,
    String language,
    Widget Function(String, {TextStyle? style, Alignment? align}) cell,
  ) {
    if (p == null) return const SizedBox.shrink();
    final tint = p.rate.clamp(0.0, 1.0) * 0.55;
    return Tooltip(
      message: formatCount(p.users, language),
      child: Container(
        margin: const EdgeInsets.all(1),
        decoration: BoxDecoration(
          color: primary.withValues(alpha: tint),
          borderRadius: BorderRadius.circular(4),
        ),
        child: cell(formatShare(p.rate)),
      ),
    );
  }
}

/// One named thing per row with its counts, its choice of dimension, and the
/// ways of not having them. Cards here where the web has a table's rows.
class _BreakdownSection<T> extends StatelessWidget {
  const _BreakdownSection({
    required this.title,
    required this.testId,
    required this.nameLabel,
    required this.columns,
    required this.part,
    required this.list,
    required this.baseUrl,
    this.picker,
    this.note,
  });

  final String title;
  final String testId;
  final String nameLabel;
  final List<String> columns;
  final AnalyticsPart<T> part;
  final BreakdownList list;
  final String baseUrl;
  final Widget? picker;
  final String? note;

  String _sortLabel(String sort) => sort == 'name'
      ? nameLabel
      : columns[int.parse(sort.substring('value'.length))];

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return DetailSection(
      title: title,
      children: [
        ?picker,
        FailureBanner(failure: part.failure, baseUrl: baseUrl),
        if (part.tooOld)
          _Plain(l.rumServerTooOld, key: Key('$testId-too-old'))
        else if (part.pending)
          const _Loading()
        else if (part.value != null)
          ListenableBuilder(
            listenable: list,
            builder: (context, _) {
              // Rows are handed over a moment after the answer: until then
              // there is nothing to claim about them.
              if (list.items.isEmpty) {
                if (!list.loaded || list.loadingFirst) {
                  return const SizedBox.shrink();
                }
                return _Plain(l.analyticsNoRows, key: Key('$testId-empty'));
              }
              return Column(
                key: Key(testId),
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  ListSortBar(
                    controller: list,
                    columns: [
                      for (var i = 0; i < columns.length; i++) 'value$i',
                      'name',
                    ],
                    label: _sortLabel,
                  ),
                  for (final (i, row) in list.items.indexed)
                    _BreakdownCard(
                      key: Key('$testId-row-$i'),
                      row: row,
                      columns: columns,
                    ),
                  LoadMoreTile(controller: list),
                ],
              );
            },
          ),
        if (note != null)
          Padding(padding: const EdgeInsets.only(top: 6), child: _Note(note!)),
      ],
    );
  }
}

class _BreakdownCard extends StatelessWidget {
  const _BreakdownCard({super.key, required this.row, required this.columns});

  final BreakdownRow row;
  final List<String> columns;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final language = Localizations.localeOf(context).languageCode;
    return Card(
      margin: const EdgeInsets.symmetric(vertical: 4),
      child: Container(
        // The full width, or each card is as wide as its own name and the
        // list reads as a staircase.
        width: double.infinity,
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(row.name, style: mono(theme.textTheme.bodySmall)),
            const SizedBox(height: 6),
            Wrap(
              spacing: 18,
              runSpacing: 6,
              children: [
                for (var i = 0; i < columns.length; i++)
                  Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      _Note(columns[i]),
                      Text(
                        formatCount(row.values[i], language),
                        style: theme.textTheme.titleSmall,
                      ),
                      if (row.changeOf(i) case final c?)
                        _Delta(change: c.change, rate: c.rate),
                    ],
                  ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}
