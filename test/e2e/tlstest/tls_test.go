//go:build tlstest

// Package tlstest proves TLS/SASL end to end: a compose project (openlog-tlstest) with Kafka
// (SSL + SASL_SSL listeners only), a 2-shard ClickHouse cluster (secure native port only, client
// certificates required) and PostgreSQL (ssl=on) runs openlog-allinone and loadgen with the
// OPENLOG_*_TLS_* / OPENLOG_KAFKA_SASL_* settings. Certificates are generated at test time.
// See docs/operations/e2e.md ("TLS integration test").
//
//	go test -tags tlstest -v -count=1 -timeout 30m ./test/e2e/tlstest
//
// Environment: TLSTEST_KEEP=1 keeps the stack, TLSTEST_SKIP_BUILD=1 reuses the openlog:tlstest image,
// TLSTEST_WORKDIR sets where certificates are written (default a new directory under /tmp).
package tlstest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/testutil/testcerts"
)

const project = "openlog-tlstest"

var (
	repoRoot string
	certs    *testcerts.Bundle
)

func TestMain(m *testing.M) { os.Exit(runMain(m)) }

func runMain(m *testing.M) int {
	var err error
	if repoRoot, err = filepath.Abs("../../.."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	work := os.Getenv("TLSTEST_WORKDIR")
	if work == "" {
		// /tmp (not $TMPDIR) so that Docker Desktop / OrbStack can bind-mount it.
		if work, err = os.MkdirTemp("/tmp", "openlog-tlstest-"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(work)
	}
	certs, err = testcerts.Generate(filepath.Join(work, "certs"), "kafka", "ch1", "ch2", "postgres", "localhost", "127.0.0.1")
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate certificates:", err)
		return 1
	}
	os.Setenv("TLSTEST_CERTS", certs.Dir)
	fmt.Printf("tlstest: certificates in %s\n", certs.Dir)

	args := []string{"up", "-d", "--wait", "--wait-timeout", "600"}
	if os.Getenv("TLSTEST_SKIP_BUILD") != "1" {
		args = append(args, "--build")
	}
	start := time.Now()
	if out, err := compose(args...); err != nil {
		fmt.Fprintf(os.Stderr, "compose up failed: %v\n%s\n", err, tail(out, 60))
		logs, _ := compose("logs", "--no-color", "--tail", "60")
		fmt.Fprintln(os.Stderr, logs)
		teardown()
		return 1
	}
	fmt.Printf("tlstest: stack up in %s\n", time.Since(start).Round(time.Second))
	code := m.Run()
	if code != 0 {
		logs, _ := compose("logs", "--no-color", "--tail", "40", "openlog", "kafka", "ch1", "ch2")
		fmt.Println(logs)
	}
	teardown()
	return code
}

func teardown() {
	if os.Getenv("TLSTEST_KEEP") == "1" {
		fmt.Println("tlstest: TLSTEST_KEEP=1, stack left running (docker compose -p " + project + " down -v)")
		return
	}
	if out, err := compose("--profile", "badca", "down", "-v", "--remove-orphans", "--timeout", "10"); err != nil {
		fmt.Fprintf(os.Stderr, "compose down: %v\n%s\n", err, out)
	}
}

func compose(args ...string) (string, error) {
	base := []string{"compose", "-p", project, "-f", filepath.Join(repoRoot, "test/e2e/tlstest/docker-compose.yml")}
	cmd := exec.Command("docker", append(base, args...)...)
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func eventually(t *testing.T, timeout time.Duration, what string, fn func() error) bool {
	t.Helper()
	start := time.Now()
	for {
		err := fn()
		if err == nil {
			t.Logf("%s: ok after %s", what, time.Since(start).Round(100*time.Millisecond))
			return true
		}
		if time.Since(start) > timeout {
			t.Errorf("%s: not satisfied within %s: %v", what, timeout, err)
			return false
		}
		time.Sleep(3 * time.Second)
	}
}

// ---- Kafka ----

type kafkaCase struct {
	name      string
	addr      string
	tls       config.TLS
	sasl      config.KafkaSASL
	wantErr   string // substring; "" = must succeed
	anyErrors bool   // any error is acceptable (handshake failures differ by timing)
}

func kafkaCheck(c config.Common, addr string) ([]string, error) {
	opts, err := queue.ClientOptions(c)
	if err != nil {
		return nil, err
	}
	cl, err := kgo.NewClient(append([]kgo.Opt{kgo.SeedBrokers(addr), kgo.DialTimeout(5 * time.Second), kgo.RequestRetries(0)}, opts...)...)
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := cl.Ping(ctx); err != nil {
		return nil, err
	}
	topics, err := kadm.NewClient(cl).ListTopics(ctx)
	if err != nil {
		return nil, err
	}
	return topics.Names(), topics.Error()
}

func TestKafkaClients(t *testing.T) {
	ca := config.TLS{Enabled: true, CAFile: certs.CAFile}
	mtls := config.TLS{Enabled: true, CAFile: certs.CAFile, CertFile: certs.ClientCert, KeyFile: certs.ClientKey}
	wrongCA := config.TLS{Enabled: true, CAFile: certs.WrongCA}
	scram := func(m, pw string) config.KafkaSASL {
		return config.KafkaSASL{Mechanism: m, Username: "openlog", Password: pw}
	}
	cases := []kafkaCase{
		{name: "SSL mutual TLS", addr: "localhost:26093", tls: mtls},
		{name: "SASL_SSL PLAIN", addr: "localhost:26094", tls: ca, sasl: scram(config.SASLPlain, "openlog-plain-pw")},
		{name: "SASL_SSL SCRAM-SHA-256", addr: "localhost:26094", tls: ca, sasl: scram(config.SASLScramSHA256, "openlog-scram-pw")},
		{name: "SASL_SSL SCRAM-SHA-512", addr: "localhost:26094", tls: ca, sasl: scram(config.SASLScramSHA512, "openlog-scram-pw")},
		{name: "SASL_SSL insecure_skip_verify with an unrelated CA (opt-in)", addr: "localhost:26094", tls: config.TLS{Enabled: true, CAFile: certs.WrongCA, InsecureSkipVerify: true}, sasl: scram(config.SASLScramSHA512, "openlog-scram-pw")},
		{name: "wrong CA", addr: "localhost:26094", tls: wrongCA, sasl: scram(config.SASLScramSHA512, "openlog-scram-pw"), wantErr: "certificate signed by unknown authority"},
		{name: "wrong SCRAM password", addr: "localhost:26094", tls: ca, sasl: scram(config.SASLScramSHA512, "nope"), wantErr: "SASL_AUTHENTICATION_FAILED"},
		{name: "SSL listener without client certificate", addr: "localhost:26093", tls: ca, anyErrors: true},
		{name: "plaintext client on a TLS listener", addr: "localhost:26094", anyErrors: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			names, err := kafkaCheck(config.Common{KafkaTLS: c.tls, KafkaSASL: c.sasl}, c.addr)
			switch {
			case c.wantErr == "" && !c.anyErrors:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				t.Logf("ok, topics: %v", names)
			case err == nil:
				t.Fatalf("connected, want an error")
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Fatalf("error %q does not contain %q", err, c.wantErr)
			default:
				t.Logf("rejected as expected: %v", err)
			}
		})
	}
}

// ---- ClickHouse ----

func TestClickHouseClients(t *testing.T) {
	base := clickhouse.Options{Addr: []string{"127.0.0.1:26440"}, Database: "default", User: "openlog", Password: "openlog", MaxConns: 2}
	mtls := config.TLS{Enabled: true, CAFile: certs.CAFile, CertFile: certs.ClientCert, KeyFile: certs.ClientKey}
	cases := []struct {
		name    string
		tls     config.TLS
		addr    string
		wantErr string
		anyErr  bool
	}{
		{name: "mutual TLS", tls: mtls},
		{name: "server name override", tls: config.TLS{Enabled: true, CAFile: certs.CAFile, CertFile: certs.ClientCert, KeyFile: certs.ClientKey, ServerName: "ch2"}, addr: "127.0.0.1:26441"},
		{name: "wrong CA", tls: config.TLS{Enabled: true, CAFile: certs.WrongCA, CertFile: certs.ClientCert, KeyFile: certs.ClientKey}, wantErr: "certificate signed by unknown authority"},
		{name: "name mismatch", tls: config.TLS{Enabled: true, CAFile: certs.CAFile, CertFile: certs.ClientCert, KeyFile: certs.ClientKey, ServerName: "not-clickhouse"}, wantErr: "not-clickhouse"},
		{name: "no client certificate", tls: config.TLS{Enabled: true, CAFile: certs.CAFile}, anyErr: true},
		{name: "plaintext on the secure port", anyErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := base
			o.TLS = c.tls
			if c.addr != "" {
				o.Addr = []string{c.addr}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			conn, err := clickhouse.Open(ctx, o)
			if c.wantErr == "" && !c.anyErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				defer conn.Close()
				var shards uint64
				if err := conn.QueryRow(ctx, "SELECT count() FROM system.clusters WHERE cluster = 'openlog'").Scan(&shards); err != nil || shards != 2 {
					t.Fatalf("system.clusters: %d %v", shards, err)
				}
				t.Logf("ok: cluster openlog has %d shards", shards)
				return
			}
			if err == nil {
				conn.Close()
				t.Fatal("connected, want an error")
			}
			if c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not contain %q", err, c.wantErr)
			}
			t.Logf("rejected as expected: %v", err)
		})
	}
}

