// One incident, end to end: what fired, what the server did about it, what was
// sent where, and the two things an on-call person can do from a phone.
//
// This is the screen that makes the app worth opening at three in the morning:
// the list says something is wrong, and this says enough to decide whether to
// get out of bed -- including a tap through to the service, which is where the
// answer usually is.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'service_screen.dart';
import 'severity.dart';

class IncidentScreen extends StatefulWidget {
  const IncidentScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.incidentId,
    required this.ruleName,
  });

  final SessionController session;
  final Sections sections;
  final String incidentId;

  /// Shown in the app bar while the detail is still loading, so the screen
  /// opens with the name of the thing the person tapped rather than blank.
  final String ruleName;

  @override
  State<IncidentScreen> createState() => _IncidentScreenState();
}

class _IncidentScreenState extends State<IncidentScreen> {
  late final IncidentController _c;
  final _note = TextEditingController();

  @override
  void initState() {
    super.initState();
    _c = widget.sections.incident(widget.incidentId);
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void dispose() {
    _note.dispose();
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);

    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => DetailScreen<AlertIncidentDetail>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: _c.value?.ruleName ?? widget.ruleName,
        subtitle: _c.value?.seriesKey.isNotEmpty == true
            ? _c.value!.seriesKey
            : null,
        builder: (context, incident) => _body(context, l, incident),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, AlertIncidentDetail i) {
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;
    final service = _c.serviceName;
    final labels = _c.labels;

    return [
      const SizedBox(height: 4),
      Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          SeverityChip(severity: i.severity),
          if (i.muted) OutlineTag(label: l.alertsMuted),
          if (i.flapping) OutlineTag(label: l.alertsFlapping),
        ],
      ),
      if (i.summary.isNotEmpty) ...[
        const SizedBox(height: 12),
        Text(i.summary, style: text.bodyLarge),
      ],

      // The numbers first, because "is it still true" is the first question.
      if (i.value != null || i.threshold != null) ...[
        const SizedBox(height: 18),
        Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            if (i.value != null)
              Expanded(
                child: Stat(
                  label: l.incidentValue,
                  value: _number(i.lastValue ?? i.value!),
                  color: severityTextColor(context, _levelOf(i.severity)),
                ),
              ),
            if (i.threshold != null)
              Expanded(
                child: Stat(
                  label: l.incidentThreshold,
                  value: _number(i.threshold!),
                ),
              ),
          ],
        ),
      ],

      const SizedBox(height: 18),
      Text(
        l.alertsOpened(relativeTimeOf(l, i.openedAt)),
        style: text.bodySmall?.copyWith(color: scheme.onSurfaceVariant),
      ),
      if (i.acknowledgedAt != null)
        Text(
          i.acknowledgedByEmail == null
              ? l.alertsAcknowledgedUnknown
              : l.alertsAcknowledgedBy(i.acknowledgedByEmail!),
          style: text.bodySmall?.copyWith(color: scheme.primary),
        ),
      if (i.resolvedAt != null) ...[
        Text(
          l.incidentResolved(relativeTimeOf(l, i.resolvedAt!)),
          style: text.bodySmall?.copyWith(color: scheme.onSurfaceVariant),
        ),
        if (i.resolvedByEmail != null)
          Text(
            l.incidentResolvedBy(i.resolvedByEmail!),
            style: text.bodySmall?.copyWith(color: scheme.onSurfaceVariant),
          ),
      ],

      // The tap that makes this an investigation rather than a notification.
      if (service != null) ...[
        const SizedBox(height: 16),
        OutlinedButton.icon(
          key: const Key('incident-open-service'),
          onPressed: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => ServiceScreen(
                session: widget.session,
                sections: widget.sections,
                serviceName: service,
              ),
            ),
          ),
          icon: const Icon(Icons.arrow_forward),
          label: Text(l.incidentOpenService(service)),
        ),
      ],

      if (i.state != AlertIncidentState.resolved) _actions(context, l, i),

      if (labels.isNotEmpty)
        DetailSection(
          title: l.incidentLabels,
          children: [
            for (final e in labels.entries)
              KeyValue(name: e.key, value: e.value),
          ],
        ),

      DetailSection(
        title: l.incidentTimeline,
        children: i.events.isEmpty
            ? [
                Text(
                  l.incidentNoEvents,
                  style: text.bodyMedium?.copyWith(
                    color: scheme.onSurfaceVariant,
                  ),
                ),
              ]
            // Newest first: the last thing that happened is the thing being
            // asked about.
            : [
                for (final e in i.events.reversed)
                  _Event(event: e, key: Key('event-${e.id}')),
              ],
      ),

      DetailSection(
        title: l.incidentDeliveries,
        children: i.deliveries.isEmpty
            ? [
                Text(
                  l.incidentNoDeliveries,
                  style: text.bodyMedium?.copyWith(
                    color: scheme.onSurfaceVariant,
                  ),
                ),
              ]
            : [
                for (final d in i.deliveries)
                  _Delivery(delivery: d, key: Key('delivery-${d.id}')),
              ],
      ),
    ];
  }

  Widget _actions(BuildContext context, L l, AlertIncidentDetail i) {
    final busy = _c.busy;
    return Padding(
      padding: const EdgeInsets.only(top: 18),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Wrap(
            spacing: 10,
            runSpacing: 8,
            children: [
              // Taking it is the common, reversible action, so it is the one
              // that looks like the button; resolving is final and sits beside
              // it rather than under the thumb.
              if (i.state == AlertIncidentState.open)
                FilledButton(
                  key: const Key('incident-ack'),
                  onPressed: busy != null ? null : _c.acknowledge,
                  child: _label(busy == 'acknowledge', l.alertsAcknowledge),
                ),
              OutlinedButton(
                key: const Key('incident-resolve'),
                onPressed: busy != null
                    ? null
                    : () => _c.resolve(note: _note.text.trim()),
                child: _label(busy == 'resolve', l.incidentResolve),
              ),
            ],
          ),
          const SizedBox(height: 8),
          Text(
            l.incidentResolveHint,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: Theme.of(context).colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 14),
          TextField(
            key: const Key('incident-note'),
            controller: _note,
            minLines: 1,
            maxLines: 4,
            maxLength: 4000,
            decoration: InputDecoration(
              labelText: l.incidentNote,
              hintText: l.incidentNoteHint,
              border: const OutlineInputBorder(),
              isDense: true,
            ),
          ),
          Align(
            alignment: Alignment.centerRight,
            child: TextButton(
              key: const Key('incident-note-send'),
              onPressed: busy != null
                  ? null
                  : () async {
                      final t = _note.text.trim();
                      if (t.isEmpty) return;
                      await _c.addNote(t);
                      // Only clear once the server has it, so a failed note is
                      // still in the box to retry rather than lost.
                      if (_c.failure == null) _note.clear();
                    },
              child: _label(busy == 'note', l.incidentNoteSend),
            ),
          ),
        ],
      ),
    );
  }

  Widget _label(bool busy, String text) => busy
      ? const SizedBox(
          width: 16,
          height: 16,
          child: CircularProgressIndicator(strokeWidth: 2),
        )
      : Text(text);
}

