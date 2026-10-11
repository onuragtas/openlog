// What this installation runs, and what the updater is doing about it.
//
// The web's "Version and updates" section, on a phone: the running build, the
// release available, the updater's state with its notices and steps, the
// maintenance window, the last request, and the two buttons an operator has
// — check now, update now.
//
// The notices are the point of carrying this screen at all. "The compose
// files of 0.1.113 change clickhouse in ways the updater does not apply" is
// something an operator finds out hours later otherwise.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../session.dart';
import '../version.dart';
import 'detail_scaffold.dart';
import 'failure_text.dart';
import 'list_scaffold.dart' show relativeTimeOf;
import 'severity.dart';

/// The updater's fixed messages in this app's language.
///
/// The server sends a stable code and the parameters beside the English
/// sentence; where this build knows the code it says it in Turkish, and
/// where it does not — a newer server, a free-text error — it shows what the
/// server sent rather than nothing.
String updateMessage(
  L l,
  String fallback,
  String? code,
  Map<String, String>? params,
) {
  final template = _template(l, code ?? '');
  if (template == null) return fallback;
  var out = template;
  for (final e in (params ?? const <String, String>{}).entries) {
    out = out.replaceAll('%${e.key}%', e.value);
  }
  // A placeholder the server did not fill would read as "%version%"; the
  // English sentence is better than that.
  return out.contains('%') ? fallback : out;
}

/// An updater state in this app's language, or the raw state for one this
/// build has not heard of -- the server can be newer than the app.
String updateStateLabel(L l, UpdaterStatusState state) => switch (state) {
  UpdaterStatusState.off => l.updateStateOff,
  UpdaterStatusState.error => l.updateStateError,
  UpdaterStatusState.upToDate => l.updateStateUpToDate,
  UpdaterStatusState.available => l.updateStateAvailable,
  UpdaterStatusState.waitingForMaintenanceWindow =>
    l.updateStateWaitingForWindow,
  UpdaterStatusState.updating => l.updateStateUpdating,
  UpdaterStatusState.succeeded => l.updateStateSucceeded,
  UpdaterStatusState.failed => l.updateStateFailed,
  UpdaterStatusState.rolledBack => l.updateStateRolledBack,
  UpdaterStatusState.rollbackFailed => l.updateStateRollbackFailed,
  _ => state.wire,
};

/// A step of an update run, by the name the updater gives it.
String updateStepLabel(L l, String name) => switch (name) {
  'self-update' => l.updateStepSelfUpdate,
  'backup' => l.updateStepBackup,
  'pull' => l.updateStepPull,
  'compose-bundle' => l.updateStepComposeBundle,
  'migrate' => l.updateStepMigrate,
  'recreate' => l.updateStepRecreate,
  'rollout' => l.updateStepRollout,
  'health' => l.updateStepHealth,
  'rollback' => l.updateStepRollback,
  'cleanup' => l.updateStepCleanup,
  'contract-migrate' => l.updateStepContractMigrate,
  _ => name,
};

String? _template(L l, String code) => switch (code) {
  'mode_off' => l.updateMsgModeOff,
  'update_available' => l.updateMsgUpdateAvailable,
  'update_available_notify' => l.updateMsgUpdateAvailableNotify,
  'waiting_for_window' => l.updateMsgWaitingForWindow,
  'updated' => l.updateMsgUpdated,
  'rolled_back' => l.updateMsgRolledBack,
  'rollback_failed' => l.updateMsgRollbackFailed,
  'failed_before_change' => l.updateMsgFailedBeforeChange,
  'up_to_date_newest' => l.updateMsgUpToDateNewest,
  'up_to_date_no_eligible' => l.updateMsgUpToDateNoEligible,
  'request_mode_off' => l.updateMsgRequestModeOff,
  'request_not_installable' => l.updateMsgRequestNotInstallable,
  'request_target_changed' => l.updateMsgRequestTargetChanged,
  'request_outside_window' => l.updateMsgRequestOutsideWindow,
  'request_interrupted' => l.updateMsgRequestInterrupted,
  'request_expired' => l.updateMsgRequestExpired,
  'compose_outdated' => l.updateMsgComposeOutdated,
  'compose_outdated_bundle' => l.updateMsgComposeOutdatedBundle,
  'compose_changes_pending' => l.updateMsgComposeChangesPending,
  'updater_outdated' => l.updateMsgUpdaterOutdated,
  'updater_outdated_bundle' => l.updateMsgUpdaterOutdatedBundle,
  'updater_outdated_kubernetes' => l.updateMsgUpdaterOutdatedKubernetes,
  'updater_self_update_failed' => l.updateMsgUpdaterSelfUpdateFailed,
  _ => null,
};

class VersionTab extends StatefulWidget {
  const VersionTab({
    super.key,
    required this.session,
    required this.controller,
    required this.active,
  });

  final SessionController session;
  final VersionController controller;

  /// Whether this is the tab on screen, so it loads when it is first looked
  /// at rather than when the settings screen is built.
  final bool active;

  @override
  State<VersionTab> createState() => _VersionTabState();
}

