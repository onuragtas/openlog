package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/rum"
	"github.com/onuragtas/openlog/internal/symbols"
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
// The session half cannot come from the same place, because `rum_sessions` has no version column and a view
// cannot be altered (§3.7). It reads rum_release_sessions_1h (0103_release_sessions), which reaches the same 30
// days as the release list. A range starts at its first whole hour, and sessions before that migration are not
// in it, so the rates of an upgraded installation start from then.

// rumMaxReleases bounds the versions a request with no limit gets, which is what it always got. An application
// with more distinct versions than this in one window is not releasing, it is leaking build identifiers into
// service.version.
const rumMaxReleases = 200

// rumReleasesList computes the rates in the statement the way the handler does, so the order and the numbers
// can't disagree. A build that cannot report a crash has a null rate, which ClickHouse sorts last either way.
var rumReleasesList = listSpec{
	columns: map[string]string{
		"last_seen": "r_last", "first_seen": "r_first", "sessions": "r_sessions", "error_sessions": "r_errors",
		"error_free": "if(r_sessions = 0, 1, greatest(1 - r_errors / r_sessions, 0))",
		"crash_free": "if(r_capturing = 0, NULL, greatest(1 - r_crashed / r_capturing, 0))",
		// By its numbers, or 1.10 would read as older than 1.9.
		"version": "arrayMap(x -> toUInt32OrZero(x), splitByChar('.', r_version))",
	},
	sort:     "last_seen",
	asc:      map[string]bool{"version": true},
	tiebreak: []string{"r_version"},
	limit:    rumMaxReleases,
	maxLimit: 1000,
}

type rumReleaseJSON struct {
	Version string `json:"version"`
	// Sessions and ErrorSessions are the sessions seen in the range, and those of them that produced at
	// least one error span. Estimates past a few thousand, like every count of people here.
	Sessions      uint64 `json:"sessions"`
	ErrorSessions uint64 `json:"error_sessions"`
	// CrashedSessions had a crash, among the CapturingSessions of builds able to report one.
	CrashedSessions   uint64 `json:"crashed_sessions"`
	CapturingSessions uint64 `json:"capturing_sessions"`
	// ErrorFreeRate is the share of sessions with no error span; with no sessions it is 1 and not absent.
	ErrorFreeRate float64 `json:"error_free_rate"`
	// CrashFreeRate is ErrorFreeRate under its old name, kept for one release because consoles already read
	// it and the store's console updates slowly. It never measured crashes (mobile-crash.md §5).
	CrashFreeRate float64 `json:"crash_free_rate"`
	// SessionCrashFreeRate is what the name above promised. It is null for a version with no capturing
	// sessions, because 100% would be a false statement about a build that cannot report a crash.
	SessionCrashFreeRate *float64 `json:"session_crash_free_rate"`
	FirstSeen            string   `json:"first_seen"`
	LastSeen             string   `json:"last_seen"`
}

// rumReleasesSelect reads sessions per version from the hourly rollup.
//
// It is a function of its own so a test can build it without data: the handler reaches this query only
// after its filter parses, and a fragment the query layer refuses would otherwise be found in production
// (the same lesson as rumTimelineSelect).
func rumReleasesSelect(sc *query.Scope, f rumFilter, from, to time.Time, l listRequest) *query.Select {
	q := sc.From(query.RumReleaseSessions).Columns(
		"version AS r_version",
		// A session counts once however many errors it had, which is what "error-free session" means.
		"uniqCombined64Merge(sessions) AS r_sessions",
		"uniqCombined64Merge(error_sessions) AS r_errors",
		"uniqCombined64Merge(crashed_sessions) AS r_crashed",
		"uniqCombined64Merge(capturing_sessions) AS r_capturing",
		"min(first_seen) AS r_first",
		"max(last_seen) AS r_last",
	).
		Where("app = {r_app:String}").Param("r_app", f.app).
		Where("hour >= toStartOfHour(fromUnixTimestamp({t_from:Int64})) AND hour < fromUnixTimestamp({t_to:Int64})").
		Param("t_from", from.Unix()).Param("t_to", to.Unix()).
		GroupBy("r_version").OrderBy(rumReleasesList.order(l)...).Limit(l.fetch()).Offset(l.offset)
	if f.env != nil {
		q.Where("deployment_environment = {r_env:String}").Param("r_env", *f.env)
	}
	return q
}

