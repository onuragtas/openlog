// Package auth implements user authentication and authorization for the API:
// organizations (tenants), users, roles, sessions, API keys, ingest license
// key management, invitations and the audit log. Persistence is behind the
// Store interface (PostgreSQL in production, see internal/store/postgres).
package auth

// Role is a membership role. Roles are ordered: owner > admin > member > viewer.
// An API key carries a role too (viewer, member or admin; D-133), so one matrix
// below describes what users and keys may do.
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

// ValidKeyRole reports whether r may be given to an API key. Owner is not a key
// role: the owner-only operations (granting ownership, deleting the
// organization) are account lifecycle and stay with a human.
func (r Role) ValidKeyRole() bool { return r == RoleViewer || r == RoleMember || r == RoleAdmin }

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
	// ActCreateWritingAPIKey is creating a key whose role is more than viewer (admin, owner; D-133).
	ActCreateWritingAPIKey Action = "api_keys.create_writing"
	ActRevokeAPIKey        Action = "api_keys.revoke"
	ActRevokeAnyAPIKey     Action = "api_keys.revoke_any"
	ActReadAudit           Action = "audit.read"
	ActReadFleet           Action = "fleet.read"
	ActManageFleet         Action = "fleet.manage"
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

	// Dashboards and saved views: members create and change their own, admins change any (api.md "Roles").
	ActWriteDashboards  Action = "dashboards.write"
	ActManageDashboards Action = "dashboards.manage"
	ActWriteSavedViews  Action = "saved_views.write"
	ActManageSavedViews Action = "saved_views.manage"

	// Service level objectives and synthetic checks (slo.md, api.md "Synthetic monitoring").
	ActWriteSLOs       Action = "slos.write"
	ActWriteSynthetics Action = "synthetics.write"

	// Integration settings (docs/contracts/integrations.md).
	ActReadIntegrationSettings   Action = "integration_settings.read"
	ActManageIntegrationSettings Action = "integration_settings.manage"

	// ActManageQueryLimits is the organization's own query limits (usage.go, D-080).
	ActManageQueryLimits Action = "query_limits.manage"
	// ActExportUsage is the usage report export (usage.go, D-079).
	ActExportUsage Action = "usage.export"
	// ActManageSupportAccess grants or revokes openlog operators' support access (operator.go, D-106).
	ActManageSupportAccess Action = "support_access.manage"
	// ActDeleteOrganization schedules or cancels deletion of the organization (privacy.go, D-107).
	ActDeleteOrganization Action = "organization.delete"
	// ActReadOrgExports reads the organization's data exports (privacy.go, D-107).
	ActReadOrgExports Action = "org_exports.read"

	// ActManageOwnAccount is everything a principal does to its own account rather than to an
	// organization: sign out, change the password or language, list and revoke sessions, export
	// or delete personal data. It needs no membership, and no API key can do it.
	ActManageOwnAccount Action = "account.manage"
)

// Permission is what one operation requires.
type Permission struct {
	// Min is the minimum membership role; "" for operations that need no organization.
	Min Role
	// UserOnly refuses API keys whatever their role: operations on identity and credentials
	// (members, invitations, keys), on the caller's own account, and on the machines of the
	// installation (fleet rollouts, backend updates) are for signed-in people.
	UserOnly bool
}

// matrix is the permission matrix (docs/contracts/api.md "Roles"). It is the only
// description of who may do what: Allow below is the single decision every handler
// goes through, and nothing else compares a principal's Kind or Role.
var matrix = map[Action]Permission{
	// Reads and organization configuration: an API key may do these with a role that allows them.
	ActReadTelemetry:             {Min: RoleViewer},
	ActReadOrg:                   {Min: RoleViewer},
	ActListMembers:               {Min: RoleViewer},
	ActListLicenseKeys:           {Min: RoleMember},
	ActListAPIKeys:               {Min: RoleMember},
	ActReadAudit:                 {Min: RoleAdmin},
	ActReadFleet:                 {Min: RoleViewer},
	ActReadAlerts:                {Min: RoleViewer},
	ActReadIntegrationSettings:   {Min: RoleViewer},
	ActUpdateOrg:                 {Min: RoleAdmin},
	ActWriteAlerts:               {Min: RoleMember},
	ActManageAlerts:              {Min: RoleAdmin},
	ActWriteAPMErrors:            {Min: RoleMember},
	ActManageAPMErrors:           {Min: RoleAdmin},
	ActWriteDashboards:           {Min: RoleMember},
	ActManageDashboards:          {Min: RoleAdmin},
	ActWriteSavedViews:           {Min: RoleMember},
	ActManageSavedViews:          {Min: RoleAdmin},
	ActWriteSLOs:                 {Min: RoleMember},
	ActWriteSynthetics:           {Min: RoleMember},
	ActManageIntegrationSettings: {Min: RoleAdmin},
	ActExportUsage:               {Min: RoleAdmin},

	// Identity, credentials and the installation itself: signed-in users only.
	ActManageMembers:       {Min: RoleAdmin, UserOnly: true},
	ActManageInvitations:   {Min: RoleAdmin, UserOnly: true},
	ActManageLicenseKeys:   {Min: RoleAdmin, UserOnly: true},
	ActCreateAPIKey:        {Min: RoleMember, UserOnly: true},
	ActCreateWritingAPIKey: {Min: RoleAdmin, UserOnly: true},
	ActRevokeAPIKey:        {Min: RoleMember, UserOnly: true},
	ActRevokeAnyAPIKey:     {Min: RoleAdmin, UserOnly: true},
	ActManageFleet:         {Min: RoleAdmin, UserOnly: true},
	ActRequestUpdate:       {Min: RoleAdmin, UserOnly: true},
	ActManageQueryLimits:   {Min: RoleOwner, UserOnly: true},
	ActManageSupportAccess: {Min: RoleOwner, UserOnly: true},
	ActDeleteOrganization:  {Min: RoleOwner, UserOnly: true},
	ActReadOrgExports:      {Min: RoleOwner, UserOnly: true},
	ActManageOwnAccount:    {UserOnly: true},
}

// Can reports whether role r may perform a, by role rank alone. Unknown actions
// and actions that need no organization are denied; callers that authorize a
// request use Authorize, which also applies Permission.UserOnly.
func (r Role) Can(a Action) bool {
	perm, ok := matrix[a]
	return ok && perm.Min != "" && r.AtLeast(perm.Min)
}

// Allow is the single authorization decision of the API: it reports whether p
// may perform an operation that requires perm. Every handler goes through it
// (directly or through Authorize), so a principal's kind and role are compared
// in exactly one place.
//
// Errors: ErrUnauthenticated (no principal), ErrPermissionDenied.
func Allow(p *Principal, perm Permission) error {
	if p == nil {
		return unauthenticated("missing credentials")
	}
	if perm.UserOnly && p.Kind != KindSession {
		return denied("this operation requires a signed-in user; API keys cannot perform it")
	}
	if perm.Min == "" {
		return nil
	}
	if !p.HasOrg() {
		return denied("you are not a member of any organization")
	}
	if !p.Role.AtLeast(perm.Min) {
		if p.Kind == KindAPIKey {
			return denied("this API key's role (" + string(p.Role) + ") does not allow this operation")
		}
		return denied("your role (" + string(p.Role) + ") does not allow this operation")
	}
	return nil
}

// Authorize reports whether p may perform a, using the permission matrix.
// Unknown actions are denied.
func Authorize(p *Principal, a Action) error {
	perm, ok := matrix[a]
	if !ok {
		return denied("this operation is not allowed")
	}
	return Allow(p, perm)
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
