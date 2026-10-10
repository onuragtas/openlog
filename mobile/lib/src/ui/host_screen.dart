// One host: what it is, how loaded it is, and what the agent found on it.
//
// The list row already says the name and the load. The reason to open this is
// the last part -- what is actually running here -- which no list can show.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../discovery.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'service_screen.dart';
import 'severity.dart';

class HostScreen extends StatefulWidget {
  const HostScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.hostId,
    required this.hostName,
  });

  final SessionController session;
  final Sections sections;
  final String hostId;

  /// Shown while the detail loads, so the screen opens with the name of the
  /// thing that was tapped rather than blank.
  final String hostName;

  @override
  State<HostScreen> createState() => _HostScreenState();
}

class _HostScreenState extends State<HostScreen> {
  late final HostController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.host(widget.hostId);
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);

    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => DetailScreen<Host>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: _c.value?.hostName ?? widget.hostName,
        subtitle: _c.value?.osDescription,
        builder: (context, host) => _body(context, l, host),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, Host host) {
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final u = host.usage;

    String pct(double? v) => v == null ? '-' : '${(v * 100).round()}%';
    Color? hot(double? v) => v != null && v >= 0.9
        ? severityTextColor(context, SeverityLevel.critical)
        : v != null && v >= 0.75
        ? severityTextColor(context, SeverityLevel.warning)
        : null;

    return [
      const SizedBox(height: 4),
      if (u != null)
        Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Expanded(
              child: Stat(
                label: l.hostCpu,
                value: pct(u.cpu),
                color: hot(u.cpu),
              ),
            ),
            Expanded(
              child: Stat(
                label: l.hostMemory,
                value: pct(u.memory),
                color: hot(u.memory),
              ),
            ),
            Expanded(
              child: Stat(
                label: l.hostDisk,
                value: pct(u.disk),
                color: hot(u.disk),
              ),
            ),
            Expanded(
              child: Stat(
                label: l.hostLoad,
                // Load per CPU, not raw load1: 8 means nothing without
                // knowing how many cores the machine has.
                value: u.loadPerCpu?.toStringAsFixed(2) ?? '-',
                color: hot(u.loadPerCpu),
              ),
            ),
          ],
        ),

      const SizedBox(height: 14),
      Wrap(
        spacing: 12,
        runSpacing: 4,
        children: [
          Text(host.arch, style: muted),
          Text(l.hostAgent(host.agentVersion), style: muted),
          Text(relativeTimeOf(l, host.lastSeen), style: muted),
        ],
      ),

      DetailSection(
        title: l.hostRuns,
        children: [
          if (_c.servicesError != null)
            Text(
              l.hostServicesFailed(_c.servicesError!),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            )
          else if (_c.services.isEmpty)
            Text(l.hostNothingFound, style: muted)
          else
            for (final s in _c.services)
              _ServiceRow(instance: s, key: Key('runs-${s.key}')),
        ],
      ),

      // What sent traces from here, which is not the same list as what the
      // infra agent found running: a service can be instrumented without
      // being discovered, and discovered without being instrumented.
      if (_c.apmServices.isNotEmpty)
        DetailSection(
          title: l.hostApmServices,
          children: [
            for (final s in _c.apmServices)
              ListTile(
                key: Key('host-apm-${s.serviceName}'),
                contentPadding: EdgeInsets.zero,
                visualDensity: VisualDensity.compact,
                title: Text(s.serviceName, style: theme.textTheme.bodyMedium),
                subtitle: s.environment.isEmpty
                    ? null
                    : Text(s.environment, style: theme.textTheme.bodySmall),
                trailing: const Icon(Icons.chevron_right, size: 18),
                onTap: () => Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => ServiceScreen(
                      session: widget.session,
                      sections: widget.sections,
                      serviceName: s.serviceName,
                    ),
                  ),
                ),
              ),
          ],
        ),

      if (host.resourceAttributes.isNotEmpty)
        DetailSection(
          title: l.hostAttributes,
          children: [
            for (final e in host.resourceAttributes.entries)
              KeyValue(name: e.key, value: e.value),
          ],
        ),
    ];
  }
}

/// One thing the agent found running, with whatever its integration says.
class _ServiceRow extends StatelessWidget {
  const _ServiceRow({super.key, required this.instance});

  final IntegrationInstance instance;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final integ = instance.service.integration;
    final (label, level) = switch (instance.status) {
      DiscoveredServiceIntegrationStatus.enabled => (
        l.integEnabled,
        SeverityLevel.good,
      ),
      DiscoveredServiceIntegrationStatus.needsConfiguration => (
        l.integNeedsConfig,
        SeverityLevel.warning,
      ),
      DiscoveredServiceIntegrationStatus.error => (
        l.integError,
        SeverityLevel.critical,
      ),
      _ => (l.integNotAvailable, SeverityLevel.unknown),
    };

    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  instance.name,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              const SizedBox(width: 8),
              // Only where an integration exists at all: "not available" on
              // every ordinary process would be noise on a busy host.
              if (integ != null) Tag(label: label, level: level),
            ],
          ),
          if (instance.service.version != null)
            Text(
              instance.service.version!,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          if (integ?.error != null && integ!.error!.isNotEmpty)
            Text(
              integ.error!,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            ),
        ],
      ),
    );
  }
}
