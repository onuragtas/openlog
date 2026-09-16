package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
)

// pagerDutyChannel and opsgenieChannel are channel writes for the on-call providers (alerting.md §5.3).
var pagerDutyChannel = map[string]any{"name": "on-call", "type": "pagerduty",
	"config": map[string]any{"pagerduty": map[string]any{"region": "eu"}}, "secrets": map[string]string{"routing_key": "R0UT1NGKEY"}}

var opsgenieChannel = map[string]any{"name": "opsgenie team", "type": "opsgenie",
	"config": map[string]any{"opsgenie": map[string]any{"region": "us", "priority": "P2", "tags": []string{"payments"},
		"responders": []map[string]string{{"type": "team", "name": "ops"}}}},
	"secrets": map[string]string{"api_key": "GEN1EKEY"}}

func TestAlertOnCallChannels(t *testing.T) {
	e := newAlertEnv(t)
	rec := e.owner.do(http.MethodPost, "/api/v1/alerts/channels", pagerDutyChannel)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create pagerduty channel: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "R0UT1NGKEY") {
		t.Error("integration key returned in plaintext")
	}
	ch := decode[map[string]any](t, rec)
	hints := ch["secret_hints"].(map[string]any)
	if hints["routing_key"] != "•••••••• (set)" {
		t.Errorf("secret hints %v", hints)
	}
	if region := ch["config"].(map[string]any)["pagerduty"].(map[string]any)["region"]; region != "eu" {
		t.Errorf("region %v", region)
	}
	// The stored key stays hidden on reads and is kept when the write omits it.
	path := "/api/v1/alerts/channels/" + ch["id"].(string)
	if body := e.owner.do(http.MethodGet, path, nil).Body.String(); strings.Contains(body, "R0UT1NGKEY") {
		t.Error("channel read leaks the integration key")
	}
	update := map[string]any{"name": "on-call (eu)", "type": "pagerduty", "config": map[string]any{"pagerduty": map[string]any{"region": "eu"}}}
	if rec := e.owner.do(http.MethodPut, path, update); rec.Code != http.StatusOK {
		t.Fatalf("update without secrets: %d %s", rec.Code, rec.Body)
	}

	rec = e.owner.do(http.MethodPost, "/api/v1/alerts/channels", opsgenieChannel)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create opsgenie channel: %d %s", rec.Code, rec.Body)
	}
	og := decode[map[string]any](t, rec)["config"].(map[string]any)["opsgenie"].(map[string]any)
	if og["priority"] != "P2" || len(og["responders"].([]any)) != 1 {
		t.Errorf("opsgenie config %v", og)
	}

	bad := []map[string]any{
		{"name": "x", "type": "pagerduty"}, // no routing key
		{"name": "x", "type": "pagerduty", "secrets": map[string]string{"url": "https://example.com"}}, // wrong secret
		{"name": "x", "type": "pagerduty", "secrets": map[string]string{"routing_key": "k"}, // unknown region
			"config": map[string]any{"pagerduty": map[string]any{"region": "moon"}}},
		{"name": "x", "type": "opsgenie"}, // no API key
		{"name": "x", "type": "opsgenie", "secrets": map[string]string{"api_key": "k"}, // bad priority
			"config": map[string]any{"opsgenie": map[string]any{"priority": "P9"}}},
		{"name": "x", "type": "opsgenie", "secrets": map[string]string{"api_key": "k"}, // responder without a name
			"config": map[string]any{"opsgenie": map[string]any{"responders": []map[string]string{{"type": "team"}}}}},
		{"name": "x", "type": "slack", "secrets": map[string]string{"url": "https://hooks.slack.com/x"}, // config of another type
			"config": map[string]any{"pagerduty": map[string]any{"region": "eu"}}},
	}
	for _, b := range bad {
		if rec := e.owner.do(http.MethodPost, "/api/v1/alerts/channels", b); rec.Code != http.StatusBadRequest {
			t.Errorf("invalid channel %v: %d %s", b, rec.Code, rec.Body)
		}
	}
}

// createChannel creates a channel and returns its id.
func createChannel(t *testing.T, c *client, body map[string]any) string {
	t.Helper()
	rec := c.do(http.MethodPost, "/api/v1/alerts/channels", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create channel: %d %s", rec.Code, rec.Body)
	}
	return decode[map[string]any](t, rec)["id"].(string)
}

