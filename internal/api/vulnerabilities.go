package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/vuln"
)

// Vulnerabilities (docs/contracts/api.md "Vulnerabilities", D-142). The findings are per host and therefore
// tenant data, read through the tenant-scoped query layer like every other telemetry read; the advisories
// they name come from a catalog that is shared by the whole installation, because OSV is public data.
//
// Reads only: a finding is produced by the matcher, not by a person. What a person does about it happens on
// the host.

// maxVulnRows bounds a listing.
const maxVulnRows = 1000

// SetVulnerabilities enables /api/v1/vulnerabilities/* with the catalog behind them. Must be called before Run.
func (s *Server) SetVulnerabilities(catalog vuln.Catalog) {
	s.vulnCatalog = catalog
	s.srv.Handler = s.Handler()
}

func (s *Server) vulnRoutes(mux *http.ServeMux) {
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/vulnerabilities", s.listVulnerabilities)
	route("GET /api/v1/vulnerabilities/{id}", s.getVulnerability)
	route("GET /api/v1/hosts/{host_id}/vulnerabilities", s.hostVulnerabilities)
	if s.vulnCatalog != nil {
		route("GET /api/v1/vulnerabilities/catalog/status", s.vulnerabilityCatalogStatus)
	}
}

type vulnGroupJSON struct {
	VulnID    string   `json:"vuln_id"`
	CVE       string   `json:"cve"`
	Severity  string   `json:"severity"`
	Score     float64  `json:"score"`
	Summary   string   `json:"summary"`
	Hosts     uint64   `json:"hosts"`
	Packages  []string `json:"packages"`
	FirstSeen string   `json:"first_seen"`
	LastSeen  string   `json:"last_seen"`
}

type vulnFindingJSON struct {
	HostID    string  `json:"host_id"`
	HostName  string  `json:"host_name"`
	VulnID    string  `json:"vuln_id"`
	CVE       string  `json:"cve"`
	Severity  string  `json:"severity"`
	Score     float64 `json:"score"`
	Ecosystem string  `json:"ecosystem"`
	Package   string  `json:"package"`
	Version   string  `json:"version"`
	FixedIn   string  `json:"fixed_in"`
	Summary   string  `json:"summary"`
	FirstSeen string  `json:"first_seen"`
	LastSeen  string  `json:"last_seen"`
}

// listVulnerabilities groups the open findings by advisory: the question a person starts with is "what is
// wrong with my fleet", not "what is wrong with host 7".
func (s *Server) listVulnerabilities(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	q := sc.From(query.HostVulnerabilities).Final().Columns(
		"vuln_id AS id",
		"any(cve) AS cve_id",
		"any(severity) AS sev",
		"toFloat64(max(score)) AS sc",
		"any(summary) AS sum",
		// Not `AS hosts`: the query layer refuses any fragment containing a table name, and `hosts` is one.
		"uniqExact(host_id) AS host_count",
		"arraySlice(arraySort(groupUniqArray(package)), 1, 20) AS pkgs",
		"toInt64(toUnixTimestamp64Milli(min(first_seen))) AS first_ms",
		"toInt64(toUnixTimestamp64Milli(max(last_seen))) AS last_ms",
	).Where("resolved_at = toDateTime(0)").GroupBy("vuln_id").Limit(maxVulnRows)
	if sev := r.URL.Query().Get("severity"); sev != "" {
		if !knownSeverity(sev) {
			return &apiError{http.StatusBadRequest, "invalid_argument", "severity must be one of " + strings.Join(vuln.Severities, ", ")}
		}
		q.Where("severity = {severity:String}").Param("severity", sev)
	}
	if hostID := r.URL.Query().Get("host_id"); hostID != "" {
		q.Where("host_id = {host_id:String}").Param("host_id", hostID)
	}
	// Critical first, then the advisory that affects the most hosts: the order a person works down.
	q.OrderBy("multiIf(sev = 'critical', 0, sev = 'high', 1, sev = 'medium', 2, sev = 'low', 3, 4)", "host_count DESC", "id")

	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []vulnGroupJSON{}
	counts := map[string]uint64{}
	for rows.Next() {
		var (
			g               vulnGroupJSON
			pkgs            []string
			firstMs, lastMs int64
			hosts           uint64
		)
		if err := rows.Scan(&g.VulnID, &g.CVE, &g.Severity, &g.Score, &g.Summary, &hosts, &pkgs, &firstMs, &lastMs); err != nil {
			return err
		}
		g.Hosts, g.Packages = hosts, pkgs
		if g.Packages == nil {
			g.Packages = []string{}
		}
		g.FirstSeen, g.LastSeen = formatMillis(firstMs), formatMillis(lastMs)
		counts[g.Severity]++
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	severities := map[string]uint64{}
	for _, sev := range vuln.Severities {
		severities[sev] = counts[sev]
	}
	writeJSON(w, http.StatusOK, map[string]any{"vulnerabilities": out, "severity_counts": severities})
	return nil
}

// getVulnerability lists the hosts one advisory was found on.
func (s *Server) getVulnerability(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	id := r.PathValue("id")
	if id == "" {
		return &apiError{http.StatusBadRequest, "invalid_argument", "the advisory id is required"}
	}
	findings, err := s.vulnFindings(r, sc, "vuln_id = {vuln_id:String}", "vuln_id", id)
	if err != nil {
		return err
	}
	if len(findings) == 0 {
		return notFound("no open finding for this advisory")
	}
	writeJSON(w, http.StatusOK, map[string]any{"vuln_id": id, "findings": findings})
	return nil
}

// hostVulnerabilities lists the open findings of one host, which is what the host's own screen shows.
func (s *Server) hostVulnerabilities(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	hostID := r.PathValue("host_id")
	if err := requireHost(r, sc, hostID); err != nil {
		return err
	}
	findings, err := s.vulnFindings(r, sc, "host_id = {host_id:String}", "host_id", hostID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"host_id": hostID, "findings": findings})
	return nil
}

