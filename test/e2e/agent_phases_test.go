//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// M1 agent phases: log collection (files + discovery, rotation), process metrics with discovery
// ids, container inventory and container metrics.

type logRecordJSON struct {
	Timestamp    string            `json:"timestamp"`
	SeverityText string            `json:"severity_text"`
	Body         string            `json:"body"`
	HostID       string            `json:"host_id"`
	ServiceName  string            `json:"service_name"`
	Attributes   map[string]string `json:"attributes"`
}

const nginxRequestsPerPhase = 100

var (
	logRunID      = strconv.FormatInt(time.Now().UnixNano(), 36)
	logMarkerUsed bool
	markerRe      = regexp.MustCompile(`/e2e-log/[0-9a-z]+/[ab]/[0-9]+`)
)

// logMarker is the URL prefix of this run's nginx requests; it appears in access and error log lines.
func logMarker() string { return "/e2e-log/" + logRunID + "/" }

func listLogs(k string, q url.Values) ([]logRecordJSON, error) {
	var resp struct {
		Logs []logRecordJSON `json:"logs"`
	}
	_, err := apiGet(k, "/api/v1/logs", q, &resp)
	return resp.Logs, err
}

// nginxRequests sends n HTTP/1.0 requests for <marker><phase>/<i> to nginx inside the target. Every
// path is missing, so each request writes one access log line and one "[error] open() failed" line.
func nginxRequests(phase string, n int) error {
	script := fmt.Sprintf(`for i in $(seq 1 %d); do exec 3<>/dev/tcp/127.0.0.1/80; printf 'GET %s%s/%%s HTTP/1.0\r\nHost: localhost\r\n\r\n' "$i" >&3; cat <&3 >/dev/null; exec 3<&-; done`,
		n, logMarker(), phase)
	if out, err := compose("exec", "-T", "target", "bash", "-c", script); err != nil {
		return fmt.Errorf("nginx requests (%s): %v\n%s", phase, err, out)
	}
	return nil
}