// sessionsFreeOf returns the share of total not in bad, with sketches allowed to disagree by a little.
func sessionsFreeOf(bad, total uint64) float64 {
	if total == 0 || bad >= total {
		if total == 0 {
			return 1
		}
		return 0
	}
	return float64(total-bad) / float64(total)
}

type rumCrashJSON struct {
	GroupID  string  `json:"group_id"`
	Type     string  `json:"type"`
	Message  string  `json:"message"`
	Kind     string  `json:"kind"`
	Count    float64 `json:"count"`
	Sessions uint64  `json:"sessions"`
	Version  string  `json:"last_version"`
	LastSeen string  `json:"last_seen"`
	Stack    string  `json:"stack"`
	// Frame is the function this crash is grouped on, and empty when no frame could be read.
	Frame string `json:"frame"`
	// SymbolicatedFrames counts the frames symbols resolved. MissingSymbols says what to upload for the rest.
	SymbolicatedFrames int      `json:"symbolicated_frames"`
	MissingSymbols     []string `json:"missing_symbols"`

	last     time.Time
	sessions map[string]struct{}
}

// rumMaxCrashStacks bounds the distinct stacks one read symbolicates. It is the expensive part, and past this the list is sorted
// by count anyway, so what falls off is what happened least.
const rumMaxCrashStacks = 500

// rumMaxCrashSessions bounds the session ids read per stack. They come back as ids because one session can crash on two stacks, and
// counts could not be added up. Past this a figure reads low, on a crash that is already at the top of the list.
const rumMaxCrashSessions = 2000

// rumCrashesSelect lists crashes by error group, version and stack over the raw spans, because the stack only lives there. Crashes
// are rare and these rows are found by attribute, so this reads little, and it reaches the 7-day trace retention.
func rumCrashesSelect(sc *query.Scope, f rumFilter, from, to time.Time, limit int) *query.Select {
	q := sc.From(query.Spans).Columns(
		"error_group_id AS c_group",
		"any(error_type) AS c_type",
		"any(error_message) AS c_message",
		"any(attributes[{a_kind:String}]) AS c_kind",
		"sum(sample_weight) AS c_count",
		"groupUniqArray({r_max_sessions:UInt32})(attributes[{a_session:String}]) AS c_sessions",
		"resource_attributes['service.version'] AS c_version",
		"max(timestamp) AS c_last",
		"events_attributes[1]['exception.stacktrace'] AS c_stack",
	).
		Param("a_kind", rum.AttrCrashKind).Param("a_session", rum.AttrSessionID).Param("r_max_sessions", rumMaxCrashSessions).
		Where("service_name = {r_app:String}").Param("r_app", f.app).
		Where("attributes[{a_kind:String}] != ''").
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).
		GroupBy("c_group", "c_version", "c_stack").OrderBy("c_count DESC").Limit(limit)
	if f.env != nil {
		q.Where("deployment_environment = {r_env:String}").Param("r_env", *f.env)
	}
	return q
}

// crashKey is what a crash groups on here. The error group ignores the stack, because symbols are not read on the write path
// (mobile-crash.md §4), so two different crashes can share one, and it hashes the type, which R8 renames each release, so one
// crash can have many. Once symbols have named a frame neither holds, and the type and that frame say which crash this is.
// Until then the group stands, parted only by what the device named, or every abort would become one crash.
func crashKey(c rumCrashJSON) string {
	if c.SymbolicatedFrames > 0 {
		return c.Type + "\x00" + c.Frame
	}
	return c.GroupID + "\x00" + c.Frame
}

