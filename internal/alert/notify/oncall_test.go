package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// provider is a fake PagerDuty / Opsgenie endpoint recording the requests it received.
type provider struct {
	mu      sync.Mutex
	paths   []string
	bodies  []map[string]any
	headers []http.Header
	status  int    // 0 = 202 Accepted
	retry   string // Retry-After header
}

func (p *provider) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.paths = append(p.paths, r.URL.RequestURI())
		p.bodies = append(p.bodies, body)
		p.headers = append(p.headers, r.Header.Clone())
		status, retry := p.status, p.retry
		p.mu.Unlock()
		if retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		if status == 0 {
			status = http.StatusAccepted
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (p *provider) request(i int) (string, map[string]any, http.Header) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i >= len(p.paths) {
		return "", nil, nil
	}
	return p.paths[i], p.bodies[i], p.headers[i]
}

func (p *provider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.paths)
}

// oncallSender is a sender that talks to srv instead of the public provider endpoints.
func oncallSender(t *testing.T, srv *httptest.Server) *Sender {
	t.Helper()
	s := NewSender(Options{Timeout: 2 * time.Second, UserAgent: "openlog-alert/test"})
	s.SetProviderEndpoints(srv.URL, srv.URL)
	return s
}

func TestPagerDutyTriggerPayload(t *testing.T) {
	p := &provider{}
	s := oncallSender(t, p.start(t))
	target := Target{Type: TypePagerDuty, Key: "rk-1", PagerDuty: &PagerDutyConfig{Region: "us"}}
	res := s.Send(context.Background(), target, sampleEvent(), "")
	if !res.OK() || res.StatusCode != http.StatusAccepted {
		t.Fatalf("trigger: %+v", res)
	}
	_, body, _ := p.request(0)
	if body["routing_key"] != "rk-1" || body["event_action"] != PagerDutyTrigger || body["dedup_key"] != "openlog-inc-1" {
		t.Fatalf("event keys %v", body)
	}
	if body["client"] != "openlog" || body["client_url"] != "https://ol.example/alerts/incidents/inc-1" {
		t.Fatalf("client %v", body)
	}
	payload, _ := body["payload"].(map[string]any)
	if payload == nil || payload["severity"] != "critical" || payload["source"] != "web-1" || payload["class"] != "metric_threshold" {
		t.Fatalf("payload %v", payload)
	}
	if !strings.Contains(payload["summary"].(string), "FIRING: High CPU") {
		t.Errorf("summary %v", payload["summary"])
	}
	details, _ := payload["custom_details"].(map[string]any)
	if details["host.name"] != "web-1" || details["rule"] != "High CPU" || details["value"] != "0.93" || details["incident_id"] != "inc-1" {
		t.Fatalf("custom details %v", details)
	}
	if _, internal := details["alert.severity"]; internal {
		t.Error("internal labels sent as details")
	}
	if links, _ := body["links"].([]any); len(links) != 2 { // incident and rule URL; no runbook in the sample
		t.Errorf("links %v", body["links"])
	}
	// Severity mapping and the service region.
	for severity, want := range map[string]string{"critical": "critical", "warning": "warning", "info": "info", "": "warning"} {
		if got := pagerDutySeverity(severity); got != want {
			t.Errorf("severity %q → %q, want %q", severity, got, want)
		}
	}
	if !strings.Contains(PagerDutyEndpoint("eu"), "events.eu.pagerduty.com") || !strings.Contains(PagerDutyEndpoint(""), "events.pagerduty.com") {
		t.Error("endpoint region mapping")
	}
}

func TestPagerDutyLifecycleUsesOneDedupKey(t *testing.T) {
	p := &provider{}
	s := oncallSender(t, p.start(t))
	target := Target{Type: TypePagerDuty, Key: "rk-1"}
	ev := sampleEvent()
	for i, c := range []struct{ event, action string }{
		{EventOpened, PagerDutyTrigger},
		{EventRenotify, PagerDutyTrigger}, // a re-notification updates the open alert
		{EventAcknowledged, PagerDutyAcknowledge},
		{EventResolved, PagerDutyResolve},
	} {
		ev.Event = c.event
		if res := s.Send(context.Background(), target, ev, ""); !res.OK() {
			t.Fatalf("%s: %+v", c.event, res)
		}
		_, body, _ := p.request(i)
		if body["event_action"] != c.action || body["dedup_key"] != "openlog-inc-1" {
			t.Fatalf("%s → action %v, dedup_key %v", c.event, body["event_action"], body["dedup_key"])
		}
		// Acknowledge and resolve carry only the keys.
		if _, hasPayload := body["payload"]; hasPayload != (c.action == PagerDutyTrigger) {
			t.Errorf("%s: payload present = %v", c.event, hasPayload)
		}
	}
	// A test notification triggers and resolves at once and uses the notification id (no incident).
	ev.Event, ev.Incident.ID = EventTest, ""
	if res := s.Send(context.Background(), target, ev, ""); !res.OK() {
		t.Fatalf("test notification: %+v", res)
	}
	if p.count() != 6 {
		t.Fatalf("test notification sent %d requests, want trigger + resolve", p.count()-4)
	}
	_, trigger, _ := p.request(4)
	_, resolve, _ := p.request(5)
	if trigger["dedup_key"] != "openlog-test-n-1" || resolve["event_action"] != PagerDutyResolve || resolve["dedup_key"] != "openlog-test-n-1" {
		t.Fatalf("test notification %v / %v", trigger, resolve)
	}
}

