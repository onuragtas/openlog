// The three key tabs: lisans, API and tarayıcı.
//
// One shape for all three, because the web's three tabs are one shape too:
// a list of credentials, a form that makes one, and the value the server
// shows exactly once. What differs is the fields the form asks for and the
// line under each row.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../keys.dart';
import '../roles.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';

/// The frame all three share.
class KeysTab<T> extends StatelessWidget {
  const KeysTab({
    super.key,
    required this.session,
    required this.controller,
    required this.emptyText,
    required this.rows,
    required this.form,
  });

  final SessionController session;
  final KeysController<T> controller;
  final String emptyText;

  /// One row per key, built by the tab that knows what a key of its kind
  /// has to say.
  final List<Widget> Function(BuildContext, List<T>) rows;

  /// The form that makes one, or nothing when this role may not.
  final Widget? form;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final c = controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.loading) {
          return const Center(child: CircularProgressIndicator());
        }
        return RefreshIndicator(
          onRefresh: c.load,
          child: ListView(
            padding: const EdgeInsets.all(16),
            children: [
              FailureBanner(failure: c.failure, baseUrl: session.baseUrl ?? ''),
              if (c.createdValue != null || c.createdName != null)
                CreatedKeyCard(controller: c),
              if (c.items.isEmpty)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 12),
                  child: Text(
                    emptyText,
                    key: const Key('keys-empty'),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              else
                ...rows(context, c.items),
              if (form != null) ...[const SizedBox(height: 14), form!],
            ],
          ),
        );
      },
    );
  }
}

/// The value the server shows once.
class CreatedKeyCard extends StatelessWidget {
  const CreatedKeyCard({super.key, required this.controller});

  final KeysController<Object?> controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final value = controller.createdValue;

    return Card(
      key: const Key('key-created'),
      margin: const EdgeInsets.only(bottom: 12),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              l.keysCreated(controller.createdName ?? ''),
              style: theme.textTheme.bodyMedium,
            ),
            const SizedBox(height: 6),
            if (value == null)
              // An imported license key: the operator chose the value, so
              // there is nothing for the server to reveal.
              Text(
                l.keysImported,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              )
            else ...[
              SelectableText(
                value,
                key: const Key('key-value'),
                style: theme.textTheme.bodySmall?.copyWith(
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
              Text(
                l.keysShownOnce,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ],
            Align(
              alignment: Alignment.centerRight,
              child: TextButton(
                key: const Key('key-dismiss'),
                onPressed: controller.dismissCreated,
                child: Text(l.invitationsDone),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// One credential: its name, what it is, and the way to revoke it.
class KeyRow extends StatelessWidget {
  const KeyRow({
    super.key,
    required this.name,
    required this.prefix,
    required this.subtitle,
    required this.revokedAt,
    required this.busy,
    required this.onRevoke,
  });

  final String name;
  final String prefix;
  final String subtitle;
  final DateTime? revokedAt;
  final bool busy;
  final VoidCallback? onRevoke;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final revoked = revokedAt != null;

    return Opacity(
      // A revoked key stays in the list -- the web keeps them too, because
      // "this key existed and was revoked" is part of the answer to "who
      // could write to us".
      opacity: revoked ? 0.55 : 1,
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 6),
        child: Row(
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
                      if (revoked) ...[
                        const SizedBox(width: 6),
                        Tag(label: l.keysRevoked, level: SeverityLevel.unknown),
                      ],
                    ],
                  ),
                  Text(
                    '$prefix… · $subtitle',
                    maxLines: 2,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
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
            else if (!revoked && onRevoke != null)
              TextButton(
                key: Key('key-revoke-$prefix'),
                onPressed: onRevoke,
                child: Text(l.keysRevoke),
              ),
          ],
        ),
      ),
    );
  }
}

/// Asks before revoking: a key cannot be un-revoked, and whatever was using
/// it stops working.
Future<bool> confirmRevoke(
  BuildContext context,
  String name,
  String what,
) async {
  final l = L.of(context);
  final ok = await showDialog<bool>(
    context: context,
    builder: (context) => AlertDialog(
      title: Text(l.keysRevokeTitle(name)),
      content: Text(what),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(false),
          child: Text(l.ruleCancel),
        ),
        FilledButton(
          key: const Key('key-revoke-confirm'),
          onPressed: () => Navigator.of(context).pop(true),
          child: Text(l.keysRevoke),
        ),
      ],
    ),
  );
  return ok ?? false;
}

