package templates

import "time"

// Data subject request e-mails (D-107): export ready, organization deletion stages, account deleted.
const (
	NameDataExport      = "data_export"
	NameOrgDeletion     = "org_deletion"
	NameAccountDeletion = "account_deletion"
)

// privacyNames registers the templates before init parses every name (package variables initialize before init).
var privacyNames = func() []string {
	add := []string{NameDataExport, NameOrgDeletion, NameAccountDeletion}
	names = append(names, add...)
	return add
}()

// DataExportData is the "export ready" e-mail.
type DataExportData struct {
	Kind      string // organization | user
	OrgName   string
	Size      string // formatted archive size
	Truncated bool
	ExpiresAt time.Time
	Link      string // expiring download link
}

// DataExport renders the "export ready" e-mail.
func DataExport(locale string, d DataExportData) Message {
	return must(Render(NameDataExport, locale, d))
}

// Organization deletion stages.
const (
	StageScheduled = "scheduled"
	StageCancelled = "cancelled"
	StageCompleted = "completed"
)

// OrgDeletionData is an organization deletion e-mail to the owners.
type OrgDeletionData struct {
	Stage    string // scheduled | cancelled | completed
	OrgName  string
	PurgeAt  time.Time
	Operator bool // scheduled by an operator (no self-service cancellation)
	Link     string
}

// OrgDeletion renders an organization deletion e-mail.
func OrgDeletion(locale string, d OrgDeletionData) Message {
	return must(Render(NameOrgDeletion, locale, d))
}

// AccountDeletionData confirms a deleted account.
type AccountDeletionData struct {
	DeletedAt time.Time
	Link      string
}

// AccountDeletion renders the account deletion confirmation.
func AccountDeletion(locale string, d AccountDeletionData) Message {
	return must(Render(NameAccountDeletion, locale, d))
}
