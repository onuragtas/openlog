package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/exporter"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"
	"github.com/onuragtas/openlog/agents/infra/internal/phpforwarder"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
	"github.com/onuragtas/openlog/agents/infra/internal/testfixtures"
)

func phpSocketDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "olagent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func phpMessage(i int) []byte {
	return fmt.Appendf(nil, `{"v":1,"pid":100,"trace_id":"%032x","seq":0,"last":true,"resource":{"service.name":"shop-api","host.id":"forged"},`+
		`"sampling_ratio":1,"spans":[{"id":"%016x","parent":"","name":"GET /orders","kind":2,"start":%d,"dur":1000,"status":0,`+
		`"attrs":{"http.request.method":"GET","http.route":"/orders"}}]}`, i+1, i+1, time.Now().UnixNano())
}

// PHP spans go through the agent pipeline: during an ingest outage they are buffered (memory queue spills to disk)
// and delivered after recovery, with the agent's host.id.
func TestPHPSpansSurviveOutage(t *testing.T) {
	f := &fakeSender{}
	f.down.Store(true)
	fs := hostfstest.Build(t, testfixtures.ServiceHost())
	cfg := testConfig(t, fs.Root(), "http://ingest.invalid:4318")
	cfg.Interval = config.Duration(100 * time.Millisecond)
	on := true
	sock := filepath.Join(phpSocketDir(t), "php.sock")
	cfg.PHPForwarder.Enabled = &on
	cfg.PHPForwarder.Socket = sock
	cfg.PHPForwarder.SocketGroup = strconv.Itoa(os.Getgid())
	a, err := New(cfg, "test", quiet(), true)
	if err != nil {
		t.Fatal(err)
	}
	a.pipe.sender = f
	a.pipe.maxItems = 3 // force spills to disk during the outage
	a.pipe.retryBase, a.pipe.retryMax = 10*time.Millisecond, 40*time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	waitUntil(t, "socket", func() bool { _, err := os.Stat(sock); return err == nil })
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	const n = 30
	for i := 0; i < n; i++ {
		if _, err := conn.Write(phpMessage(i)); err != nil {
			t.Fatal(err)
		}
		if i == n/2 {
			time.Sleep(1200 * time.Millisecond) // two batches (flush interval 1s)
		}
	}
	waitUntil(t, "trace payloads buffered on disk", func() bool {
		s := a.stats.Snapshot()
		return s.ExportItems[selfmon.ExportKey{Signal: "traces", Outcome: "buffered"}] == n
	})
	if a.buf.Len() == 0 {
		t.Fatal("disk buffer empty during outage")
	}
	f.down.Store(false)
	waitUntil(t, "spans delivered after recovery", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		spans := 0
		for _, td := range f.traces {
			spans += phpforwarder.SpanCount(td)
		}
		return spans == n
	})
	f.mu.Lock()
	for _, td := range f.traces {
		for _, rs := range td.ResourceSpans {
			for _, kv := range rs.Resource.Attributes {
				if kv.Key == "host.id" && kv.Value.GetStringValue() != a.HostID() {
					t.Errorf("host.id = %q, want the agent's %q", kv.Value.GetStringValue(), a.HostID())
				}
			}
		}
	}
	f.mu.Unlock()
	if !a.php.active(time.Now()) {
		t.Error("php module must be active after receiving spans")
	}
	if s := a.stats.Snapshot().ExportItems[selfmon.ExportKey{Signal: string(exporter.SignalTraces), Outcome: "sent"}]; s != n {
		t.Errorf("traces sent = %d, want %d", s, n)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPHPModuleEnableDecision(t *testing.T) {
	dir := phpSocketDir(t)
	newModule := func(enabled *bool, name string) *phpModule {
		return &phpModule{
			cfg: config.PHPForwarder{Enabled: enabled}, fs: hostfstest.Build(t, map[string]string{"/etc/hostname": "h"}), log: quiet(),
			fwd: phpforwarder.New(phpforwarder.Options{Socket: filepath.Join(dir, name), SocketGroup: strconv.Itoa(os.Getgid())}),
		}
	}
	fpm := []discovery.Service{{RuleID: "php-fpm"}}

	auto := newModule(nil, "auto.sock")
	auto.observe(fpm)
	if auto.fwd.Running() {
		t.Error("must not start before Run (arm)")
	}
	auto.arm()
	if !auto.fwd.Running() {
		t.Error("auto: php-fpm discovered, forwarder must run")
	}
	auto.observe(nil)
	if auto.fwd.Running() {
		t.Error("auto: PHP gone, forwarder must stop")
	}
	auto.observe(fpm)
	if !auto.fwd.Running() {
		t.Error("auto: PHP back, forwarder must restart")
	}
	auto.shutdown()
	if auto.fwd.Running() {
		t.Error("shutdown must stop the forwarder")
	}

	off := false
	disabled := newModule(&off, "off.sock")
	disabled.arm()
	disabled.observe(fpm)
	if disabled.fwd.Running() {
		t.Error("explicit enabled: false wins over discovery")
	}
	on := true
	enabled := newModule(&on, "on.sock")
	enabled.arm()
	defer enabled.shutdown()
	if !enabled.fwd.Running() {
		t.Error("explicit enabled: true starts without a PHP runtime")
	}
}

func TestAPMHintStatus(t *testing.T) {
	shared := &discovery.APMHint{Language: "php", Agent: phpforwarder.APMAgent}
	svcs := []discovery.Service{{RuleID: "php-fpm", APMHint: shared}, {RuleID: "php-fpm", Instance: "b", APMHint: shared}, {RuleID: "redis"}}
	annotateAPMHints(svcs, true)
	if svcs[0].APMHint.Status != "active" || svcs[1].APMHint.Status != "active" || svcs[2].APMHint != nil || shared.Status != "" {
		t.Errorf("hints = %+v %+v %+v, rule hint %+v", svcs[0].APMHint, svcs[1].APMHint, svcs[2].APMHint, shared)
	}
	annotateAPMHints(svcs, false)
	b, _ := json.Marshal(svcs[0])
	if !strings.Contains(string(b), `"apm_hint":{"language":"php","agent":"openlog-agent-php","status":"not_installed"}`) {
		t.Errorf("body = %s", b)
	}
	var nilModule *phpModule
	if nilModule.active(time.Now()) {
		t.Error("no module (e.g. -once) is never active")
	}
}