func testAgentLogs(t *testing.T) {
	logMarkerUsed = true
	from := strconv.FormatInt(time.Now().Add(-2*time.Minute).UnixMilli(), 10)
	if err := nginxRequests("a", nginxRequestsPerPhase); err != nil {
		t.Fatal(err)
	}
	// logrotate-style rotation (rename + reopen, like postrotate `nginx -s reopen`) while the agent is
	// tailing: lines of phase a that were not read yet must still come from the renamed file.
	rotated := time.Now()
	if out, err := compose("exec", "-T", "target", "bash", "-c", "mv /var/log/nginx/access.log /var/log/nginx/access.log.1 && nginx -s reopen && sleep 1 && test -f /var/log/nginx/access.log"); err != nil {
		t.Fatalf("rotate access log: %v\n%s", err, out)
	}
	if err := nginxRequests("b", nginxRequestsPerPhase); err != nil {
		t.Fatal(err)
	}
	t.Logf("sent %d requests (2 x %d, access log rotated in between at %s), marker %s", 2*nginxRequestsPerPhase, nginxRequestsPerPhase, rotated.Format(time.TimeOnly), logMarker())

	want := 2 * nginxRequestsPerPhase
	var logs []logRecordJSON
	access, errorLog := map[string]int{}, map[string]int{}
	ok := eventually(t, 3*time.Minute, 3*time.Second, "every nginx request has its access and error log record", func() error {
		var err error
		logs, err = listLogs(key(), url.Values{"host_id": {targetID()}, "q": {logMarker()}, "from": {from}, "limit": {"5000"}})
		if err != nil {
			return err
		}
		clear(access)
		clear(errorLog)
		for _, l := range logs {
			m := markerRe.FindString(l.Body)
			if strings.Contains(l.Body, "open() ") {
				errorLog[m]++
			} else {
				access[m]++
			}
		}
		if len(access) != want || len(errorLog) != want {
			return fmt.Errorf("unique requests: access %d/%d (phase a %d, b %d), error %d/%d", len(access), want,
				countPhase(access, "a"), countPhase(access, "b"), len(errorLog), want)
		}
		return nil
	})
	dupes := 0
	for _, m := range []map[string]int{access, errorLog} {
		for _, c := range m {
			dupes += c - 1
		}
	}
	t.Logf("records: %d for %d requests (access %d unique, error %d unique, %d duplicates; delivery is at-least-once)", len(logs), want, len(access), len(errorLog), dupes)
	if !ok {
		return
	}

	// Attributes (semantic-conventions §4). Agent records have no service.name.
	paths := map[string]int{}
	for _, l := range logs {
		a := l.Attributes
		paths[a["log.file.path"]]++
		isError := strings.Contains(l.Body, "open() ")
		checks := map[string][2]string{
			"host_id":              {l.HostID, targetID()},
			"service_name":         {l.ServiceName, ""},
			"openlog.log.source":   {a["openlog.log.source"], "file"},
			"openlog.discovery.id": {a["openlog.discovery.id"], "nginx"},
		}
		if isError {
			checks["log.file.path"] = [2]string{a["log.file.path"], "/var/log/nginx/error.log"}
			checks["log.file.name"] = [2]string{a["log.file.name"], "error.log"}
			checks["severity_text"] = [2]string{l.SeverityText, "ERROR"}
			// error.log is not in logs.files: it is tailed only because nginx's rule lists it (auto_from_discovery).
			checks["e2e.source (absent)"] = [2]string{a["e2e.source"], ""}
		} else {
			if p := a["log.file.path"]; p != "/var/log/nginx/access.log" && p != "/var/log/nginx/access.log.1" {
				t.Errorf("access record path %q: %s", p, l.Body)
			}
			checks["e2e.source (logs.files attribute)"] = [2]string{a["e2e.source"], "logs.files"}
			if strings.Contains(l.Body, logMarker()+"b/") {
				checks["log.file.path (after rotation)"] = [2]string{a["log.file.path"], "/var/log/nginx/access.log"}
			}
		}
		for name, gw := range checks {
			if gw[0] != gw[1] {
				t.Errorf("%s = %q, want %q (body %q)", name, gw[0], gw[1], l.Body)
				break
			}
		}
	}
	t.Logf("records by log.file.path: %v", paths)

	// Server-side attribute filters (attr.<key>) agree with the client-side classification.
	count := func(q url.Values) (int, error) {
		q.Set("host_id", targetID())
		q.Set("q", logMarker())
		q.Set("from", from)
		q.Set("limit", "5000")
		ls, err := listLogs(key(), q)
		uniq := map[string]bool{}
		for _, l := range ls {
			uniq[markerRe.FindString(l.Body)] = true
		}
		return len(uniq), err
	}
	for _, c := range []struct {
		name string
		q    url.Values
		want int
	}{
		{"error.log by path", url.Values{"attr.log.file.path": {"/var/log/nginx/error.log"}}, want},
		{"nginx + file source", url.Values{"attr.openlog.discovery.id": {"nginx"}, "attr.openlog.log.source": {"file"}}, want},
		{"redis discovery id", url.Values{"attr.openlog.discovery.id": {"redis"}}, 0},
		{"journald source", url.Values{"attr.openlog.log.source": {"journald"}}, 0},
	} {
		if n, err := count(c.q); err != nil || n != c.want {
			t.Errorf("filter %s: %d unique requests (%v), want %d", c.name, n, err, c.want)
		}
	}
	if code, err := apiGet(key(), "/api/v1/logs", url.Values{"attr.body": {"x"}}, nil); code != http.StatusBadRequest {
		t.Errorf("non-allowlisted attr filter: HTTP %d %v, want 400", code, err)
	}

	// ClickHouse cross-check: rows vs. requests.
	rows, err := chQuery("SELECT count(), uniqExact(extract(body, '/e2e-log/[0-9a-z]+/[ab]/[0-9]+'), position(body, 'open() ') > 0) FROM openlog.logs WHERE host_id = {h:String} AND position(body, {m:String}) > 0",
		map[string]string{"h": targetID(), "m": logMarker()})
	if err != nil || len(rows) != 1 {
		t.Fatalf("clickhouse: %v %v", rows, err)
	}
	t.Logf("clickhouse logs: %s rows, %s unique (request, file) for %d requests x 2 files", rows[0][0], rows[0][1], want)
	if rows[0][1] != strconv.Itoa(2*want) {
		t.Errorf("clickhouse: %s unique (request, file) records, want %d", rows[0][1], 2*want)
	}
}

