// What the app exists for: what is firing, and taking one of them.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../alerts.dart';
import '../api/schema.g.dart';
import '../session.dart';
import 'account_drawer.dart';
import 'failure_text.dart';

class AlertsScreen extends StatefulWidget {
  const AlertsScreen({super.key, required this.session, required this.alerts});

  final SessionController session;
  final AlertsController alerts;

  @override
  State<AlertsScreen> createState() => _AlertsScreenState();
}

class _AlertsScreenState extends State<AlertsScreen> {
  @override
  void initState() {
    super.initState();
    // Load after the first frame, so the screen appears immediately and the
    // list fills in rather than the app looking stuck on a white screen.
    WidgetsBinding.instance.addPostFrameCallback(
      (_) => widget.alerts.refresh(),
    );
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final alerts = widget.alerts;

    return ListenableBuilder(
      listenable: alerts,
      builder: (context, _) => _scaffold(context, l),
    );
  }

  Widget _scaffold(BuildContext context, L l) {
    final alerts = widget.alerts;
    return Scaffold(
      appBar: AppBar(
        title: Text(l.alertsTitle),
        actions: [
          IconButton(
            key: const Key('refresh'),
            tooltip: l.refresh,
            onPressed: alerts.refresh,
            icon: const Icon(Icons.refresh),
          ),
        ],
      ),
      drawer: AccountDrawer(session: widget.session),
      body: SafeArea(
        child: RefreshIndicator(
          onRefresh: alerts.refresh,
          child: _body(context, l),
        ),
      ),
    );
  }

  Widget _body(BuildContext context, L l) {
    final alerts = widget.alerts;
    if (alerts.loadingFirst) {
      return const Center(child: CircularProgressIndicator());
    }

    final banner = FailureBanner(
      failure: alerts.failure,
      baseUrl: widget.session.baseUrl ?? '',
    );

    if (alerts.incidents.isEmpty) {
      // Always scrollable, or pull-to-refresh has nothing to pull on exactly
      // when the person most wants to check again.
      return ListView(
        padding: const EdgeInsets.all(24),
        children: [
          const SizedBox(height: 48),
          Icon(
            Icons.check_circle_outline,
            size: 48,
            color: Theme.of(context).colorScheme.primary,
          ),
          const SizedBox(height: 16),
          Text(
            l.alertsEmpty,
            textAlign: TextAlign.center,
            style: Theme.of(context).textTheme.titleMedium,
          ),
          const SizedBox(height: 8),
          Text(l.alertsEmptyHint, textAlign: TextAlign.center),
          _resolvedLine(context, l),
          banner,
        ],
      );
    }

    return ListView.builder(
      padding: const EdgeInsets.fromLTRB(12, 12, 12, 24),
      itemCount: alerts.incidents.length + 2,
      itemBuilder: (context, i) {
        if (i == 0) {
          return Padding(
            padding: const EdgeInsets.all(8),
            child: _countsLine(context, l),
          );
        }
        if (i == alerts.incidents.length + 1) {
          return banner;
        }
        return _IncidentCard(
          incident: alerts.incidents[i - 1],
          busy: alerts.acknowledging == alerts.incidents[i - 1].id,
          onAcknowledge: () => alerts.acknowledge(alerts.incidents[i - 1].id),
        );
      },
    );
  }

  Widget _countsLine(BuildContext context, L l) {
    final c = widget.alerts.counts;
    if (c == null) return const SizedBox.shrink();
    return Text(
      l.alertsCounts(c.open, c.acknowledged),
      style: Theme.of(context).textTheme.bodySmall,
    );
  }

  Widget _resolvedLine(BuildContext context, L l) {
    final c = widget.alerts.counts;
    if (c == null || c.resolved == 0) return const SizedBox.shrink();
    // Worth saying on an empty screen: "nothing is firing" reads differently
    // when eleven things fired and recovered today.
    return Padding(
      padding: const EdgeInsets.only(top: 16),
      child: Text(
        l.alertsResolvedRecently(c.resolved),
        textAlign: TextAlign.center,
        style: Theme.of(context).textTheme.bodySmall,
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
              l.alertsOpened(relativeTime(l, incident.openedAt)),
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
    final scheme = Theme.of(context).colorScheme;
    // Severity is semantic, not decorative: critical must read as alarm and
    // warning as caution, at a glance and in any theme. Taking them from the
    // theme's tertiary slot made "warning" come out magenta against a blue
    // seed, which reads as a second kind of critical.
    final dark = Theme.of(context).brightness == Brightness.dark;
    final (label, bg, fg) = switch (severity) {
      AlertSeverity.critical => (
        l.severityCritical,
        dark ? const Color(0xFF5C1A1A) : const Color(0xFFFFDAD6),
        dark ? const Color(0xFFFFB4AB) : const Color(0xFF8C1D18),
      ),
      AlertSeverity.warning => (
        l.severityWarning,
        dark ? const Color(0xFF4A3400) : const Color(0xFFFFE28A),
        dark ? const Color(0xFFFFD166) : const Color(0xFF6B4E00),
      ),
      AlertSeverity.info => (
        l.severityInfo,
        dark ? const Color(0xFF1E3A5F) : const Color(0xFFD8E6FF),
        dark ? const Color(0xFFADC9F5) : const Color(0xFF1B3A66),
      ),
      // A severity this build has never heard of still has to render as
      // something, since the server can be newer than the app.
      AlertSeverity.unknown => (
        l.severityUnknown,
        scheme.surfaceContainerHighest,
        scheme.onSurface,
      ),
    };
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

/// "3 dk önce". Coarse on purpose: the exact second of an alert matters less
/// than how long it has been going, and a phone screen has little room.
String relativeTime(L l, DateTime at) {
  final d = DateTime.now().toUtc().difference(at.toUtc());
  if (d.inMinutes < 1) return l.justNow;
  if (d.inMinutes < 60) return l.minutesAgo(d.inMinutes);
  if (d.inHours < 48) return l.hoursAgo(d.inHours);
  return l.daysAgo(d.inDays);
}
