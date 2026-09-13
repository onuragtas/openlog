package update

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestClampPollAndJitter(t *testing.T) {
	for _, c := range []struct {
		in   int
		want time.Duration
	}{
		{0, 300 * time.Second}, {-5, 300 * time.Second}, {1, 60 * time.Second}, {59, 60 * time.Second},
		{60, 60 * time.Second}, {300, 300 * time.Second}, {3600, 3600 * time.Second}, {86400, 3600 * time.Second},
	} {
		if got := ClampPoll(c.in); got != c.want {
			t.Errorf("ClampPoll(%d) = %s, want %s", c.in, got, c.want)
		}
	}
	d := 100 * time.Second
	if Jitter(d, 0) != 90*time.Second || Jitter(d, 0.5) != d || Jitter(d, 0.999999) > 110*time.Second || Jitter(d, 0.999999) < 109*time.Second {
		t.Errorf("jitter bounds: %s %s %s", Jitter(d, 0), Jitter(d, 0.5), Jitter(d, 0.999999))
	}
}

func TestSyncOnceRequestAndResponse(t *testing.T) {
	var got SyncRequest
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/openlog/agent/sync" {
			http.Error(w, "bad", 400)
			return
		}
		headers = r.Header.Clone()
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"poll_interval_seconds": 120, "server_version": "0.4.0", "future_field": 1,
			"update": {"action": "upgrade", "target_version": "0.4.0", "manifest": "e30=", "signature": "s",
			           "download_url": "https://x/a.tar.gz", "rollout_id": "r1", "not_before": "2026-10-01T02:00:00Z", "unknown": true}}`))
	}))
	defer srv.Close()
	s := &Syncer{Endpoint: srv.URL + "/", LicenseKey: "lk", UserAgent: "openlog-infra-agent/0.3.0", Request: func() SyncRequest {
		return SyncRequest{HostID: "h1", HostName: "web-1",
			Agent:  AgentInfo{Name: AgentName, Version: "0.3.0", Commit: "abc", OS: "linux", Arch: "amd64", InstallMethod: MethodTarball, UpdateCapable: true},
			Update: Report{State: StateIdle}, ConfigHash: "sha256:00"}
	}}
	resp, err := s.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if headers.Get("openlog-license-key") != "lk" || headers.Get("Content-Type") != "application/json" || headers.Get("User-Agent") != "openlog-infra-agent/0.3.0" {
		t.Errorf("headers %v", headers)
	}
	if got.HostID != "h1" || got.Agent.InstallMethod != "tarball" || !got.Agent.UpdateCapable || got.Update.State != "idle" || got.ConfigHash != "sha256:00" {
		t.Errorf("request %+v", got)
	}
	if resp.PollIntervalSeconds != 120 || resp.Update == nil || resp.Update.TargetVersion != "0.4.0" || resp.Update.RolloutID != "r1" {
		t.Errorf("response %+v", resp)
	}
	if nb, _ := resp.Update.times(); nb.IsZero() {
		t.Error("not_before not parsed")
	}
}

func TestSyncDeliversRemoteIntegrationConfig(t *testing.T) {
	var revs []string
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req SyncRequest
		json.NewDecoder(r.Body).Decode(&req)
		revs = append(revs, req.IntegrationsConfigRevision)
		if calls.Add(1) == 1 {
			w.Write([]byte(`{"poll_interval_seconds": 60, "update": null, "integrations_config": {"revision": "sha256:ab",
				"items": [{"integration": "redis", "match": {"instance": "/usr/bin/redis-server"}, "enabled": true, "password": "pw"}]}}`))
			return
		}
		w.Write([]byte(`{"poll_interval_seconds": 60, "update": null, "integrations_config": null}`))
	}))
	defer srv.Close()
	applied := ""
	var got []string
	kick := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Syncer{Endpoint: srv.URL, Kick: kick, InitialDelay: 0, Rand: func() float64 { return 0 },
		Request: func() SyncRequest { return SyncRequest{IntegrationsConfigRevision: applied} },
		Integrations: func(rc *config.RemoteIntegrations) {
			applied = rc.Revision
			got = append(got, rc.Revision+"/"+rc.Items[0].Match.Instance+"/"+rc.Items[0].Password)
			kick <- struct{}{}
		},
		Handle: func(context.Context, *Instruction) { t.Error("no update expected") },
	}
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("second sync did not happen")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if len(got) != 1 || got[0] != "sha256:ab//usr/bin/redis-server/pw" {
		t.Errorf("delivered = %v", got)
	}
	if len(revs) < 2 || revs[0] != "" || revs[1] != "sha256:ab" {
		t.Errorf("reported revisions = %v", revs)
	}
}

func TestSyncRequestJSONShape(t *testing.T) {
	b, _ := json.Marshal(SyncRequest{Agent: AgentInfo{Name: AgentName}, Update: Report{State: StateIdle}})
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, k := range []string{"host_id", "host_name", "agent", "update", "config_hash", "integrations_config_revision"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %s in %s", k, b)
		}
	}
	agent := m["agent"].(map[string]any)
	for _, k := range []string{"name", "version", "commit", "os", "arch", "install_method", "update_capable"} {
		if _, ok := agent[k]; !ok {
			t.Errorf("missing agent.%s", k)
		}
	}
	upd := m["update"].(map[string]any)
	for _, k := range []string{"state", "from_version", "to_version", "error", "changed_at"} {
		if _, ok := upd[k]; !ok {
			t.Errorf("missing update.%s", k)
		}
	}
}

func TestSyncRunDisablesOn404And501(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusNotImplemented} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(code)
		}))
		kick := make(chan struct{}, 1)
		s := &Syncer{Endpoint: srv.URL, Request: func() SyncRequest { return SyncRequest{} }, Kick: kick,
			Handle: func(context.Context, *Instruction) { t.Error("handle called") }}
		done := make(chan struct{})
		go func() { s.Run(context.Background()); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("HTTP %d: Run did not stop", code)
		}
		if calls.Load() != 1 {
			t.Errorf("HTTP %d: %d calls", code, calls.Load())
		}
		srv.Close()
	}
}

func TestSyncRunHandlesInstructionsAndKicks(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch n {
		case 1:
			w.Write([]byte(`{"poll_interval_seconds": 5, "update": {"action": "upgrade", "target_version": "1.0.0"}}`))
		case 2:
			http.Error(w, "overloaded", http.StatusServiceUnavailable) // transient: keeps running
		default:
			w.Write([]byte(`{"poll_interval_seconds": 5, "update": null}`))
		}
	}))
	defer srv.Close()
	kick := make(chan struct{}, 1)
	handled := make(chan *Instruction, 4)
	s := &Syncer{Endpoint: srv.URL, Request: func() SyncRequest { return SyncRequest{} }, Kick: kick,
		Handle: func(_ context.Context, ins *Instruction) { handled <- ins }, Rand: func() float64 { return 0.5 }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	select {
	case ins := <-handled:
		if ins.TargetVersion != "1.0.0" {
			t.Errorf("instruction %+v", ins)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("instruction not handled")
	}
	// The next poll would be 60 s away (5 s clamped); kicks sync immediately.
	for want := int32(2); want <= 3; want++ {
		kick <- struct{}{}
		deadline := time.Now().Add(5 * time.Second)
		for calls.Load() < want {
			if time.Now().After(deadline) {
				t.Fatalf("kick %d did not sync", want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
	if len(handled) != 0 {
		t.Error("null update handled")
	}
}
