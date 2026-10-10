// Settings, with the web's tabs.
//
// The web's /settings is one page with a tab per subject -- Profil,
// Organizasyon, Üyeler, the keys, Güvenlik, SSO, Denetim kaydı, APM
// örnekleme, Kullanım, Depolama -- each gated by the role that may see it.
// This is the same list, filled in as each one is built; a tab appears when
// it has something in it, never as an empty promise.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../account.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../audit.dart';
import '../keys.dart';
import '../members.dart';
import '../roles.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'audit_tab.dart';
import 'keys_tabs.dart';
import 'members_tab.dart';
import 'org_tab.dart';
import 'source_maps_tab.dart';
import 'severity.dart';
import 'theme.dart';

class SettingsBody extends StatefulWidget {
  const SettingsBody({
    super.key,
    required this.session,
    required this.sessions,
    required this.account,
    required this.org,
    required this.members,
    required this.licenseKeys,
    required this.apiKeys,
    required this.browserKeys,
    required this.sourceMaps,
    required this.audit,
    required this.active,
  });

  final SessionController session;

  /// The person's own sessions. Here rather than in the drawer because that
  /// is where the web keeps them, and because they are about this account
  /// rather than about anything being monitored.
  final SessionsController sessions;

  /// The password and the language: the account behind the token, which is
  /// what the web's Profil and Güvenlik tabs change.
  final AccountController account;

  /// The organization itself.
  final OrgController org;

  /// Who is in the organization, and who has been asked to join.
  final MembersController members;

  /// The three kinds of key, each its own tab as on the web.
  final LicenseKeysController licenseKeys;
  final ApiKeysController apiKeys;
  final BrowserKeysController browserKeys;

  /// The stored source maps, and the organization's own record of change.
  final SourceMapsController sourceMaps;
  final AuditController audit;
  final bool active;

  @override
  State<SettingsBody> createState() => _SettingsBodyState();
}

/// One settings tab: its label, what it needs to be allowed to see, and
/// the thing that fills it. The list is the web's, in the web's order.
class _Tab {
  const _Tab(this.key, this.label, this.permission, this.build);

  final String key;
  final String Function(L) label;

  /// Null when every role may look, as the web's own table has it.
  final String? permission;
  final Widget Function(_SettingsBodyState) build;
}

final _tabs = <_Tab>[
  _Tab('profile', (l) => l.settingsProfile, null, (s) => s._profileTab()),
  _Tab(
    'organization',
    (l) => l.settingsOrganization,
    null,
    (s) => OrgTab(session: s.widget.session, controller: s.widget.org),
  ),
  _Tab('security', (l) => l.settingsSecurity, null, (s) => s._securityTab()),
  _Tab('members', (l) => l.settingsMembers, null, (s) => s._membersTab()),
  _Tab(
    'license-keys',
    (l) => l.settingsLicenseKeys,
    'license_keys.list',
    (s) => LicenseKeysTab(
      session: s.widget.session,
      controller: s.widget.licenseKeys,
    ),
  ),
  _Tab(
    'api-keys',
    (l) => l.settingsApiKeys,
    'api_keys.list',
    (s) => ApiKeysTab(session: s.widget.session, controller: s.widget.apiKeys),
  ),
  _Tab(
    'browser-keys',
    (l) => l.settingsBrowserKeys,
    'browser_keys.list',
    (s) => BrowserKeysTab(
      session: s.widget.session,
      controller: s.widget.browserKeys,
    ),
  ),
  _Tab(
    'source-maps',
    (l) => l.settingsSourceMaps,
    'source_maps.list',
    (s) => SourceMapsTab(
      session: s.widget.session,
      controller: s.widget.sourceMaps,
    ),
  ),
  _Tab(
    'audit-log',
    (l) => l.settingsAuditLog,
    'audit.read',
    (s) => AuditTab(session: s.widget.session, controller: s.widget.audit),
  ),
];

