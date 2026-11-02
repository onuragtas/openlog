// The whole mute: the web's form, on one scrolling screen.
//
// Editing a mute is what someone on call does, so this is here and not on the
// web alone. The schedule's next occurrences are asked as it changes, half a
// second after the last change as on the web, because a recurrence rule is the
// kind of thing that is right only once somebody has seen where it lands.
import 'dart:async';

import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../infra_detail.dart';
import '../mute_schedule.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'failure_text.dart';

String weekDayShort(L l, String d) => switch (d) {
  'mon' => l.muteDayMon,
  'tue' => l.muteDayTue,
  'wed' => l.muteDayWed,
  'thu' => l.muteDayThu,
  'fri' => l.muteDayFri,
  'sat' => l.muteDaySat,
  'sun' => l.muteDaySun,
  _ => d,
};

String muteOrdinal(L l, int o) => switch (o) {
  1 => l.muteOrdinalFirst,
  2 => l.muteOrdinalSecond,
  3 => l.muteOrdinalThird,
  4 => l.muteOrdinalFourth,
  _ => l.muteOrdinalLast,
};

String muteOp(L l, String op) => switch (op) {
  'eq' => l.muteOpEq,
  'neq' => l.muteOpNeq,
  'contains' => l.muteOpContains,
  _ => op,
};

/// A stored schedule in one line: its days or its monthly rule, and its hours.
String muteScheduleSummary(L l, AlertMuteSchedule s) {
  final rrule = s.rrule ?? '';
  String when;
  if (rrule.startsWith('FREQ=MONTHLY')) {
    final m = parseMonthlyRRule(rrule);
    when = m == null
        ? rrule
        : m.mode == 'monthday'
        ? l.muteSummaryMonthday(m.monthDays)
        : m.day == anyWeekday
        ? l.muteSummaryNthWeekday(muteOrdinal(l, m.ordinal))
        : l.muteSummaryNthDay(
            muteOrdinal(l, m.ordinal),
            weekDayShort(l, m.day),
          );
  } else {
    when = [for (final d in s.days) weekDayShort(l, d.wire)].join(', ');
  }
  return '$when ${s.startTime}–${s.endTime} (${s.timezone})';
}

class MuteFormScreen extends StatefulWidget {
  const MuteFormScreen({
    super.key,
    required this.session,
    required this.sections,
    this.mute,
  });

  final SessionController session;
  final Sections sections;

  /// The mute being edited, or null for a new one.
  final AlertMute? mute;

  @override
  State<MuteFormScreen> createState() => _MuteFormScreenState();
}

