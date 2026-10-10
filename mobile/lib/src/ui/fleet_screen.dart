// The agent fleet: how far behind it is, what is rolling out, and which
// host is where.
//
// The web's rollout panel and its policy are here too, because the two
// questions a phone is actually used for are "is the rollout hurting
// anything" and "stop it". Both are fleet-wide actions, so both ask
// first, and both are for admins -- the server says so and so does the
// screen. Writing a whole policy (waves, windows, agent versions) stays
// on the web; the mode, which is the switch that stops everything, does
// not.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../roles.dart';
import '../sections.dart';
import '../session.dart';
import 'failure_text.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'sections_screen.dart';
import 'severity.dart';
import 'theme.dart';

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
          if (s == null) return const SizedBox.shrink();
          final canManage = can(session.me?.role, 'fleet.manage');
          return Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              _Summary(summary: s),
              _Rollout(
                session: session,
                controller: c,
                summary: s,
                canManage: canManage,
              ),
              _Policy(controller: c, canManage: canManage),
              const SizedBox(height: 10),
            ],
          );
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

/// What is rolling out right now: which wave, how it is going, and the
/// buttons that stop or finish it.
class _Rollout extends StatelessWidget {
  const _Rollout({
    required this.session,
    required this.controller,
    required this.summary,
    required this.canManage,
  });

  final SessionController session;
  final FleetController controller;
  final FleetSummary summary;
  final bool canManage;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final r = summary.currentRollout;
    final running = [for (final v in summary.versions) v.version];
    final candidates = rollbackCandidates(
      running,
      r?.toVersion ?? summary.target?.version,
    );

    return Padding(
      padding: const EdgeInsets.only(top: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(l.fleetRollout, style: theme.textTheme.titleSmall),
          FailureBanner(
            failure: controller.failure,
            baseUrl: session.baseUrl ?? '',
          ),
          if (r == null)
            Text(
              l.fleetNoRollout,
              key: const Key('fleet-no-rollout'),
              style: muted,
            )
          else
            _RolloutDetails(rollout: r),
          if (canManage) ...[
            const SizedBox(height: 8),
            Wrap(
              spacing: 8,
              runSpacing: 4,
              children: [
                if (r != null && r.state == FleetRolloutState.active)
                  _Action(
                    actionKey: const Key('fleet-pause'),
                    label: l.fleetPause,
                    confirm: l.fleetConfirmPause,
                    busy: controller.acting,
                    onConfirm: () => controller.act(
                      () => controller.client.pauseRollout(r.id),
                    ),
                  ),
                if (r != null &&
                    r.state == FleetRolloutState.active &&
                    r.currentWave < r.waves.length - 1)
                  _Action(
                    actionKey: const Key('fleet-deploy-now'),
                    label: l.fleetDeployNow,
                    // The rest of the fleet at once, so it says so.
                    confirm: l.fleetConfirmDeployNow,
                    busy: controller.acting,
                    onConfirm: () => controller.act(
                      () => controller.client.deployRolloutNow(r.id),
                    ),
                  ),
                if (r != null &&
                    (r.state == FleetRolloutState.paused ||
                        r.state == FleetRolloutState.halted))
                  _Action(
                    actionKey: const Key('fleet-resume'),
                    label: l.fleetResume,
                    confirm: l.fleetConfirmResume,
                    busy: controller.acting,
                    onConfirm: () => controller.act(
                      () => controller.client.resumeRollout(r.id),
                    ),
                  ),
                if (candidates.isNotEmpty)
                  _Action(
                    actionKey: const Key('fleet-rollback'),
                    label: l.fleetRollback(candidates.first),
                    confirm: l.fleetConfirmRollback,
                    destructive: true,
                    busy: controller.acting,
                    onConfirm: () => controller.act(
                      () => controller.client.rollbackFleet(candidates.first),
                    ),
                  ),
              ],
            ),
          ],
          if (controller.rollouts.length > 1) ...[
            const SizedBox(height: 10),
            Text(l.fleetHistory, style: muted),
            for (final past in controller.rollouts.skip(1).take(5))
              Padding(
                key: Key('fleet-rollout-${past.id}'),
                padding: const EdgeInsets.symmetric(vertical: 3),
                child: Text(
                  '${_rolloutName(l, past)} · ${_stateLabel(l, past.state)} · '
                  '${relativeTimeOf(l, past.createdAt)}',
                  style: muted,
                ),
              ),
          ],
        ],
      ),
    );
  }
}

class _RolloutDetails extends StatelessWidget {
  const _RolloutDetails({required this.rollout});

