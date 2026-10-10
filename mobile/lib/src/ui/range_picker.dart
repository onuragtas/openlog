// The window picker: a chip in the app bar, and the sheet behind it.
//
// The web puts presets and a from/to form in the top bar; below `lg` it
// already collapses them into a select and a bottom sheet, which is what
// this is. The chip says the window in force, because a screen that is
// showing the last seven days and looks like the last hour is the worst
// of both.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../time_range.dart';

/// The chip for the app bar.
class RangeChip extends StatelessWidget {
  const RangeChip({super.key, required this.controller, required this.onPick});

  final TimeRangeController controller;

  /// Told when a new window was chosen, so the screen on view can ask
  /// again. Not called when the same one is chosen twice.
  final VoidCallback onPick;

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: controller,
    builder: (context, _) => TextButton.icon(
      key: const Key('range-chip'),
      onPressed: () async {
        final picked = await showModalBottomSheet<TimeRange>(
          context: context,
          isScrollControlled: true,
          useSafeArea: true,
          builder: (context) => _RangeSheet(current: controller.value),
        );
        if (picked != null && controller.choose(picked)) onPick();
      },
      icon: const Icon(Icons.schedule, size: 18),
      // The short form in the bar, the long one in the sheet: the bar
      // also holds a title and the refresh button.
      label: Text(rangeChipLabel(L.of(context), controller.value)),
    ),
  );
}

/// What the window is called: the preset, or the two ends of it.
String rangeLabel(L l, TimeRange range) {
  if (!range.absolute) return presetLabel(l, range.range);
  return '${_stamp(range.from!)} – ${_stamp(range.to!)}';
}

/// The same thing in the few characters an app bar has: "1 sa", or the
/// two ends without the day when the window is within one.
String rangeChipLabel(L l, TimeRange range) {
  if (!range.absolute) return shortPresetLabel(l, range.range);
  final from = range.from!.toLocal();
  final to = range.to!.toLocal();
  final sameDay =
      from.year == to.year && from.month == to.month && from.day == to.day;
  return sameDay
      ? '${_clock(from)}–${_clock(to)}'
      : '${_day(from)}–${_day(to)}';
}

String shortPresetLabel(L l, String preset) => switch (preset) {
  '15m' => l.range15mShort,
  '1h' => l.range1hShort,
  '6h' => l.range6hShort,
  '24h' => l.range24hShort,
  '7d' => l.range7dShort,
  _ => preset,
};

String presetLabel(L l, String preset) => switch (preset) {
  '15m' => l.range15m,
  '1h' => l.range1h,
  '6h' => l.range6h,
  '24h' => l.range24h,
  '7d' => l.range7d,
  _ => preset,
};

/// A local timestamp without the year: the window is a thing somebody
/// picked minutes ago, and "2026-" in a chip is four characters of noise.
String _stamp(DateTime t) => '${_day(t)} ${_clock(t)}';

String _two(int v) => v.toString().padLeft(2, '0');

String _day(DateTime t) {
  final l = t.toLocal();
  return '${_two(l.day)}.${_two(l.month)}';
}

String _clock(DateTime t) {
  final l = t.toLocal();
  return '${_two(l.hour)}:${_two(l.minute)}';
}

class _RangeSheet extends StatefulWidget {
  const _RangeSheet({required this.current});

  final TimeRange current;

  @override
  State<_RangeSheet> createState() => _RangeSheetState();
}

class _RangeSheetState extends State<_RangeSheet> {
  late DateTime _from;
  late DateTime _to;
  bool _invalid = false;

  @override
  void initState() {
    super.initState();
    // The custom form starts from the window in force, so switching to it
    // is a nudge rather than filling two fields from nothing.
    final w = widget.current.resolve(DateTime.now());
    _from = w.from.toLocal();
    _to = w.to.toLocal();
  }

  Future<void> _pick(bool isFrom) async {
    final initial = isFrom ? _from : _to;
    final now = DateTime.now();
    final date = await showDatePicker(
      context: context,
      initialDate: initial,
      firstDate: now.subtract(const Duration(days: 400)),
      lastDate: now.add(const Duration(days: 1)),
    );
    if (date == null || !mounted) return;
    final time = await showTimePicker(
      context: context,
      initialTime: TimeOfDay.fromDateTime(initial),
    );
    if (time == null || !mounted) return;
    setState(() {
      final picked = DateTime(
        date.year,
        date.month,
        date.day,
        time.hour,
        time.minute,
      );
      if (isFrom) {
        _from = picked;
      } else {
        _to = picked;
      }
      _invalid = !_from.isBefore(_to);
    });
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final current = widget.current;

    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 16, 16, 24),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(l.rangeTitle, style: theme.textTheme.titleMedium),
          const SizedBox(height: 10),
          for (final preset in presetRanges)
            ListTile(
              key: Key('range-$preset'),
              contentPadding: EdgeInsets.zero,
              dense: true,
              title: Text(presetLabel(l, preset)),
              // A custom window matches no preset, so nothing is ticked.
              trailing: !current.absolute && current.range == preset
                  ? Icon(Icons.check, color: theme.colorScheme.primary)
                  : null,
              onTap: () => Navigator.of(context).pop(TimeRange.preset(preset)),
            ),
          const Divider(height: 20),
          Text(l.rangeCustom, style: theme.textTheme.titleSmall),
          const SizedBox(height: 6),
          Row(
            children: [
              Expanded(
                child: OutlinedButton(
                  key: const Key('range-from'),
                  onPressed: () => _pick(true),
                  child: Text('${l.rangeFrom}: ${_stamp(_from)}'),
                ),
              ),
              const SizedBox(width: 8),
              Expanded(
                child: OutlinedButton(
                  key: const Key('range-to'),
                  onPressed: () => _pick(false),
                  child: Text('${l.rangeTo}: ${_stamp(_to)}'),
                ),
              ),
            ],
          ),
          if (_invalid)
            Padding(
              padding: const EdgeInsets.only(top: 6),
              child: Text(
                l.rangeInvalid,
                key: const Key('range-invalid'),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.error,
                ),
              ),
            ),
          const SizedBox(height: 10),
          Align(
            alignment: Alignment.centerRight,
            child: FilledButton(
              key: const Key('range-apply'),
              onPressed: _from.isBefore(_to)
                  ? () => Navigator.of(
                      context,
                    ).pop(TimeRange.absolute(_from, _to))
                  : null,
              child: Text(l.rangeApply),
            ),
          ),
        ],
      ),
    );
  }
}