class _SettingsBodyState extends State<SettingsBody>
    with TickerProviderStateMixin {
  late TabController _tabsController;
  List<_Tab> _visible = const [];

  @override
  void initState() {
    super.initState();
    _visible = _allowed();
    _tabsController = TabController(length: _visible.length, vsync: this)
      ..addListener(() {
        if (!_tabsController.indexIsChanging) _loadIfVisible();
      });
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(SettingsBody old) {
    super.didUpdateWidget(old);
    // The role arrives with `me`, which can come after the first build:
    // the tabs it unlocks appear then rather than on the next visit.
    final allowed = _allowed();
    if (allowed.length != _visible.length) {
      final was = _visible.isEmpty
          ? null
          : _visible[_tabsController.index.clamp(0, _visible.length - 1)].key;
      _visible = allowed;
      _tabsController.dispose();
      final index = allowed.indexWhere((t) => t.key == was);
      _tabsController =
          TabController(
            length: allowed.length,
            initialIndex: index < 0 ? 0 : index,
            vsync: this,
          )..addListener(() {
            if (!_tabsController.indexIsChanging) _loadIfVisible();
          });
    }
    _loadIfVisible();
  }

  List<_Tab> _allowed() {
    final role = widget.session.me?.role;
    return [
      for (final t in _tabs)
        if (t.permission == null || can(role, t.permission!)) t,
    ];
  }

  @override
  void dispose() {
    _tabsController.dispose();
    super.dispose();
  }

  /// Each tab asks for its own when it is looked at, not when settings is
  /// opened: the web does not read the member list of a tab nobody chose.
  void _loadIfVisible() {
    if (!widget.active || _visible.isEmpty) return;
    final key =
        _visible[_tabsController.index.clamp(0, _visible.length - 1)].key;
    void later(void Function() load) =>
        WidgetsBinding.instance.addPostFrameCallback((_) => load());
    switch (key) {
      case 'organization':
        final c = widget.org;
        if (!c.loading && c.org == null) later(c.load);
      case 'security':
        final c = widget.sessions;
        if (!c.loaded && !c.loadingFirst) later(c.refresh);
      case 'members':
        final c = widget.members;
        if (!c.loading && c.members.isEmpty) later(c.load);
      case 'license-keys':
        final c = widget.licenseKeys;
        if (!c.loading && c.items.isEmpty) later(c.load);
      case 'api-keys':
        final c = widget.apiKeys;
        if (!c.loading && c.items.isEmpty) later(c.load);
      case 'browser-keys':
        final c = widget.browserKeys;
        if (!c.loading && c.items.isEmpty) later(c.load);
      case 'source-maps':
        final c = widget.sourceMaps;
        if (!c.loading && c.items.isEmpty) later(c.load);
      case 'audit-log':
        final c = widget.audit;
        if (!c.loading && c.events.isEmpty) later(c.load);
    }
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return Column(
      children: [
        TabBar(
          controller: _tabsController,
          isScrollable: true,
          tabAlignment: TabAlignment.start,
          tabs: [
            for (final t in _visible)
              Tab(key: Key('settings-tab-${t.key}'), text: t.label(l)),
          ],
        ),
        Expanded(
          child: TabBarView(
            controller: _tabsController,
            children: [for (final t in _visible) t.build(this)],
          ),
        ),
      ],
    );
  }

  Widget _securityTab() => SecurityTab(
    session: widget.session,
    sessions: widget.sessions,
    account: widget.account,
  );

  Widget _membersTab() =>
      MembersTab(session: widget.session, members: widget.members);

  Widget _profileTab() => Builder(builder: _profile);

  Widget _profile(BuildContext context) {
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
          _Language(session: session, account: widget.account),
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
class SessionsCard extends StatelessWidget {
  const SessionsCard({
    super.key,
    required this.controller,
    required this.baseUrl,
  });

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

/// The Güvenlik tab: the password, and where this account is signed in.
///
/// The two belong together because changing the password ends the other
/// sessions -- the list underneath is what that sentence is about.
class SecurityTab extends StatefulWidget {
  const SecurityTab({
    super.key,
    required this.session,
    required this.sessions,
    required this.account,
  });

  final SessionController session;
  final SessionsController sessions;
  final AccountController account;

  @override
  State<SecurityTab> createState() => _SecurityTabState();
}

class _SecurityTabState extends State<SecurityTab> {
  final _current = TextEditingController();
  final _next = TextEditingController();

  @override
  void dispose() {
    _current.dispose();
    _next.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final a = widget.account;
    final minLength = widget.session.authConfig?.passwordMinLength ?? 8;
    final tooShort = _next.text.isNotEmpty && _next.text.length < minLength;

    return ListenableBuilder(
      listenable: a,
      builder: (context, _) => ListView(
        padding: const EdgeInsets.all(16),
        children: [
          Card(
            child: Padding(
              padding: const EdgeInsets.all(16),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(l.securityPassword, style: theme.textTheme.titleSmall),
                  const SizedBox(height: 4),
                  Text(
                    // The part that is not obvious, said before the button
                    // rather than after it.
                    l.securityPasswordHint,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                  const SizedBox(height: 12),
                  TextField(
                    key: const Key('password-current'),
                    controller: _current,
                    obscureText: true,
                    onChanged: (_) => setState(() {}),
                    decoration: InputDecoration(
                      labelText: l.securityCurrentPassword,
                      border: const OutlineInputBorder(),
                      isDense: true,
                    ),
                  ),
                  const SizedBox(height: 10),
                  TextField(
                    key: const Key('password-new'),
                    controller: _next,
                    obscureText: true,
                    onChanged: (_) => setState(() {}),
                    decoration: InputDecoration(
                      labelText: l.securityNewPassword,
                      helperText: l.securityMinLength(minLength),
                      errorText: tooShort
                          ? l.securityMinLength(minLength)
                          : null,
                      border: const OutlineInputBorder(),
                      isDense: true,
                    ),
                  ),
                  FailureBanner(
                    failure: a.failure,
                    baseUrl: widget.session.baseUrl ?? '',
                  ),
                  if (a.passwordChanged)
                    Padding(
                      padding: const EdgeInsets.only(top: 8),
                      child: Text(
                        l.securityPasswordChanged,
                        key: const Key('password-changed'),
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: severityTextColor(context, SeverityLevel.good),
                        ),
                      ),
                    ),
                  const SizedBox(height: 10),
                  Align(
                    alignment: Alignment.centerRight,
                    child: FilledButton(
                      key: const Key('password-save'),
                      onPressed:
                          a.busy ||
                              _current.text.isEmpty ||
                              _next.text.length < minLength
                          ? null
                          : () async {
                              final ok = await a.changePassword(
                                currentPassword: _current.text,
                                newPassword: _next.text,
                              );
                              if (ok) {
                                _current.clear();
                                _next.clear();
                                // The other sessions are gone, so the list
                                // below is now wrong until it is reloaded.
                                await widget.sessions.refresh();
                                if (context.mounted) setState(() {});
                              }
                            },
                      child: Text(l.securityChangePassword),
                    ),
                  ),
                ],
              ),
            ),
          ),
          const SizedBox(height: 12),
          ListenableBuilder(
            listenable: widget.sessions,
            builder: (context, _) => SessionsCard(
              controller: widget.sessions,
              baseUrl: widget.session.baseUrl ?? '',
            ),
          ),
        ],
      ),
    );
  }
}

/// The language the server writes in.
///
/// Not the app's language, which follows the phone: this is what alert
/// e-mails and generated rule names come back in, and it belongs to the
/// account rather than to this device.
class _Language extends StatelessWidget {
  const _Language({required this.session, required this.account});

  final SessionController session;
  final AccountController account;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final current = session.me?.user?.language;

    return ListenableBuilder(
      listenable: account,
      builder: (context, _) => Card(
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(l.profileLanguage, style: theme.textTheme.titleSmall),
              const SizedBox(height: 4),
              Text(
                l.profileLanguageHint,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
              const SizedBox(height: 10),
              DropdownButtonFormField<UserLanguage>(
                key: const Key('profile-language'),
                initialValue: current == UserLanguage.unknown ? null : current,
                items: [
                  DropdownMenuItem(
                    value: UserLanguage.auto,
                    child: Text(l.profileLanguageAuto),
                  ),
                  const DropdownMenuItem(
                    value: UserLanguage.en,
                    child: Text('English'),
                  ),
                  const DropdownMenuItem(
                    value: UserLanguage.tr,
                    child: Text('Türkçe'),
                  ),
                ],
                onChanged: account.busy
                    ? null
                    : (v) {
                        if (v != null) account.setLanguage(v.wire);
                      },
              ),
            ],
          ),
        ),
      ),
    );
  }
}