func (s *Server) vulnFindings(r *http.Request, sc *query.Scope, where, param, value string) ([]vulnFindingJSON, error) {
	limit := maxVulnRows
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxVulnRows {
			return nil, &apiError{http.StatusBadRequest, "invalid_argument", "limit must be between 1 and 1000"}
		}
		limit = n
	}
	q := sc.From(query.HostVulnerabilities).Final().Columns(
		"host_id AS hid", "host_name AS hname", "vuln_id AS vid", "cve AS cve_id", "severity AS sev",
		"toFloat64(score) AS sc", "ecosystem AS eco", "package AS pkg", "version AS ver",
		"fixed_in AS fixed", "summary AS sum",
		"toInt64(toUnixTimestamp64Milli(first_seen)) AS first_ms",
		"toInt64(toUnixTimestamp64Milli(last_seen)) AS last_ms",
	).Where("resolved_at = toDateTime(0)").Where(where).Param(param, value).
		OrderBy("multiIf(sev = 'critical', 0, sev = 'high', 1, sev = 'medium', 2, sev = 'low', 3, 4)", "pkg", "vid").
		Limit(limit)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []vulnFindingJSON{}
	for rows.Next() {
		var f vulnFindingJSON
		var firstMs, lastMs int64
		if err := rows.Scan(&f.HostID, &f.HostName, &f.VulnID, &f.CVE, &f.Severity, &f.Score, &f.Ecosystem,
			&f.Package, &f.Version, &f.FixedIn, &f.Summary, &firstMs, &lastMs); err != nil {
			return nil, err
		}
		f.FirstSeen, f.LastSeen = formatMillis(firstMs), formatMillis(lastMs)
		out = append(out, f)
	}
	return out, rows.Err()
}

// vulnerabilityCatalogStatus says how fresh the catalog is, so "no findings" can be told from "the feed
// never synced" — the difference between a clean fleet and a broken feature.
func (s *Server) vulnerabilityCatalogStatus(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
	noStore(w)
	stats, err := s.vulnCatalog.Stats(r.Context())
	if err != nil {
		return err
	}
	syncs, err := s.vulnCatalog.SyncStatus(r.Context())
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(syncs))
	for _, st := range syncs {
		out = append(out, map[string]any{"source": st.Source, "ecosystem": st.Ecosystem,
			"synced_at": formatTime(st.SyncedAt), "count": st.Count, "error": st.Error})
	}
	body := map[string]any{"vulnerabilities": stats.Vulnerabilities, "affected_ranges": stats.AffectedRanges,
		"ecosystems": stats.Ecosystems, "last_synced_at": nil, "sources": out}
	if stats.LastSyncedAt != nil {
		body["last_synced_at"] = formatTime(*stats.LastSyncedAt)
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

func knownSeverity(s string) bool {
	for _, v := range vuln.Severities {
		if v == s {
			return true
		}
	}
	return false
}
