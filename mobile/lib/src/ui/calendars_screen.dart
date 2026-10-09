// The holiday calendars a recurring mute skips.
//
// Reached from the mutes screen, as on the web, because a calendar is only
// ever interesting because of a mute: either one that fired on a holiday, or
// one about to. Full editing on a phone is worth it here where a recurrence
// editor was not -- a calendar is a name and a list of dates, which is typing,
// and typing is the one thing a phone is as good at as a laptop.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../calendar_dates.dart';
import '../sections.dart';
import '../session.dart';
import 'failure_text.dart';

class CalendarsScreen extends StatefulWidget {
  const CalendarsScreen({
    super.key,
    required this.session,
    required this.calendars,
  });

  final SessionController session;
  final AlertCalendarsController calendars;

  @override
  State<CalendarsScreen> createState() => _CalendarsScreenState();
}

class _CalendarsScreenState extends State<CalendarsScreen> {
  /// The calendar being edited, `''` for a new one, null when the form is
  /// closed. An id rather than the record so a refresh under the form does not
  /// put stale dates back in the fields.
  String? _editing;

  @override
  void initState() {
    super.initState();
    final c = widget.calendars;
    if (!c.loaded && !c.loadingFirst) {
      WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
    }
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.calendars;
    final theme = Theme.of(context);

    return Scaffold(
      appBar: AppBar(
        title: Text(l.calendarsTitle),
        actions: [
          IconButton(
            key: const Key('calendars-refresh'),
            tooltip: l.refresh,
            onPressed: c.refresh,
            icon: const Icon(Icons.refresh),
          ),
        ],
      ),
      body: ListenableBuilder(
        listenable: c,
        builder: (context, _) {
          if (c.loadingFirst) {
            return const Center(child: CircularProgressIndicator());
          }
          final editing = _editing;
          final current = editing == null || editing.isEmpty
              ? null
              : c.items.where((x) => x.id == editing).firstOrNull;
          return RefreshIndicator(
            onRefresh: c.refresh,
            child: ListView(
              padding: const EdgeInsets.fromLTRB(12, 12, 12, 28),
              children: [
                Text(
                  l.calendarsAbout,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                FailureBanner(
                  failure: c.failure,
                  baseUrl: widget.session.baseUrl ?? '',
                ),
                const SizedBox(height: 8),
                if (editing == null)
                  Align(
                    alignment: Alignment.centerLeft,
                    child: OutlinedButton.icon(
                      key: const Key('calendar-new'),
                      onPressed: () => setState(() => _editing = ''),
                      icon: const Icon(Icons.add),
                      label: Text(l.calendarsNew),
                    ),
                  )
                else
                  _CalendarForm(
                    // Keyed by what is being edited so switching rows rebuilds
                    // the fields rather than keeping the previous calendar's
                    // dates in them.
                    key: ValueKey(editing),
                    controller: c,
                    calendar: current,
                    onDone: () => setState(() => _editing = null),
                  ),
                const SizedBox(height: 8),
                if (c.items.isEmpty)
                  Padding(
                    padding: const EdgeInsets.symmetric(vertical: 20),
                    child: Center(
                      child: Text(
                        l.calendarsEmpty,
                        key: const Key('calendars-empty'),
                        style: theme.textTheme.bodyMedium?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    ),
                  )
                else
                  for (final cal in c.items)
                    _CalendarCard(
                      calendar: cal,
                      busy: c.busy == cal.id,
                      onEdit: () => setState(() => _editing = cal.id),
                      onDelete: () => _confirmDelete(context, c, cal),
                    ),
              ],
            ),
          );
        },
      ),
    );
  }

  Future<void> _confirmDelete(
    BuildContext context,
    AlertCalendarsController c,
    AlertHolidayCalendar cal,
  ) async {
    final l = L.of(context);
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(l.calendarsDeleteTitle(cal.name)),
        // Says what deleting does to the mutes that use it, which is the part
        // the row's count hints at and the dialog has to spell out.
        content: Text(
          cal.muteCount == 0
              ? l.calendarsDeleteBody
              : l.calendarsDeleteInUse(cal.muteCount),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: Text(l.ruleCancel),
          ),
          FilledButton(
            key: const Key('calendar-confirm'),
            onPressed: () => Navigator.of(context).pop(true),
            child: Text(l.calendarsDelete),
          ),
        ],
      ),
    );
    if (ok ?? false) {
      await c.remove(cal.id);
      // A form left open over a deleted calendar would save it back.
      if (mounted && _editing == cal.id) setState(() => _editing = null);
    }
  }
}

class _CalendarCard extends StatelessWidget {
  const _CalendarCard({
    required this.calendar,
    required this.busy,
    required this.onEdit,
    required this.onDelete,
  });

  final AlertHolidayCalendar calendar;
  final bool busy;
  final VoidCallback onEdit;
  final VoidCallback onDelete;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    // The first few dates, so a calendar can be told apart from another one
    // without opening it. The count next to them says how many are not shown.
    final shown = calendar.dates.take(8).join(', ');