/// Lisans anahtarları: what an agent sends data with.
class LicenseKeysTab extends StatefulWidget {
  const LicenseKeysTab({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final LicenseKeysController controller;

  @override
  State<LicenseKeysTab> createState() => _LicenseKeysTabState();
}

class _LicenseKeysTabState extends State<LicenseKeysTab> {
  final _name = TextEditingController();

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.controller;
    final manage = can(widget.session.me?.role, 'license_keys.manage');

    return KeysTab<LicenseKey>(
      session: widget.session,
      controller: c,
      emptyText: l.licenseKeysEmpty,
      rows: (context, keys) => [
        for (final k in keys)
          KeyRow(
            key: Key('license-key-${k.id}'),
            name: k.name,
            prefix: k.prefix,
            subtitle: k.lastUsedAt == null
                ? l.keysNeverUsed
                : l.keysLastUsed(relativeTimeOf(l, k.lastUsedAt!)),
            revokedAt: k.revokedAt,
            busy: c.busy == k.id,
            onRevoke: !manage
                ? null
                : () async {
                    if (await confirmRevoke(
                      context,
                      k.name,
                      // The server's own behaviour, said before the tap:
                      // ingest keeps accepting it while the auth cache is
                      // warm.
                      l.licenseKeysRevokeBody,
                    )) {
                      await c.revoke(k.id);
                    }
                  },
          ),
      ],
      form: !manage
          ? null
          : _NameForm(
              fieldKey: const Key('license-key-name'),
              buttonKey: const Key('license-key-create'),
              title: l.licenseKeysNew,
              label: l.keysName,
              busy: c.busy != null,
              controller: _name,
              onChanged: () => setState(() {}),
              onSubmit: () async {
                await c.add(_name.text.trim());
                if (c.failure == null) _name.clear();
              },
            ),
    );
  }
}

/// API anahtarları: what a script reads with.
class ApiKeysTab extends StatefulWidget {
  const ApiKeysTab({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final ApiKeysController controller;

  @override
  State<ApiKeysTab> createState() => _ApiKeysTabState();
}

class _ApiKeysTabState extends State<ApiKeysTab> {
  final _name = TextEditingController();
  APIKeyRole _role = APIKeyRole.viewer;

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  String _roleName(L l, APIKeyRole r) => switch (r) {
    APIKeyRole.admin => l.roleAdmin,
    APIKeyRole.member => l.roleMember,
    APIKeyRole.viewer => l.roleViewer,
    _ => l.roleUnknown,
  };

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.controller;
    final role = widget.session.me?.role;
    final create = can(role, 'api_keys.create');
    // A key above viewer changes configuration, so only admins and owners
    // may make one (D-133) -- the choice is narrowed, not the whole form.
    final writing = can(role, 'api_keys.create_writing');

    return KeysTab<APIKey>(
      session: widget.session,
      controller: c,
      emptyText: l.apiKeysEmpty,
      rows: (context, keys) => [
        for (final k in keys)
          KeyRow(
            key: Key('api-key-${k.id}'),
            name: k.name,
            prefix: k.prefix,
            subtitle: [
              _roleName(l, k.role),
              if (k.lastUsedAt != null)
                l.keysLastUsed(relativeTimeOf(l, k.lastUsedAt!))
              else
                l.keysNeverUsed,
              if (k.expiresAt != null)
                l.keysExpires(relativeTimeOf(l, k.expiresAt!)),
            ].join(' · '),
            revokedAt: k.revokedAt,
            busy: c.busy == k.id,
            onRevoke: !create
                ? null
                : () async {
                    if (await confirmRevoke(
                      context,
                      k.name,
                      l.apiKeysRevokeBody,
                    )) {
                      await c.revoke(k.id);
                    }
                  },
          ),
      ],
      form: !create
          ? null
          : _NameForm(
              fieldKey: const Key('api-key-name'),
              buttonKey: const Key('api-key-create'),
              title: l.apiKeysNew,
              label: l.keysName,
              busy: c.busy != null,
              controller: _name,
              onChanged: () => setState(() {}),
              extra: DropdownButtonFormField<APIKeyRole>(
                key: const Key('api-key-role'),
                initialValue: _role,
                decoration: InputDecoration(
                  labelText: l.membersRole,
                  helperText: writing ? null : l.apiKeysViewerOnly,
                  helperMaxLines: 2,
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
                items: [
                  DropdownMenuItem(
                    value: APIKeyRole.viewer,
                    child: Text(_roleName(l, APIKeyRole.viewer)),
                  ),
                  if (writing) ...[
                    DropdownMenuItem(
                      value: APIKeyRole.member,
                      child: Text(_roleName(l, APIKeyRole.member)),
                    ),
                    DropdownMenuItem(
                      value: APIKeyRole.admin,
                      child: Text(_roleName(l, APIKeyRole.admin)),
                    ),
                  ],
                ],
                onChanged: (r) => setState(() => _role = r ?? _role),
              ),
              onSubmit: () async {
                await c.add(_name.text.trim(), _role.wire);
                if (c.failure == null) _name.clear();
              },
            ),
    );
  }
}

/// Tarayıcı anahtarları: what a web page or an app sends RUM with.
class BrowserKeysTab extends StatefulWidget {
  const BrowserKeysTab({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final BrowserKeysController controller;

  @override
  State<BrowserKeysTab> createState() => _BrowserKeysTabState();
}

class _BrowserKeysTabState extends State<BrowserKeysTab> {
  final _name = TextEditingController();
  final _service = TextEditingController();
  final _allowlist = TextEditingController();
  BrowserKeyKind _kind = BrowserKeyKind.browser;

  @override
  void dispose() {
    _name.dispose();
    _service.dispose();
    _allowlist.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.controller;
    final manage = can(widget.session.me?.role, 'browser_keys.manage');
    final mobile = _kind == BrowserKeyKind.mobile;
    final allowlist = parseAllowlist(_allowlist.text);

    return KeysTab<BrowserKey>(
      session: widget.session,
      controller: c,
      emptyText: l.browserKeysEmpty,
      rows: (context, keys) => [
        for (final k in keys)
          KeyRow(
            key: Key('browser-key-${k.id}'),
            name: k.name,
            // The one credential openlog reads back: it ships inside a web
            // page, so withholding it here would only be theatre.
            prefix: k.key.isEmpty ? k.prefix : k.key,
            subtitle: [
              k.serviceName,
              if (k.environment.isNotEmpty) k.environment,
              k.kind == BrowserKeyKind.mobile
                  ? l.browserKeysApps(k.appIds.length)
                  : l.browserKeysOrigins(k.origins.length),
            ].join(' · '),
            revokedAt: k.revokedAt,
            busy: c.busy == k.id,
            onRevoke: !manage
                ? null
                : () async {
                    if (await confirmRevoke(
                      context,
                      k.name,
                      l.browserKeysRevokeBody,
                    )) {
                      await c.revoke(k.id);
                    }
                  },
          ),
      ],
      form: !manage
          ? null
          : Card(
              child: Padding(
                padding: const EdgeInsets.all(14),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(l.browserKeysNew, style: theme.textTheme.titleSmall),
                    const SizedBox(height: 10),
                    TextField(
                      key: const Key('browser-key-name'),
                      controller: _name,
                      onChanged: (_) => setState(() {}),
                      decoration: InputDecoration(
                        labelText: l.keysName,
                        border: const OutlineInputBorder(),
                        isDense: true,
                      ),
                    ),
                    const SizedBox(height: 10),
                    TextField(
                      key: const Key('browser-key-service'),
                      controller: _service,
                      onChanged: (_) => setState(() {}),
                      decoration: InputDecoration(
                        labelText: l.browserKeysService,
                        border: const OutlineInputBorder(),
                        isDense: true,
                      ),
                    ),
                    const SizedBox(height: 10),
                    SegmentedButton<BrowserKeyKind>(
                      key: const Key('browser-key-kind'),
                      showSelectedIcon: false,
                      segments: [
                        ButtonSegment(
                          value: BrowserKeyKind.browser,
                          label: Text(l.browserKeysKindBrowser),
                        ),
                        ButtonSegment(
                          value: BrowserKeyKind.mobile,
                          label: Text(l.browserKeysKindMobile),
                        ),
                      ],
                      selected: {_kind},
                      onSelectionChanged: (s) =>
                          setState(() => _kind = s.first),
                    ),
                    const SizedBox(height: 10),
                    TextField(
                      key: const Key('browser-key-allowlist'),
                      controller: _allowlist,
                      minLines: 2,
                      maxLines: 4,
                      onChanged: (_) => setState(() {}),
                      decoration: InputDecoration(
                        // Exactly one allowlist goes with the kind, and the
                        // other one is a 400 -- so the field changes with
                        // the choice rather than offering both.
                        labelText: mobile
                            ? l.browserKeysAppIds
                            : l.browserKeysOriginsLabel,
                        helperText: mobile
                            ? l.browserKeysAppIdsHint
                            : l.browserKeysOriginsHint,
                        helperMaxLines: 2,
                        border: const OutlineInputBorder(),
                        isDense: true,
                      ),
                    ),
                    const SizedBox(height: 10),
                    Align(
                      alignment: Alignment.centerRight,
                      child: FilledButton(
                        key: const Key('browser-key-create'),
                        onPressed:
                            c.busy != null ||
                                _name.text.trim().isEmpty ||
                                _service.text.trim().isEmpty ||
                                allowlist.isEmpty
                            ? null
                            : () async {
                                await c.add(
                                  name: _name.text.trim(),
                                  serviceName: _service.text.trim(),
                                  kind: _kind.wire,
                                  allowlist: allowlist,
                                );
                                if (c.failure == null) {
                                  _name.clear();
                                  _service.clear();
                                  _allowlist.clear();
                                }
                              },
                        child: Text(l.keysCreate),
                      ),
                    ),
                  ],
                ),
              ),
            ),
    );
  }
}

/// The form two of the three tabs share: a name, maybe one more field, and
/// a button.
class _NameForm extends StatelessWidget {
  const _NameForm({
    required this.fieldKey,
    required this.buttonKey,
    required this.title,
    required this.label,
    required this.busy,
    required this.controller,
    required this.onChanged,
    required this.onSubmit,
    this.extra,
  });

  final Key fieldKey;
  final Key buttonKey;
  final String title;
  final String label;
  final bool busy;
  final TextEditingController controller;
  final VoidCallback onChanged;
  final Future<void> Function() onSubmit;
  final Widget? extra;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return Card(
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(title, style: theme.textTheme.titleSmall),
            const SizedBox(height: 10),
            TextField(
              key: fieldKey,
              controller: controller,
              onChanged: (_) => onChanged(),
              decoration: InputDecoration(
                labelText: label,
                border: const OutlineInputBorder(),
                isDense: true,
              ),
            ),
            if (extra != null) ...[const SizedBox(height: 10), extra!],
            const SizedBox(height: 10),
            Align(
              alignment: Alignment.centerRight,
              child: FilledButton(
                key: buttonKey,
                onPressed: busy || controller.text.trim().isEmpty
                    ? null
                    : onSubmit,
                child: Text(l.keysCreate),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
