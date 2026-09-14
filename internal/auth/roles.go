// Package auth implements user authentication and authorization for the API:
// organizations (tenants), users, roles, sessions, API keys, ingest license
// key management, invitations and the audit log. Persistence is behind the
// Store interface (PostgreSQL in production, see internal/store/postgres).
package auth

// Role is a membership role. Roles are ordered: owner > admin > member > viewer.
type Role string

// Membership roles.
const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

var roleRank = map[Role]int{RoleViewer: 1, RoleMember: 2, RoleAdmin: 3, RoleOwner: 4}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return roleRank[r] > 0 }

// AtLeast reports whether r is min or a higher role. Unknown roles never pass.
func (r Role) AtLeast(min Role) bool {
	return r.Valid() && min.Valid() && roleRank[r] >= roleRank[min]
}

// Action is something a principal can do inside an organization.
type Action string

// Actions checked by the management API.
const (
	ActReadTelemetry     Action = "telemetry.read"
	ActReadOrg           Action = "org.read"
	ActUpdateOrg         Action = "org.update"
	ActListMembers       Action = "members.list"
	ActManageMembers     Action = "members.manage"
	ActManageInvitations Action = "invitations.manage"
	ActListLicenseKeys   Action = "license_keys.list"
	ActManageLicenseKeys Action = "license_keys.manage"
	ActListAPIKeys       Action = "api_keys.list"
	ActCreateAPIKey      Action = "api_keys.create"
	ActRevokeAnyAPIKey   Action = "api_keys.revoke_any"
	ActReadAudit         Action = "audit.read"
	ActReadFleet         Action = "fleet.read"
	ActManageFleet       Action = "fleet.manage"
	// ActRequestUpdate is "Check now" / "Update now" of the backend (installation-wide; the API also
	// refuses it when OPENLOG_SIGNUP_ENABLED=true, where organization admins are not operators).
	ActRequestUpdate Action = "updates.request"
	// Alerting (docs/contracts/alerting.md §7): members write their own rules and mutes and work on incidents;
	// admins change any rule or mute and manage channels.
	ActReadAlerts   Action = "alerts.read"
	ActWriteAlerts  Action = "alerts.write"
	ActManageAlerts Action = "alerts.manage"

	// APM error inbox workflow (docs/contracts/apm.md §3.4): status, assignee, comments; managing deletes any comment.
	ActWriteAPMErrors  Action = "apm_errors.write"
	ActManageAPMErrors Action = "apm_errors.manage"
)

// minRole is the permission matrix (docs/contracts/api.md "Roles").
var minRole = map[Action]Role{
	ActReadTelemetry:     RoleViewer,
	ActReadOrg:           RoleViewer,
	ActListMembers:       RoleViewer,
	ActListLicenseKeys:   RoleMember,
	ActListAPIKeys:       RoleMember,
	ActCreateAPIKey:      RoleMember,
	ActUpdateOrg:         RoleAdmin,
	ActManageMembers:     RoleAdmin,
	ActManageInvitations: RoleAdmin,
	ActManageLicenseKeys: RoleAdmin,
	ActRevokeAnyAPIKey:   RoleAdmin,
	ActReadAudit:         RoleAdmin,
	ActReadFleet:         RoleViewer,
	ActManageFleet:       RoleAdmin,
	ActRequestUpdate:     RoleAdmin,
	ActReadAlerts:        RoleViewer,
	ActWriteAlerts:       RoleMember,
	ActManageAlerts:      RoleAdmin,
	ActWriteAPMErrors:    RoleMember,
	ActManageAPMErrors:   RoleAdmin,
}

// Can reports whether role r may perform a. Unknown actions are denied.
func (r Role) Can(a Action) bool {
	min, ok := minRole[a]
	return ok && r.AtLeast(min)
}

// CanAssign reports whether actor may give target the role to (or change a
// member whose current role is from). Only owners can grant or take away the
// owner role; admins manage admin, member and viewer.
func CanAssign(actor, from, to Role) bool {
	if !actor.Can(ActManageMembers) || !to.Valid() {
		return false
	}
	if from == RoleOwner || to == RoleOwner {
		return actor == RoleOwner
	}
	return true
}