    return Card(
      key: Key('calendar-${calendar.id}'),
      margin: const EdgeInsets.symmetric(vertical: 5),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 12, 8, 8),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(calendar.name, style: theme.textTheme.titleSmall),
            if (calendar.description.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 2),
                child: Text(calendar.description, style: muted),
              ),
            const SizedBox(height: 6),
            Text(
              l.calendarsSummary(calendar.dates.length, calendar.muteCount),
              style: muted,
            ),
            if (shown.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 2),
                child: Text(
                  calendar.dates.length > 8 ? '$shown …' : shown,
                  // Tabular figures rather than a monospace family: there is
                  // no font called `monospace` on iOS, so asking for one
                  // silently gets the default font on half the devices. These
                  // line the digits up on both.
                  style: theme.textTheme.bodySmall?.copyWith(
                    fontFeatures: const [FontFeature.tabularFigures()],
                  ),
                ),
              ),
            Align(
              alignment: Alignment.centerRight,
              child: busy
                  ? const Padding(
                      padding: EdgeInsets.all(12),
                      child: SizedBox(
                        width: 16,
                        height: 16,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      ),
                    )
                  : Row(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        TextButton(
                          key: Key('calendar-edit-${calendar.id}'),
                          onPressed: onEdit,
                          child: Text(l.calendarsEdit),
                        ),
                        TextButton(
                          key: Key('calendar-delete-${calendar.id}'),
                          onPressed: onDelete,
                          child: Text(l.calendarsDelete),
                        ),
                      ],
                    ),
            ),
          ],
        ),
      ),
    );
  }
}

/// A name, a description and the dates, as text.
///
/// The dates are a text field rather than a date picker: a calendar of public
/// holidays is a dozen dates typed in one go, and `MM-DD` -- every year --
/// is not a thing a date picker can express at all.
class _CalendarForm extends StatefulWidget {
  const _CalendarForm({
    super.key,
    required this.controller,
    required this.calendar,
    required this.onDone,
  });

  final AlertCalendarsController controller;
  final AlertHolidayCalendar? calendar;
  final VoidCallback onDone;

  @override
  State<_CalendarForm> createState() => _CalendarFormState();
}

class _CalendarFormState extends State<_CalendarForm> {
  late final TextEditingController _name;
  late final TextEditingController _description;
  late final TextEditingController _dates;

  @override
  void initState() {
    super.initState();
    final cal = widget.calendar;
    _name = TextEditingController(text: cal?.name ?? '');
    _description = TextEditingController(text: cal?.description ?? '');
    _dates = TextEditingController(text: (cal?.dates ?? const []).join('\n'));
  }

  @override
  void dispose() {
    _name.dispose();
    _description.dispose();
    _dates.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.controller;
    final parsed = parseCalendarDates(_dates.text);
    final invalid = parsed.invalid.isEmpty
        ? null
        : l.calendarsInvalid(parsed.invalid.join(', '));

    return Card(
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            TextField(
              key: const Key('calendar-name'),
              controller: _name,
              onChanged: (_) => setState(() {}),
              decoration: InputDecoration(
                labelText: l.calendarsName,
                border: const OutlineInputBorder(),
                isDense: true,
              ),
            ),
            const SizedBox(height: 10),
            TextField(
              key: const Key('calendar-description'),
              controller: _description,
              decoration: InputDecoration(
                labelText: l.calendarsDescription,
                border: const OutlineInputBorder(),
                isDense: true,
              ),
            ),
            const SizedBox(height: 10),
            TextField(
              key: const Key('calendar-dates'),
              controller: _dates,
              minLines: 4,
              maxLines: 10,
              keyboardType: TextInputType.multiline,
              style: const TextStyle(
                fontFeatures: [FontFeature.tabularFigures()],
              ),
              onChanged: (_) => setState(() {}),
              decoration: InputDecoration(
                labelText: l.calendarsDates,
                helperText: l.calendarsDatesHint,
                helperMaxLines: 3,
                errorText: invalid,
                errorMaxLines: 3,
                border: const OutlineInputBorder(),
                isDense: true,
              ),
            ),
            const SizedBox(height: 10),
            Row(
              children: [
                Expanded(
                  child: Text(
                    l.calendarsCount(parsed.dates.length),
                    key: const Key('calendar-count'),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ),
                TextButton(
                  key: const Key('calendar-cancel'),
                  onPressed: c.saving ? null : widget.onDone,
                  child: Text(l.ruleCancel),
                ),
                const SizedBox(width: 4),
                FilledButton(
                  key: const Key('calendar-save'),
                  // A nameless calendar and an unparseable date list are both
                  // 400s; the button waits rather than sending them.
                  onPressed:
                      c.saving ||
                          invalid != null ||
                          _name.text.trim().isEmpty ||
                          parsed.dates.isEmpty
                      ? null
                      : () async {
                          final saved = await c.save(
                            id: widget.calendar?.id,
                            name: _name.text.trim(),
                            description: _description.text.trim(),
                            dates: parsed.dates,
                          );
                          if (saved) widget.onDone();
                        },
                  child: c.saving
                      ? const SizedBox(
                          width: 16,
                          height: 16,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : Text(
                          widget.calendar == null
                              ? l.calendarsCreate
                              : l.calendarsSave,
                        ),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}