/// A timeline entry. The server writes the sentence; this app only says what
/// kind of thing it was and when, because inventing wording for an event the
/// server already described would only let the two disagree.
class _Event extends StatelessWidget {
  const _Event({super.key, required this.event});

  final AlertIncidentEvent event;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final kind = switch (event.kind) {
      AlertIncidentEventKind.opened => l.incidentEventOpened,
      AlertIncidentEventKind.flapping => l.incidentEventFlapping,
      AlertIncidentEventKind.acknowledged => l.incidentEventAcknowledged,
      AlertIncidentEventKind.note => l.incidentEventNote,
      AlertIncidentEventKind.renotified => l.incidentEventRenotified,
      AlertIncidentEventKind.resolved => l.incidentEventResolved,
      AlertIncidentEventKind.notificationDelivered => l.incidentEventDelivered,
      AlertIncidentEventKind.notificationFailed => l.incidentEventFailed,
      AlertIncidentEventKind.notificationSuppressed =>
        l.incidentEventSuppressed,
      AlertIncidentEventKind.notificationMuted => l.incidentEventMuted,
      AlertIncidentEventKind.unknown => l.incidentEventUnknown,
    };

    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Wrap(
            spacing: 8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Text(
                kind,
                style: theme.textTheme.bodyMedium?.copyWith(
                  fontWeight: FontWeight.w600,
                ),
              ),
              Text(
                relativeTimeOf(l, event.at),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
              if (event.actorEmail != null)
                Text(
                  event.actorEmail!,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.primary,
                  ),
                ),
            ],
          ),
          if (event.message.isNotEmpty)
            Text(event.message, style: theme.textTheme.bodyMedium),
        ],
      ),
    );
  }
}

/// One notification: where it was supposed to go, whether it got there, and the
/// server's error if it did not -- which is the difference between "nobody was
/// told" and "nobody looked".
class _Delivery extends StatelessWidget {
  const _Delivery({super.key, required this.delivery});

  final AlertDelivery delivery;

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
              Text(delivery.channelName, style: theme.textTheme.bodyMedium),
              Text(
                delivery.channelType.wire,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
              if (delivery.attempts > 1)
                Text(
                  l.deliveryAttempts(delivery.attempts),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
            ],
          ),
          if (delivery.lastError.isNotEmpty)
            Text(
              delivery.lastError,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            ),
        ],
      ),
    );
  }
}

SeverityLevel _levelOf(AlertSeverity s) => switch (s) {
  AlertSeverity.critical => SeverityLevel.critical,
  AlertSeverity.warning => SeverityLevel.warning,
  AlertSeverity.info => SeverityLevel.info,
  AlertSeverity.unknown => SeverityLevel.unknown,
};

/// Enough digits to be useful and not enough to be noise: a threshold of 0.05
/// and a value of 1234.5 both have to read correctly, and neither should carry
/// zeroes it does not mean -- "0.1400" reads as a precision the rule never had.
String _number(double v) {
  final abs = v.abs();
  final fixed = abs >= 100
      ? v.toStringAsFixed(0)
      : abs >= 1
      ? v.toStringAsFixed(2)
      : v.toStringAsFixed(4);
  if (!fixed.contains('.')) return fixed;
  return fixed
      .replaceFirst(RegExp(r'0+$'), '')
      .replaceFirst(RegExp(r'\.$'), '');
}
