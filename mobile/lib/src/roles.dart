// What a role is allowed to do, as the web knows it.
//
// A port of `web/src/api/roles.ts`, which is itself a mirror of
// `internal/auth/roles.go`. The server enforces every one of these; this
// table exists so the app can hide a tab or a button rather than offer one
// that answers 403.
import 'api/schema.g.dart';

const _rank = <Role, int>{
  Role.viewer: 1,
  Role.member: 2,
  Role.admin: 3,
  Role.owner: 4,
};

/// The lowest role that may do each thing, by the web's own names.
const _minRole = <String, Role>{
  'license_keys.list': Role.member,
  'api_keys.list': Role.member,
  'api_keys.create': Role.member,
  // An API key above viewer changes configuration, so only admins and
  // owners may create one (D-133).
  'api_keys.create_writing': Role.admin,
  'org.update': Role.admin,
  'members.manage': Role.admin,
  'invitations.manage': Role.admin,
  'license_keys.manage': Role.admin,
  'api_keys.revoke_any': Role.admin,
  'browser_keys.list': Role.member,
  'browser_keys.manage': Role.admin,
  'source_maps.list': Role.member,
  'source_maps.manage': Role.admin,
  'audit.read': Role.admin,
  'fleet.manage': Role.admin,
  'updates.request': Role.admin,
  'alerts.write': Role.member,
  'alerts.manage': Role.admin,
  'cloud_connections.manage': Role.admin,
  'disk_space.read': Role.admin,
};

bool atLeast(Role? role, Role min) {
  final r = role == null ? null : _rank[role];
  final m = _rank[min];
  return r != null && m != null && r >= m;
}

/// Whether [role] may do [permission]. An unknown permission is denied
/// rather than allowed: a typo should hide a button, not offer a 403.
bool can(Role? role, String permission) {
  final min = _minRole[permission];
  return min != null && atLeast(role, min);
}

/// The roles [actor] may give a member who is [from] now. Only owners grant
/// or take away "owner".
List<Role> assignableRoles(Role? actor, Role from) {
  if (!can(actor, 'members.manage')) return const [];
  if (from == Role.owner && actor != Role.owner) return const [];
  return [
    for (final r in [Role.owner, Role.admin, Role.member, Role.viewer])
      if (r != Role.owner || actor == Role.owner) r,
  ];
}
