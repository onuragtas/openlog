// The agent fleet: how far behind it is, and which host is where.
//
// Read-only. Changing a rollout policy is a fleet-wide action with a blast
// radius, and a phone in a pocket is the wrong place for the button that
// starts one -- so this screen says where that is done instead.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'sections_screen.dart';
import 'severity.dart';

class FleetBody extends StatelessWidget {
  const FleetBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  Widget build(BuildContext context) {
    final c = sections.fleet;

    return SectionBody<FleetHost>(
      session: session,
      controller: c,
      searchKey: 'fleet-search',
      active: active,
      emptyTitle: (l) => l.fleetEmpty,
      header: ListenableBuilder(
        listenable: c,
        builder: (context, _) {
          final s = c.summary;
          return s == null ? const SizedBox.shrink() : _Summary(summary: s);
        },
      ),
      card: (context, host) => _HostRow(host: host),
    );
  }
}

class _Summary extends StatelessWidget {
  const _Summary({required this.summary});

  final FleetSummary summary;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final latest = summary.latest.stable?.version ?? '';

    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(
                child: Stat(
                  label: l.fleetAgents,
                  value: '${summary.activeHosts}',
                ),
              ),
              Expanded(
                child: Stat(
                  label: l.fleetOutdated,
                  value: '${summary.outdated}',
                  color: summary.outdated > 0
                      ? severityTextColor(context, SeverityLevel.warning)
                      : null,
                ),
              ),
              Expanded(
                child: Stat(
                  label: l.fleetInProgress,
                  value: '${summary.inProgress}',
                ),
              ),
              Expanded(
                child: Stat(
                  label: l.fleetFailed,
                  value: '${summary.failed}',
                  color: summary.failed > 0
                      ? severityTextColor(context, SeverityLevel.critical)
                      : null,
                ),
              ),
            ],
          ),
          const SizedBox(height: 8),
          // Without a catalogue the server has nothing to compare against, so
          // "0 outdated" above would mean "unknown", not "all up to date".
          if (summary.catalog.status != FleetCatalogStatusStatus.ok)
            Text(
              l.fleetNoCatalog,
              style: theme.textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, SeverityLevel.warning),
              ),
            )
          else if (latest.isNotEmpty)
            Text(l.fleetLatest(latest), style: muted),
          Text(l.fleetReadOnly, style: muted),
        ],
      ),
    );
  }
}

class _HostRow extends StatelessWidget {
  const _HostRow({required this.host});

  final FleetHost host;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final updating =
        host.update.state.isNotEmpty && host.update.state != 'idle';

    return Padding(
      key: Key('fleet-${host.hostId}'),
      padding: const EdgeInsets.symmetric(vertical: 8, horizontal: 4),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  host.hostName.isEmpty ? host.hostId : host.hostName,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              const SizedBox(width: 8),
              Text(
                host.agent.version,
                style: theme.textTheme.bodyMedium?.copyWith(
                  fontWeight: FontWeight.w600,
                  color: host.outdated
                      ? severityTextColor(context, SeverityLevel.warning)
                      : null,
                ),
              ),
            ],
          ),
          const SizedBox(height: 2),
          Wrap(
            spacing: 8,
            runSpacing: 4,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Text('${host.agent.os}/${host.agent.arch}', style: muted),
              Text(host.agent.installMethod, style: muted),
              if (!host.supported)
                Tag(label: l.fleetUnsupported, level: SeverityLevel.critical),
              if (updating)
                Text(
                  l.fleetStatePrefix(_stateWord(l, host.update.state)),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: host.update.state == 'failed'
                        ? severityTextColor(context, SeverityLevel.critical)
                        : theme.colorScheme.primary,
                  ),
                ),
              Text(relativeTimeOf(l, host.lastSyncAt), style: muted),
            ],
          ),
          if (host.update.error.isNotEmpty)
            Text(
              host.update.error,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            ),
        ],
      ),
    );
  }
}

/// The nine states the contract names, in the person's language.
///
/// Anything else is shown as the server wrote it: the contract says unknown
/// values are passed through, which means a newer server can invent one, and
/// its own word beats this build's "unknown".
String _stateWord(L l, String state) => switch (state) {
  'idle' => l.fleetStateIdle,
  'downloading' => l.fleetStateDownloading,
  'verifying' => l.fleetStateVerifying,
  'staged' => l.fleetStateStaged,
  'restarting' => l.fleetStateRestarting,
  'confirming' => l.fleetStateConfirming,
  'succeeded' => l.fleetStateSucceeded,
  'failed' => l.fleetStateFailed,
  'rolled_back' => l.fleetStateRolledBack,
  _ => state,
};
