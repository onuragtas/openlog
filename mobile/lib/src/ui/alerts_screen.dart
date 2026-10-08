// What the app exists for: what is firing, and taking one of them.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../alerts.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'incident_screen.dart';
import 'list_scaffold.dart';
import 'severity.dart';
import 'theme.dart';

class AlertsBody extends StatefulWidget {
  const AlertsBody({
    super.key,
    required this.session,
    required this.sections,
    required this.alerts,
  });

  final SessionController session;

  /// Needed to open an incident: the detail screen makes its own controller,
  /// and a test hands this app a scripted one through the same door.
  final Sections sections;
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
          onOpen: () => _open(context, c.items[i]),
        ),
      ),
    );
  }

  /// Opens the incident, then reloads the list on the way back: the person may
  /// have acknowledged or resolved it on the detail screen, and a row that still
  /// says "open" after they closed it is worse than a moment's spinner.
  Future<void> _open(BuildContext context, AlertIncident incident) async {
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => IncidentScreen(
          session: widget.session,
          sections: widget.sections,
          incidentId: incident.id,
          ruleName: incident.ruleName,
        ),
      ),
    );
    await widget.alerts.refresh();
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
    required this.onOpen,
  });

  final AlertIncident incident;
  final bool busy;
  final VoidCallback onAcknowledge;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;
    final open = incident.state == AlertIncidentState.open;

    return Card(
      key: Key('incident-${incident.id}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: InkWell(
        onTap: onOpen,
        borderRadius: BorderRadius.circular(Radii.lg),
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
                  SeverityChip(severity: incident.severity),
                  if (incident.muted) OutlineTag(label: l.alertsMuted),
                  if (incident.flapping) OutlineTag(label: l.alertsFlapping),
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
                  style: text.bodySmall?.copyWith(
                    color: scheme.onSurfaceVariant,
                  ),
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
      ),
    );
  }
}