class _MuteFormScreenState extends State<MuteFormScreen> {
  late final MuteDraft _d = MuteDraft.from(widget.mute);
  late final _name = TextEditingController(text: _d.name);
  late final _comment = TextEditingController(text: _d.comment);
  late final _timezone = TextEditingController(text: _d.timezone);
  late final _monthDays = TextEditingController(text: _d.monthly.monthDays);
  late final _preview = MutePreviewController(widget.sections.client);
  Timer? _debounce;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final s = widget.sections;
      if (!s.rules.loaded && !s.rules.loadingFirst) s.rules.refresh();
      if (!s.calendars.loaded && !s.calendars.loadingFirst) {
        s.calendars.refresh();
      }
      _schedulePreview();
    });
  }

  @override
  void dispose() {
    _debounce?.cancel();
    _name.dispose();
    _comment.dispose();
    _timezone.dispose();
    _monthDays.dispose();
    _preview.dispose();
    super.dispose();
  }

  void _changed(VoidCallback change) {
    setState(change);
    _schedulePreview();
  }

  void _schedulePreview() {
    _debounce?.cancel();
    if (!_d.previewable) {
      _preview.clear();
      return;
    }
    _debounce = Timer(
      const Duration(milliseconds: 500),
      () => _preview.run(_d.schedule),
    );
  }

  Future<void> _pickMoment(DateTime current, ValueChanged<DateTime> set) async {
    final date = await showDatePicker(
      context: context,
      initialDate: current,
      firstDate: DateTime(2000),
      lastDate: DateTime(2100),
    );
    if (date == null || !mounted) return;
    final time = await showTimePicker(
      context: context,
      initialTime: TimeOfDay.fromDateTime(current),
    );
    if (time == null || !mounted) return;
    setState(
      () => set(
        DateTime(date.year, date.month, date.day, time.hour, time.minute),
      ),
    );
  }

  Future<void> _pickTime(String current, ValueChanged<String> set) async {
    final parts = current.split(':');
    final time = await showTimePicker(
      context: context,
      initialTime: TimeOfDay(
        hour: int.tryParse(parts.first) ?? 0,
        minute: parts.length > 1 ? int.tryParse(parts[1]) ?? 0 : 0,
      ),
    );
    if (time == null || !mounted) return;
    _changed(
      () => set(
        '${time.hour.toString().padLeft(2, '0')}:'
        '${time.minute.toString().padLeft(2, '0')}',
      ),
    );
  }

  Future<void> _addException() async {
    final now = DateTime.now();
    final date = await showDatePicker(
      context: context,
      initialDate: now,
      firstDate: DateTime(2000),
      lastDate: DateTime(2100),
    );
    if (date == null || !mounted) return;
    _changed(
      () => _d.exdates = ({..._d.exdates, dateOnly(date)}.toList()..sort()),
    );
  }

  Future<void> _save() async {
    _d
      ..name = _name.text
      ..comment = _comment.text
      ..timezone = _timezone.text;
    final saved = await widget.sections.mutes.save(
      widget.mute?.id,
      _d.toInput(),
    );
    if (saved && mounted) Navigator.of(context).pop();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.sections.mutes;
    final theme = Theme.of(context);

    return Scaffold(
      appBar: AppBar(
        title: Text(widget.mute == null ? l.mutesNew : l.muteEditTitle),
      ),
      body: ListenableBuilder(
        listenable: Listenable.merge([
          c,
          _name,
          _preview,
          widget.sections.rules,
          widget.sections.calendars,
        ]),
        builder: (context, _) => ListView(
          padding: const EdgeInsets.fromLTRB(16, 12, 16, 32),
          children: [
            TextField(
              key: const Key('mute-form-name'),
              controller: _name,
              decoration: InputDecoration(
                labelText: l.muteName,
                border: const OutlineInputBorder(),
                isDense: true,
              ),
            ),
            const SizedBox(height: 10),
            TextField(
              key: const Key('mute-form-comment'),
              controller: _comment,
              decoration: InputDecoration(
                labelText: l.muteComment,
                border: const OutlineInputBorder(),
                isDense: true,
              ),
            ),
            SwitchListTile(
              key: const Key('mute-form-recurring'),
              contentPadding: EdgeInsets.zero,
              title: Text(l.muteRecurringToggle),
              value: _d.recurring,
              onChanged: (v) => _changed(() => _d.recurring = v),
            ),
            if (_d.recurring) ..._schedule(context) else ..._window(context),
            ..._scope(context),
            const SizedBox(height: 16),
            if (c.saveNotOffered)
              const NotOfferedNote(padding: EdgeInsets.only(bottom: 10)),
            FailureBanner(
              failure: c.saveFailure,
              baseUrl: widget.session.baseUrl ?? '',
            ),
            const SizedBox(height: 10),
            Align(
              alignment: Alignment.centerRight,
              child: FilledButton(
                key: const Key('mute-form-save'),
                onPressed:
                    c.saving || _name.text.trim().isEmpty || _d.monthDaysInvalid
                    ? null
                    : _save,
                child: c.saving
                    ? const SizedBox(
                        width: 16,
                        height: 16,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    : Text(widget.mute == null ? l.muteCreate : l.muteSave),
              ),
            ),
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text(
                widget.mute == null
                    ? ''
                    : l.muteCreatedBy(widget.mute!.createdByEmail),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  /// A one-off window: when it starts and when it ends, on this phone's clock.
  List<Widget> _window(BuildContext context) {
    final l = L.of(context);
    String at(DateTime d) =>
        '${dateOnly(d)} ${d.hour.toString().padLeft(2, '0')}:'
        '${d.minute.toString().padLeft(2, '0')}';
    return [
      ListTile(
        key: const Key('mute-form-starts'),
        contentPadding: EdgeInsets.zero,
        title: Text(l.muteStarts),
        subtitle: Text(at(_d.starts)),
        trailing: const Icon(Icons.edit_calendar, size: 18),
        onTap: () => _pickMoment(_d.starts, (v) => _d.starts = v),
      ),
      ListTile(
        key: const Key('mute-form-ends'),
        contentPadding: EdgeInsets.zero,
        title: Text(l.muteEnds),
        subtitle: Text(at(_d.ends)),
        trailing: const Icon(Icons.edit_calendar, size: 18),
        onTap: () => _pickMoment(_d.ends, (v) => _d.ends = v),
      ),
    ];
  }

  List<Widget> _schedule(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final calendars = widget.sections.calendars.items;

    return [
      SegmentedButton<String>(
        key: const Key('mute-form-frequency'),
        showSelectedIcon: false,
        segments: [
          ButtonSegment(value: 'weekly', label: Text(l.muteWeekly)),
          ButtonSegment(value: 'monthly', label: Text(l.muteMonthly)),
        ],
        selected: {_d.recurrence},
        onSelectionChanged: (s) => _changed(() => _d.recurrence = s.first),
      ),
      const SizedBox(height: 10),
      TextField(
        key: const Key('mute-form-timezone'),
        controller: _timezone,
        onChanged: (v) => _changed(() => _d.timezone = v),
        decoration: InputDecoration(
          labelText: l.muteTimezone,
          hintText: 'Europe/Istanbul',
          border: const OutlineInputBorder(),
          isDense: true,
        ),
      ),
      Row(
        children: [
          Expanded(
            child: ListTile(
              key: const Key('mute-form-start-time'),
              contentPadding: EdgeInsets.zero,
              title: Text(l.muteStartTime),
              subtitle: Text(_d.startTime),
              onTap: () => _pickTime(_d.startTime, (v) => _d.startTime = v),
            ),
          ),
          Expanded(
            child: ListTile(
              key: const Key('mute-form-end-time'),
              contentPadding: EdgeInsets.zero,
              title: Text(l.muteEndTime),
              subtitle: Text(_d.endTime),
              onTap: () => _pickTime(_d.endTime, (v) => _d.endTime = v),
            ),
          ),
        ],
      ),
      Text(l.muteEndTimeHint, style: muted),
      const SizedBox(height: 10),
      if (_d.recurrence == 'weekly')
        Wrap(
          spacing: 6,
          runSpacing: 4,
          children: [
            for (final d in weekDays)
              FilterChip(
                key: Key('mute-form-day-$d'),
                label: Text(weekDayShort(l, d)),
                selected: _d.days.contains(d),
                onSelected: (on) => _changed(
                  () => _d.days = on
                      ? [..._d.days, d]
                      : [
                          for (final x in _d.days)
                            if (x != d) x,
                        ],
                ),
              ),
          ],
        )
      else
        ..._monthly(context),
      DetailSection(
        title: l.muteExceptions,
        children: [
          Text(l.muteExceptionsHint, style: muted),
          const SizedBox(height: 6),
          Wrap(
            spacing: 6,
            runSpacing: 4,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              for (final d in _d.exdates)
                InputChip(
                  key: Key('mute-form-exdate-$d'),
                  label: Text(d),
                  onDeleted: () => _changed(
                    () => _d.exdates = [
                      for (final x in _d.exdates)
                        if (x != d) x,
                    ],
                  ),
                ),
              TextButton.icon(
                key: const Key('mute-form-add-exception'),
                onPressed: _addException,
                icon: const Icon(Icons.add, size: 18),
                label: Text(l.muteAddException),
              ),
            ],
          ),
          if (calendars.isNotEmpty) ...[
            const SizedBox(height: 6),
            Text(l.mutesCalendars, style: muted),
            Wrap(
              spacing: 6,
              runSpacing: 4,
              children: [
                for (final c in calendars)
                  FilterChip(
                    key: Key('mute-form-calendar-${c.id}'),
                    label: Text('${c.name} (${c.dates.length})'),
                    selected: _d.calendarIds.contains(c.id),
                    onSelected: (on) => _changed(
                      () => _d.calendarIds = on
                          ? [..._d.calendarIds, c.id]
                          : [
                              for (final x in _d.calendarIds)
                                if (x != c.id) x,
                            ],
                    ),
                  ),
              ],
            ),
          ],
        ],
      ),
      DetailSection(
        key: const Key('mute-upcoming'),
        title: l.muteUpcoming,
        children: [_upcoming(context)],
      ),
    ];
  }

  List<Widget> _monthly(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final custom = _d.customRRule;
    final m = _d.monthly;

    void set(MonthlyForm next) => _changed(() {
      _d
        ..customRRule = null
        ..monthly = next;
    });

    return [
      SegmentedButton<String>(
        key: const Key('mute-form-monthly-mode'),
        showSelectedIcon: false,
        emptySelectionAllowed: true,
        segments: [
          ButtonSegment(value: 'monthday', label: Text(l.muteModeMonthday)),
          ButtonSegment(value: 'weekday', label: Text(l.muteModeWeekday)),
        ],
        selected: custom != null ? const {} : {m.mode},
        onSelectionChanged: (s) {
          if (s.isNotEmpty) set(m.copyWith(mode: s.first));
        },
      ),
      const SizedBox(height: 10),
      if (custom != null)
        Text(
          '${l.muteCustomRule}: $custom',
          key: const Key('mute-form-custom-rule'),
          style: theme.textTheme.bodySmall,
        )
      else if (m.mode == 'monthday')
        TextField(
          key: const Key('mute-form-month-days'),
          controller: _monthDays,
          onChanged: (v) => set(m.copyWith(monthDays: v)),
          decoration: InputDecoration(
            labelText: l.muteMonthDays,
            hintText: '1, 15, -1',
            helperText: l.muteMonthDaysHint,
            errorText: _d.monthDaysInvalid ? l.muteMonthDaysInvalid : null,
            border: const OutlineInputBorder(),
            isDense: true,
          ),
        )
      else
        Row(
          children: [
            Expanded(
              child: DropdownButton<int>(
                key: const Key('mute-form-ordinal'),
                isExpanded: true,
                value: m.ordinal,
                items: [
                  for (final o in muteOrdinals)
                    DropdownMenuItem(value: o, child: Text(muteOrdinal(l, o))),
                ],
                onChanged: (v) {
                  if (v != null) set(m.copyWith(ordinal: v));
                },
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: DropdownButton<String>(
                key: const Key('mute-form-monthly-day'),
                isExpanded: true,
                value: m.day,
                items: [
                  for (final d in weekDays)
                    DropdownMenuItem(value: d, child: Text(weekDayShort(l, d))),
                  DropdownMenuItem(
                    value: anyWeekday,
                    child: Text(l.muteAnyWeekday),
                  ),
                ],
                onChanged: (v) {
                  if (v != null) set(m.copyWith(day: v));
                },
              ),
            ),
          ],
        ),
    ];
  }

  Widget _upcoming(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final p = _preview;
    if (!_d.previewable) return Text(l.muteUpcomingInvalid, style: muted);
    // An older server: it can still save a schedule it cannot preview.
    if (p.notOffered) return const NotOfferedNote(padding: EdgeInsets.zero);
    if (p.error != null) {
      return Text(
        p.error!,
        style: theme.textTheme.bodySmall?.copyWith(
          color: theme.colorScheme.error,
        ),
      );
    }
    final o = p.occurrences;
    if (o == null) return const LinearProgressIndicator();
    if (o.isEmpty) return Text(l.muteUpcomingNone, style: muted);
    String at(DateTime d) {
      final t = d.toLocal();
      return '${dateOnly(t)} ${t.hour.toString().padLeft(2, '0')}:'
          '${t.minute.toString().padLeft(2, '0')}';
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final x in o)
          Text('${at(x.startsAt)} – ${at(x.endsAt)}', style: muted),
      ],
    );
  }

  /// Which rules, and which labels, the mute covers.
  List<Widget> _scope(BuildContext context) {
    final l = L.of(context);
    final rules = widget.sections.rules.items;
    final theme = Theme.of(context);

    return [
      DetailSection(
        title: l.muteRules,
        children: [
          Text(
            l.muteRulesHint,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 6),
          Wrap(
            spacing: 6,
            runSpacing: 4,
            children: [
              for (final r in rules)
                FilterChip(
                  key: Key('mute-form-rule-${r.id}'),
                  label: Text(r.name),
                  selected: _d.ruleIds.contains(r.id),
                  onSelected: (on) => setState(
                    () => _d.ruleIds = on
                        ? [..._d.ruleIds, r.id]
                        : [
                            for (final x in _d.ruleIds)
                              if (x != r.id) x,
                          ],
                  ),
                ),
            ],
          ),
        ],
      ),
      DetailSection(
        title: l.muteMatchers,
        children: [
          for (final (i, m) in _d.matchers.indexed)
            _MatcherRow(
              key: ObjectKey(m),
              index: i,
              matcher: m,
              onRemove: () => setState(() => _d.matchers.remove(m)),
            ),
          Align(
            alignment: Alignment.centerLeft,
            child: TextButton.icon(
              key: const Key('mute-form-add-matcher'),
              onPressed: () => setState(
                () => _d.matchers = [..._d.matchers, MuteMatcherDraft()],
              ),
              icon: const Icon(Icons.add, size: 18),
              label: Text(l.muteAddMatcher),
            ),
          ),
        ],
      ),
    ];
  }
}

class _MatcherRow extends StatefulWidget {
  const _MatcherRow({
    super.key,
    required this.index,
    required this.matcher,
    required this.onRemove,
  });

  final int index;
  final MuteMatcherDraft matcher;
  final VoidCallback onRemove;

  @override
  State<_MatcherRow> createState() => _MatcherRowState();
}

class _MatcherRowState extends State<_MatcherRow> {
  late final _label = TextEditingController(text: widget.matcher.label);
  late final _value = TextEditingController(text: widget.matcher.value);

  @override
  void dispose() {
    _label.dispose();
    _value.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final m = widget.matcher;
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Row(
        children: [
          Expanded(
            child: TextField(
              key: Key('mute-matcher-label-${widget.index}'),
              controller: _label,
              onChanged: (v) => m.label = v,
              decoration: const InputDecoration(
                hintText: 'host.name',
                isDense: true,
              ),
            ),
          ),
          const SizedBox(width: 6),
          DropdownButton<String>(
            key: Key('mute-matcher-op-${widget.index}'),
            value: m.op,
            items: [
              for (final op in const ['eq', 'neq', 'contains'])
                DropdownMenuItem(value: op, child: Text(muteOp(l, op))),
            ],
            onChanged: (v) {
              if (v != null) setState(() => m.op = v);
            },
          ),
          const SizedBox(width: 6),
          Expanded(
            child: TextField(
              key: Key('mute-matcher-value-${widget.index}'),
              controller: _value,
              onChanged: (v) => m.value = v,
              decoration: const InputDecoration(isDense: true),
            ),
          ),
          IconButton(
            tooltip: l.muteRemoveMatcher,
            icon: const Icon(Icons.close, size: 18),
            onPressed: widget.onRemove,
          ),
        ],
      ),
    );
  }
}
