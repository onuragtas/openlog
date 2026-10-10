// The organization itself.
//
// What the web's Organizasyon tab shows: the name (which an admin may
// change), the ids an operator asks for, the role this account has in it,
// when it was created, and the language the server writes in for everyone.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../account.dart';
import '../api/schema.g.dart';
import '../roles.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';

class OrgTab extends StatefulWidget {
  const OrgTab({super.key, required this.session, required this.controller});

  final SessionController session;
  final OrgController controller;

  @override
  State<OrgTab> createState() => _OrgTabState();
}

class _OrgTabState extends State<OrgTab> {
  final _name = TextEditingController();
  bool _renaming = false;

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  String _roleName(L l, Role? role) => switch (role) {
    Role.owner => l.roleOwner,
    Role.admin => l.roleAdmin,
    Role.member => l.roleMember,
    Role.viewer => l.roleViewer,
    _ => l.roleUnknown,
  };

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.controller;
    final canUpdate = can(widget.session.me?.role, 'org.update');

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.loading) {
          return const Center(child: CircularProgressIndicator());
        }
        final org = c.org;
        return RefreshIndicator(
          onRefresh: c.load,
          child: ListView(
            padding: const EdgeInsets.all(16),
            children: [
              FailureBanner(
                failure: c.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
              if (org == null)
                const SizedBox.shrink()
              else ...[
                Card(
                  child: Padding(
                    padding: const EdgeInsets.all(16),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        if (_renaming)
                          Row(
                            children: [
                              Expanded(
                                child: TextField(
                                  key: const Key('org-name'),
                                  controller: _name,
                                  onChanged: (_) => setState(() {}),
                                  decoration: InputDecoration(
                                    labelText: l.orgName,
                                    border: const OutlineInputBorder(),
                                    isDense: true,
                                  ),
                                ),
                              ),
                              const SizedBox(width: 8),
                              FilledButton(
                                key: const Key('org-rename-save'),
                                onPressed: c.busy || _name.text.trim().isEmpty
                                    ? null
                                    : () async {
                                        final ok = await c.update(
                                          name: _name.text.trim(),
                                        );
                                        if (ok) {
                                          setState(() => _renaming = false);
                                        }
                                      },
                                child: Text(l.calendarsSave),
                              ),
                            ],
                          )
                        else
                          Row(
                            children: [
                              Expanded(
                                child: Text(
                                  org.name,
                                  style: theme.textTheme.titleMedium,
                                ),
                              ),
                              if (canUpdate)
                                IconButton(
                                  key: const Key('org-rename'),
                                  tooltip: l.orgRename,
                                  onPressed: () {
                                    _name.text = org.name;
                                    setState(() => _renaming = true);
                                  },
                                  icon: const Icon(Icons.edit_outlined),
                                ),
                            ],
                          ),
                        const SizedBox(height: 10),
                        _Row(label: l.homeRole, value: _roleName(l, org.role)),
                        _Row(
                          label: l.orgCreated,
                          value: relativeTimeOf(l, org.createdAt),
                        ),
                        // The ids an operator asks for. Selectable because
                        // the reason anybody looks at them is to send one
                        // to somebody.
                        _Row(
                          label: l.orgTenantId,
                          value: org.tenantId,
                          selectable: true,
                        ),
                        _Row(label: l.orgId, value: org.id, selectable: true),
                      ],
                    ),
                  ),
                ),
                const SizedBox(height: 12),
                Card(
                  child: Padding(
                    padding: const EdgeInsets.all(16),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(l.orgLanguage, style: theme.textTheme.titleSmall),
                        const SizedBox(height: 4),
                        Text(
                          // Different from the profile one: this is what
                          // the server writes in for people who have not
                          // chosen a language of their own.
                          l.orgLanguageHint,
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: theme.colorScheme.onSurfaceVariant,
                          ),
                        ),
                        const SizedBox(height: 10),
                        DropdownButtonFormField<OrgLanguage>(
                          key: const Key('org-language'),
                          initialValue: org.language == OrgLanguage.unknown
                              ? null
                              : org.language,
                          items: [
                            // "" is a real choice: nobody has picked a
                            // language for the organization, and the
                            // recipient's own preference decides.
                            DropdownMenuItem(
                              value: OrgLanguage.empty,
                              child: Text(l.orgLanguageNone),
                            ),
                            const DropdownMenuItem(
                              value: OrgLanguage.en,
                              child: Text('English'),
                            ),
                            const DropdownMenuItem(
                              value: OrgLanguage.tr,
                              child: Text('Türkçe'),
                            ),
                          ],
                          onChanged: !canUpdate || c.busy
                              ? null
                              : (v) {
                                  if (v != null) c.update(language: v.wire);
                                },
                        ),
                      ],
                    ),
                  ),
                ),
              ],
            ],
          ),
        );
      },
    );
  }
}

class _Row extends StatelessWidget {
  const _Row({
    required this.label,
    required this.value,
    this.selectable = false,
  });

  final String label;
  final String value;
  final bool selectable;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Text(
              label,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ),
          Flexible(
            child: selectable
                ? SelectableText(
                    value,
                    textAlign: TextAlign.end,
                    style: theme.textTheme.bodySmall,
                  )
                : Text(
                    value,
                    textAlign: TextAlign.end,
                    style: theme.textTheme.bodyMedium,
                  ),
          ),
        ],
      ),
    );
  }
}
