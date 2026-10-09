// Who is signed in, where, and as what.
//
// A section of its own rather than a drawer, because that is where the web
// keeps it: `nav.settings` sits in the same list as the signals, and signing
// out is at the bottom of the sidebar.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';
import 'theme.dart';

class SettingsBody extends StatefulWidget {
  const SettingsBody({
    super.key,
    required this.session,
    required this.sessions,
    required this.active,
  });

  final SessionController session;

  /// The person's own sessions. Here rather than in the drawer because that
  /// is where the web keeps them, and because they are about this account
  /// rather than about anything being monitored.
  final SessionsController sessions;
  final bool active;

  @override
  State<SettingsBody> createState() => _SettingsBodyState();
}

class _SettingsBodyState extends State<SettingsBody> {
  @override
  void initState() {
    super.initState();
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(SettingsBody old) {
    super.didUpdateWidget(old);
    _loadIfVisible();
  }

  void _loadIfVisible() {
    final c = widget.sessions;
    if (!widget.active || c.loaded || c.loadingFirst) return;
    WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
  }

  @override
  Widget build(BuildContext context) {
    final session = widget.session;
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final c = colorsOf(context);
    final me = session.me;
    final orgs = me?.organizations ?? const <OrgRef>[];

    return SafeArea(
      child: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          Card(
            child: Padding(
              padding: const EdgeInsets.all(16),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(l.settingsAccount, style: text.titleMedium),
                  const SizedBox(height: 12),
                  Text(
                    l.homeSignedInAs(me?.user?.email ?? ''),
                    key: const Key('signed-in-as'),
                    style: text.bodyMedium,
                  ),
                  const SizedBox(height: 4),
                  Text(
                    session.baseUrl ?? '',
                    style: text.bodySmall?.copyWith(color: c.mutedForeground),
                  ),
                  const SizedBox(height: 16),
                  _row(
                    context,
                    l.homeOrganization,
                    session.organization?.name ?? '—',
                  ),
                  _row(context, l.homeRole, _roleName(l, me?.role)),
                ],
              ),
            ),
          ),
          // Only when there is a choice to make.
          if (orgs.length > 1) ...[
            const SizedBox(height: 12),
            Card(
              child: Padding(
                padding: const EdgeInsets.all(16),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(l.homeSwitchOrganization, style: text.titleSmall),
                    const SizedBox(height: 10),
                    DropdownButtonFormField<String>(
                      key: const Key('org-picker'),
                      initialValue: session.organization?.id,
                      items: [
                        for (final o in orgs)
                          DropdownMenuItem(value: o.id, child: Text(o.name)),
                      ],
                      onChanged: session.busy
                          ? null
                          : (id) {
                              if (id != null) session.switchOrganization(id);
                            },
                    ),
                  ],
                ),
              ),
            ),
          ],
          const SizedBox(height: 12),
          ListenableBuilder(
            listenable: widget.sessions,
            builder: (context, _) => _Sessions(
              controller: widget.sessions,
              baseUrl: session.baseUrl ?? '',
            ),
          ),
          FailureBanner(
            failure: session.failure,
            baseUrl: session.baseUrl ?? '',
          ),
        ],
      ),
    );
  }

  Widget _row(BuildContext context, String label, String value) {
    final text = Theme.of(context).textTheme;
    final c = colorsOf(context);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 5),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          Text(
            label,
            style: text.bodyMedium?.copyWith(color: c.mutedForeground),
          ),
          Flexible(
            child: Text(
              value,
              textAlign: TextAlign.end,
              style: text.bodyMedium?.copyWith(fontWeight: FontWeight.w600),
            ),
          ),
        ],
      ),
    );
  }

  String _roleName(L l, Role? role) {
    switch (role) {
      case Role.owner:
        return l.roleOwner;
      case Role.admin:
        return l.roleAdmin;
      case Role.member:
        return l.roleMember;
      case Role.viewer:
        return l.roleViewer;
      case Role.unknown:
      case null:
        return l.roleUnknown;
    }
  }
}

/// Where this account is signed in, and the one write that belongs on a
/// phone: ending a session somewhere else.
class _Sessions extends StatelessWidget {
  const _Sessions({required this.controller, required this.baseUrl});

  final SessionsController controller;
  final String baseUrl;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final sessions = controller.sessions;

    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(l.sessionsTitle, style: theme.textTheme.titleSmall),
            const SizedBox(height: 10),
            if (controller.failure != null)
              FailureBanner(failure: controller.failure, baseUrl: baseUrl)
            else if (controller.loadingFirst)
              const Padding(
                padding: EdgeInsets.symmetric(vertical: 8),
                child: SizedBox(
                  width: 18,
                  height: 18,
                  child: CircularProgressIndicator(strokeWidth: 2),
                ),
              )
            else
              for (final s in sessions)
                _SessionRow(
                  key: Key('session-${s.id}'),
                  session: s,
                  busy: controller.revoking == s.id,
                  onRevoke: () => _confirm(context, s),
                ),
          ],
        ),
      ),
    );
  }

  Future<void> _confirm(BuildContext context, Session s) async {
    final l = L.of(context);
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(l.sessionsEndTitle),
        content: Text(l.sessionsEndBody),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: Text(l.ruleCancel),
          ),
          FilledButton(
            key: const Key('session-confirm'),
            onPressed: () => Navigator.of(context).pop(true),
            child: Text(l.sessionsEnd),
          ),
        ],
      ),
    );
    if (ok ?? false) await controller.revoke(s.id);
  }
}

class _SessionRow extends StatelessWidget {
  const _SessionRow({
    super.key,
    required this.session,
    required this.busy,
    required this.onRevoke,
  });

  final Session session;
  final bool busy;
  final VoidCallback onRevoke;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    // A device names itself at sign-in; a browser does not, and its user
    // agent is the only thing that tells two of them apart.
    final name = session.deviceName.isNotEmpty
        ? session.deviceName
        : session.userAgent.isNotEmpty
        ? session.userAgent
        : l.sessionsBrowser;

    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Flexible(
                      child: Text(
                        name,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.bodyMedium,
                      ),
                    ),
                    if (session.current) ...[
                      const SizedBox(width: 8),
                      Tag(
                        label: l.sessionsThisDevice,
                        level: SeverityLevel.good,
                      ),
                    ],
                  ],
                ),
                Wrap(
                  spacing: 8,
                  children: [
                    if (session.ip.isNotEmpty) Text(session.ip, style: muted),
                    Text(
                      l.sessionsLastSeen(relativeTimeOf(l, session.lastSeenAt)),
                      style: muted,
                    ),
                  ],
                ),
              ],
            ),
          ),
          // Not offered for this device: ending it is signing out, which has
          // its own button and leaves the app somewhere it knows how to be.
          if (!session.current)
            busy
                ? const Padding(
                    padding: EdgeInsets.all(8),
                    child: SizedBox(
                      width: 16,
                      height: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    ),
                  )
                : TextButton(
                    key: Key('session-end-${session.id}'),
                    onPressed: onRevoke,
                    child: Text(l.sessionsEnd),
                  ),
        ],
      ),
    );
  }
}