// ---- PostgreSQL ----

func TestPostgresClients(t *testing.T) {
	const dsn = "postgres://openlog:openlog@localhost:26432/openlog?sslmode=verify-full&connect_timeout=5"
	ping := func(o postgres.Options) (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		pool, err := postgres.Open(ctx, o)
		if err != nil {
			return false, err
		}
		defer pool.Close()
		var ssl bool
		err = pool.QueryRow(ctx, "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()").Scan(&ssl)
		return ssl, err
	}
	ssl, err := ping(postgres.Options{DSN: dsn, TLSCAFile: certs.CAFile})
	if err != nil || !ssl {
		t.Fatalf("verify-full with CA: ssl=%v err=%v", ssl, err)
	}
	t.Log("verify-full with OPENLOG_POSTGRES_TLS_CA_FILE: connected, pg_stat_ssl.ssl=true")
	if _, err := ping(postgres.Options{DSN: dsn, TLSCAFile: certs.WrongCA}); err == nil || !strings.Contains(err.Error(), "certificate signed by unknown authority") {
		t.Errorf("wrong CA: %v", err)
	} else {
		t.Logf("wrong CA rejected: %v", err)
	}
}

// ---- data flow through openlog-allinone over TLS ----

func apiGet(path string, q url.Values, out any) error {
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:26080"+path+"?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer ola_tls-api-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d %s", path, resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

func chHTTP(port int, sql string) (string, error) {
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(port)+"/", strings.NewReader(sql))
	req.Header.Set("X-ClickHouse-User", "openlog")
	req.Header.Set("X-ClickHouse-Key", "openlog")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("clickhouse :%d HTTP %d: %s", port, resp.StatusCode, b)
	}
	return strings.TrimSpace(string(b)), nil
}