func TestAlertRoutingRulesCRUDAndRoles(t *testing.T) {
	e := newAlertEnv(t)
	admin := e.member(t, "admin@example.com", auth.RoleAdmin)
	member := e.member(t, "member@example.com", auth.RoleMember)
	viewer := e.member(t, "viewer@example.com", auth.RoleViewer)
	pager := createChannel(t, e.owner, pagerDutyChannel)
	slack := createChannel(t, e.owner, map[string]any{"name": "#ops", "type": "slack",
		"secrets": map[string]string{"url": "https://hooks.slack.com/services/x"}})

	route := map[string]any{"name": "critical to on-call", "channel_ids": []string{pager},
		"match": map[string]any{"severities": []string{"critical"}, "services": []string{"checkout"},
			"labels":      []map[string]string{{"label": "env", "op": "eq", "value": "prod"}},
			"time_window": map[string]any{"timezone": "Europe/Istanbul", "days": []string{"mon", "tue"}, "start_time": "09:00", "end_time": "18:00"}}}

	// Reads: every role. Writes: admins and owners only.
	if rec := viewer.do(http.MethodGet, "/api/v1/alerts/routing-rules", nil); rec.Code != http.StatusOK {
		t.Errorf("viewer list: %d %s", rec.Code, rec.Body)
	}
	for name, c := range map[string]*client{"viewer": viewer, "member": member} {
		if rec := c.do(http.MethodPost, "/api/v1/alerts/routing-rules", route); rec.Code != http.StatusForbidden {
			t.Errorf("%s create routing rule: %d", name, rec.Code)
		}
	}
	rec := admin.do(http.MethodPost, "/api/v1/alerts/routing-rules", route)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin create: %d %s", rec.Code, rec.Body)
	}
	created := decode[map[string]any](t, rec)
	if created["enabled"] != true || created["is_default"] != false {
		t.Errorf("defaults %v", created)
	}
	match := created["match"].(map[string]any)
	if len(match["severities"].([]any)) != 1 || match["time_window"].(map[string]any)["timezone"] != "Europe/Istanbul" {
		t.Errorf("match %v", match)
	}
	rulePath := "/api/v1/alerts/routing-rules/" + created["id"].(string)

	// One default route per organization.
	def := map[string]any{"name": "everything else", "is_default": true, "channel_ids": []string{slack}}
	if rec := admin.do(http.MethodPost, "/api/v1/alerts/routing-rules", def); rec.Code != http.StatusCreated {
		t.Fatalf("create default route: %d %s", rec.Code, rec.Body)
	}
	if rec := admin.do(http.MethodPost, "/api/v1/alerts/routing-rules", def); rec.Code != http.StatusConflict {
		t.Errorf("second default route: %d %s", rec.Code, rec.Body)
	}

	bad := []map[string]any{
		{"name": "", "channel_ids": []string{pager}},
		{"name": "x"},
		{"name": "x", "channel_ids": []string{"11111111-1111-1111-1111-111111111111"}}, // unknown channel
		{"name": "x", "channel_ids": []string{pager}, "match": map[string]any{"severities": []string{"fatal"}}},
		{"name": "x", "channel_ids": []string{pager}, "is_default": true, "match": map[string]any{"severities": []string{"critical"}}},
	}
	for _, b := range bad {
		if rec := admin.do(http.MethodPost, "/api/v1/alerts/routing-rules", b); rec.Code != http.StatusBadRequest {
			t.Errorf("invalid routing rule %v: %d %s", b, rec.Code, rec.Body)
		}
	}

	// Update, reorder and delete.
	updated := map[string]any{"name": "critical to on-call", "channel_ids": []string{pager, slack}, "enabled": false,
		"match": map[string]any{"severities": []string{"critical"}}}
	if rec := admin.do(http.MethodPut, rulePath, updated); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if got := decode[map[string]any](t, admin.do(http.MethodGet, rulePath, nil)); got["enabled"] != false || len(got["channel_ids"].([]any)) != 2 {
		t.Errorf("updated rule %v", got)
	}
	list := decode[map[string][]map[string]any](t, admin.do(http.MethodGet, "/api/v1/alerts/routing-rules", nil))["routing_rules"]
	if len(list) != 2 {
		t.Fatalf("list has %d rules", len(list))
	}
	ids := []string{list[1]["id"].(string), list[0]["id"].(string)}
	rec = admin.do(http.MethodPost, "/api/v1/alerts/routing-rules/reorder", map[string]any{"ids": ids})
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder: %d %s", rec.Code, rec.Body)
	}
	reordered := decode[map[string][]map[string]any](t, rec)["routing_rules"]
	if reordered[0]["id"] != ids[0] || reordered[0]["position"].(float64) != 0 {
		t.Errorf("reordered %v", reordered)
	}
	if rec := admin.do(http.MethodPost, "/api/v1/alerts/routing-rules/reorder", map[string]any{"ids": ids[:1]}); rec.Code != http.StatusBadRequest {
		t.Errorf("partial reorder: %d %s", rec.Code, rec.Body)
	}
	if rec := admin.do(http.MethodDelete, rulePath, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d %s", rec.Code, rec.Body)
	}

	// Another organization neither sees nor changes the rules (indistinguishable from unknown).
	otherPath := "/api/v1/alerts/routing-rules/" + list[0]["id"].(string)
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		if rec := e.otherB.do(m, otherPath, nil); rec.Code != http.StatusNotFound {
			t.Errorf("org B %s org A routing rule: %d", m, rec.Code)
		}
	}
	if rec := e.otherB.do(http.MethodPut, otherPath, updated); rec.Code != http.StatusNotFound {
		t.Errorf("org B PUT org A routing rule: %d", rec.Code)
	}
	if got := decode[map[string][]map[string]any](t, e.otherB.do(http.MethodGet, "/api/v1/alerts/routing-rules", nil))["routing_rules"]; len(got) != 0 {
		t.Errorf("org B sees %d routing rules", len(got))
	}
}
