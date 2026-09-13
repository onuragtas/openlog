//go:build alertproof

// Package alertproof is the live proof of alerting (docs/contracts/alerting.md, 10-m2.md §6) against the ONE shared
// local backend (compose project `openlog`, never started or changed here). It adds, as plain containers on the
// `openlog_default` network: a notification receiver (generic webhook with HMAC verification, fake Slack and fake
// Teams over HTTPS with a test CA), Mailpit (SMTP) and three extra openlog-alert replicas with the `openlog` service's
// environment. Channels and the rule are created through the API, so rules, incidents and deliveries stay visible in
// the UI. The lease owner (one of the extra replicas) is SIGKILLed while the incident is open; every channel must get
// exactly one opening and one resolve notification.
//
//	go test -tags alertproof -v -count=1 -timeout 30m ./test/alertproof
//
// ALERTPROOF_KEEP=1 keeps the extra containers; ALERTPROOF_LOG=<file> also writes the log.
package alertproof

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

const (
	apiBase      = "http://localhost:8080"
	ingestBase   = "http://localhost:4318"
	adminReady   = "http://localhost:9464/readyz"
	licenseKey   = "dev-license-key"
	ownerEmail   = "admin@openlog.local"
	ownerPass    = "openlog-dev-password"
	network      = "openlog_default"
	prefix       = "openlog-alertproof"
	receiverHost = "alertproof-receiver"
	mailpitHost  = "alertproof-mailpit"
	receiverPort = "26880"
	mailpitPort  = "26825"
	hmacSecret   = "alertproof-hmac-secret"
	hostName     = "alert-proof-host"
	hostID       = "a1e7a1e7a1e7a1e7a1e7a1e7a1e7a1e7"
	replicas     = 3
	proofPrefix  = "[proof] "
)

var (
	repoRoot string
	certDir  string
	logOut   io.Writer = os.Stdout
	logMu    sync.Mutex
)

