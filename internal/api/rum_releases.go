package api

import (
	"net/http"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/rum"
)

// Release health (rum.md §2.7): how a version of a browser or mobile application behaved, and when its
// releases happened.
//
// **Both halves reuse what already exists rather than adding a pipeline.** Versions per minute are already
// in `apm_service_versions_1m`: its materialized view reads every span with a service name and takes
// `service.version` from the resource, and a RUM span has both — the name forced from the key, the version
// sent by the SDK. So a mobile release is detected by exactly the code that detects a backend deployment
// (apm.DetectDeployments), and nothing new had to learn what a release is.
//
// The crash-free rate cannot come from the same place. `rum_sessions` has no version column, and it is
// written by a materialized view, which cannot be altered — the same wall identity and geography hit
// (§3.7). So sessions are counted from the spans themselves, and **the consequence is stated rather than
// discovered: this half reaches back only as far as the 7-day trace retention, while the release list
// reaches 30 days.** A version older than a week shows its releases with no sessions against them.

// rumMaxReleases bounds the versions a list returns. An application with more distinct versions than this
// in one window is not releasing, it is leaking build identifiers into service.version.
const rumMaxReleases = 200

type rumReleaseJSON struct {
	Version string `json:"version"`
	// Sessions and ErrorSessions are distinct session ids seen in the range, and those of them that
	// produced at least one error span.
	Sessions      uint64 `json:"sessions"`
	ErrorSessions uint64 `json:"error_sessions"`
	// CrashFreeRate is 1 when every session was clean and 0 when none was; null has no meaning here, so a
	// version with no sessions reports 0 sessions and a rate of 1 rather than an absent field.
	CrashFreeRate float64 `json:"crash_free_rate"`
	FirstSeen     string  `json:"first_seen"`
	LastSeen      string  `json:"last_seen"`
}

// rumReleasesSelect counts sessions per version over the raw spans.
//
// It is a function of its own so a test can build it without data: the handler reaches this query only
// after its filter parses, and a fragment the query layer refuses would otherwise be found in production
// (the same lesson as rumTimelineSelect).
func rumReleasesSelect(sc *query.Scope, f rumFilter, from, to time.Time, limit int) *query.Select {
	q := sc.From(query.Spans).Columns(
		"resource_attributes['service.version'] AS r_version",
		// Distinct sessions, and those of them that produced an error: uniqExactIf counts a session once
		// however many errors it had, which is what "crash-free session" means.
		"uniqExact(attributes[{a_session:String}]) AS r_sessions",
		"uniqExactIf(attributes[{a_session:String}], attributes[{a_event:String}] = {v_error:String}) AS r_errors",
		"min(timestamp) AS r_first",
		"max(timestamp) AS r_last",
	).
		// The keys are bound parameters, not literals: the query layer refuses the token "openlog" anywhere
		// in a fragment, and every RUM attribute key starts with it.
		Param("a_session", rum.AttrSessionID).Param("a_event", rum.AttrEvent).Param("v_error", rum.EventError).
		Where("service_name = {r_app:String}").Param("r_app", f.app).
		// Only RUM spans: a backend service reporting under the same name has its own screens in APM.
		Where("attributes[{a_event:String}] != ''").
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).
		GroupBy("r_version").OrderBy("r_last DESC").Limit(limit)
	if f.env != nil {
		q.Where("deployment_environment = {r_env:String}").Param("r_env", *f.env)
	}
	return q
}

// rumReleases serves GET /api/v1/rum/releases.
func (s *Server) rumReleases(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	gap, err := parseBoundedDuration(r, "gap", apm.DeploymentGap, 5*time.Minute, 24*time.Hour)
	if err != nil {
		return err
	}

	rows, err := sc.Query(r.Context(), rumReleasesSelect(sc, f, from, to, min(rumMaxReleases, s.cfg.MaxRows)))
	if err != nil {
		return err
	}
	releases := []rumReleaseJSON{}
	for rows.Next() {
		var rel rumReleaseJSON
		var first, last time.Time
		if err := rows.Scan(&rel.Version, &rel.Sessions, &rel.ErrorSessions, &first, &last); err != nil {
			rows.Close()
			return err
		}
		rel.FirstSeen, rel.LastSeen = formatTime(first), formatTime(last)
		rel.CrashFreeRate = 1
		if rel.Sessions > 0 {
			rel.CrashFreeRate = float64(rel.Sessions-rel.ErrorSessions) / float64(rel.Sessions)
		}
		releases = append(releases, rel)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// The releases themselves come from the APM version rollup, which a RUM span already feeds.
	deployments, err := serviceDeployments(r.Context(), sc, svcFilter{name: f.app, env: f.env}, from, to, gap)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": releases, "deployments": deployments})
	return nil
}
