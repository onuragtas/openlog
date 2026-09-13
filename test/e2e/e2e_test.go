//go:build e2e

// Package e2e runs the openlog infra agent against the real backend (compose `single` profile)
// and asserts the results through the Query API and ClickHouse. See docs/operations/e2e.md.
//
//	go test -tags e2e -v -count=1 -timeout 45m ./test/e2e
//
// Environment:
//
//	E2E_KEEP=1        keep the stack running after the test (default: docker compose down -v)
//	E2E_SKIP_UP=1     reuse an already running stack (implies no build)
//	E2E_SKIP_BUILD=1  do not rebuild images
//	E2E_SKIP_OUTAGE=1 skip the backend and Kafka outage phases
//	E2E_DOCKER=0      no dockerd in the target (skips the containers phase)
//	E2E_UI=1          also run the Playwright UI phase against the stack (ui_test.go)
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

const (
	project       = "openlog-e2e"
	interval      = 10 * time.Second
	secretMarker  = "E2eSecret"
	debianPretty  = "Debian GNU/Linux 12 (bookworm)"
	targetName    = "e2e-target"
	plainName     = "e2e-plain"
	agentName     = "openlog-infra-agent"
	outageBackend = 60 * time.Second
	outageKafka   = 30 * time.Second
)

var (
	repoRoot  string
	env       map[string]string
	testStart = time.Now()
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	var err error
	if repoRoot, err = filepath.Abs("../.."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	envFile := os.Getenv("E2E_ENV_FILE") // another stack's ports and keys (apm_test.go TestAPMStandalone)
	if envFile == "" {
		envFile = filepath.Join(repoRoot, "test/e2e/e2e.env")
	}
	if env, err = readEnvFile(envFile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if os.Getenv("E2E_SKIP_UP") != "1" {
		args := []string{"up", "-d", "--wait", "--wait-timeout", "600"}
		if os.Getenv("E2E_SKIP_BUILD") != "1" {
			args = append(args, "--build")
		}
		start := time.Now()
		if out, err := compose(args...); err != nil {
			fmt.Fprintf(os.Stderr, "compose up failed: %v\n%s\n", err, out)
			dumpLogs()
			teardown()
			return 1
		}
		fmt.Printf("e2e: stack up in %s\n", time.Since(start).Round(time.Second))
	}
	code := m.Run()
	if code != 0 {
		dumpLogs()
	}
	teardown()
	return code
}

func teardown() {
	if os.Getenv("E2E_KEEP") == "1" {
		fmt.Println("e2e: E2E_KEEP=1, leaving the stack running (docker compose -p " + project + " ... down -v to remove)")
		return
	}
	if out, err := compose("down", "-v", "--remove-orphans", "--timeout", "10"); err != nil {
		fmt.Fprintf(os.Stderr, "compose down failed: %v\n%s\n", err, out)
	}
}

func dumpLogs() {
	out, _ := compose("logs", "--no-color", "--tail", "80", "openlog", "target", "plain")
	fmt.Printf("---- compose logs (tail) ----\n%s\n", out)
}

func composeArgs(args ...string) []string {
	base := []string{"compose", "-p", project,
		"-f", filepath.Join(repoRoot, "deploy/compose/docker-compose.yml"),
		"-f", filepath.Join(repoRoot, "test/e2e/docker-compose.e2e.yml"),
		"--env-file", filepath.Join(repoRoot, "test/e2e/e2e.env")}
	return append(base, args...)
}

func compose(args ...string) (string, error) {
	cmd := exec.Command("docker", composeArgs(args...)...)
	cmd.Dir = repoRoot
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out, sc.Err()
}

// ---- HTTP / ClickHouse helpers ----

var httpClient = &http.Client{Timeout: 60 * time.Second}

func apiURL(path string, q url.Values) string {
	u := "http://127.0.0.1:" + env["OPENLOG_API_PORT"] + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// apiGet decodes a Query API response authenticated with a read-only API key; it returns the HTTP status.
func apiGet(key, path string, q url.Values, out any) (int, error) {
	req, _ := http.NewRequest(http.MethodGet, apiURL(path, q), nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, body)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("GET %s: %v: %s", path, err, body)
		}
	}
	return resp.StatusCode, nil
}

// key and otherKey are the read-only API keys of the e2e organization and of a second
// organization (both created by the compose bootstrap services); ingestKey is the e2e
// organization's ingest license key used by the agents.
func key() string       { return env["E2E_API_KEY"] }
func otherKey() string  { return env["E2E_OTHER_API_KEY"] }
func ingestKey() string { return env["E2E_LICENSE_KEY"] }

// chQuery runs a ClickHouse query over HTTP and returns TSV rows. params are bound as {name:String}.
func chQuery(sql string, params map[string]string) ([][]string, error) {
	q := url.Values{"default_format": {"TSV"}}
	for k, v := range params {
		q.Set("param_"+k, v)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+env["CLICKHOUSE_HTTP_HOST_PORT"]+"/?"+q.Encode(), strings.NewReader(sql))
	req.Header.Set("X-ClickHouse-User", env["OPENLOG_CLICKHOUSE_USER"])
	req.Header.Set("X-ClickHouse-Key", env["OPENLOG_CLICKHOUSE_PASSWORD"])
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clickhouse: HTTP %d: %s", resp.StatusCode, body)
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		if line != "" {
			rows = append(rows, strings.Split(line, "\t"))
		}
	}
	return rows, nil
}

func chInt(t *testing.T, sql string, params map[string]string) int64 {
	t.Helper()
	rows, err := chQuery(sql, params)
	if err != nil || len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("clickhouse %q: rows=%v err=%v", sql, rows, err)
	}
	n, err := strconv.ParseInt(rows[0][0], 10, 64)
	if err != nil {
		t.Fatalf("clickhouse %q: %v", sql, err)
	}
	return n
}