func logf(format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintf(logOut, "%s  %s\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), fmt.Sprintf(format, args...))
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = repoRoot
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

func mustRun(name string, args ...string) string {
	out, err := run(name, args...)
	if err != nil {
		panic(fmt.Sprintf("%s %v: %v\n%s", name, args, err, out))
	}
	return out
}

// writeCerts creates a CA and a server certificate for the receiver (trusted by the extra replicas via SSL_CERT_FILE).
func writeCerts(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "openlog alert proof CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, _ := x509.ParseCertificate(caDER)
	write := func(name, typ string, der []byte) error {
		return os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o644)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: receiverHost},
		DNSNames: []string{receiverHost, "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	if err := write("ca.crt", "CERTIFICATE", caDER); err != nil {
		return err
	}
	if err := write("receiver.crt", "CERTIFICATE", der); err != nil {
		return err
	}
	return write("receiver.key", "EC PRIVATE KEY", keyDER)
}

func containerName(kind string, i int) string {
	if i == 0 {
		return prefix + "-" + kind
	}
	return fmt.Sprintf("%s-%s-%d", prefix, kind, i)
}

func removeContainers() {
	names := []string{containerName("receiver", 0), containerName("mailpit", 0)}
	for i := 1; i <= replicas; i++ {
		names = append(names, containerName("alert", i))
	}
	_, _ = run("docker", append([]string{"rm", "-f"}, names...)...)
}

// openlogService returns the image and OPENLOG_* environment of the shared stack's `openlog` container.
func openlogService() (string, []string) {
	name := mustRun("docker", "ps", "--filter", "label=com.docker.compose.project=openlog", "--filter", "label=com.docker.compose.service=openlog", "--format", "{{.Names}}")
	if name == "" || strings.Contains(name, "\n") {
		panic("shared openlog container not found (compose project openlog, service openlog): " + name)
	}
	image := mustRun("docker", "inspect", "-f", "{{.Config.Image}}", name)
	var envs []string
	if err := json.Unmarshal([]byte(mustRun("docker", "inspect", "-f", "{{json .Config.Env}}", name)), &envs); err != nil {
		panic(err)
	}
	var out []string
	for _, e := range envs {
		if strings.HasPrefix(e, "OPENLOG_") {
			out = append(out, e)
		}
	}
	logf("shared backend: container %s image %s (%d OPENLOG_* variables reused for the extra replicas)", name, image, len(out))
	return image, out
}

func TestMain(m *testing.M) {
	var err error
	if repoRoot, err = filepath.Abs("../.."); err != nil {
		panic(err)
	}
	if p := os.Getenv("ALERTPROOF_LOG"); p != "" {
		if f, err := os.Create(p); err == nil {
			defer f.Close()
			logOut = io.MultiWriter(os.Stdout, f)
		}
	}
	resp, err := http.Get(adminReady)
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "shared openlog stack is not ready (%s): %v\n", adminReady, err)
		os.Exit(1)
	}
	resp.Body.Close()
	certDir = filepath.Join(repoRoot, "test/alertproof/.certs")
	if err := writeCerts(certDir); err != nil {
		panic(err)
	}
	image, envs := openlogService()
	removeContainers()
	logf("building receiver image")
	mustRun("docker", "build", "-q", "-t", prefix+"/receiver:dev", filepath.Join(repoRoot, "test/alertproof/receiver"))
	mount := certDir + ":/proof-certs:ro"
	mustRun("docker", "run", "-d", "--name", containerName("receiver", 0), "--network", network, "--network-alias", receiverHost,
		"-e", "HMAC_SECRET="+hmacSecret, "-e", "TLS_CERT=/proof-certs/receiver.crt", "-e", "TLS_KEY=/proof-certs/receiver.key",
		"-v", mount, "-p", "127.0.0.1:"+receiverPort+":8080", prefix+"/receiver:dev")
	mustRun("docker", "run", "-d", "--name", containerName("mailpit", 0), "--network", network, "--network-alias", mailpitHost,
		"-p", "127.0.0.1:"+mailpitPort+":8025", "axllent/mailpit:latest")
	for i := 1; i <= replicas; i++ {
		args := []string{"run", "-d", "--name", containerName("alert", i), "--hostname", fmt.Sprintf("alertproof-alert-%d", i),
			"--network", network, "--entrypoint", "/usr/local/bin/openlog-alert", "-v", mount, "-e", "SSL_CERT_FILE=/proof-certs/ca.crt"}
		for _, e := range envs {
			args = append(args, "-e", e)
		}
		mustRun("docker", append(args, image)...)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for i := 1; i <= replicas; i++ {
		for {
			if out, err := run("docker", "exec", containerName("alert", i), "wget", "-qO-", "http://127.0.0.1:9464/readyz"); err == nil && strings.Contains(out, "ok") {
				break
			}
			if time.Now().After(deadline) {
				logs, _ := run("docker", "logs", "--tail", "40", containerName("alert", i))
				fmt.Fprintf(os.Stderr, "replica %d not ready:\n%s\n", i, logs)
				removeContainers()
				os.Exit(1)
			}
			time.Sleep(2 * time.Second)
		}
	}
	logf("started receiver, mailpit and %d openlog-alert replicas on %s", replicas, network)
	code := m.Run()
	if code != 0 {
		for i := 1; i <= replicas; i++ {
			logs, _ := run("docker", "logs", "--tail", "30", containerName("alert", i))
			fmt.Printf("---- %s ----\n%s\n", containerName("alert", i), logs)
		}
	}
	if os.Getenv("ALERTPROOF_KEEP") != "1" {
		removeContainers()
		logf("removed the extra containers (rules, incidents and deliveries remain visible in the shared UI)")
	}
	os.Exit(code)
}

// ---- API client (session) ----

type client struct {
	t    *testing.T
	http *http.Client
	csrf string
}

func login(t *testing.T) *client {
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	var me struct {
		CSRFToken string `json:"csrf_token"`
	}
	c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": ownerEmail, "password": ownerPass}, &me, http.StatusOK)
	c.csrf = me.CSRFToken
	return c
}

func (c *client) do(method, path string, body, out any, want int) {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, apiBase+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: HTTP %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
}

// ---- CPU generator ----

type cpuGen struct {
	busy atomic.Uint64
	sent atomic.Int64
}

