// What the app exists for: what is firing, and taking one of them.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../alerts.dart';
import '../api/schema.g.dart';
import '../session.dart';
import 'list_scaffold.dart';
import 'severity.dart';

class AlertsBody extends StatefulWidget {
  const AlertsBody({super.key, required this.session, required this.alerts});

  final SessionController session;
  final AlertsController alerts;

  @override
  State<AlertsBody> createState() => _AlertsBodyState();
}

class _AlertsBodyState extends State<AlertsBody> {
  @override
  void initState() {
    super.initState();
    // Load after the first frame, so the screen appears immediately and the
    // list fills in rather than the app looking stuck.
    WidgetsBinding.instance.addPostFrameCallback(
      (_) => widget.alerts.refresh(),
    );
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.alerts;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListScreen(
        controller: c,
        baseUrl: widget.session.baseUrl ?? '',
        emptyTitle: l.alertsEmpty,
        header: _header(context, l),
        itemBuilder: (context, i) => _IncidentCard(
          incident: c.items[i],
          busy: c.acknowledging == c.items[i].id,
          onAcknowledge: () => c.acknowledge(c.items[i].id),
        ),
      ),
    );
  }

  Widget _header(BuildContext context, L l) {
    final counts = widget.alerts.counts;
    if (counts == null) return const SizedBox.shrink();
    final text = Theme.of(context).textTheme;
    return Padding(
      padding: const EdgeInsets.only(bottom: 4, left: 4),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (widget.alerts.items.isNotEmpty)
            Text(
              l.alertsCounts(counts.open, counts.acknowledged),
              style: text.bodySmall,
            )
          else ...[
            Text(
              l.alertsEmptyHint,
              textAlign: TextAlign.center,
              style: text.bodyMedium,
            ),
            // Worth saying on an empty screen: "nothing is firing" reads
            // differently when eleven things fired and recovered today.
            if (counts.resolved > 0)
              Padding(
                padding: const EdgeInsets.only(top: 8),
                child: Text(
                  l.alertsResolvedRecently(counts.resolved),
                  style: text.bodySmall,
                ),
              ),
          ],
        ],
      ),
    );
  }
}

class _IncidentCard extends StatelessWidget {
  const _IncidentCard({
    required this.incident,
    required this.busy,
    required this.onAcknowledge,
  });

  final AlertIncident incident;
  final bool busy;
  final VoidCallback onAcknowledge;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;
    final open = incident.state == AlertIncidentState.open;

    return Card(
      key: Key('incident-${incident.id}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Wrap(
              spacing: 8,
              runSpacing: 4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                _SeverityChip(severity: incident.severity),
                if (incident.muted) _Tag(label: l.alertsMuted),
                if (incident.flapping) _Tag(label: l.alertsFlapping),
              ],
            ),
            const SizedBox(height: 8),
            Text(incident.ruleName, style: text.titleMedium),
            if (incident.summary.isNotEmpty) ...[
              const SizedBox(height: 4),
              Text(incident.summary, style: text.bodyMedium),
            ],
            if (incident.seriesKey.isNotEmpty) ...[
              const SizedBox(height: 4),
              Text(
                incident.seriesKey,
                style: text.bodySmall?.copyWith(color: scheme.onSurfaceVariant),
              ),
            ],
            const SizedBox(height: 10),
            Text(
              l.alertsOpened(relativeTimeOf(l, incident.openedAt)),
              style: text.bodySmall?.copyWith(color: scheme.onSurfaceVariant),
            ),
            const SizedBox(height: 6),
            if (open)
              Align(
                alignment: Alignment.centerRight,
                child: FilledButton.tonal(
                  key: Key('ack-${incident.id}'),
                  onPressed: busy ? null : onAcknowledge,
                  child: busy
                      ? const SizedBox(
                          width: 16,
                          height: 16,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : Text(l.alertsAcknowledge),
                ),
              )
            else
              Text(
                incident.acknowledgedByEmail == null
                    ? l.alertsAcknowledgedUnknown
                    : l.alertsAcknowledgedBy(incident.acknowledgedByEmail!),
                style: text.bodySmall?.copyWith(color: scheme.primary),
              ),
          ],
        ),
      ),
    );
  }
}

class _SeverityChip extends StatelessWidget {
  const _SeverityChip({required this.severity});

  final AlertSeverity severity;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final (label, level) = switch (severity) {
      AlertSeverity.critical => (l.severityCritical, SeverityLevel.critical),
      AlertSeverity.warning => (l.severityWarning, SeverityLevel.warning),
      AlertSeverity.info => (l.severityInfo, SeverityLevel.info),
      // A severity this build has never heard of still has to render as
      // something, since the server can be newer than the app.
      AlertSeverity.unknown => (l.severityUnknown, SeverityLevel.unknown),
    };
    final colors = severityChipColors(context, level);
    final bg = colors.background;
    final fg = colors.foreground;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
      decoration: BoxDecoration(
        color: bg,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Text(
        label,
        style: TextStyle(color: fg, fontWeight: FontWeight.w600, fontSize: 12),
      ),
    );
  }
}

class _Tag extends StatelessWidget {
  const _Tag({required this.label});

  final String label;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      decoration: BoxDecoration(
        border: Border.all(color: scheme.outlineVariant),
        borderRadius: BorderRadius.circular(10),
      ),
      child: Text(
        label,
        style: TextStyle(color: scheme.onSurfaceVariant, fontSize: 11),
      ),
    );
  }
}
