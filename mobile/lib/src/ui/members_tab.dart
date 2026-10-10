// Who is in the organization, and who has been asked to join.
//
// Both on one tab, as on the web: it is the same question at two stages. A
// row is a person; the role is a dropdown on it, and removing one asks
// first -- taking somebody out of an organization is not undone by tapping
// again.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../members.dart';
import '../roles.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';

class MembersTab extends StatefulWidget {
  const MembersTab({super.key, required this.session, required this.members});

  final SessionController session;
  final MembersController members;

  @override
  State<MembersTab> createState() => _MembersTabState();
}

class _MembersTabState extends State<MembersTab> {
  final _email = TextEditingController();
  Role _role = Role.member;

  @override
  void dispose() {
    _email.dispose();
    super.dispose();
  }

  String roleName(L l, Role? role) => switch (role) {
    Role.owner => l.roleOwner,
    Role.admin => l.roleAdmin,
    Role.member => l.roleMember,
    Role.viewer => l.roleViewer,
    _ => l.roleUnknown,
  };

  Future<void> _confirmRemove(Member m) async {
    final l = L.of(context);
    final me = widget.session.me?.user?.id;
    final self = me != null && me == m.userId;
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(self ? l.membersLeaveTitle : l.membersRemoveTitle),
        content: Text(
          self
              ? l.membersLeaveBody(widget.session.organization?.name ?? '')
              : l.membersRemoveBody(m.email),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: Text(l.ruleCancel),
          ),
          FilledButton(
            key: const Key('member-remove-confirm'),
            onPressed: () => Navigator.of(context).pop(true),
            child: Text(self ? l.membersLeave : l.membersRemove),
          ),
        ],
      ),
    );
    if (ok ?? false) await widget.members.remove(m.userId);
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.members;
    final myRole = widget.session.me?.role;
    final myId = widget.session.me?.user?.id;
    final manage = can(myRole, 'members.manage');

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
              FailureBanner(
                failure: c.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
              if (c.created != null) _Created(controller: c),
              Text(l.membersTitle, style: theme.textTheme.titleSmall),
              for (final m in c.members)
                _MemberRow(
                  member: m,
                  // Only owners hand out owner, and nobody changes their own
                  // role: the server refuses both, so the screen does not
                  // offer them.
                  roles: m.userId == myId
                      ? const []
                      : assignableRoles(myRole, m.role),
                  busy: c.busy == m.userId,
                  self: m.userId == myId,
                  roleName: (r) => roleName(l, r),
                  onRole: (r) => c.setRole(m.userId, r),
                  // Any member may leave; removing somebody else takes an
                  // admin.
                  onRemove: manage || m.userId == myId
                      ? () => _confirmRemove(m)
                      : null,
                ),
              const SizedBox(height: 18),
              Text(l.invitationsTitle, style: theme.textTheme.titleSmall),
              if (c.invitations.isEmpty)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 8),
                  child: Text(
                    l.invitationsEmpty,
                    key: const Key('invitations-empty'),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              else
                for (final i in c.invitations)
                  _InvitationRow(
                    invitation: i,
                    roleName: (r) => roleName(l, r),
                    busy: c.busy == i.id,
                    manage: can(myRole, 'invitations.manage'),
                    onResend: () => c.resend(i.id),
                    onRevoke: () => c.revoke(i.id),
                  ),
              if (can(myRole, 'invitations.manage')) ...[
                const SizedBox(height: 14),
                Card(
                  child: Padding(
                    padding: const EdgeInsets.all(14),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          l.invitationsNew,
                          style: theme.textTheme.titleSmall,
                        ),
                        const SizedBox(height: 10),
                        TextField(
                          key: const Key('invite-email'),
                          controller: _email,
                          keyboardType: TextInputType.emailAddress,
                          autocorrect: false,
                          onChanged: (_) => setState(() {}),
                          decoration: InputDecoration(
                            labelText: l.invitationsEmail,
                            border: const OutlineInputBorder(),
                            isDense: true,
                          ),
                        ),
                        const SizedBox(height: 10),
                        DropdownButtonFormField<Role>(
                          key: const Key('invite-role'),
                          initialValue: _role,
                          decoration: InputDecoration(
                            labelText: l.membersRole,
                            border: const OutlineInputBorder(),
                            isDense: true,
                          ),
                          items: [
                            for (final r in assignableRoles(
                              myRole,
                              Role.member,
                            ))
                              DropdownMenuItem(
                                value: r,
                                child: Text(roleName(l, r)),
                              ),
                          ],
                          onChanged: (r) => setState(() => _role = r ?? _role),
                        ),
                        const SizedBox(height: 10),
                        Align(
                          alignment: Alignment.centerRight,
                          child: FilledButton(
                            key: const Key('invite-send'),
                            onPressed:
                                c.busy != null || !_email.text.contains('@')
                                ? null
                                : () async {
                                    await c.invite(
                                      email: _email.text.trim(),
                                      role: _role,
                                    );
                                    if (c.failure == null) _email.clear();
                                  },
                            child: Text(l.invitationsSend),
                          ),
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

/// The one-time invitation link.
///
/// The server hands the token over once. When it could not send the e-mail,
/// this is the only copy there is -- so it stays on screen until somebody
/// says they have passed it on.
class _Created extends StatelessWidget {
  const _Created({required this.controller});

  final MembersController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final created = controller.created!;

    return Card(
      key: const Key('invitation-created'),
      margin: const EdgeInsets.only(bottom: 12),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              created.emailSent
                  ? l.invitationsSentTo(created.invitation.email)
                  : l.invitationsNotSent(created.invitation.email),
              style: theme.textTheme.bodyMedium,
            ),
            const SizedBox(height: 6),
            SelectableText(
              created.token,
              key: const Key('invitation-token'),
              style: theme.textTheme.bodySmall?.copyWith(
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
            Text(
              l.invitationsTokenOnce,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            Align(
              alignment: Alignment.centerRight,
              child: TextButton(
                key: const Key('invitation-dismiss'),
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

class _MemberRow extends StatelessWidget {
  const _MemberRow({
    required this.member,
    required this.roles,
    required this.busy,
    required this.self,
    required this.roleName,
    required this.onRole,
    required this.onRemove,
  });

  final Member member;
  final List<Role> roles;
  final bool busy;
  final bool self;
  final String Function(Role) roleName;
  final ValueChanged<Role> onRole;
  final VoidCallback? onRemove;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return Padding(
      key: Key('member-${member.userId}'),
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
                        member.name.isEmpty ? member.email : member.name,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.bodyMedium,
                      ),
                    ),
                    if (self) ...[
                      const SizedBox(width: 6),
                      Tag(label: l.membersYou, level: SeverityLevel.info),
                    ],
                  ],
                ),
                Text(
                  member.name.isEmpty
                      ? l.membersJoined(relativeTimeOf(l, member.joinedAt))
                      : '${member.email} · '
                            '${l.membersJoined(relativeTimeOf(l, member.joinedAt))}',
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
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
          else if (roles.isEmpty)
            Text(roleName(member.role), style: theme.textTheme.bodySmall)
          else
            DropdownButton<Role>(
              key: Key('member-role-${member.userId}'),
              value: roles.contains(member.role) ? member.role : null,
              hint: Text(roleName(member.role)),
              underline: const SizedBox.shrink(),
              items: [
                for (final r in roles)
                  DropdownMenuItem(value: r, child: Text(roleName(r))),
              ],
              onChanged: (r) {
                if (r != null && r != member.role) onRole(r);
              },
            ),
          if (onRemove != null)
            IconButton(
              key: Key('member-remove-${member.userId}'),
              tooltip: self ? l.membersLeave : l.membersRemove,
              onPressed: busy ? null : onRemove,
              icon: Icon(
                self ? Icons.logout : Icons.person_remove_outlined,
                size: 20,
              ),
            ),
        ],
      ),
    );
  }
}

class _InvitationRow extends StatelessWidget {
  const _InvitationRow({
    required this.invitation,
    required this.roleName,
    required this.busy,
    required this.manage,
    required this.onResend,
    required this.onRevoke,
  });

  final Invitation invitation;
  final String Function(Role) roleName;
  final bool busy;
  final bool manage;
  final VoidCallback onResend;
  final VoidCallback onRevoke;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return Padding(
      key: Key('invitation-${invitation.id}'),
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
                        invitation.email,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.bodyMedium,
                      ),
                    ),
                    if (invitation.expired) ...[
                      const SizedBox(width: 6),
                      Tag(
                        label: l.invitationsExpired,
                        level: SeverityLevel.warning,
                      ),
                    ],
                  ],
                ),
                Text(
                  // Past and future are different sentences: "expires 16
                  // days ago" is not a thing anybody says.
                  '${roleName(invitation.role)} · '
                  '${invitation.expired ? l.invitationsExpiredAt(relativeTimeOf(l, invitation.expiresAt)) : l.invitationsExpires(relativeTimeOf(l, invitation.expiresAt))}',
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
          else if (manage) ...[
            TextButton(
              key: Key('invitation-resend-${invitation.id}'),
              onPressed: onResend,
              child: Text(l.invitationsResend),
            ),
            IconButton(
              key: Key('invitation-revoke-${invitation.id}'),
              tooltip: l.invitationsRevoke,
              onPressed: onRevoke,
              icon: const Icon(Icons.close, size: 20),
            ),
          ],
        ],
      ),
    );
  }
}