func countPhase(m map[string]int, phase string) int {
	n := 0
	for k := range m {
		if strings.Contains(k, "/"+phase+"/") {
			n++
		}
	}
	return n
}

// Forced rotation with unread lines (agent_log_rotation_unread). The agent tails
// /var/log/e2e-rotate/*.log (logs.files, default logs.rate_limit_lines). One exec appends
// rotateUnread lines to app.log, renames it to app.log.1 (outside the glob) and writes rotateNew
// lines to a new app.log. The tailer starts the burst with at most rotateRateLimit tokens and
// refills rotateRateLimit per second, so when the mv runs `ms` after the burst began at most
// rotateRateLimit*(1+ms/1000) burst lines were read: the rest exists only in the renamed file,
// which the agent must read to its end.
const (
	rotateUnread    = 10000
	rotateNew       = 2000
	rotateRateLimit = 2000 // logs.rate_limit_lines default
	rotateDir       = "/var/log/e2e-rotate"
)

func rotateMarker() string { return "/e2e-rotate/" + logRunID + "/" }

func testAgentLogRotationUnread(t *testing.T) {
	m := rotateMarker()
	params := map[string]string{"h": targetID(), "m": m}
	// The file must be tailed (at its end) before the burst: one line, then wait for its record.
	if out, err := compose("exec", "-T", "target", "bash", "-c", fmt.Sprintf("echo 'rotation %sa/0' >>%s/app.log", m, rotateDir)); err != nil {
		t.Fatalf("create %s/app.log: %v\n%s", rotateDir, err, out)
	}
	if !eventually(t, 2*time.Minute, 2*time.Second, "first line of "+rotateDir+"/app.log", func() error {
		rows, err := chQuery("SELECT count() FROM openlog.logs WHERE host_id = {h:String} AND position(body, {m:String}) > 0", params)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0][0] == "0" {
			return fmt.Errorf("no record yet (%v)", rows)
		}
		return nil
	}) {
		return
	}

	script := fmt.Sprintf(`set -e
cd %[1]s
start=$(date +%%s%%N)
awk -v m=%[2]q -v n=%[3]d 'BEGIN { for (i = 1; i <= n; i++) printf "rotation %%sa/%%d\n", m, i }' >>app.log
mv app.log app.log.1
rotated=$(( ($(date +%%s%%N) - start) / 1000000 ))
awk -v m=%[2]q -v n=%[4]d 'BEGIN { for (i = 1; i <= n; i++) printf "rotation %%sb/%%d\n", m, i }' >>app.log
echo "$rotated"`, rotateDir, m, rotateUnread, rotateNew)
	out, err := compose("exec", "-T", "target", "bash", "-c", script)
	if err != nil {
		t.Fatalf("burst + rotation: %v\n%s", err, out)
	}
	ms, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("burst + rotation output %q: %v", out, err)
	}
	unread := rotateUnread - rotateRateLimit*(1000+ms)/1000
	t.Logf("appended %d lines and renamed app.log after %d ms (>= %d lines unread at the rotation), then %d lines to the new file, marker %s", rotateUnread, ms, unread, rotateNew, m)
	if unread <= 0 {
		t.Fatalf("burst + rotation took %d ms: unread lines at the rotation are not guaranteed", ms)
	}

	want := 1 + rotateUnread + rotateNew
	const re = `'/e2e-rotate/[0-9a-z]+/[ab]/[0-9]+'`
	var rows [][]string
	ok := eventually(t, 4*time.Minute, 3*time.Second, "every line of app.log and app.log.1 has a record", func() error {
		var err error
		rows, err = chQuery(`SELECT count(), uniqExact(extract(body, `+re+`)), uniqExactIf(extract(body, `+re+`), position(body, '/a/') > 0),
  countIf(position(body, '\n') > 0), countIf(attributes['e2e.source'] != 'rotation')
FROM openlog.logs WHERE host_id = {h:String} AND position(body, {m:String}) > 0`, params)
		if err != nil {
			return err
		}
		if len(rows) != 1 || len(rows[0]) != 5 {
			return fmt.Errorf("clickhouse rows %v", rows)
		}
		if rows[0][1] != strconv.Itoa(want) {
			return fmt.Errorf("unique lines %s/%d (old file %s/%d)", rows[0][1], want, rows[0][2], 1+rotateUnread)
		}
		return nil
	})
	if len(rows) == 1 && len(rows[0]) == 5 {
		t.Logf("clickhouse: %s records, %s unique lines (%s from before the rotation), %s multi-line, %s without e2e.source=rotation", rows[0][0], rows[0][1], rows[0][2], rows[0][3], rows[0][4])
	}
	if !ok {
		return
	}
	if r := rows[0]; r[0] != strconv.Itoa(want) || r[2] != strconv.Itoa(1+rotateUnread) || r[3] != "0" || r[4] != "0" {
		t.Errorf("want each of %d lines exactly once (%d before the rotation), no multi-line record, all with e2e.source=rotation: got %v", want, 1+rotateUnread, r)
	}
	// After reading app.log.1 to its end (and the rotation grace period) the agent closes it.
	eventually(t, time.Minute, 2*time.Second, "agent closed app.log.1", func() error {
		out, err := compose("exec", "-T", "target", "bash", "-c", "ls -l /proc/$(pgrep -x openlog-infra-a)/fd | grep -c 'e2e-rotate/app.log.1' || true")
		if err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		if n := strings.TrimSpace(out); n != "0" {
			return fmt.Errorf("%s open descriptors", n)
		}
		return nil
	})
}