// eventually polls fn until it returns nil or timeout elapses; on timeout it reports the last error.
func eventually(t *testing.T, timeout, every time.Duration, what string, fn func() error) bool {
	t.Helper()
	start := time.Now()
	var err error
	for {
		if err = fn(); err == nil {
			t.Logf("%s: ok after %s", what, time.Since(start).Round(100*time.Millisecond))
			return true
		}
		if time.Since(start) > timeout {
			t.Errorf("%s: not satisfied within %s: %v", what, timeout, err)
			return false
		}
		time.Sleep(every)
	}
}

// ---- API types ----

type hostJSON struct {
	HostID             string            `json:"host_id"`
	HostName           string            `json:"host_name"`
	OSDescription      string            `json:"os_description"`
	Arch               string            `json:"arch"`
	AgentVersion       string            `json:"agent_version"`
	LastSeen           string            `json:"last_seen"`
	ResourceAttributes map[string]string `json:"resource_attributes"`
}

type itemJSON struct {
	HostID   string          `json:"host_id"`
	HostName string          `json:"host_name"`
	Category string          `json:"category"`
	Key      string          `json:"key"`
	Data     json.RawMessage `json:"data"`
}

type inventoryJSON struct {
	SnapshotID   string     `json:"snapshot_id"`
	SnapshotTime *string    `json:"snapshot_time"`
	Items        []itemJSON `json:"items"`
}

func (inv inventoryJSON) time() time.Time {
	if inv.SnapshotTime == nil {
		return time.Time{}
	}
	ts, _ := time.Parse(time.RFC3339Nano, *inv.SnapshotTime)
	return ts
}

type seriesJSON struct {
	Attributes map[string]string `json:"attributes"`
	Points     [][2]float64      `json:"points"`
}

type metricJSON struct {
	Metric struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Unit string `json:"unit"`
	} `json:"metric"`
	Step   string       `json:"step"`
	Series []seriesJSON `json:"series"`
}