  final FleetRollout rollout;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final r = rollout;
    final c = r.counters;
    final done = c.succeeded + c.failed + c.rolledBack;
    final total = done + c.pending;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Expanded(
              child: Text(_rolloutName(l, r), style: theme.textTheme.bodyLarge),
            ),
            Tag(
              label: _stateLabel(l, r.state),
              level: switch (r.state) {
                FleetRolloutState.active => SeverityLevel.info,
                FleetRolloutState.paused => SeverityLevel.warning,
                FleetRolloutState.halted => SeverityLevel.critical,
                _ => SeverityLevel.unknown,
              },
            ),
          ],
        ),
        Text(
          // Which wave, and how big it is: a rollout at 10% of the fleet
          // is a different thing from the same rollout at 100%.
          l.fleetWave(r.currentWave + 1, r.waves.length, r.wavePercent),
          style: muted,
        ),
        if (r.stateReason.isNotEmpty)
          Text(
            r.stateReason,
            style: theme.textTheme.bodySmall?.copyWith(
              color: severityTextColor(context, SeverityLevel.warning),
            ),
          ),
        const SizedBox(height: 6),
        if (total > 0) ...[
          // Three shares, not one bar: with seven agents updated and one
          // failed, a bar that turns red says the rollout failed.
          ClipRRect(
            borderRadius: BorderRadius.circular(3),
            child: SizedBox(
              height: 6,
              child: Row(
                key: const Key('fleet-progress'),
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  if (c.succeeded > 0)
                    Expanded(
                      flex: c.succeeded,
                      child: ColoredBox(color: theme.colorScheme.primary),
                    ),
                  if (c.failed + c.rolledBack > 0)
                    Expanded(
                      flex: c.failed + c.rolledBack,
                      child: ColoredBox(
                        color: severityTextColor(
                          context,
                          SeverityLevel.critical,
                        ),
                      ),
                    ),
                  if (c.pending > 0)
                    Expanded(
                      flex: c.pending,
                      child: ColoredBox(
                        color: theme.colorScheme.surfaceContainerHighest,
                      ),
                    ),
                ],
              ),
            ),
          ),
          const SizedBox(height: 4),
          Text(
            [
              l.fleetSucceeded(c.succeeded),
              if (c.failed + c.rolledBack > 0)
                l.fleetFailedCount(c.failed + c.rolledBack),
              l.fleetPending(c.pending),
              if (r.nextWaveAt != null)
                l.fleetNextWave(relativeTimeOf(l, r.nextWaveAt!)),
            ].join(' · '),
            style: muted,
          ),
        ],
      ],
    );
  }
}

/// How the fleet updates itself. The mode is a switch here; the rest is
/// read, because waves and maintenance windows are a form, not a tap.
class _Policy extends StatelessWidget {
  const _Policy({required this.controller, required this.canManage});

  final FleetController controller;
  final bool canManage;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final p = controller.policy;
    if (p == null) return const SizedBox.shrink();

    return Padding(
      padding: const EdgeInsets.only(top: 14),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(l.fleetPolicy, style: theme.textTheme.titleSmall),
          const SizedBox(height: 6),
          if (canManage)
            SegmentedButton<String>(
              key: const Key('fleet-mode'),
              showSelectedIcon: false,
              segments: [
                ButtonSegment(value: 'off', label: Text(l.fleetModeOff)),
                ButtonSegment(value: 'notify', label: Text(l.fleetModeNotify)),
                ButtonSegment(value: 'auto', label: Text(l.fleetModeAuto)),
              ],
              selected: {p.mode.wire},
              onSelectionChanged: controller.acting
                  ? null
                  : (set) => controller.setMode(set.first),
            )
          else
            Text(_modeLabel(l, p.mode), style: theme.textTheme.bodyMedium),
          const SizedBox(height: 6),
          Text(
            [
              p.channel.wire,
              p.pinnedVersion == null
                  ? p.target.wire
                  : l.fleetPinned(p.pinnedVersion!),
              l.fleetWaves(p.waves.join('%, ')),
              l.fleetSoak(p.waveSoakMinutes),
              l.fleetHalt((p.haltFailureRate * 100).round()),
            ].join(' · '),
            style: muted,
          ),
          if (p.maintenanceWindows.isNotEmpty)
            Text(l.fleetWindows(p.maintenanceWindows.length), style: muted),
          Text(l.fleetPolicyOnWeb, style: muted),
        ],
      ),
    );
  }
}

/// A button that asks before it does something to the whole fleet.
class _Action extends StatefulWidget {
  const _Action({
    required this.actionKey,
    required this.label,
    required this.confirm,
    required this.busy,
    required this.onConfirm,
    this.destructive = false,
  });

  final Key actionKey;
  final String label;
  final String confirm;
  final bool busy;
  final bool destructive;
  final VoidCallback onConfirm;

  @override
  State<_Action> createState() => _ActionState();
}

class _ActionState extends State<_Action> {
  bool _asking = false;

  @override
  Widget build(BuildContext context) {
    final colors = colorsOf(context);
    if (!_asking) {
      return OutlinedButton(
        key: widget.actionKey,
        onPressed: widget.busy ? null : () => setState(() => _asking = true),
        child: Text(
          widget.label,
          style: widget.destructive
              ? TextStyle(color: colors.destructiveText)
              : null,
        ),
      );
    }
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        FilledButton(
          key: Key('${(widget.actionKey as ValueKey<String>).value}-confirm'),
          onPressed: widget.busy
              ? null
              : () {
                  setState(() => _asking = false);
                  widget.onConfirm();
                },
          child: Text(widget.confirm),
        ),
        TextButton(
          onPressed: () => setState(() => _asking = false),
          child: Text(L.of(context).cancel),
        ),
      ],
    );
  }
}

String _rolloutName(L l, FleetRollout r) {
  if (r.toVersion == null) return l.fleetRolloutPatch;
  return r.action == FleetRolloutAction.rollback
      ? l.fleetRolloutRollback(r.toVersion!)
      : l.fleetRolloutUpgrade(r.toVersion!);
}

String _stateLabel(L l, FleetRolloutState state) => switch (state) {
  FleetRolloutState.active => l.fleetRolloutActive,
  FleetRolloutState.paused => l.fleetRolloutPaused,
  FleetRolloutState.halted => l.fleetRolloutHalted,
  FleetRolloutState.completed => l.fleetRolloutCompleted,
  FleetRolloutState.superseded => l.fleetRolloutSuperseded,
  _ => state.wire,
};

String _modeLabel(L l, FleetMode mode) => switch (mode) {
  FleetMode.off => l.fleetModeOff,
  FleetMode.notify => l.fleetModeNotify,
  FleetMode.auto => l.fleetModeAuto,
  _ => mode.wire,
};
