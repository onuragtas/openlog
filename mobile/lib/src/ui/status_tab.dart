// The public status page of this installation: how each component is doing,
// what its last ninety days looked like, and what is open right now.
//
// It needs no authentication and is the one screen worth having on a phone
// when nothing else answers — "is it them or is it me" is asked away from a
// desk more often than at one.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../session.dart';
import '../status.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart' show relativeTimeOf;
import 'severity.dart';
import 'theme.dart';

/// A status in this app's language, and the colour it is read in.
(String, SeverityLevel) statusLook(L l, StatusValue v) => switch (v) {
  StatusValue.operational => (l.statusOperational, SeverityLevel.good),
  StatusValue.degraded => (l.statusDegraded, SeverityLevel.warning),
  StatusValue.partialOutage => (l.statusPartial, SeverityLevel.warning),
  StatusValue.majorOutage => (l.statusMajor, SeverityLevel.critical),
  StatusValue.maintenance => (l.statusMaintenance, SeverityLevel.info),
  _ => (l.statusUnknown, SeverityLevel.unknown),
};

String componentName(L l, StatusPageComponentsItemId id) => switch (id) {
  StatusPageComponentsItemId.ingest => l.statusComponentIngest,
  StatusPageComponentsItemId.queryApi => l.statusComponentQueryApi,
  StatusPageComponentsItemId.alerting => l.statusComponentAlerting,
  StatusPageComponentsItemId.processing => l.statusComponentProcessing,
  _ => id.wire,
};

class StatusTab extends StatefulWidget {
  const StatusTab({
    super.key,
    required this.session,
    required this.controller,
    required this.active,
  });

  final SessionController session;
  final StatusPageController controller;
  final bool active;

  @override
  State<StatusTab> createState() => _StatusTabState();
}

class _StatusTabState extends State<StatusTab> {
  @override
  void initState() {
    super.initState();
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(StatusTab old) {
    super.didUpdateWidget(old);
    _loadIfVisible();
  }

  void _loadIfVisible() {
    final c = widget.controller;
    if (!widget.active || c.loaded || c.loadingFirst) return;
    WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        // Turned off is not an outage, and the screen says which.
        if (c.unavailable) {
          return Padding(
            padding: const EdgeInsets.all(16),
            child: Text(
              l.statusUnavailable,
              style: Theme.of(context).textTheme.bodyMedium,
            ),
          );
        }
        return DetailBody<StatusPage>(
          controller: c,
          baseUrl: widget.session.baseUrl ?? '',
          builder: (context, v) => _body(context, l, v),
        );
      },
    );
  }

  List<Widget> _body(BuildContext context, L l, StatusPage v) {
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final (label, level) = statusLook(l, v.status);

    return [
      DetailSection(
        title: l.statusOverall,
        children: [
          Wrap(
            spacing: 8,
            runSpacing: 6,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Tag(label: label, level: level),
              if (v.checkedAt != null && v.checkedAt!.isNotEmpty)
                Text(l.statusCheckedAt(_when(l, v.checkedAt!)), style: muted),
            ],
          ),
        ],
      ),
      DetailSection(
        title: l.statusComponents,
        children: [
          for (final c in v.components)
            _Component(component: c, key: Key('status-${c.id.wire}')),
        ],
      ),
      DetailSection(
        title: l.statusIncidents,
        children: [
          if (v.incidents.isEmpty && v.maintenance.isEmpty)
            Text(l.statusNoIncidents, style: muted)
          else ...[
            for (final i in v.incidents)
              _Incident(incident: i, key: Key('incident-${i.id}')),
            for (final m in v.maintenance)
              _Incident(incident: m, key: Key('maintenance-${m.id}')),
          ],
        ],
      ),
    ];
  }

  String _when(L l, String ts) {
    final at = DateTime.tryParse(ts);
    return at == null ? ts : relativeTimeOf(l, at);
  }
}

class _Component extends StatelessWidget {
  const _Component({super.key, required this.component});

  final StatusPageComponentsItem component;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final (label, level) = statusLook(l, component.status);
    final uptime = component.uptime90d;

    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  componentName(l, component.id),
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              if (uptime != null)
                Text(
                  l.statusUptime(uptime.toStringAsFixed(2)),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              const SizedBox(width: 8),
              Tag(label: label, level: level),
            ],
          ),
          const SizedBox(height: 6),
          _History(days: component.days),
        ],
      ),
    );
  }
}

/// Ninety days as ninety strips, the way every status page draws them: the
/// shape of a bad week is visible where ninety numbers would not be.
class _History extends StatelessWidget {
  const _History({required this.days});

  final List<StatusPageComponentsItemDaysItem> days;

  @override
  Widget build(BuildContext context) {
    if (days.isEmpty) return const SizedBox.shrink();
    final colors = colorsOf(context);
    // The day strips have their own four values, coarser than a component's.
    Color colorOf(StatusPageComponentsItemDaysItemStatus v) => switch (v) {
      StatusPageComponentsItemDaysItemStatus.operational => colors.successText,
      StatusPageComponentsItemDaysItemStatus.degraded => colors.warningText,
      StatusPageComponentsItemDaysItemStatus.outage => colors.destructiveText,
      _ => colors.muted,
    };

    return SizedBox(
      height: 18,
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          for (final d in days)
            Expanded(
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: 0.5),
                child: ClipRRect(
                  borderRadius: BorderRadius.circular(1),
                  child: ColoredBox(color: colorOf(d.status)),
                ),
              ),
            ),
        ],
      ),
    );
  }
}

/// "3 days ago" from the contract's RFC3339 text.
String _since(L l, String ts) {
  final at = DateTime.tryParse(ts);
  return at == null ? ts : relativeTimeOf(l, at);
}

class _Incident extends StatelessWidget {
  const _Incident({super.key, required this.incident});

  final StatusIncident incident;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(incident.title, style: theme.textTheme.bodyMedium),
          Text(
            [
              incident.status.wire,
              incident.impact.wire,
              _since(l, incident.startsAt),
            ].where((p) => p.isNotEmpty).join(' · '),
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ],
      ),
    );
  }
}
