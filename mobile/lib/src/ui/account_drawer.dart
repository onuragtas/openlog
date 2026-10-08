// Who is signed in, where, and as what -- out of the way of the alerts.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../session.dart';
import 'failure_text.dart';

class AccountDrawer extends StatelessWidget {
  const AccountDrawer({super.key, required this.session});

  final SessionController session;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;
    final me = session.me;
    final orgs = me?.organizations ?? const <OrgRef>[];

    return Drawer(
      child: SafeArea(
        child: ListView(
          padding: const EdgeInsets.all(20),
          children: [
            Text(l.accountTitle, style: text.titleLarge),
            const SizedBox(height: 16),
            Text(
              l.homeSignedInAs(me?.user?.email ?? ''),
              key: const Key('signed-in-as'),
              style: text.bodyMedium,
            ),
            const SizedBox(height: 4),
            Text(
              session.baseUrl ?? '',
              style: text.bodySmall?.copyWith(color: scheme.onSurfaceVariant),
            ),
            const Divider(height: 32),
            _row(
              context,
              l.homeOrganization,
              session.organization?.name ?? '—',
            ),
            _row(context, l.homeRole, _roleName(l, me?.role)),
            // Only when there is a choice to make.
            if (orgs.length > 1) ...[
              const SizedBox(height: 16),
              Text(l.homeSwitchOrganization, style: text.labelLarge),
              const SizedBox(height: 8),
              DropdownButtonFormField<String>(
                key: const Key('org-picker'),
                initialValue: session.organization?.id,
                decoration: const InputDecoration(border: OutlineInputBorder()),
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
            FailureBanner(
              failure: session.failure,
              baseUrl: session.baseUrl ?? '',
            ),
            const SizedBox(height: 24),
            OutlinedButton.icon(
              key: const Key('sign-out'),
              onPressed: session.busy ? null : session.signOut,
              icon: const Icon(Icons.logout),
              label: Text(l.homeSignOut),
            ),
          ],
        ),
      ),
    );
  }

  Widget _row(BuildContext context, String label, String value) {
    final text = Theme.of(context).textTheme;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 6),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          Text(label, style: text.bodyMedium),
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
