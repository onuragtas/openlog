package config

// RUM configures real user monitoring (docs/contracts/rum.md, D-136).
//
// There is deliberately one variable. Everything else that bounds RUM — which origins a key serves, how many
// events a minute it may send, what share of sessions it keeps — lives on the browser key itself, where an
// admin can change it per application without a restart and without operator access. A per-installation
// setting for those would be the wrong place: one openlog serves many applications with different traffic.
type RUM struct {
	// Enabled serves POST /v1/rum on ingest and the /api/v1/rum/* reads (OPENLOG_RUM_ENABLED). Turning it
	// off leaves existing browser keys in place but stops accepting their data, which is the switch an
	// operator wants when a page is flooding them and revoking one key at a time is not fast enough.
	Enabled bool
}

func loadRUM(p *parser) RUM {
	return RUM{Enabled: p.bool("OPENLOG_RUM_ENABLED", true)}
}
