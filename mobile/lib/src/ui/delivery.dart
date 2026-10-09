// One notification: shared by the incident page, which shows what was sent
// for that incident, and the delivery log, which shows what was sent at all.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import 'list_scaffold.dart';
import 'severity.dart';

/// One notification: where it was supposed to go, whether it got there, and the
/// server's error if it did not -- which is the difference between "nobody was
/// told" and "nobody looked".
class DeliveryTile extends StatelessWidget {
  const DeliveryTile({
    super.key,
    required this.delivery,
    this.detailed = false,
    this.showChannel = true,
  });

  final AlertDelivery delivery;

  /// False in a log that is already about one channel, where repeating its
  /// name on every row says nothing and pushes out what differs.
  final bool showChannel;

  /// The log screen's extra lines: which rule it was for, when it was queued,
  /// and every attempt. An incident's own page does not need them -- the rule
  /// is the page, and the timeline above already says when.
  final bool detailed;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final (label, level) = switch (delivery.status) {
      AlertNotificationStatus.delivered => (
        l.deliveryDelivered,
        SeverityLevel.info,
      ),
      AlertNotificationStatus.failed => (
        l.deliveryFailed,
        SeverityLevel.critical,
      ),
      AlertNotificationStatus.suppressed => (
        l.deliverySuppressed,
        SeverityLevel.warning,
      ),
      AlertNotificationStatus.sending => (
        l.deliverySending,
        SeverityLevel.unknown,
      ),
      AlertNotificationStatus.pending => (
        l.deliveryPending,
        SeverityLevel.unknown,
      ),
      AlertNotificationStatus.unknown => (
        l.deliveryUnknown,
        SeverityLevel.unknown,
      ),
    };
    final colors = severityChipColors(context, level);

    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Wrap(
            spacing: 8,
            runSpacing: 4,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 3),
                decoration: BoxDecoration(
                  color: colors.background,
                  borderRadius: BorderRadius.circular(10),
                ),
                child: Text(
                  label,
                  style: TextStyle(
                    color: colors.foreground,
                    fontSize: 11,
                    fontWeight: FontWeight.w600,
                  ),
                ),
              ),
              if (showChannel) ...[
                Text(delivery.channelName, style: theme.textTheme.bodyMedium),
                Text(
                  delivery.channelType.wire,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ],
              if (delivery.attempts > 1)
                Text(
                  l.deliveryAttempts(delivery.attempts),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              if (detailed)
                Text(
                  relativeTimeOf(l, delivery.createdAt),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
            ],
          ),
          if (detailed && delivery.ruleName.isNotEmpty)
            Text(
              l.deliveryForRule(delivery.ruleName),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          if (delivery.lastError.isNotEmpty)
            Text(
              delivery.lastError,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            ),
          // Every attempt, not just the count: three failures at 30 ms and
          // three at 10 s are different problems, and the one that matters is
          // whether the receiver answered at all.
          if (detailed)
            for (final a in delivery.attemptLog)
              Text(
                l.deliveryAttempt(
                  a.attempt,
                  a.success
                      ? l.deliveryAttemptOk
                      : a.statusCode > 0
                      ? '${l.deliveryAttemptFailed} ${a.statusCode}'
                      : l.deliveryAttemptFailed,
                  a.durationMs,
                ),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: severityTextColor(
                    context,
                    a.success ? SeverityLevel.good : SeverityLevel.critical,
                  ),
                ),
              ),
        ],
      ),
    );
  }
}
