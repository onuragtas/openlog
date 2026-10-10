// How much arrived, and when: the chart above the explorers.
//
// One line, no axes, as everywhere else in this app. What makes it worth
// the space is the total beside it and, for traces, the p95 -- a bucket
// count alone does not say whether the requests were slow.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../session.dart';
import '../volume.dart';
import 'severity.dart';
import 'sparkline.dart';

class VolumeChart extends StatelessWidget {
  const VolumeChart({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final VolumeController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        // Nothing to draw is not an error and not a spinner: the list
        // underneath already says whether there is anything at all.
        if (c.counts.length < 2) return const SizedBox.shrink();
        return Padding(
          padding: const EdgeInsets.fromLTRB(12, 6, 12, 2),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Text(
                      l.volumeTotal(c.total, c.step),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  ),
                  if (c.p95.isNotEmpty)
                    Text(
                      l.volumeP95(_ms(c.p95.last)),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                ],
              ),
              SizedBox(
                height: 44,
                child: Sparkline(
                  values: c.counts,
                  color: theme.colorScheme.primary,
                ),
              ),
              if (c.p95.length > 1)
                SizedBox(
                  height: 28,
                  child: Sparkline(
                    values: c.p95,
                    color: severityTextColor(context, SeverityLevel.warning),
                  ),
                ),
            ],
          ),
        );
      },
    );
  }

  String _ms(double v) => v >= 100 || v == v.roundToDouble()
      ? v.toStringAsFixed(0)
      : v.toStringAsFixed(1);
}