const journalEntries = 20

func journalMarker() string { return "/e2e-journal/" + logRunID + "/" }

// testAgentJournald writes entries to the target's systemd-journald (standalone, see
// target/entrypoint.sh) with systemd-cat (priority err, stdout stream) and logger (warning,
// through /dev/log); the agent reads the journal with journalctl (logs.journald.enabled).
func testAgentJournald(t *testing.T) {
	from := strconv.FormatInt(time.Now().Add(-2*time.Minute).UnixMilli(), 10)
	m := journalMarker()
	script := fmt.Sprintf(`set -e
for i in $(seq 1 %[1]d); do
  if [ $((i %% 2)) = 0 ]; then echo "journal entry %[2]s$i" | systemd-cat -t openlog-e2e -p err
  else logger --socket-errors=on -t openlog-e2e -p user.warning "journal entry %[2]s$i"; fi
done`, journalEntries, m)
	if out, err := compose("exec", "-T", "target", "bash", "-c", script); err != nil {
		t.Fatalf("write journal entries: %v\n%s", err, out)
	}
	t.Logf("wrote %d journal entries, marker %s", journalEntries, m)

	byBody := map[string]logRecordJSON{}
	var logs []logRecordJSON
	ok := eventually(t, 2*time.Minute, 3*time.Second, "every journal entry has a record", func() error {
		var err error
		logs, err = listLogs(key(), url.Values{"host_id": {targetID()}, "q": {m}, "from": {from}, "limit": {"1000"}, "attr.openlog.log.source": {"journald"}})
		if err != nil {
			return err
		}
		clear(byBody)
		for _, l := range logs {
			byBody[l.Body] = l
		}
		if len(byBody) != journalEntries {
			return fmt.Errorf("%d unique journald records, want %d", len(byBody), journalEntries)
		}
		return nil
	})
	t.Logf("records: %d (%d unique)", len(logs), len(byBody))
	if !ok {
		return
	}
	for i := 1; i <= journalEntries; i++ {
		body := fmt.Sprintf("journal entry %s%d", m, i)
		l, found := byBody[body]
		if !found {
			t.Errorf("no record with body %q", body)
			continue
		}
		sev := "WARN"
		if i%2 == 0 {
			sev = "ERROR"
		}
		checks := map[string][2]string{
			"host_id":                   {l.HostID, targetID()},
			"service_name":              {l.ServiceName, ""},
			"severity_text":             {l.SeverityText, sev},
			"openlog.log.source":        {l.Attributes["openlog.log.source"], "journald"},
			"openlog.syslog.identifier": {l.Attributes["openlog.syslog.identifier"], "openlog-e2e"},
		}
		for name, gw := range checks {
			if gw[0] != gw[1] {
				t.Errorf("%s = %q, want %q (body %q)", name, gw[0], gw[1], body)
			}
		}
		if l.Timestamp == "" {
			t.Errorf("no timestamp: %q", body)
		}
		if i == 1 {
			t.Logf("journald record: %+v", l)
		}
	}
	// Server-side filter by syslog identifier.
	ls, err := listLogs(key(), url.Values{"host_id": {targetID()}, "q": {m}, "from": {from}, "limit": {"1000"}, "attr.openlog.syslog.identifier": {"openlog-e2e"}})
	uniq := map[string]bool{}
	for _, l := range ls {
		uniq[l.Body] = true
	}
	if err != nil || len(uniq) != journalEntries {
		t.Errorf("filter attr.openlog.syslog.identifier=openlog-e2e: %d unique (%v), want %d", len(uniq), err, journalEntries)
	}
}