// mergeCrash groups stacks as they are read.
func mergeCrash(groups map[string]*rumCrashJSON, order *[]string, c rumCrashJSON) {
	key := crashKey(c)
	g := groups[key]
	if g == nil {
		groups[key] = &c
		*order = append(*order, key)
		return
	}
	g.Count += c.Count
	for id := range c.sessions {
		g.sessions[id] = struct{}{}
	}
	g.Sessions = uint64(len(g.sessions))
	seen := map[string]bool{}
	for _, m := range g.MissingSymbols {
		seen[m] = true
	}
	for _, m := range c.MissingSymbols {
		if !seen[m] {
			g.MissingSymbols = append(g.MissingSymbols, m)
		}
	}
	if c.last.After(g.last) {
		g.last, g.LastSeen, g.Version, g.Stack, g.SymbolicatedFrames = c.last, c.LastSeen, c.Version, c.Stack, c.SymbolicatedFrames
	}
}

// rumCrashes serves GET /api/v1/rum/crashes.
func (s *Server) rumCrashes(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), rumCrashesSelect(sc, f, from, to, min(rumMaxCrashStacks, s.cfg.MaxRows)))
	if err != nil {
		return err
	}
	defer rows.Close()
	var orgID string
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		orgID = p.OrgID
	}
	groups := map[string]*rumCrashJSON{}
	order := []string{}
	resolvers := map[string]symbols.Resolver{}
	for rows.Next() {
		var c rumCrashJSON
		var group uint64
		var sessions []string
		if err := rows.Scan(&group, &c.Type, &c.Message, &c.Kind, &c.Count, &sessions, &c.Version, &c.last, &c.Stack); err != nil {
			return err
		}
		c.sessions = make(map[string]struct{}, len(sessions))
		for _, id := range sessions {
			c.sessions[id] = struct{}{}
		}
		c.Sessions = uint64(len(c.sessions))
		c.GroupID, c.LastSeen = apm.GroupIDString(group), formatTime(c.last)
		resolver, ok := resolvers[c.Version]
		if !ok {
			resolver = s.crashResolver(r, orgID, f.app, c.Version)
			resolvers[c.Version] = resolver
		}
		res := symbols.Symbolicate(c.Stack, resolver)
		c.Type = resolver.ClassName(c.Type)
		if res.Stack != "" {
			c.Stack = res.Stack
		}
		c.Frame, c.SymbolicatedFrames, c.MissingSymbols = res.Frame, res.Resolved, res.Missing
		if c.MissingSymbols == nil {
			c.MissingSymbols = []string{}
		}
		if res.MissingMapping {
			c.MissingSymbols = append(c.MissingSymbols, "R8 mapping for version "+c.Version)
		}
		mergeCrash(groups, &order, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	crashes := make([]*rumCrashJSON, 0, len(order))
	for _, k := range order {
		crashes = append(crashes, groups[k])
	}
	sort.SliceStable(crashes, func(i, j int) bool { return crashes[i].Count > crashes[j].Count })
	if len(crashes) > rumMaxReleases {
		crashes = crashes[:rumMaxReleases]
	}
	writeJSON(w, http.StatusOK, map[string]any{"crashes": crashes})
	return nil
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

	l, err := rumReleasesList.parse(r, s.cfg.MaxRows)
	if err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), rumReleasesSelect(sc, f, from, to, l))
	if err != nil {
		return err
	}
	releases := []rumReleaseJSON{}
	for rows.Next() {
		var rel rumReleaseJSON
		var first, last time.Time
		if err := rows.Scan(&rel.Version, &rel.Sessions, &rel.ErrorSessions, &rel.CrashedSessions, &rel.CapturingSessions, &first, &last); err != nil {
			rows.Close()
			return err
		}
		rel.FirstSeen, rel.LastSeen = formatTime(first), formatTime(last)
		rel.ErrorFreeRate = sessionsFreeOf(rel.ErrorSessions, rel.Sessions)
		rel.CrashFreeRate = rel.ErrorFreeRate
		if rel.CapturingSessions > 0 {
			rate := sessionsFreeOf(rel.CrashedSessions, rel.CapturingSessions)
			rel.SessionCrashFreeRate = &rate
		}
		releases = append(releases, rel)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	releases, more := page(releases, l)

	// The releases themselves come from the APM version rollup, which a RUM span already feeds.
	deployments, err := serviceDeployments(r.Context(), sc, svcFilter{name: f.app, env: f.env}, from, to, gap)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": releases, "deployments": deployments, "has_more": more})
	return nil
}