func (g *cpuGen) set(busy float64) { g.busy.Store(uint64(busy * 10000)) }
func (g *cpuGen) value() float64   { return float64(g.busy.Load()) / 10000 }

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func cpuRequest(busy float64, ts time.Time) []byte {
	dp := func(mode string, v float64) *metricspb.NumberDataPoint {
		return &metricspb.NumberDataPoint{TimeUnixNano: uint64(ts.UnixNano()), Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: v},
			Attributes: []*commonpb.KeyValue{str("cpu.mode", mode)}}
	}
	req := &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("host.id", hostID), str("host.name", hostName), str("os.type", "linux"), str("env", "alert-proof")}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
			Name: "system.cpu.utilization", Unit: "1",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{
				dp("user", busy*0.8), dp("system", busy*0.2), dp("idle", 1-busy),
			}}},
		}}}},
	}}}
	b, _ := proto.Marshal(req)
	return b
}

func startCPU(t *testing.T, busy float64) *cpuGen {
	g := &cpuGen{}
	g.set(busy)
	stop := make(chan struct{})
	hc := &http.Client{Timeout: 10 * time.Second}
	send := func() {
		req, _ := http.NewRequest(http.MethodPost, ingestBase+"/v1/metrics", bytes.NewReader(cpuRequest(g.value(), time.Now())))
		req.Header.Set("Content-Type", "application/x-protobuf")
		req.Header.Set("openlog-license-key", licenseKey)
		resp, err := hc.Do(req)
		if err != nil {
			logf("cpu generator: %v", err)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			g.sent.Add(1)
		}
	}
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		send()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				send()
			}
		}
	}()
	t.Cleanup(func() { close(stop) })
	return g
}

// ---- observations ----

type received struct {
	Path           string    `json:"path"`
	ReceivedAt     time.Time `json:"received_at"`
	Event          string    `json:"event"`
	IdempotencyKey string    `json:"idempotency_key"`
	SignatureValid *bool     `json:"signature_valid"`
	Status         int       `json:"status"`
}

func receiverRequests(t *testing.T) []received {
	resp, err := http.Get("http://127.0.0.1:" + receiverPort + "/requests")
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	defer resp.Body.Close()
	var out []received
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("receiver: %v", err)
	}
	return out
}

type mail struct {
	Subject   string    `json:"Subject"`
	MessageID string    `json:"MessageID"`
	Created   time.Time `json:"Created"`
}

func mailpitMessages(t *testing.T) []mail {
	resp, err := http.Get("http://127.0.0.1:" + mailpitPort + "/api/v1/messages")
	if err != nil {
		t.Fatalf("mailpit: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Messages []mail `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("mailpit: %v", err)
	}
	sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].Created.Before(out.Messages[j].Created) })
	return out.Messages
}

var counts = func(t *testing.T) map[string]int {
	c := map[string]int{}
	for _, r := range receiverRequests(t) {
		c[r.Path+" "+r.Event]++
	}
	for _, m := range mailpitMessages(t) {
		switch {
		case strings.Contains(m.Subject, "RESOLVED"):
			c["email incident.resolved"]++
		case strings.Contains(m.Subject, "FIRING"):
			c["email incident.opened"]++
		default:
			c["email other"]++
		}
	}
	return c
}

func eventually(t *testing.T, timeout time.Duration, what string, fn func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		ok, msg := fn()
		if ok {
			return
		}
		last = msg
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s (last: %s)", timeout, what, last)
}

type ruleStatus struct {
	ID     string `json:"id"`
	Status struct {
		State           string  `json:"state"`
		LastEvaluatedAt *string `json:"last_evaluated_at"`
		Owner           *string `json:"owner"`
	} `json:"status"`
}

type incident struct {
	ID            string  `json:"id"`
	RuleID        *string `json:"rule_id"`
	State         string  `json:"state"`
	Summary       string  `json:"summary"`
	ResolveReason *string `json:"resolve_reason"`
}

// cleanupPrevious removes channels and rules of earlier proof runs (by name prefix).
func cleanupPrevious(api *client) {
	var rules struct {
		Rules []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"rules"`
	}
	api.do(http.MethodGet, "/api/v1/alerts/rules", nil, &rules, http.StatusOK)
	for _, r := range rules.Rules {
		if strings.HasPrefix(r.Name, proofPrefix) {
			api.do(http.MethodDelete, "/api/v1/alerts/rules/"+r.ID, nil, nil, http.StatusNoContent)
		}
	}
	var chans struct {
		Channels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"channels"`
	}
	api.do(http.MethodGet, "/api/v1/alerts/channels", nil, &chans, http.StatusOK)
	for _, c := range chans.Channels {
		if strings.HasPrefix(c.Name, proofPrefix) {
			api.do(http.MethodDelete, "/api/v1/alerts/channels/"+c.ID, nil, nil, http.StatusNoContent)
		}
	}
}

