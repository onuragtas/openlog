// Who is signed in, where, and as what.
//
// A section of its own rather than a drawer, because that is where the web
// keeps it: `nav.settings` sits in the same list as the signals, and signing
// out is at the bottom of the sidebar.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../session.dart';
import 'failure_text.dart';
import 'theme.dart';

class SettingsBody extends StatelessWidget {
  const SettingsBody({super.key, required this.session});

  final SessionController session;

  @override
  Widget build(BuildContext context) {
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
