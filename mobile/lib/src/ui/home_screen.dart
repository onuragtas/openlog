// Signed in. Phase 1 ends here: it proves the app knows who it is talking to
// and as whom, which is what everything after it rests on.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../session.dart';
import 'failure_text.dart';

class HomeScreen extends StatelessWidget {
  const HomeScreen({super.key, required this.session});

  final SessionController session;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final me = session.me;
    final user = me?.user;
    final orgs = me?.organizations ?? const <OrgRef>[];

    return Scaffold(
      appBar: AppBar(
        title: const Text('openlog'),
        actions: [
          IconButton(
            key: const Key('sign-out'),
            tooltip: l.homeSignOut,
            onPressed: session.busy ? null : session.signOut,
            icon: const Icon(Icons.logout),
          ),
        ],
      ),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.all(24),
          children: [
            Text(
              l.homeSignedInAs(user?.email ?? ''),
              key: const Key('signed-in-as'),
              style: text.titleMedium,
            ),
            const SizedBox(height: 4),
            Text(
              session.baseUrl ?? '',
              style: text.bodySmall?.copyWith(
                color: Theme.of(context).colorScheme.onSurfaceVariant,
              ),
            ),
            const SizedBox(height: 24),
            _row(
              context,
              l.homeOrganization,
              session.organization?.name ?? '—',
            ),
            _row(context, l.homeRole, _roleName(l, me?.role)),
            // Only when there is a choice to make: a single-organization person
            // has nothing to switch between, and the control would be noise.
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
            const SizedBox(height: 32),
            Text(l.homeNextPhase, style: text.bodyMedium),
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
          Text(
            value,
            style: text.bodyMedium?.copyWith(fontWeight: FontWeight.w600),
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
        // A role this build has never heard of: the server is newer than the
        // app, which must not leave the screen blank.
        return l.roleUnknown;
    }
  }
}