type serviceJSON struct {
	RuleID   string   `json:"rule_id"`
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Instance string   `json:"instance"`
	Version  string   `json:"version"`
	Packages []string `json:"packages"`
	Ports    []struct {
		Protocol string `json:"protocol"`
		Address  string `json:"address"`
		Port     int    `json:"port"`
	} `json:"ports"`
	Integration struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"integration"`
}

func machineID(name string) string { return strings.ToLower(env[name]) }

func targetID() string { return machineID("E2E_TARGET_MACHINE_ID") }
func plainID() string  { return machineID("E2E_PLAIN_MACHINE_ID") }

func inventory(hostID, category string) (inventoryJSON, error) {
	var inv inventoryJSON
	q := url.Values{}
	if category != "" {
		q.Set("category", category)
	}
	_, err := apiGet(key(), "/api/v1/hosts/"+hostID+"/inventory", q, &inv)
	return inv, err
}

func metrics(k, hostID, name string, from, to time.Time, q url.Values) (metricJSON, error) {
	var m metricJSON
	if q == nil {
		q = url.Values{}
	}
	q.Set("name", name)
	q.Set("from", strconv.FormatInt(from.UnixMilli(), 10))
	q.Set("to", strconv.FormatInt(to.UnixMilli(), 10))
	_, err := apiGet(k, "/api/v1/hosts/"+hostID+"/metrics", q, &m)
	return m, err
}

func recent(hostID, name string, q url.Values) (metricJSON, error) {
	return metrics(key(), hostID, name, testStart.Add(-time.Minute), time.Now().Add(time.Minute), q)
}

// counterLast returns the latest value of openlog.agent.export.items for outcome (summed over signals).
func counterLast(hostID, outcome string) (float64, error) {
	m, err := recent(hostID, "openlog.agent.export.items", url.Values{"agg": {"last"}, "group_by": {"outcome"}, "step": {"10s"}})
	if err != nil {
		return 0, err
	}
	for _, s := range m.Series {
		if s.Attributes["outcome"] == outcome && len(s.Points) > 0 {
			return s.Points[len(s.Points)-1][1], nil
		}
	}
	return 0, nil
}

func dataField[T any](raw json.RawMessage, field string) (T, bool) {
	var zero T
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return zero, false
	}
	v, ok := m[field]
	if !ok {
		return zero, false
	}
	var out T
	if json.Unmarshal(v, &out) != nil {
		return zero, false
	}
	return out, true
}

// ---- the test ----

func TestE2E(t *testing.T) {
	if !t.Run("wait_for_data", testWaitForData) {
		t.Fatal("no data from the agents; skipping the remaining phases")
	}
	t.Run("hosts", testHosts)
	t.Run("metrics", testMetrics)
	t.Run("inventory", testInventory)
	t.Run("services", testServices)
	t.Run("masking", testMasking)
	t.Run("inventory_search", testInventorySearch)
	t.Run("logs_exclude_inventory", testLogsExcludeInventory)
	t.Run("tenant_isolation", testTenantIsolation)
	t.Run("agent_logs", testAgentLogs)
	t.Run("process_metrics", testProcessMetrics)
	t.Run("containers", testContainers)
	t.Run("apm", testAPM)       // apm_test.go
	t.Run("alerts", testAlerts) // alerts_test.go
	t.Run("ui", testUI)         // skipped unless E2E_UI=1
	if os.Getenv("E2E_SKIP_OUTAGE") == "1" {
		t.Log("E2E_SKIP_OUTAGE=1: skipping outage phases")
		return
	}
	t.Run("resilience_backend_outage", testBackendOutage)
	t.Run("kafka_outage_503", testKafkaOutage)
}

func testWaitForData(t *testing.T) {
	eventually(t, 4*time.Minute, 3*time.Second, "both hosts have a complete snapshot and >= 60s of metrics", func() error {
		for _, id := range []string{targetID(), plainID()} {
			inv, err := inventory(id, "os")
			if err != nil {
				return err
			}
			if inv.SnapshotID == "" || len(inv.Items) == 0 {
				return fmt.Errorf("host %s: no complete snapshot yet", id)
			}
			m, err := recent(id, "system.uptime", url.Values{"step": {"10s"}})
			if err != nil {
				return err
			}
			if len(m.Series) == 0 || len(m.Series[0].Points) < 7 {
				return fmt.Errorf("host %s: not enough uptime points yet", id)
			}
		}
		return nil
	})
}

func testHosts(t *testing.T) {
	var resp struct {
		Hosts []hostJSON `json:"hosts"`
	}
	if _, err := apiGet(key(), "/api/v1/hosts", nil, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Hosts) != 2 {
		t.Errorf("hosts: got %d, want 2: %+v", len(resp.Hosts), resp.Hosts)
	}
	out, err := exec.Command("docker", "version", "-f", "{{.Server.Arch}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	wantArch := strings.TrimSpace(string(out))
	want := map[string]string{targetID(): targetName, plainID(): plainName}
	byID := map[string]hostJSON{}
	for _, h := range resp.Hosts {
		byID[h.HostID] = h
	}
	for id, name := range want {
		h, ok := byID[id]
		if !ok {
			t.Errorf("host %s (%s) not listed", id, name)
			continue
		}
		role := strings.TrimPrefix(name, "e2e-")
		checks := map[string][2]string{
			"host_name":                  {h.HostName, name},
			"os_description":             {h.OSDescription, debianPretty},
			"arch":                       {h.Arch, wantArch},
			"agent_version":              {h.AgentVersion, env["E2E_AGENT_VERSION"]},
			"attr openlog.agent.name":    {h.ResourceAttributes["openlog.agent.name"], agentName},
			"attr openlog.agent.version": {h.ResourceAttributes["openlog.agent.version"], env["E2E_AGENT_VERSION"]},
			"attr openlog.entity.type":   {h.ResourceAttributes["openlog.entity.type"], "host"},
			"attr os.type":               {h.ResourceAttributes["os.type"], "linux"},
			"attr env (extra)":           {h.ResourceAttributes["env"], "e2e"},
			"attr e2e.role (extra)":      {h.ResourceAttributes["e2e.role"], role},
		}
		for field, gw := range checks {
			if gw[0] != gw[1] {
				t.Errorf("host %s: %s = %q, want %q", name, field, gw[0], gw[1])
			}
		}
		if ls, err := time.Parse(time.RFC3339Nano, h.LastSeen); err != nil || time.Since(ls) > 2*time.Minute {
			t.Errorf("host %s: last_seen %q not recent (%v)", name, h.LastSeen, err)
		}
		var single hostJSON
		if _, err := apiGet(key(), "/api/v1/hosts/"+id, nil, &single); err != nil || single.HostName != name {
			t.Errorf("GET /hosts/%s: %+v %v", id, single, err)
		}
	}
}

func testMetrics(t *testing.T) {
	hid := targetID()

	t.Run("cpu_utilization", func(t *testing.T) {
		m, err := recent(hid, "system.cpu.utilization", url.Values{"group_by": {"cpu.mode"}, "step": {"10s"}})
		if err != nil {
			t.Fatal(err)
		}
		if m.Metric.Type != "gauge" || m.Metric.Unit != "1" {
			t.Errorf("metric meta = %+v", m.Metric)
		}
		modes := map[string]bool{}
		sums := map[float64]float64{}
		for _, s := range m.Series {
			mode := s.Attributes["cpu.mode"]
			modes[mode] = true
			if len(s.Points) == 0 {
				t.Errorf("cpu.mode=%s: no points", mode)
			}
			for _, p := range s.Points {
				if p[1] < 0 || p[1] > 1 {
					t.Errorf("cpu.mode=%s: value %v outside [0,1]", mode, p[1])
				}
				sums[p[0]] += p[1]
			}
		}
		for _, mode := range []string{"user", "system", "idle", "iowait", "nice", "interrupt", "softirq", "steal"} {
			if !modes[mode] {
				t.Errorf("cpu.mode=%s missing (have %v)", mode, modes)
			}
		}
		for ts, sum := range sums {
			if math.Abs(sum-1) > 0.02 {
				t.Errorf("cpu utilization modes at %d sum to %v, want ≈1", int64(ts), sum)
			}
		}
		t.Logf("cpu.utilization: %d modes, %d buckets", len(modes), len(sums))
	})

	t.Run("memory_usage_sums_to_limit", func(t *testing.T) {
		q := url.Values{"agg": {"last"}, "step": {"10s"}}
		limit, err := recent(hid, "system.memory.limit", q)
		if err != nil || len(limit.Series) != 1 {
			t.Fatalf("limit: %+v %v", limit, err)
		}
		q.Set("group_by", "system.memory.state")
		usage, err := recent(hid, "system.memory.usage", q)
		if err != nil {
			t.Fatal(err)
		}
		states := map[string]map[float64]float64{}
		for _, s := range usage.Series {
			pts := map[float64]float64{}
			for _, p := range s.Points {
				pts[p[0]] = p[1]
			}
			states[s.Attributes["system.memory.state"]] = pts
		}
		for _, st := range []string{"used", "free", "cached", "buffers"} {
			if len(states[st]) == 0 {
				t.Fatalf("system.memory.state=%s: no points (have %v)", st, slices.Collect(mapKeys(states)))
			}
		}
		checked := 0
		for _, p := range limit.Series[0].Points {
			sum, complete := 0.0, true
			for _, pts := range states {
				v, ok := pts[p[0]]
				complete = complete && ok
				sum += v
			}
			if !complete {
				continue
			}
			checked++
			if rel := math.Abs(sum-p[1]) / p[1]; rel > 0.01 {
				t.Errorf("at %d: sum(usage)=%v limit=%v (%.2f%% off)", int64(p[0]), sum, p[1], rel*100)
			}
		}
		if checked == 0 {
			t.Error("no bucket with all memory states and limit")
		}
		t.Logf("memory: %d buckets checked, limit %.0f bytes", checked, limit.Series[0].Points[0][1])
	})

	t.Run("filesystem", func(t *testing.T) {
		m, err := recent(hid, "system.filesystem.usage", url.Values{"group_by": {"system.filesystem.mountpoint,system.filesystem.state"}, "step": {"10s"}})
		if err != nil {
			t.Fatal(err)
		}
		mounts := map[string]bool{}
		for _, s := range m.Series {
			if len(s.Points) > 0 {
				mounts[s.Attributes["system.filesystem.mountpoint"]] = true
			}
		}
		if !mounts["/data"] {
			t.Errorf("filesystem usage: mountpoint /data (docker volume) missing; have %v", mounts)
		}
		u, err := recent(hid, "system.filesystem.utilization", url.Values{"step": {"10s"}})
		if err != nil || len(u.Series) == 0 {
			t.Fatalf("filesystem utilization: %+v %v", u, err)
		}
		for _, s := range u.Series {
			for _, p := range s.Points {
				if p[1] < 0 || p[1] > 1 {
					t.Errorf("filesystem utilization %v outside [0,1] (%v)", p[1], s.Attributes)
				}
			}
		}
		t.Logf("filesystem mountpoints: %v", slices.Sorted(mapKeys(mounts)))
	})

	rateCheck := func(t *testing.T, name, groupBy string, want []string) {
		m, err := recent(hid, name, url.Values{"agg": {"rate"}, "group_by": {groupBy}, "step": {"10s"}})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, s := range m.Series {
			got[s.Attributes[groupBy]] += len(s.Points)
			for _, p := range s.Points {
				if p[1] < 0 {
					t.Errorf("%s %v: negative rate %v", name, s.Attributes, p[1])
				}
			}
		}
		for _, w := range want {
			if got[w] == 0 {
				t.Errorf("%s rate by %s: %q has no points (have %v)", name, groupBy, w, got)
			}
		}
		t.Logf("%s rate points by %s: %v", name, groupBy, got)
	}
	t.Run("disk_rate", func(t *testing.T) {
		rateCheck(t, "system.disk.io", "disk.io.direction", []string{"read", "write"})
		rateCheck(t, "system.disk.operations", "disk.io.direction", []string{"read", "write"})
	})
	t.Run("network_rate", func(t *testing.T) {
		rateCheck(t, "system.network.io", "network.io.direction", []string{"receive", "transmit"})
		rateCheck(t, "system.network.io", "network.interface.name", []string{"eth0"})
		m, err := recent(hid, "system.network.io", url.Values{"agg": {"rate"}, "group_by": {"network.interface.name"}})
		if err == nil {
			for _, s := range m.Series {
				if s.Attributes["network.interface.name"] == "lo" {
					t.Error("loopback interface must be excluded by default")
				}
			}
		}
	})

	t.Run("collection_interval", func(t *testing.T) {
		for _, id := range []string{targetID(), plainID()} {
			m, err := recent(id, "openlog.agent.collection.interval", url.Values{"step": {"10s"}})
			if err != nil || len(m.Series) != 1 || len(m.Series[0].Points) == 0 {
				t.Fatalf("host %s: %+v %v", id, m, err)
			}
			for _, p := range m.Series[0].Points {
				if p[1] != interval.Seconds() {
					t.Errorf("host %s: collection interval %v at %d, want %v", id, p[1], int64(p[0]), interval.Seconds())
				}
			}
		}
	})

	t.Run("export_items_sent_increasing", func(t *testing.T) {
		m, err := recent(hid, "openlog.agent.export.items", url.Values{"agg": {"last"}, "group_by": {"outcome"}, "step": {"10s"}})
		if err != nil {
			t.Fatal(err)
		}
		var sent []float64
		outcomes := []string{}
		for _, s := range m.Series {
			outcomes = append(outcomes, s.Attributes["outcome"])
			if s.Attributes["outcome"] == "sent" {
				for _, p := range s.Points {
					sent = append(sent, p[1])
				}
			}
		}
		sort.Strings(outcomes)
		if !slices.Equal(outcomes, []string{"buffered", "dropped", "sent"}) {
			t.Errorf("outcomes = %v", outcomes)
		}
		if len(sent) < 3 {
			t.Fatalf("sent: only %d points", len(sent))
		}
		for i := 1; i < len(sent); i++ {
			if sent[i] < sent[i-1] {
				t.Errorf("sent counter decreased: %v -> %v", sent[i-1], sent[i])
			}
		}
		if sent[len(sent)-1] <= sent[0] {
			t.Errorf("sent counter not increasing: first %v last %v", sent[0], sent[len(sent)-1])
		}
		t.Logf("export.items{outcome=sent}: %v -> %v over %d buckets", sent[0], sent[len(sent)-1], len(sent))
	})
}

func mapKeys[K comparable, V any](m map[K]V) func(func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

var (
	ipv6Key   = regexp.MustCompile(`^(tcp|udp):\[[0-9a-fA-F:.]+\]:\d+$`)
	dpkgEpoch = regexp.MustCompile(`^[0-9]+:`)
)

func testInventory(t *testing.T) {
	for _, h := range []struct {
		id, name string
		services bool
	}{{targetID(), targetName, true}, {plainID(), plainName, false}} {
		t.Run(h.name, func(t *testing.T) {
			inv, err := inventory(h.id, "")
			if err != nil {
				t.Fatal(err)
			}
			if inv.SnapshotID == "" {
				t.Fatal("no complete snapshot")
			}
			byCat := map[string][]itemJSON{}
			for _, it := range inv.Items {
				byCat[it.Category] = append(byCat[it.Category], it)
			}
			counts := map[string]int{}
			for c, items := range byCat {
				counts[c] = len(items)
			}
			t.Logf("snapshot %s at %s: %d items %v", inv.SnapshotID, *inv.SnapshotTime, len(inv.Items), counts)
			for _, c := range []string{"os", "hardware", "package", "systemd_unit", "listening_port", "process", "user", "mount", "network_interface"} {
				if h.name == plainName && (c == "listening_port" || c == "systemd_unit") {
					continue // a plain container has no listeners and (almost) no unit files
				}
				if len(byCat[c]) == 0 {
					t.Errorf("category %s missing", c)
				}
			}

			// item_count of the snapshot record equals the items returned.
			rows, err := chQuery("SELECT item_count FROM openlog.inventory_snapshots WHERE host_id = {h:String} AND snapshot_id = {s:String} LIMIT 1",
				map[string]string{"h": h.id, "s": inv.SnapshotID})
			if err != nil || len(rows) != 1 {
				t.Fatalf("snapshot record: %v %v", rows, err)
			}
			if rows[0][0] != strconv.Itoa(len(inv.Items)) {
				t.Errorf("item_count = %s, API returned %d items", rows[0][0], len(inv.Items))
			}

			if os, ok := firstKey(byCat["os"], "os"); ok {
				if v, _ := dataField[string](os.Data, "pretty_name"); v != debianPretty {
					t.Errorf("os.pretty_name = %q", v)
				}
				if v, _ := dataField[string](os.Data, "hostname"); v != h.name {
					t.Errorf("os.hostname = %q, want %q", v, h.name)
				}
			} else {
				t.Error("os item missing")
			}
			for _, k := range []string{"cpu", "memory"} {
				if _, ok := firstKey(byCat["hardware"], k); !ok {
					t.Errorf("hardware/%s missing", k)
				}
			}
			dpkg := 0
			for _, it := range byCat["package"] {
				if strings.HasPrefix(it.Key, "dpkg:") {
					dpkg++
				}
			}
			if h.services && dpkg <= 100 {
				t.Errorf("dpkg packages = %d, want > 100", dpkg)
			}
			if dpkg == 0 {
				t.Error("no dpkg packages")
			}
			if _, ok := firstKey(byCat["user"], "root"); !ok {
				t.Error("user root missing")
			}
			if _, ok := firstKey(byCat["network_interface"], "eth0"); !ok {
				t.Error("network_interface eth0 missing")
			}
			if _, ok := firstKey(byCat["mount"], "/data"); h.services && !ok {
				t.Error("mount /data missing")
			}
			if !h.services {
				return
			}
			if _, ok := firstKey(byCat["systemd_unit"], "nginx.service"); !ok {
				t.Error("systemd_unit nginx.service missing")
			}

			ports := map[int][]itemJSON{}
			for _, it := range byCat["listening_port"] {
				p, _ := dataField[int](it.Data, "port")
				ports[p] = append(ports[p], it)
				if fam, _ := dataField[int](it.Data, "family"); fam == 6 && !ipv6Key.MatchString(it.Key) {
					t.Errorf("IPv6 listening_port key %q not in proto:[addr]:port form", it.Key)
				}
				if fam, _ := dataField[int](it.Data, "family"); fam == 4 && strings.Contains(it.Key, "[") {
					t.Errorf("IPv4 listening_port key %q must not be bracketed", it.Key)
				}
			}
			for port, proc := range map[int]string{80: "nginx", 6379: "redis-server", 5432: "postgres"} {
				if len(ports[port]) == 0 {
					t.Errorf("listening port %d missing", port)
					continue
				}
				for _, it := range ports[port] {
					if name, _ := dataField[string](it.Data, "process_name"); name != proc {
						t.Errorf("listening port %s: process_name %q, want %q", it.Key, name, proc)
					}
				}
			}
			keys := []string{}
			for _, it := range byCat["listening_port"] {
				keys = append(keys, it.Key)
			}
			for _, k := range []string{"tcp:0.0.0.0:80", "tcp:[::]:80"} {
				if !slices.Contains(keys, k) {
					t.Errorf("listening_port key %q missing (have %v)", k, keys)
				}
			}
			t.Logf("listening ports: %v", keys)
		})
	}
}

func firstKey(items []itemJSON, key string) (itemJSON, bool) {
	for _, it := range items {
		if it.Key == key {
			return it, true
		}
	}
	return itemJSON{}, false
}

func services(t *testing.T, hostID string) (map[string]serviceJSON, map[string]string) {
	t.Helper()
	inv, err := inventory(hostID, "")
	if err != nil {
		t.Fatal(err)
	}
	var svcResp inventoryJSON
	if _, err := apiGet(key(), "/api/v1/hosts/"+hostID+"/services", nil, &svcResp); err != nil {
		t.Fatal(err)
	}
	out := map[string]serviceJSON{}
	for _, it := range svcResp.Items {
		if it.Category != "discovered_service" {
			t.Errorf("services endpoint returned category %q", it.Category)
		}
		var s serviceJSON
		if err := json.Unmarshal(it.Data, &s); err != nil {
			t.Errorf("service %s: %v", it.Key, err)
			continue
		}
		if !strings.HasPrefix(it.Key, s.RuleID+":") {
			t.Errorf("service key %q does not start with rule id %q", it.Key, s.RuleID)
		}
		out[s.RuleID] = s
	}
	pkgs := map[string]string{}
	for _, it := range inv.Items {
		if it.Category == "package" {
			v, _ := dataField[string](it.Data, "version")
			pkgs[it.Key] = v
		}
	}
	return out, pkgs
}

func testServices(t *testing.T) {
	t.Run(targetName, func(t *testing.T) {
		svcs, pkgs := services(t, targetID())
		ids := slices.Sorted(mapKeys(svcs))
		t.Logf("discovered: %v", ids)
		for _, w := range []struct {
			rule, status, pkg string
			port              int
		}{
			{"nginx", "enabled", "dpkg:nginx", 80},
			{"redis", "enabled", "dpkg:redis-server", 6379},
			{"postgresql", "needs_configuration", "dpkg:postgresql-15", 5432},
		} {
			s, ok := svcs[w.rule]
			if !ok {
				t.Errorf("service %s not discovered (have %v)", w.rule, ids)
				continue
			}
			t.Logf("%s: version=%q instance=%q integration=%+v packages=%v ports=%v", w.rule, s.Version, s.Instance, s.Integration, s.Packages, s.Ports)
			if s.Version == "" {
				t.Errorf("%s: empty version", w.rule)
			} else if pv := dpkgEpoch.ReplaceAllString(pkgs[w.pkg], ""); pv == "" || !strings.HasPrefix(pv, s.Version) {
				t.Errorf("%s: version %q does not match package %s version %q", w.rule, s.Version, w.pkg, pv)
			}
			if s.Integration.Status != w.status || s.Integration.ID != w.rule {
				t.Errorf("%s: integration %+v, want id %s status %s", w.rule, s.Integration, w.rule, w.status)
			}
			if !slices.Contains(s.Packages, w.pkg) {
				t.Errorf("%s: packages %v missing %s", w.rule, s.Packages, w.pkg)
			}
			found := false
			for _, p := range s.Ports {
				found = found || p.Port == w.port
			}
			if !found {
				t.Errorf("%s: port %d not linked (ports %v)", w.rule, w.port, s.Ports)
			}
		}
	})
	t.Run(plainName, func(t *testing.T) {
		svcs, _ := services(t, plainID())
		t.Logf("discovered: %v", slices.Sorted(mapKeys(svcs)))
		for id, s := range svcs {
			if s.Category == "database" || s.Category == "web" || slices.Contains([]string{"nginx", "redis", "postgresql", "mysql", "mariadb"}, id) {
				t.Errorf("plain host must not have service %s (%s)", id, s.Category)
			}
		}
	})
}

func testMasking(t *testing.T) {
	inv, err := inventory(targetID(), "process")
	if err != nil {
		t.Fatal(err)
	}
	var cmdline string
	for _, it := range inv.Items {
		if exe, _ := dataField[string](it.Data, "exe"); exe == "/usr/local/bin/mysql" {
			cmdline, _ = dataField[string](it.Data, "cmdline")
		}
	}
	if cmdline == "" {
		t.Fatal("process /usr/local/bin/mysql not in process inventory")
	}
	t.Logf("masked cmdline: %s", cmdline)
	if strings.Contains(cmdline, secretMarker) {
		t.Errorf("cmdline leaks the secret: %s", cmdline)
	}
	for _, want := range []string{"-p***", "--password=***", "DB_TOKEN=***", "mysql://app:***@db.internal:3306/app", "-uapp"} {
		if !strings.Contains(cmdline, want) {
			t.Errorf("cmdline %q does not contain %q", cmdline, want)
		}
	}
	p := map[string]string{"s": secretMarker}
	if n := chInt(t, "SELECT count() FROM openlog.inventory_items WHERE positionCaseInsensitive(data, {s:String}) > 0 OR positionCaseInsensitive(item_key, {s:String}) > 0", p); n != 0 {
		t.Errorf("inventory_items: %d rows contain the secret", n)
	}
	if n := chInt(t, "SELECT count() FROM openlog.logs WHERE positionCaseInsensitive(body, {s:String}) > 0 OR arrayExists(v -> positionCaseInsensitive(v, {s:String}) > 0, mapValues(attributes)) OR arrayExists(v -> positionCaseInsensitive(v, {s:String}) > 0, mapValues(resource_attributes))", p); n != 0 {
		t.Errorf("logs: %d rows contain the secret", n)
	}
	// Positive control: the grep does find the masked form.
	if n := chInt(t, "SELECT count() FROM openlog.inventory_items WHERE position(data, {s:String}) > 0", map[string]string{"s": "mysql://app:***@"}); n == 0 {
		t.Error("positive control: masked cmdline not found in inventory_items")
	}
}

func testInventorySearch(t *testing.T) {
	var resp struct {
		Items []itemJSON `json:"items"`
	}
	if _, err := apiGet(key(), "/api/v1/inventory/search", url.Values{"category": {"package"}, "q": {"openssl"}}, &resp); err != nil {
		t.Fatal(err)
	}
	hosts := map[string]string{}
	for _, it := range resp.Items {
		if it.Category != "package" || !strings.Contains(strings.ToLower(it.Key), "openssl") {
			t.Errorf("unexpected item %+v", it)
		}
		hosts[it.HostID] = it.HostName
	}
	want := map[string]string{targetID(): targetName, plainID(): plainName}
	for id, name := range want {
		if hosts[id] != name {
			t.Errorf("search: host %s (%s) missing or misnamed: %v", id, name, hosts)
		}
	}
	if _, err := apiGet(key(), "/api/v1/inventory/search", url.Values{"q": {"openssl"}}, nil); err == nil {
		t.Error("search without category must be rejected")
	}
}

func testLogsExcludeInventory(t *testing.T) {
	var resp struct {
		Logs []struct {
			Body       string            `json:"body"`
			Attributes map[string]string `json:"attributes"`
		} `json:"logs"`
	}
	q := url.Values{"from": {strconv.FormatInt(testStart.Add(-time.Hour).UnixMilli(), 10)}, "limit": {"1000"}}
	if _, err := apiGet(key(), "/api/v1/logs", q, &resp); err != nil {
		t.Fatal(err)
	}
	for _, l := range resp.Logs {
		if strings.HasPrefix(l.Attributes["event.name"], "openlog.inventory.") || l.Attributes["openlog.inventory.snapshot_id"] != "" {
			t.Errorf("logs API returned an inventory event: %+v", l)
		}
	}
	t.Logf("logs API returned %d records (none inventory)", len(resp.Logs))
	if n := chInt(t, "SELECT count() FROM openlog.logs WHERE startsWith(event_name, 'openlog.inventory.') OR mapContains(attributes, 'openlog.inventory.snapshot_id')", nil); n != 0 {
		t.Errorf("logs table holds %d inventory events", n)
	}
	if n := chInt(t, "SELECT count() FROM openlog.inventory_items", nil); n == 0 {
		t.Error("inventory_items is empty")
	}
}

func testTenantIsolation(t *testing.T) {
	var hosts struct {
		Hosts []hostJSON `json:"hosts"`
	}
	if _, err := apiGet(otherKey(), "/api/v1/hosts", nil, &hosts); err != nil {
		t.Fatal(err)
	}
	if len(hosts.Hosts) != 0 {
		t.Errorf("other tenant sees %d hosts", len(hosts.Hosts))
	}
	if code, _ := apiGet(otherKey(), "/api/v1/hosts/"+targetID(), nil, nil); code != http.StatusNotFound {
		t.Errorf("other tenant GET host: HTTP %d, want 404", code)
	}
	var search struct {
		Items []itemJSON `json:"items"`
	}
	if _, err := apiGet(otherKey(), "/api/v1/inventory/search", url.Values{"category": {"package"}}, &search); err != nil || len(search.Items) != 0 {
		t.Errorf("other tenant search: %d items %v", len(search.Items), err)
	}
	// Host sub-resources: another organization's host is indistinguishable from an unknown host (404).
	unknownHost := "0e2e00000000000000000000000000ff"
	metricQ := url.Values{"name": {"system.cpu.utilization"}}
	for _, c := range []struct {
		name, key, host, sub string
		q                    url.Values
	}{
		{"other tenant inventory", otherKey(), targetID(), "/inventory", nil},
		{"other tenant services", otherKey(), targetID(), "/services", nil},
		{"other tenant metrics", otherKey(), targetID(), "/metrics", metricQ},
		{"unknown host", key(), unknownHost, "", nil},
		{"unknown host inventory", key(), unknownHost, "/inventory", nil},
		{"unknown host services", key(), unknownHost, "/services", nil},
		{"unknown host metrics", key(), unknownHost, "/metrics", metricQ},
	} {
		if code, err := apiGet(c.key, "/api/v1/hosts/"+c.host+c.sub, c.q, nil); code != http.StatusNotFound || !strings.Contains(fmt.Sprint(err), `"not_found"`) {
			t.Errorf("%s: HTTP %d (%v), want 404 not_found", c.name, code, err)
		}
	}
	if code, _ := apiGet(key(), "/api/v1/hosts/"+targetID()+"/services", nil, nil); code != http.StatusOK {
		t.Errorf("own host services: HTTP %d, want 200", code)
	}
	var otherLogs struct {
		Logs []json.RawMessage `json:"logs"`
	}
	if _, err := apiGet(otherKey(), "/api/v1/logs", url.Values{"host_id": {targetID()}, "attr.openlog.discovery.id": {"nginx"}, "from": {strconv.FormatInt(testStart.Add(-time.Hour).UnixMilli(), 10)}}, &otherLogs); err != nil || len(otherLogs.Logs) != 0 {
		t.Errorf("other tenant logs with attribute filter: %d records %v", len(otherLogs.Logs), err)
	}
	if code, _ := apiGet("not-a-key", "/api/v1/hosts", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("invalid key: HTTP %d, want 401", code)
	}
	// Ingest license keys are not Query API credentials.
	if code, _ := apiGet(ingestKey(), "/api/v1/hosts", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("ingest license key on the query API: HTTP %d, want 401", code)
	}
}

// pointsCover asserts that host metric buckets cover [from, to] with gaps of at most 2 intervals.
func pointsCover(hostID string, from, to time.Time) error {
	m, err := metrics(key(), hostID, "system.uptime", from.Add(-interval), to.Add(interval), url.Values{"step": {"10s"}})
	if err != nil {
		return err
	}
	if len(m.Series) != 1 || len(m.Series[0].Points) == 0 {
		return fmt.Errorf("no system.uptime points in [%s, %s]", from.Format(time.TimeOnly), to.Format(time.TimeOnly))
	}
	var ts []time.Time
	for _, p := range m.Series[0].Points {
		ts = append(ts, time.UnixMilli(int64(p[0])))
	}
	maxGap := 2 * interval
	if ts[0].Sub(from) > maxGap {
		return fmt.Errorf("first point %s is more than %s after window start %s", ts[0].Format(time.TimeOnly), maxGap, from.Format(time.TimeOnly))
	}
	if to.Sub(ts[len(ts)-1]) > maxGap {
		return fmt.Errorf("last point %s is more than %s before window end %s", ts[len(ts)-1].Format(time.TimeOnly), maxGap, to.Format(time.TimeOnly))
	}
	for i := 1; i < len(ts); i++ {
		if ts[i].Sub(ts[i-1]) > maxGap {
			return fmt.Errorf("gap %s -> %s", ts[i-1].Format(time.TimeOnly), ts[i].Format(time.TimeOnly))
		}
	}
	return nil
}

func testBackendOutage(t *testing.T) {
	buffered0, err := counterLast(targetID(), "buffered")
	if err != nil {
		t.Fatal(err)
	}
	stopStart := time.Now()
	if out, err := compose("stop", "openlog"); err != nil {
		t.Fatalf("stop openlog: %v\n%s", err, out)
	}
	down := time.Now()
	t.Logf("openlog stopped in %s at %s", down.Sub(stopStart).Round(100*time.Millisecond), down.Format(time.TimeOnly))

	// Change the listening port set during the outage so the agent sends a snapshot that can only
	// reach the backend through the buffer.
	time.Sleep(20 * time.Second)
	if out, err := compose("exec", "-T", "target", "redis-server", "--port", "6390", "--bind", "127.0.0.1", "--daemonize", "yes", "--save", "", "--logfile", "/tmp/redis-6390.log"); err != nil {
		t.Fatalf("start extra redis: %v\n%s", err, out)
	}
	time.Sleep(time.Until(down.Add(outageBackend)))

	up := time.Now()
	if out, err := compose("up", "-d", "--no-recreate", "--wait", "openlog"); err != nil {
		t.Fatalf("start openlog: %v\n%s", err, out)
	}
	t.Logf("openlog down for %s; healthy again %s after start", up.Sub(down).Round(time.Second), time.Since(up).Round(100*time.Millisecond))

	eventually(t, 90*time.Second, 2*time.Second, "snapshot taken during the outage becomes the latest complete snapshot", func() error {
		inv, err := inventory(targetID(), "listening_port")
		if err != nil {
			return err
		}
		// The 6390 listener proves the snapshot was collected after the change.
		st := inv.time()
		if st.Before(down) || st.After(up) {
			return fmt.Errorf("latest snapshot %s at %s is not from the outage window [%s, %s]", inv.SnapshotID, st.Format(time.TimeOnly), down.Format(time.TimeOnly), up.Format(time.TimeOnly))
		}
		if _, ok := firstKey(inv.Items, "tcp:127.0.0.1:6390"); !ok {
			return fmt.Errorf("latest snapshot %s lacks tcp:127.0.0.1:6390", inv.SnapshotID)
		}
		return nil
	})
	for _, h := range []struct{ id, name string }{{targetID(), targetName}, {plainID(), plainName}} {
		eventually(t, 90*time.Second, 3*time.Second, h.name+": metric points replayed for the outage window", func() error {
			return pointsCover(h.id, down, up)
		})
	}
	eventually(t, 60*time.Second, 3*time.Second, "export.items{outcome=buffered} rose", func() error {
		v, err := counterLast(targetID(), "buffered")
		if err != nil {
			return err
		}
		if v <= buffered0 {
			return fmt.Errorf("buffered %v, before outage %v", v, buffered0)
		}
		t.Logf("export.items{outcome=buffered}: %v -> %v", buffered0, v)
		return nil
	})
	if dropped, err := counterLast(targetID(), "dropped"); err != nil || dropped != 0 {
		t.Errorf("export.items{outcome=dropped} = %v (%v), want 0", dropped, err)
	}
}

// probeRequest is a tiny OTLP metrics request without host.id (so it creates no host).
func probeRequest() []byte {
	req := &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "e2e-probe"}}}}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
			Name: "e2e.probe", Unit: "1",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
				TimeUnixNano: uint64(time.Now().UnixNano()), Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 1},
			}}}},
		}}}},
	}}}
	b, _ := proto.Marshal(req)
	return b
}

func postProbe() (int, string, time.Duration, error) {
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+env["OPENLOG_OTLP_HTTP_PORT"]+"/v1/metrics", bytes.NewReader(probeRequest()))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("openlog-license-key", ingestKey())
	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, "", time.Since(start), err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Get("Retry-After"), time.Since(start), nil
}

func testKafkaOutage(t *testing.T) {
	if code, _, _, err := postProbe(); code != http.StatusOK {
		t.Fatalf("probe before outage: HTTP %d %v", code, err)
	}
	sent0, err := counterLast(targetID(), "sent")
	if err != nil {
		t.Fatal(err)
	}
	down := time.Now()
	if out, err := compose("stop", "kafka"); err != nil {
		t.Fatalf("stop kafka: %v\n%s", err, out)
	}
	t.Logf("kafka stopped in %s", time.Since(down).Round(100*time.Millisecond))

	eventually(t, 60*time.Second, time.Second, "ingest answers 503 with Retry-After", func() error {
		code, ra, took, err := postProbe()
		if err != nil {
			return err
		}
		if code != http.StatusServiceUnavailable || ra == "" {
			return fmt.Errorf("HTTP %d Retry-After %q", code, ra)
		}
		if n, err := strconv.Atoi(ra); err != nil || n <= 0 {
			return fmt.Errorf("Retry-After %q is not a positive number of seconds", ra)
		}
		t.Logf("ingest: HTTP %d, Retry-After %s, after %s", code, ra, took.Round(100*time.Millisecond))
		return nil
	})
	time.Sleep(time.Until(down.Add(outageKafka)))

	up := time.Now()
	if out, err := compose("up", "-d", "--no-recreate", "--wait", "kafka"); err != nil {
		t.Fatalf("start kafka: %v\n%s", err, out)
	}
	t.Logf("kafka down for %s; healthy again %s after start", up.Sub(down).Round(time.Second), time.Since(up).Round(100*time.Millisecond))

	eventually(t, 2*time.Minute, 2*time.Second, "ingest accepts again without restart", func() error {
		code, _, _, err := postProbe()
		if err != nil || code != http.StatusOK {
			return fmt.Errorf("HTTP %d %v", code, err)
		}
		return nil
	})
	for _, h := range []struct{ id, name string }{{targetID(), targetName}, {plainID(), plainName}} {
		eventually(t, 2*time.Minute, 3*time.Second, h.name+": new metric points after Kafka returned", func() error {
			m, err := metrics(key(), h.id, "system.uptime", up, time.Now().Add(time.Minute), url.Values{"step": {"10s"}})
			if err != nil {
				return err
			}
			if len(m.Series) == 0 || len(m.Series[0].Points) < 2 {
				return fmt.Errorf("no new points")
			}
			return nil
		})
	}
	eventually(t, 2*time.Minute, 3*time.Second, "export.items{outcome=sent} keeps increasing", func() error {
		v, err := counterLast(targetID(), "sent")
		if err != nil {
			return err
		}
		if v <= sent0 {
			return fmt.Errorf("sent %v, before outage %v", v, sent0)
		}
		return nil
	})
	// Not asserted: the agent collects and exports in one loop, so while ingest answers 503 each
	// send (produce timeout + Retry-After retries) delays the next sample. Reported for visibility.
	if err := pointsCover(targetID(), down, up); err != nil {
		t.Logf("sampling during the Kafka outage (informational, agent collection is not decoupled from export): %v", err)
	} else {
		t.Log("sampling during the Kafka outage: no gap above 2 intervals")
	}
	if state, _ := compose("ps", "--format", "{{.Service}} {{.RunningFor}} {{.Status}}"); state != "" {
		t.Logf("containers after outages:\n%s", state)
	}
}
