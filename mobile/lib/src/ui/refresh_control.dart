// Refresh now, and how often to do it by itself.
//
// The web has two controls side by side in its top bar. A phone bar also
// holds a drawer button, a title and the range chip, so the two are one
// menu here: the first item refreshes now, the rest choose the interval.
// Pulling a list down still refreshes it, which is why losing the
// one-tap button costs nothing.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../auto_refresh.dart';
import '../time_range.dart';

class RefreshControl extends StatelessWidget {
  const RefreshControl({
    super.key,
    required this.auto,
    required this.range,
    required this.onRefreshNow,
    required this.onInterval,
  });

  final AutoRefreshController auto;
  final TimeRangeController range;
  final VoidCallback onRefreshNow;
  final ValueChanged<String> onInterval;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return ListenableBuilder(
      listenable: Listenable.merge([auto, range]),
      builder: (context, _) {
        // A window with two fixed ends does not move, so asking again on
        // a timer asks the same question. The web disables it too.
        final absolute = range.value.absolute;
        final on = auto.on && !absolute;
        return PopupMenuButton<String>(
          key: const Key('refresh-menu'),
          tooltip: l.refresh,
          // Under the bar rather than over it: the menu is tall enough to
          // cover the title and the window chip, and hiding which window
          // is in force while choosing how often to ask about it is
          // exactly the wrong half to hide.
          position: PopupMenuPosition.under,
          // A child, not an icon: an icon is given a square of a fixed
          // size and the interval beside it is simply clipped off the
          // right edge of the bar -- which is where it was, reading
          // "3" for 30s.
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(
                  on ? Icons.autorenew : Icons.refresh,
                  color: on ? theme.colorScheme.primary : null,
                ),
                if (on)
                  Padding(
                    padding: const EdgeInsets.only(left: 4),
                    child: Text(
                      auto.interval,
                      style: theme.textTheme.labelMedium?.copyWith(
                        color: theme.colorScheme.primary,
                      ),
                    ),
                  ),
              ],
            ),
          ),
          onSelected: (v) =>
              v == 'now' ? onRefreshNow() : onInterval(v == 'off' ? '' : v),
          itemBuilder: (context) => [
            PopupMenuItem(value: 'now', child: Text(l.refreshNow)),
            const PopupMenuDivider(),
            PopupMenuItem(
              enabled: false,
              child: Text(
                absolute ? l.refreshAutoFixedWindow : l.refreshAuto,
                style: theme.textTheme.bodySmall,
              ),
            ),
            PopupMenuItem(
              value: 'off',
              enabled: !absolute,
              child: _Choice(label: l.refreshOff, chosen: !auto.on),
            ),
            for (final interval in refreshIntervals)
              PopupMenuItem(
                value: interval,
                enabled: !absolute,
                child: _Choice(
                  label: interval,
                  chosen: auto.interval == interval,
                ),
              ),
          ],
        );
      },
    );
  }
}

class _Choice extends StatelessWidget {
  const _Choice({required this.label, required this.chosen});

  final String label;
  final bool chosen;

  @override
  Widget build(BuildContext context) => Row(
    children: [
      Expanded(child: Text(label)),
      if (chosen)
        Icon(
          Icons.check,
          size: 18,
          color: Theme.of(context).colorScheme.primary,
        ),
    ],
  );
}