func testProcessMetrics(t *testing.T) {
	want := map[string]string{"nginx": "nginx", "redis-server": "redis"}
	eventually(t, 90*time.Second, 5*time.Second, "process.memory.usage for nginx and redis-server with openlog.discovery.id", func() error {
		m, err := recent(targetID(), "process.memory.usage", url.Values{"agg": {"last"}, "group_by": {"process.executable.name,openlog.discovery.id"}, "step": {"10s"}})
		if err != nil {
			return err
		}
		got := map[string][]string{}
		for _, s := range m.Series {
			if len(s.Points) == 0 || s.Points[len(s.Points)-1][1] <= 0 {
				continue
			}
			exe := s.Attributes["process.executable.name"]
			got[exe] = append(got[exe], s.Attributes["openlog.discovery.id"])
		}
		for exe, id := range want {
			if !slices.Contains(got[exe], id) {
				return fmt.Errorf("process %s: discovery ids %v, want %q (processes: %d)", exe, got[exe], id, len(got))
			}
		}
		if m.Metric.Unit != "By" {
			return fmt.Errorf("unit %q, want By", m.Metric.Unit)
		}
		t.Logf("process.memory.usage: nginx %v, redis-server %v, postgres %v, agent %v", got["nginx"], got["redis-server"], got["postgres"], got["openlog-infra-a"])
		return nil
	})
}

func testContainers(t *testing.T) {
	if strings.TrimSpace(getenvDefault("E2E_DOCKER", "1")) != "1" {
		t.Skip("E2E_DOCKER!=1: the target runs no dockerd")
	}
	eventually(t, 4*time.Minute, 5*time.Second, "container inventory item for e2e-sleeper", func() error {
		inv, err := inventory(targetID(), "container")
		if err != nil {
			return err
		}
		for _, it := range inv.Items {
			raw := string(it.Data)
			if strings.Contains(raw, "e2e-sleeper") && strings.Contains(raw, "openlog-e2e/sleeper") {
				var data map[string]any
				_ = json.Unmarshal(it.Data, &data)
				if !strings.Contains(raw, `"running"`) {
					return fmt.Errorf("container item %s not running: %s", it.Key, raw)
				}
				t.Logf("container item %s: %v", it.Key, data)
				return nil
			}
		}
		return fmt.Errorf("snapshot %s: %d container items, none is e2e-sleeper", inv.SnapshotID, len(inv.Items))
	})
	eventually(t, 2*time.Minute, 5*time.Second, "container.* metrics for e2e-sleeper", func() error {
		for _, name := range []string{"container.memory.usage", "container.cpu.time"} {
			m, err := recent(targetID(), name, url.Values{"group_by": {"container.name,container.image.name"}, "step": {"10s"}})
			if err != nil {
				return err
			}
			found := false
			for _, s := range m.Series {
				if s.Attributes["container.name"] == "e2e-sleeper" && len(s.Points) > 0 {
					found = true
					if name == "container.memory.usage" && s.Points[len(s.Points)-1][1] <= 0 {
						return fmt.Errorf("%s: last value %v", name, s.Points[len(s.Points)-1][1])
					}
					t.Logf("%s %v: %d points", name, s.Attributes, len(s.Points))
				}
			}
			if !found {
				return fmt.Errorf("%s: no series for container.name=e2e-sleeper (%d series)", name, len(m.Series))
			}
		}
		return nil
	})
}

func getenvDefault(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}
