package api

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/internal/api/query"
)

// requireHost returns 404 unless hostID has a host record in the caller's organization.
// The query is tenant-scoped like every other, so a host of another organization is
// indistinguishable from an unknown one.
func requireHost(r *http.Request, sc *query.Scope, hostID string) error {
	q := sc.From(query.Hosts).Columns("host_id").
		Where("host_id = {host_id:String}").Param("host_id", hostID).Limit(1)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := rows.Next()
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return notFound("host not found")
	}
	return nil
}

// logAttrFilters are the log record attributes accepted as `attr.<key>=<value>` equality
// filters on GET /api/v1/logs (semantic-conventions §4: infra agent log sources). The
// allowlist keeps filters on attributes the agent sets, not arbitrary user data.
var logAttrFilters = map[string]bool{
	"openlog.log.source":        true,
	"log.file.path":             true,
	"log.file.name":             true,
	"openlog.discovery.id":      true,
	"openlog.systemd.unit":      true,
	"openlog.syslog.identifier": true,
}

const maxAttrFilterValueBytes = 1024

// LogAttrFilterKeys returns the supported attribute filter keys, sorted.
func LogAttrFilterKeys() []string {
	keys := make([]string, 0, len(logAttrFilters))
	for k := range logAttrFilters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// addLogAttrFilters adds one bound `attributes[key] = value` condition per attr.<key> parameter.
func addLogAttrFilters(q *query.Select, qp url.Values) error {
	var keys []string
	for p := range qp {
		if k, ok := strings.CutPrefix(p, "attr."); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for i, k := range keys {
		if !logAttrFilters[k] {
			return badRequest("attr.%s: unsupported attribute filter (supported: %s)", truncate(k, 64), strings.Join(LogAttrFilterKeys(), ", "))
		}
		vals := qp["attr."+k]
		if len(vals) != 1 || vals[0] == "" || len(vals[0]) > maxAttrFilterValueBytes {
			return badRequest("attr.%s: exactly one non-empty value of at most %d bytes is required", k, maxAttrFilterValueBytes)
		}
		n := strconv.Itoa(i)
		q.Where("attributes[{attr_key_"+n+":String}] = {attr_value_"+n+":String}").
			Param("attr_key_"+n, k).Param("attr_value_"+n, vals[0])
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