func TestOpsgenieAliasPriorityAndLifecycle(t *testing.T) {
	p := &provider{}
	s := oncallSender(t, p.start(t))
	cfg := &OpsgenieConfig{Region: "eu", Responders: []OpsgenieResponder{{Type: "team", Name: "ops"}}, Tags: []string{"payments"}}
	target := Target{Type: TypeOpsgenie, Key: "gk-1", Opsgenie: cfg}
	ev := sampleEvent()
	if res := s.Send(context.Background(), target, ev, ""); !res.OK() {
		t.Fatalf("create: %+v", res)
	}
	path, body, header := p.request(0)
	if path != "/" || header.Get("Authorization") != "GenieKey gk-1" {
		t.Fatalf("create request %q %v", path, header.Get("Authorization"))
	}
	if body["alias"] != "openlog-inc-1" || body["priority"] != "P1" || body["source"] != "openlog" || body["entity"] != "web-1" {
		t.Fatalf("create body %v", body)
	}
	if !strings.Contains(body["message"].(string), "FIRING: High CPU") {
		t.Errorf("message %v", body["message"])
	}
	if !strings.Contains(body["description"].(string), "host.name: web-1") {
		t.Errorf("description %v", body["description"])
	}
	tags, _ := body["tags"].([]any)
	if len(tags) != 3 || tags[0] != "openlog" || tags[1] != "severity:critical" || tags[2] != "payments" {
		t.Fatalf("tags %v", tags)
	}
	responders, _ := body["responders"].([]any)
	if len(responders) != 1 || responders[0].(map[string]any)["name"] != "ops" {
		t.Fatalf("responders %v", responders)
	}
	// Acknowledge and close address the alert by alias.
	for i, c := range []struct{ event, path string }{
		{EventAcknowledged, "/openlog-inc-1/acknowledge?identifierType=alias"},
		{EventResolved, "/openlog-inc-1/close?identifierType=alias"},
	} {
		ev.Event = c.event
		if res := s.Send(context.Background(), target, ev, ""); !res.OK() {
			t.Fatalf("%s: %+v", c.event, res)
		}
		if path, _, _ := p.request(i + 1); path != c.path {
			t.Errorf("%s path %q, want %q", c.event, path, c.path)
		}
	}
	// Priority mapping and the configured override.
	for severity, want := range map[string]string{"critical": "P1", "warning": "P3", "info": "P5", "": "P3"} {
		if got := opsgeniePriority(severity); got != want {
			t.Errorf("priority %q → %q, want %q", severity, got, want)
		}
	}
	ev.Event = EventOpened
	if m := OpsgenieMessage(ev, &OpsgenieConfig{Priority: "P4"}); m["priority"] != "P4" {
		t.Errorf("priority override %v", m["priority"])
	}
	if !strings.Contains(OpsgenieEndpoint("eu"), "api.eu.opsgenie.com") || !strings.Contains(OpsgenieEndpoint(""), "api.opsgenie.com") {
		t.Error("endpoint region mapping")
	}
}

func TestOnCallRetryClassification(t *testing.T) {
	p := &provider{}
	s := oncallSender(t, p.start(t))
	targets := map[string]Target{
		"pagerduty": {Type: TypePagerDuty, Key: "rk-1"},
		"opsgenie":  {Type: TypeOpsgenie, Key: "gk-1"},
	}
	cases := []struct {
		status     int
		retryAfter string
		retryable  bool
	}{
		{http.StatusTooManyRequests, "7", true},
		{http.StatusBadGateway, "", true},
		{http.StatusBadRequest, "", false},   // malformed event: retrying never helps
		{http.StatusUnauthorized, "", false}, // wrong integration/API key
	}
	for name, target := range targets {
		for _, c := range cases {
			p.mu.Lock()
			p.status, p.retry = c.status, c.retryAfter
			p.mu.Unlock()
			res := s.Send(context.Background(), target, sampleEvent(), "")
			if res.OK() || res.Retryable != c.retryable || res.StatusCode != c.status {
				t.Errorf("%s HTTP %d: %+v", name, c.status, res)
			}
			if c.retryAfter != "" && res.RetryAfter != 7*time.Second {
				t.Errorf("%s: Retry-After = %v", name, res.RetryAfter)
			}
		}
	}
	// A blocked destination is permanent (no retries against an internal address).
	blocked := NewSender(Options{Timeout: time.Second, BlockPrivate: true})
	blocked.SetProviderEndpoints("http://127.0.0.1:1/enqueue", "http://127.0.0.1:1/alerts")
	if res := blocked.Send(context.Background(), Target{Type: TypePagerDuty, Key: "k"}, sampleEvent(), ""); res.OK() || res.Retryable {
		t.Errorf("blocked destination: %+v", res)
	}
}
