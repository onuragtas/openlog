// Single sign-on, as the web's tab has it: the connections, the domains
// that route to them, whether SSO is enforced, which group becomes which
// role, and the SCIM tokens.
//
// Making a connection stays on the web. It means pasting a metadata URL, a
// client secret or a certificate, which is a laptop's job; the screen says
// so rather than offering half a form.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../session.dart';
import '../sso.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';

class SsoTab extends StatefulWidget {
  const SsoTab({super.key, required this.session, required this.controller});

  final SessionController session;
  final SsoController controller;

  @override
  State<SsoTab> createState() => _SsoTabState();
}

class _SsoTabState extends State<SsoTab> {
  final _domain = TextEditingController();
  final _group = TextEditingController();
  Role _role = Role.member;

  @override
  void dispose() {
    _domain.dispose();
    _group.dispose();
    super.dispose();
  }

  String _roleName(L l, Object role) => switch (role) {
    Role.admin || SSORoleMappingRole.admin => l.roleAdmin,
    Role.viewer || SSORoleMappingRole.viewer => l.roleViewer,
    _ => l.roleMember,
  };

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.loading) {
          return const Center(child: CircularProgressIndicator());
        }
        final state = c.state;
        return RefreshIndicator(
          onRefresh: c.load,
          child: ListView(
            padding: const EdgeInsets.all(16),
            children: [
              FailureBanner(
                failure: c.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
              if (state == null)
                const SizedBox.shrink()
              else ...[
                if (!state.available)
                  Text(
                    // Without a public URL the identity provider has
                    // nowhere to send anybody back to.
                    l.ssoUnavailable,
                    key: const Key('sso-unavailable'),
                    style: theme.textTheme.bodyMedium?.copyWith(
                      color: severityTextColor(context, SeverityLevel.warning),
                    ),
                  ),
                if (!state.secretsEncrypted)
                  Padding(
                    padding: const EdgeInsets.only(top: 6),
                    child: Text(
                      l.ssoSecretsPlain,
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: severityTextColor(
                          context,
                          SeverityLevel.warning,
                        ),
                      ),
                    ),
                  ),
                const SizedBox(height: 8),
                Text(l.ssoConnections, style: theme.textTheme.titleSmall),
                if (state.connections.isEmpty)
                  Padding(
                    padding: const EdgeInsets.symmetric(vertical: 8),
                    child: Text(
                      l.ssoNoConnections,
                      key: const Key('sso-no-connections'),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  )
                else
                  for (final conn in state.connections)
                    _Connection(
                      connection: conn,
                      busy: c.busy == conn.id,
                      test: c.tests[conn.id],
                      onToggle: () =>
                          c.setEnabled(conn.id, enabled: !conn.enabled),
                      onTest: () => c.test(conn.id),
                    ),
                Padding(
                  padding: const EdgeInsets.only(top: 6),
                  child: Text(
                    l.ssoEditOnWeb,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ),
                const SizedBox(height: 16),
                _Enforcement(controller: c, state: state),
                const SizedBox(height: 16),
                Text(l.ssoDomains, style: theme.textTheme.titleSmall),
                Text(
                  l.ssoDomainsHint,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                for (final d in c.domains)
                  _Domain(
                    domain: d,
                    busy: c.busy == d.id,
                    emailAvailable: state.emailVerificationAvailable,
                    onVerifyDns: () => c.verifyDomain(d.id, method: 'dns_txt'),
                    onVerifyEmail: () => c.verifyDomain(
                      d.id,
                      method: 'email',
                      emailLocalPart: state.domainEmailLocalParts.isEmpty
                          ? 'admin'
                          : state.domainEmailLocalParts.first,
                    ),
                    onRemove: () => c.removeDomain(d.id),
                  ),
                Row(
                  children: [
                    Expanded(
                      child: TextField(
                        key: const Key('sso-domain'),
                        controller: _domain,
                        autocorrect: false,
                        onChanged: (_) => setState(() {}),
                        decoration: InputDecoration(
                          labelText: l.ssoDomainAdd,
                          border: const OutlineInputBorder(),
                          isDense: true,
                        ),
                      ),
                    ),
                    const SizedBox(width: 8),
                    FilledButton(
                      key: const Key('sso-domain-add'),
                      onPressed: c.busy != null || !_domain.text.contains('.')
                          ? null
                          : () async {
                              await c.addDomain(_domain.text.trim());
                              if (c.failure == null) _domain.clear();
                            },
                      child: Text(l.keysCreate),
                    ),
                  ],
                ),
                const SizedBox(height: 16),
                Text(l.ssoRoleMappings, style: theme.textTheme.titleSmall),
                Text(
                  l.ssoRoleMappingsHint,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                for (final m in c.mappings)
                  Padding(
                    key: Key('sso-mapping-${m.group}'),
                    padding: const EdgeInsets.symmetric(vertical: 4),
                    child: Row(
                      children: [
                        Expanded(
                          child: Text(
                            m.group,
                            style: theme.textTheme.bodyMedium,
                          ),
                        ),
                        Text(
                          _roleName(l, m.role),
                          style: theme.textTheme.bodySmall,
                        ),
                        IconButton(
                          key: Key('sso-mapping-remove-${m.group}'),
                          tooltip: l.calendarsDelete,
                          onPressed: c.busy != null
                              ? null
                              : () => c.removeMapping(m.group),
                          icon: const Icon(Icons.close, size: 18),
                        ),
                      ],
                    ),
                  ),
                Row(
                  children: [
                    Expanded(
                      child: TextField(
                        key: const Key('sso-group'),
                        controller: _group,
                        onChanged: (_) => setState(() {}),
                        decoration: InputDecoration(
                          labelText: l.ssoGroup,
                          border: const OutlineInputBorder(),
                          isDense: true,
                        ),
                      ),
                    ),
                    const SizedBox(width: 8),
                    DropdownButton<Role>(
                      key: const Key('sso-group-role'),
                      value: _role,
                      underline: const SizedBox.shrink(),
                      items: [
                        for (final r in [Role.admin, Role.member, Role.viewer])
                          DropdownMenuItem(
                            value: r,
                            child: Text(_roleName(l, r)),
                          ),
                      ],
                      onChanged: (r) => setState(() => _role = r ?? _role),
                    ),
                    const SizedBox(width: 8),
                    FilledButton(
                      key: const Key('sso-mapping-add'),
                      onPressed: c.busy != null || _group.text.trim().isEmpty
                          ? null
                          : () async {
                              await c.setMapping(_group.text.trim(), _role);
                              if (c.failure == null) _group.clear();
                            },
                      child: Text(l.keysCreate),
                    ),
                  ],
                ),
                if (state.scimEnabled) ...[
                  const SizedBox(height: 16),
                  Text(l.ssoScimTokens, style: theme.textTheme.titleSmall),
                  if (c.scimTokens.isEmpty)
                    Padding(
                      padding: const EdgeInsets.symmetric(vertical: 6),
                      child: Text(
                        l.ssoNoScimTokens,
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    )
                  else
                    for (final t in c.scimTokens)
                      Padding(
                        key: Key('scim-${t.id}'),
                        padding: const EdgeInsets.symmetric(vertical: 4),
                        child: Row(
                          children: [
                            Expanded(
                              child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text(
                                    t.name,
                                    style: theme.textTheme.bodyMedium,
                                  ),
                                  Text(
                                    '${t.prefix}… · '
                                    '${t.lastUsedAt == null ? l.keysNeverUsed : l.keysLastUsed(relativeTimeOf(l, t.lastUsedAt!))}',
                                    style: theme.textTheme.bodySmall?.copyWith(
                                      color: theme.colorScheme.onSurfaceVariant,
                                    ),
                                  ),
                                ],
                              ),
                            ),
                            if (t.revokedAt != null)
                              Tag(
                                label: l.keysRevoked,
                                level: SeverityLevel.unknown,
                              )
                            else
                              TextButton(
                                key: Key('scim-revoke-${t.id}'),
                                onPressed: c.busy != null
                                    ? null
                                    : () => c.revokeScimToken(t.id),
                                child: Text(l.keysRevoke),
                              ),
                          ],
                        ),
                      ),
                ],
              ],
            ],
          ),
        );
      },
    );
  }
}

