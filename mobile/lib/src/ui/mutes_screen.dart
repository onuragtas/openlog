// What is silenced, and until when.
//
// The write this screen exists for is the one an on-call person makes with a
// deploy about to start: silence everything for the next two hours. Recurring
// schedules are shown but edited on the web -- a weekly-recurrence editor on
// a phone would be a worse one than the web already has.
//
// The holiday calendars those schedules skip are a screen of their own,
// reached from here as on the web, because a calendar is only ever
// interesting because of a mute.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'calendars_screen.dart';
import 'list_scaffold.dart';
import 'sections_screen.dart';
import 'severity.dart';

/// What a phone offers. Longer than four hours is a maintenance window, which
/// is a planned thing and belongs where plans are made.
const muteDurations = <(String, Duration)>[
  ('30m', Duration(minutes: 30)),
  ('1h', Duration(hours: 1)),
  ('2h', Duration(hours: 2)),
  ('4h', Duration(hours: 4)),
];

class MutesBody extends StatefulWidget {
  const MutesBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  State<MutesBody> createState() => _MutesBodyState();
}

class _MutesBodyState extends State<MutesBody> {
  @override
  Widget build(BuildContext context) {
    final c = widget.sections.mutes;

    return SectionBody<AlertMute>(
      session: widget.session,
      controller: c,
      searchKey: 'mutes-search',
      searchHint: (l) => l.mutesSearch,
      active: widget.active,
      emptyTitle: (l) => l.mutesEmpty,
      header: ListenableBuilder(
        listenable: c,
        builder: (context, _) => Column(
          children: [
            _NewMute(controller: c),
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton.icon(
                key: const Key('mutes-calendars'),
                onPressed: () => Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => CalendarsScreen(
                      session: widget.session,
                      calendars: widget.sections.calendars,
                    ),
                  ),
                ),
                icon: const Icon(Icons.event_busy),
                label: Text(L.of(context).mutesCalendars),
              ),
            ),
          ],
        ),
      ),
      card: (context, mute) => ListenableBuilder(
        listenable: c,
        builder: (context, _) => _MuteCard(
          mute: mute,
          busy: c.busy == mute.id,
          onEnd: () => _confirmEnd(context, c, mute),
        ),
      ),
    );
  }

  Future<void> _confirmEnd(
    BuildContext context,
    AlertMutesController c,
    AlertMute mute,
  ) async {
    final l = L.of(context);
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(l.mutesEndTitle),
        content: Text(l.mutesEndBody),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: Text(l.ruleCancel),
          ),
          FilledButton(
            key: const Key('mute-confirm'),
            onPressed: () => Navigator.of(context).pop(true),
            child: Text(l.mutesEnd),
          ),
        ],
      ),
    );
    if (ok ?? false) await c.end(mute.id);
  }
}

/// A name and a duration, and nothing else.
///
/// The server wants a window; a phone can give it one that starts now. Rule
/// and label scoping is on the web, because choosing from a hundred rules is
/// not a thing a thumb does well -- and silencing everything for two hours is
/// what gets asked for anyway.
class _NewMute extends StatefulWidget {
  const _NewMute({required this.controller});

  final AlertMutesController controller;

  @override
  State<_NewMute> createState() => _NewMuteState();
}

class _NewMuteState extends State<_NewMute> {
  final _name = TextEditingController();
  Duration _duration = muteDurations[1].$2;

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.controller;

    return Card(
      margin: const EdgeInsets.only(bottom: 10),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(l.mutesNew, style: theme.textTheme.titleSmall),
            const SizedBox(height: 10),
            TextField(
              key: const Key('mute-name'),
              controller: _name,
              decoration: InputDecoration(
                labelText: l.mutesNewName,
                hintText: l.mutesNewNameHint,
                border: const OutlineInputBorder(),
                isDense: true,
              ),
            ),
            const SizedBox(height: 10),
            SegmentedButton<Duration>(
              key: const Key('mute-duration'),
              showSelectedIcon: false,
              segments: [
                for (final (label, d) in muteDurations)
                  ButtonSegment(value: d, label: Text(label)),
              ],
              selected: {_duration},
              onSelectionChanged: (s) => setState(() => _duration = s.first),
            ),
            const SizedBox(height: 10),
            Align(
              alignment: Alignment.centerRight,
              child: FilledButton(
                key: const Key('mute-create'),
                // A mute with no reason is one nobody can judge later, so the
                // button waits for one rather than inventing a name.
                onPressed: c.creating || _name.text.trim().isEmpty
                    ? null
                    : () async {
                        await c.createFor(
                          name: _name.text.trim(),
                          duration: _duration,
                        );
                        if (c.failure == null) _name.clear();
                      },
                child: c.creating
                    ? const SizedBox(
                        width: 16,
                        height: 16,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    : Text(l.mutesCreate),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _MuteCard extends StatelessWidget {
  const _MuteCard({
    required this.mute,
    required this.busy,
    required this.onEnd,
  });

  final AlertMute mute;
  final bool busy;
  final VoidCallback onEnd;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Card(
      key: Key('mute-${mute.id}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 12, 8, 12),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    mute.name,
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.titleSmall,
                  ),
                  const SizedBox(height: 6),
                  Wrap(
                    spacing: 8,
                    runSpacing: 4,
                    crossAxisAlignment: WrapCrossAlignment.center,
                    children: [
                      if (mute.active)
                        Tag(label: l.mutesActive, level: SeverityLevel.warning)
                      else
                        Text(
                          l.mutesUpcoming(relativeTimeOf(l, mute.startsAt)),
                          style: muted,
                        ),
                      Text(
                        l.mutesUntil(relativeTimeOf(l, mute.endsAt)),
                        style: muted,
                      ),
                      // Which rules: "every rule" is the dangerous one and
                      // has to be legible at a glance.
                      Text(
                        mute.ruleIds.isEmpty
                            ? l.mutesAllRules
                            : l.mutesSomeRules(mute.ruleIds.length),
                        style: mute.ruleIds.isEmpty
                            ? theme.textTheme.bodySmall?.copyWith(
                                color: severityTextColor(
                                  context,
                                  SeverityLevel.warning,
                                ),
                              )
                            : muted,
                      ),
                      if (mute.schedule != null)
                        Text(l.mutesRecurring, style: muted),
                    ],
                  ),
                  if (mute.comment.isNotEmpty)
                    Padding(
                      padding: const EdgeInsets.only(top: 4),
                      child: Text(
                        mute.comment,
                        style: theme.textTheme.bodySmall,
                      ),
                    ),
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
                : TextButton(
                    key: Key('mute-end-${mute.id}'),
                    onPressed: onEnd,
                    child: Text(l.mutesEnd),
                  ),
          ],
        ),
      ),
    );
  }
}