class _VersionTabState extends State<VersionTab> {
  @override
  void initState() {
    super.initState();
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(VersionTab old) {
    super.didUpdateWidget(old);
    _loadIfVisible();
  }

  void _loadIfVisible() {
    final c = widget.controller;
    if (!widget.active || c.loaded || c.loadingFirst) return;
    WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
  }

  Future<void> _confirmAndApply(String target, VersionInfo v) async {
    final l = L.of(context);
    final window = v.updater?.maintenanceWindow;
    var ignore = false;
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => StatefulBuilder(
        builder: (context, setDialog) => AlertDialog(
          key: const Key('version-confirm'),
          title: Text(l.versionConfirmTitle(target)),
          content: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(l.versionConfirmBody),
              // Only where there is a window to be outside of: the checkbox
              // means nothing on an installation that updates at any time.
              if (window != null && window.spec.isNotEmpty) ...[
                const SizedBox(height: 10),
                CheckboxListTile(
                  key: const Key('version-ignore-window'),
                  contentPadding: EdgeInsets.zero,
                  controlAffinity: ListTileControlAffinity.leading,
                  value: ignore,
                  title: Text(l.versionIgnoreWindow),
                  onChanged: (on) => setDialog(() => ignore = on ?? false),
                ),
              ],
            ],
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.of(context).pop(false),
              child: Text(l.cancel),
            ),
            FilledButton(
              key: const Key('version-confirm-apply'),
              onPressed: () => Navigator.of(context).pop(true),
              child: Text(l.versionConfirm),
            ),
          ],
        ),
      ),
    );
    if (ok != true || !mounted) return;
    final messenger = ScaffoldMessenger.of(context);
    await widget.controller.applyNow(target, ignoreMaintenanceWindow: ignore);
    if (!mounted || widget.controller.requestFailure != null) return;
    messenger.showSnackBar(
      SnackBar(
        key: const Key('version-queued'),
        content: Text(l.versionQueued),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => DetailBody<VersionInfo>(
        controller: c,
        baseUrl: widget.session.baseUrl ?? '',
        builder: (context, v) => _body(context, l, v),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, VersionInfo v) {
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final c = widget.controller;
    final target = updateTarget(v);

    return [
      DetailSection(
        title: l.versionRunning,
        children: [
          Text(v.version, style: theme.textTheme.titleMedium),
          Text(
            [
              if (v.commit.isNotEmpty && v.commit != 'unknown') v.commit,
              if (_built(l, v.date) != null) _built(l, v.date)!,
            ].join(' · '),
            style: muted,
          ),
          const SizedBox(height: 10),
          Wrap(
            spacing: 8,
            runSpacing: 6,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Text('${l.versionCheck}:', style: muted),
              Tag(
                label: switch (v.updateCheck) {
                  VersionInfoUpdateCheck.enabled => l.versionCheckEnabled,
                  VersionInfoUpdateCheck.disabled => l.versionCheckDisabled,
                  _ => l.versionCheckFailed,
                },
                level: switch (v.updateCheck) {
                  VersionInfoUpdateCheck.enabled => SeverityLevel.good,
                  VersionInfoUpdateCheck.disabled => SeverityLevel.unknown,
                  _ => SeverityLevel.critical,
                },
              ),
            ],
          ),
          const SizedBox(height: 8),
          if (v.latestAvailable != null)
            KeyValue(
              name: l.versionLatest,
              value:
                  '${v.latestAvailable!.version} · '
                  '${l.versionChecked} ${relativeTimeOf(l, v.latestAvailable!.checkedAt)}',
            )
          else
            KeyValue(name: l.versionLatest, value: l.versionUpToDate),
        ],
      ),
      _updater(context, l, v, muted),
      if (v.updateRequests?.latest != null)
        _request(context, l, v.updateRequests!.latest!, muted),
      if (canRequestUpdates(v)) ...[
        const SizedBox(height: 18),
        FailureBanner(
          failure: c.requestFailure,
          baseUrl: widget.session.baseUrl ?? '',
        ),
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            OutlinedButton.icon(
              key: const Key('version-check'),
              onPressed: c.busy ? null : c.checkNow,
              icon: const Icon(Icons.refresh, size: 18),
              label: Text(l.versionCheckNow),
            ),
            if (target.isNotEmpty)
              FilledButton(
                key: const Key('version-update'),
                onPressed: c.busy ? null : () => _confirmAndApply(target, v),
                child: Text(l.versionUpdateNow),
              ),
          ],
        ),
      ],
    ];
  }

  Widget _updater(BuildContext context, L l, VersionInfo v, TextStyle? muted) {
    final u = v.updater;
    if (u == null) {
      return DetailSection(
        title: l.versionUpdater,
        children: [Text(l.versionNoUpdater, style: muted)],
      );
    }
    final window = u.maintenanceWindow;
    return DetailSection(
      title: l.versionUpdater,
      children: [
        Wrap(
          spacing: 8,
          runSpacing: 6,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Tag(
              label: updateStateLabel(l, u.state),
              level: _stateLevel(u.state),
            ),
            Text(
              '${u.engine.wire} · ${l.versionMode}: ${u.mode.wire}',
              style: muted,
            ),
            if (u.targetVersion != null && u.targetVersion!.isNotEmpty)
              Text('${l.versionTarget}: ${u.targetVersion}', style: muted),
          ],
        ),
        if (u.message != null && u.message!.isNotEmpty) ...[
          const SizedBox(height: 6),
          Text(updateMessage(l, u.message!, u.messageCode, u.messageParams)),
        ],
        if (u.error != null && u.error!.isNotEmpty)
          Text(
            u.error!,
            style: theme(context).copyWith(
              color: severityTextColor(context, SeverityLevel.critical),
            ),
          ),
        // What the operator has to do something about, in the colour that
        // says so. This is the screen's reason to exist.
        for (final n in u.notices ?? const <UpdaterStatusNoticesItem>[])
          Padding(
            padding: const EdgeInsets.only(top: 8),
            child: Text(
              updateMessage(l, n.message, n.code, n.params),
              key: Key('version-notice-${n.code}'),
              style: theme(context).copyWith(
                color: severityTextColor(context, SeverityLevel.warning),
              ),
            ),
          ),
        if ((u.steps ?? const []).isNotEmpty) ...[
          const SizedBox(height: 10),
          Text(l.versionSteps, style: muted),
          const SizedBox(height: 4),
          Wrap(
            spacing: 6,
            runSpacing: 6,
            children: [
              for (final s in u.steps!)
                Tag(
                  label: updateStepLabel(l, s.name),
                  level: switch (s.status) {
                    UpdaterStatusStepsItemStatus.ok => SeverityLevel.good,
                    UpdaterStatusStepsItemStatus.failed =>
                      SeverityLevel.critical,
                    _ => SeverityLevel.warning,
                  },
                ),
            ],
          ),
        ],
        if (u.finishedAt != null) ...[
          const SizedBox(height: 8),
          Text(
            [
              '${l.versionFinished} ${relativeTimeOf(l, u.finishedAt!)}',
              if (u.previousVersion != null && u.previousVersion!.isNotEmpty)
                l.versionFrom(u.previousVersion!),
            ].join(' · '),
            style: muted,
          ),
        ],
        if (window != null) ...[
          const SizedBox(height: 10),
          KeyValue(
            name: l.versionWindow,
            value: window.spec.isEmpty
                ? l.versionWindowAnyTime
                : [
                    window.spec,
                    if (window.openNow)
                      l.versionWindowOpen
                    else if (window.nextOpenAt != null)
                      '${l.versionWindowNext} ${relativeTimeOf(l, window.nextOpenAt!)}',
                  ].join(' · '),
          ),
        ],
      ],
    );
  }

  Widget _request(
    BuildContext context,
    L l,
    UpdateRequest r,
    TextStyle? muted,
  ) => DetailSection(
    title: l.versionRequest,
    children: [
      Wrap(
        spacing: 8,
        runSpacing: 6,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Tag(
            label: _requestState(l, r.state),
            level: switch (r.state) {
              UpdateRequestState.done => SeverityLevel.good,
              UpdateRequestState.failed ||
              UpdateRequestState.expired => SeverityLevel.critical,
              _ => SeverityLevel.warning,
            },
          ),
          Text(
            r.action == UpdateRequestAction.apply
                ? l.versionRequestApply(r.targetVersion)
                : l.versionRequestCheck,
          ),
          Text(relativeTimeOf(l, r.requestedAt), style: muted),
          // Who asked, where the server knows: an automatic check carries
          // no e-mail, and "by " with nothing after it is worse than silence.
          if ((r.requestedByEmail ?? '').isNotEmpty)
            Text(l.versionRequestedBy(r.requestedByEmail!), style: muted),
        ],
      ),
      if (r.message.isNotEmpty) ...[
        const SizedBox(height: 6),
        Text(updateMessage(l, r.message, r.messageCode, r.messageParams)),
      ],
    ],
  );

  /// A request's own state, which is not the updater's.
  String _requestState(L l, UpdateRequestState state) => switch (state) {
    UpdateRequestState.done => l.updateStateSucceeded,
    UpdateRequestState.failed => l.updateStateFailed,
    UpdateRequestState.running => l.updateStateUpdating,
    UpdateRequestState.expired => l.versionRequestExpired,
    _ => l.versionRequestPending,
  };

  TextStyle theme(BuildContext context) =>
      Theme.of(context).textTheme.bodyMedium ?? const TextStyle();

  SeverityLevel _stateLevel(UpdaterStatusState state) => switch (state) {
    UpdaterStatusState.upToDate ||
    UpdaterStatusState.succeeded => SeverityLevel.good,
    UpdaterStatusState.failed ||
    UpdaterStatusState.rollbackFailed ||
    UpdaterStatusState.error => SeverityLevel.critical,
    UpdaterStatusState.off => SeverityLevel.unknown,
    _ => SeverityLevel.warning,
  };

  /// "built 3 days ago", or nothing when the build carries no date.
  String? _built(L l, String date) {
    final at = DateTime.tryParse(date);
    return at == null ? null : '${l.versionBuilt} ${relativeTimeOf(l, at)}';
  }
}