class _Connection extends StatelessWidget {
  const _Connection({
    required this.connection,
    required this.busy,
    required this.test,
    required this.onToggle,
    required this.onTest,
  });

  final SSOConnection connection;
  final bool busy;
  final SSOTestResult? test;
  final VoidCallback onToggle;
  final VoidCallback onTest;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final health = connection.health;
    final level = switch (health.status) {
      SSOHealthStatus.ok => SeverityLevel.good,
      SSOHealthStatus.warning => SeverityLevel.warning,
      SSOHealthStatus.error => SeverityLevel.critical,
      _ => SeverityLevel.unknown,
    };

    return Card(
      key: Key('sso-connection-${connection.id}'),
      margin: const EdgeInsets.symmetric(vertical: 4),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 10, 4, 10),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Row(
                    children: [
                      Flexible(
                        child: Text(
                          connection.name,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: theme.textTheme.titleSmall,
                        ),
                      ),
                      const SizedBox(width: 6),
                      Text(
                        connection.protocol.wire.toUpperCase(),
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                      if (connection.defaultValue) ...[
                        const SizedBox(width: 6),
                        Tag(label: l.routesDefault, level: SeverityLevel.info),
                      ],
                    ],
                  ),
                ),
                if (busy)
                  const Padding(
                    padding: EdgeInsets.all(10),
                    child: SizedBox(
                      width: 16,
                      height: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    ),
                  )
                else
                  Switch(
                    key: Key('sso-enabled-${connection.id}'),
                    value: connection.enabled,
                    onChanged: (_) => onToggle(),
                  ),
              ],
            ),
            // What the background refresh found: an expired certificate is
            // an outage that has not happened yet. The status in words,
            // because `ok` alone on a screen is not a sentence.
            Text(
              health.message.isEmpty
                  ? _healthText(l, health.status)
                  : '${_healthText(l, health.status)}: ${health.message}',
              style: theme.textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, level),
              ),
            ),
            if (test != null)
              Text(
                test!.ok
                    ? l.ssoTestOk
                    : l.ssoTestFailed(
                        test!.checks
                            .where((x) => !x.ok)
                            .map((x) => x.name)
                            .join(', '),
                      ),
                key: Key('sso-test-${connection.id}'),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: severityTextColor(
                    context,
                    test!.ok ? SeverityLevel.good : SeverityLevel.critical,
                  ),
                ),
              ),
            Align(
              alignment: Alignment.centerRight,
              child: TextButton(
                key: Key('sso-test-run-${connection.id}'),
                onPressed: busy ? null : onTest,
                child: Text(l.ssoTest),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

String _healthText(L l, SSOHealthStatus status) => switch (status) {
  SSOHealthStatus.ok => l.ssoHealthOk,
  SSOHealthStatus.warning => l.ssoHealthWarning,
  SSOHealthStatus.error => l.ssoHealthError,
  _ => l.ssoHealthUnknown,
};

class _Enforcement extends StatelessWidget {
  const _Enforcement({required this.controller, required this.state});

  final SsoController controller;
  final SSOState state;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final conn = state.connection;
    final enforced = conn?.enforce ?? false;
    final breakGlass = conn?.breakGlassUserIds ?? const <String>[];

    return Card(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 6, 14, 12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            SwitchListTile(
              key: const Key('sso-enforce'),
              contentPadding: EdgeInsets.zero,
              title: Text(l.ssoEnforce),
              subtitle: Text(
                l.ssoEnforceHint,
                style: theme.textTheme.bodySmall,
              ),
              value: enforced,
              onChanged: controller.busy != null || conn == null
                  ? null
                  : (v) => controller.setEnforce(enforce: v),
            ),
            // Who can still sign in with a password. The list is kept as it
            // is when the switch moves: the endpoint replaces both, and
            // emptying it would lock everybody into the IdP.
            Text(
              breakGlass.isEmpty
                  ? l.ssoNoBreakGlass
                  : l.ssoBreakGlass(breakGlass.length),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _Domain extends StatelessWidget {
  const _Domain({
    required this.domain,
    required this.busy,
    required this.emailAvailable,
    required this.onVerifyDns,
    required this.onVerifyEmail,
    required this.onRemove,
  });

  final SSODomain domain;
  final bool busy;
  final bool emailAvailable;
  final VoidCallback onVerifyDns;
  final VoidCallback onVerifyEmail;
  final VoidCallback onRemove;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return Padding(
      key: Key('sso-domain-${domain.id}'),
      padding: const EdgeInsets.symmetric(vertical: 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Row(
                  children: [
                    Flexible(
                      child: Text(
                        domain.domain,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.bodyMedium,
                      ),
                    ),
                    const SizedBox(width: 6),
                    Tag(
                      label: domain.verified ? l.ssoVerified : l.ssoUnverified,
                      level: domain.verified
                          ? SeverityLevel.good
                          : SeverityLevel.warning,
                    ),
                  ],
                ),
              ),
              if (busy)
                const Padding(
                  padding: EdgeInsets.all(8),
                  child: SizedBox(
                    width: 16,
                    height: 16,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  ),
                )
              else
                IconButton(
                  key: Key('sso-domain-remove-${domain.id}'),
                  tooltip: l.calendarsDelete,
                  onPressed: onRemove,
                  icon: const Icon(Icons.close, size: 18),
                ),
            ],
          ),
          if (!domain.verified) ...[
            // The record to add, which is the whole of what DNS
            // verification asks of somebody.
            SelectableText(
              '${domain.dnsRecord.name} TXT ${domain.dnsRecord.value}',
              style: theme.textTheme.bodySmall?.copyWith(
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
            Row(
              children: [
                TextButton(
                  key: Key('sso-verify-dns-${domain.id}'),
                  onPressed: busy ? null : onVerifyDns,
                  child: Text(l.ssoVerifyDns),
                ),
                if (emailAvailable)
                  TextButton(
                    key: Key('sso-verify-email-${domain.id}'),
                    onPressed: busy ? null : onVerifyEmail,
                    child: Text(l.ssoVerifyEmail),
                  ),
              ],
            ),
          ],
        ],
      ),
    );
  }
}