func TestDataFlowsOverTLS(t *testing.T) {
	eventually(t, 3*time.Minute, "loadgen hosts, metrics and logs are queryable through the API", func() error {
		var hosts struct {
			Hosts []struct {
				HostName string `json:"host_name"`
			} `json:"hosts"`
		}
		if err := apiGet("/api/v1/hosts", nil, &hosts); err != nil {
			return err
		}
		if len(hosts.Hosts) < 6 {
			return fmt.Errorf("%d hosts", len(hosts.Hosts))
		}
		var logs struct {
			Logs []json.RawMessage `json:"logs"`
		}
		if err := apiGet("/api/v1/logs", url.Values{"limit": {"10"}}, &logs); err != nil {
			return err
		}
		if len(logs.Logs) == 0 {
			return fmt.Errorf("no logs yet")
		}
		t.Logf("%d hosts (e.g. %s), logs present", len(hosts.Hosts), hosts.Hosts[0].HostName)
		return nil
	})

	eventually(t, 2*time.Minute, "direct inserts reached both shards (processor replica connections over TLS)", func() error {
		for _, port := range []int{26123, 26124} {
			n, err := chHTTP(port, "SELECT count() FROM openlog.metrics_local")
			if err != nil {
				return err
			}
			if n == "0" {
				return fmt.Errorf("shard on :%d has no metrics rows yet", port)
			}
			t.Logf("shard :%d metrics_local rows: %s", port, n)
		}
		return nil
	})

	// Every native-protocol query of the openlog user was made over TLS (the plain tcp_port is disabled).
	for _, port := range []int{26123, 26124} {
		if _, err := chHTTP(port, "SYSTEM FLUSH LOGS"); err != nil {
			t.Fatal(err)
		}
		out, err := chHTTP(port, "SELECT count(), countIf(is_secure = 1) FROM system.query_log WHERE interface = 1 AND user = 'openlog' AND type = 'QueryFinish'")
		if err != nil {
			t.Fatal(err)
		}
		f := strings.Fields(out)
		if len(f) != 2 || f[0] == "0" || f[0] != f[1] {
			t.Errorf("clickhouse :%d native queries (total, secure) = %q", port, out)
		} else {
			t.Logf("clickhouse :%d native queries by openlog: %s, all over TLS", port, f[0])
		}
	}

	// Kafka: the processor's consumer group is active and topics have data (brokers have TLS listeners only).
	c := config.Common{KafkaTLS: config.TLS{Enabled: true, CAFile: certs.CAFile, CertFile: certs.ClientCert, KeyFile: certs.ClientKey}}
	opts, err := queue.ClientOptions(c)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := kgo.NewClient(append([]kgo.Opt{kgo.SeedBrokers("localhost:26093")}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	groups, err := adm.DescribeGroups(ctx, "openlog-processor")
	if err != nil {
		t.Fatal(err)
	}
	g := groups["openlog-processor"]
	if g.State != "Stable" || len(g.Members) == 0 {
		t.Errorf("consumer group openlog-processor: state %q, %d members", g.State, len(g.Members))
	} else {
		t.Logf("consumer group openlog-processor: %s, %d member(s) (client %s)", g.State, len(g.Members), g.Members[0].ClientHost)
	}
	ends, err := adm.ListEndOffsets(ctx, queue.Topic("openlog", queue.SignalMetrics))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	ends.Each(func(o kadm.ListedOffset) { total += o.Offset })
	if total == 0 {
		t.Error("metrics topic has no records")
	}
	t.Logf("metrics topic end offsets total %d", total)

	// PostgreSQL: all openlog sessions use TLS.
	pctx, pcancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer pcancel()
	pool, err := postgres.Open(pctx, postgres.Options{DSN: "postgres://openlog:openlog@localhost:26432/openlog?sslmode=verify-full", TLSCAFile: certs.CAFile})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var sessions, secure int
	if err := pool.QueryRow(pctx, "SELECT count(*), count(*) FILTER (WHERE s.ssl) FROM pg_stat_activity a JOIN pg_stat_ssl s USING (pid) WHERE a.application_name LIKE 'openlog-%'").Scan(&sessions, &secure); err != nil {
		t.Fatal(err)
	}
	if sessions == 0 || sessions != secure {
		t.Errorf("openlog PostgreSQL sessions: %d, with TLS: %d", sessions, secure)
	} else {
		t.Logf("openlog PostgreSQL sessions: %d, all TLS", sessions)
	}
}

// ---- wrong CA: the services fail with a clear error ----

func TestWrongCAFailsClearly(t *testing.T) {
	if out, err := compose("--profile", "badca", "up", "-d", "--no-deps", "badca-postgres", "badca-clickhouse", "badca-kafka"); err != nil {
		t.Fatalf("start badca services: %v\n%s", err, out)
	}
	const want = "certificate signed by unknown authority"
	for _, svc := range []struct{ name, what string }{
		{"badca-postgres", "waiting for postgres"},
		{"badca-clickhouse", "waiting for clickhouse"},
	} {
		eventually(t, 90*time.Second, svc.name+" logs a certificate verification error", func() error {
			out, _ := compose("logs", "--no-color", svc.name)
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, svc.what) && strings.Contains(line, want) {
					t.Logf("%s: %s", svc.name, strings.TrimSpace(line))
					return nil
				}
			}
			return fmt.Errorf("no %q line with %q yet:\n%s", svc.what, want, tail(out, 5))
		})
	}
	eventually(t, 90*time.Second, "badca-kafka (ingest) is not ready and names the TLS error", func() error {
		resp, err := http.Get("http://127.0.0.1:26466/readyz")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusOK || !strings.Contains(string(b), want) {
			return fmt.Errorf("readyz HTTP %d: %s", resp.StatusCode, b)
		}
		t.Logf("badca-kafka /readyz: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
		return nil
	})
	if out, err := compose("--profile", "badca", "rm", "-sf", "badca-postgres", "badca-clickhouse", "badca-kafka"); err != nil {
		t.Logf("remove badca services: %v\n%s", err, out)
	}
}