func TestAlertingLiveProof(t *testing.T) {
	api := login(t)
	var chanList struct {
		SecretsConfigured bool `json:"secrets_configured"`
	}
	api.do(http.MethodGet, "/api/v1/alerts/channels", nil, &chanList, http.StatusOK)
	// Without OPENLOG_SECRETS_KEY on the shared backend no channel can be created: the proof then covers evaluation,
	// incidents and lease takeover only (no notifications).
	notifyOn := chanList.SecretsConfigured
	if !notifyOn {
		logf("WARNING: the shared backend has no OPENLOG_SECRETS_KEY (secrets_configured=false): channels and deliveries are skipped")
	}
	cleanupPrevious(api)

	type idResp struct {
		ID string `json:"id"`
	}
	var webhook, slack, teams, email idResp
	channelIDs := []string{}
	if notifyOn {
		api.do(http.MethodPost, "/api/v1/alerts/channels", map[string]any{"name": proofPrefix + "webhook (HMAC)", "type": "webhook",
			"secrets": map[string]string{"url": "http://" + receiverHost + ":8080/webhook", "hmac_secret": hmacSecret}}, &webhook, http.StatusCreated)
		api.do(http.MethodPost, "/api/v1/alerts/channels", map[string]any{"name": proofPrefix + "slack", "type": "slack",
			"secrets": map[string]string{"url": "https://" + receiverHost + ":8443/slack"}}, &slack, http.StatusCreated)
		api.do(http.MethodPost, "/api/v1/alerts/channels", map[string]any{"name": proofPrefix + "teams", "type": "teams",
			"secrets": map[string]string{"url": "https://" + receiverHost + ":8443/teams"}}, &teams, http.StatusCreated)
		api.do(http.MethodPost, "/api/v1/alerts/channels", map[string]any{"name": proofPrefix + "e-mail (Mailpit)", "type": "email",
			"config": map[string]any{"to": []string{"oncall@alertproof.test"}, "smtp": map[string]any{"host": mailpitHost, "port": 1025, "tls": "none", "from": "openlog <alerts@alertproof.test>"}}},
			&email, http.StatusCreated)
		logf("channels created via API: webhook=%s slack=%s teams=%s email=%s", webhook.ID, slack.ID, teams.ID, email.ID)
		var chk struct {
			SecretHints map[string]string `json:"secret_hints"`
		}
		api.do(http.MethodGet, "/api/v1/alerts/channels/"+webhook.ID, nil, &chk, http.StatusOK)
		logf("GET channel returns only masked secrets: %v", chk.SecretHints)
		if strings.Contains(fmt.Sprint(chk.SecretHints), hmacSecret) {
			t.Fatal("secret returned by the API")
		}
		channelIDs = []string{webhook.ID, slack.ID, teams.ID, email.ID}
	}

	gen := startCPU(t, 0.25)
	ruleBody := map[string]any{
		"name": proofPrefix + "CPU busy on " + hostName, "type": "metric_threshold", "severity": "critical", "interval_seconds": 10, "for_seconds": 0,
		"description": "Live proof: avg over 20s of the non-idle CPU modes (summed) above 90%, recovery below 70%.",
		"condition": map[string]any{"metric": "system.cpu.utilization", "aggregation": "avg", "series_aggregation": "sum", "window_seconds": 20,
			"filters":  []map[string]any{{"field": "host.name", "op": "eq", "values": []string{hostName}}, {"field": "attr.cpu.mode", "op": "not_in", "values": []string{"idle"}}},
			"group_by": []string{"host"}, "operator": "gt", "threshold": 0.9, "recovery_threshold": 0.7},
		"channel_ids": channelIDs,
		"flapping":    map[string]any{"enabled": false, "transitions": 4, "window_seconds": 3600, "hold_seconds": 600},
	}
	// The shared allinone evaluates rules too; for the kill test the lease must land on one of the extra replicas.
	var rule ruleStatus
	var owner string
	for attempt := 1; ; attempt++ {
		api.do(http.MethodPost, "/api/v1/alerts/rules", ruleBody, &rule, http.StatusCreated)
		eventually(t, 90*time.Second, "a lease owner", func() (bool, string) {
			api.do(http.MethodGet, "/api/v1/alerts/rules/"+rule.ID, nil, &rule, http.StatusOK)
			if rule.Status.Owner != nil {
				owner = *rule.Status.Owner
				return true, ""
			}
			return false, "no owner"
		})
		logf("rule %s created (attempt %d), lease owner %s", rule.ID, attempt, owner)
		if strings.HasPrefix(owner, "alertproof-alert-") {
			break
		}
		if attempt == 10 {
			t.Fatalf("the lease never landed on an extra replica (last owner %s)", owner)
		}
		api.do(http.MethodDelete, "/api/v1/alerts/rules/"+rule.ID, nil, nil, http.StatusNoContent)
	}

	eventually(t, 3*time.Minute, "a successful evaluation with data", func() (bool, string) {
		api.do(http.MethodGet, "/api/v1/alerts/rules/"+rule.ID, nil, &rule, http.StatusOK)
		return rule.Status.State == "ok" && gen.sent.Load() >= 5, fmt.Sprintf("state=%s sent=%d", rule.Status.State, gen.sent.Load())
	})
	logf("baseline: rule state ok at CPU busy=%.2f (samples sent: %d)", gen.value(), gen.sent.Load())

	spikeAt := time.Now()
	gen.set(0.97)
	logf("CPU SPIKE: busy=0.97 on %s", hostName)
	var open incident
	eventually(t, 90*time.Second, "an open incident", func() (bool, string) {
		var list struct {
			Incidents []incident `json:"incidents"`
		}
		api.do(http.MethodGet, "/api/v1/alerts/incidents?state=open,acknowledged&rule_id="+rule.ID, nil, &list, http.StatusOK)
		if len(list.Incidents) > 0 {
			open = list.Incidents[0]
			return true, ""
		}
		return false, "none"
	})
	fireLatency := time.Since(spikeAt)
	logf("INCIDENT OPENED %s after %s: %s", open.ID, fireLatency.Round(100*time.Millisecond), open.Summary)
	if fireLatency > time.Minute {
		t.Errorf("incident opened %s after the spike, want <= 1m", fireLatency)
	}
	want := func(kind string) map[string]int {
		m := map[string]int{}
		if !notifyOn {
			return m
		}
		for _, p := range []string{"/webhook", "/slack", "/teams", "email"} {
			m[p+" incident.opened"] = 1
			if kind == "resolved" {
				m[p+" incident.resolved"] = 1
			}
		}
		return m
	}
	if !notifyOn {
		counts = func(*testing.T) map[string]int { return map[string]int{} }
	}
	match := func(c, w map[string]int) bool {
		for k, v := range w {
			if c[k] != v {
				return false
			}
		}
		return len(c) == len(w)
	}
	eventually(t, 3*time.Minute, "one opening notification per channel", func() (bool, string) {
		c := counts(t)
		return match(c, want("opened")), fmt.Sprint(c)
	})
	logf("OPENING NOTIFICATIONS delivered %s after the spike: %v", time.Since(spikeAt).Round(100*time.Millisecond), counts(t))

	// Kill the lease owner while the incident is open.
	api.do(http.MethodGet, "/api/v1/alerts/rules/"+rule.ID, nil, &rule, http.StatusOK)
	owner = *rule.Status.Owner
	host := owner[:strings.LastIndex(owner, "-")]
	victim := strings.Replace(host, "alertproof-alert-", prefix+"-alert-", 1)
	killAt := time.Now()
	if out, err := run("docker", "kill", "--signal", "KILL", victim); err != nil {
		t.Fatalf("docker kill %s: %v %s", victim, err, out)
	}
	logf("KILLED lease owner %s (container %s, SIGKILL, no graceful release) with the incident open", owner, victim)
	var newOwner string
	eventually(t, 2*time.Minute, "lease takeover", func() (bool, string) {
		api.do(http.MethodGet, "/api/v1/alerts/rules/"+rule.ID, nil, &rule, http.StatusOK)
		if rule.Status.Owner != nil && *rule.Status.Owner != owner {
			newOwner = *rule.Status.Owner
			return true, ""
		}
		return false, fmt.Sprint(rule.Status.Owner)
	})
	logf("LEASE TAKEN OVER by %s after %s (lease TTL 30s)", newOwner, time.Since(killAt).Round(100*time.Millisecond))
	time.Sleep(25 * time.Second)
	c := counts(t)
	logf("still firing after takeover: %v", c)
	if !match(c, want("opened")) {
		t.Fatalf("duplicate or lost opening notification after takeover: %v", c)
	}
	var incs struct {
		Incidents []incident `json:"incidents"`
	}
	api.do(http.MethodGet, "/api/v1/alerts/incidents?rule_id="+rule.ID, nil, &incs, http.StatusOK)
	if len(incs.Incidents) != 1 {
		t.Fatalf("incidents of the rule after takeover = %d, want 1", len(incs.Incidents))
	}

	recoverAt := time.Now()
	gen.set(0.30)
	logf("CPU RECOVERED: busy=0.30")
	var resolved incident
	eventually(t, 2*time.Minute, "incident resolved", func() (bool, string) {
		api.do(http.MethodGet, "/api/v1/alerts/incidents/"+open.ID, nil, &resolved, http.StatusOK)
		return resolved.State == "resolved", resolved.State
	})
	logf("INCIDENT RESOLVED after %s (reason %s)", time.Since(recoverAt).Round(100*time.Millisecond), *resolved.ResolveReason)
	eventually(t, 3*time.Minute, "one resolve notification per channel", func() (bool, string) {
		c := counts(t)
		return match(c, want("resolved")), fmt.Sprint(c)
	})
	logf("RESOLVE NOTIFICATIONS delivered; settling 40s to catch late duplicates")
	time.Sleep(40 * time.Second)
	final := counts(t)
	logf("FINAL notification counts: %v", final)
	if !match(final, want("resolved")) {
		t.Errorf("final counts %v, want %v", final, want("resolved"))
	}
	for _, r := range receiverRequests(t) {
		logf("receiver %-9s %-18s status=%d signature_valid=%-5v key=%s at %s", r.Path, r.Event, r.Status,
			r.SignatureValid != nil && *r.SignatureValid, r.IdempotencyKey, r.ReceivedAt.Format("15:04:05.000"))
		if r.Path == "/webhook" && (r.SignatureValid == nil || !*r.SignatureValid) {
			t.Errorf("webhook signature not valid: %+v", r)
		}
	}
	for _, m := range mailpitMessages(t) {
		logf("mailpit  %q message-id=%s at %s", m.Subject, m.MessageID, m.Created.UTC().Format("15:04:05.000"))
	}

	var log struct {
		Deliveries []struct {
			ChannelName string `json:"channel_name"`
			Kind        string `json:"kind"`
			Status      string `json:"status"`
			CreatedAt   string `json:"created_at"`
			FinishedAt  string `json:"finished_at"`
			AttemptLog  []struct {
				Attempt    int    `json:"attempt"`
				At         string `json:"at"`
				DurationMs int    `json:"duration_ms"`
				Success    bool   `json:"success"`
				StatusCode int    `json:"status_code"`
				Error      string `json:"error"`
			} `json:"attempt_log"`
		} `json:"deliveries"`
	}
	api.do(http.MethodGet, "/api/v1/alerts/deliveries?incident_id="+url.QueryEscape(open.ID), nil, &log, http.StatusOK)
	logf("delivery log (GET /api/v1/alerts/deliveries?incident_id=%s):", open.ID)
	for _, d := range log.Deliveries {
		for _, a := range d.AttemptLog {
			logf("  %-26s %-8s %-9s attempt=%d at=%s status_code=%d %dms err=%q", d.ChannelName, d.Kind, d.Status, a.Attempt, a.At[11:23], a.StatusCode, a.DurationMs, a.Error)
		}
		if d.Status != "delivered" {
			t.Errorf("%s %s: %s", d.ChannelName, d.Kind, d.Status)
		}
	}
	if want := len(channelIDs) * 2; len(log.Deliveries) != want {
		t.Errorf("delivery log has %d notifications, want %d (channels × opened/resolved)", len(log.Deliveries), want)
	}
	notifications := "notifications=skipped (no OPENLOG_SECRETS_KEY on the shared backend)"
	if notifyOn {
		notifications = "per-channel opened=1 resolved=1 duplicates=0 lost=0"
	}
	logf("SUMMARY fire_latency=%s lease_owner_killed=%s new_owner=%s incidents=1 %s",
		fireLatency.Round(100*time.Millisecond), owner, newOwner, notifications)
}
