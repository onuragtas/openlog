//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"
)

// Alerts phase (docs/contracts/alerting.md): a rule on the target agent's CPU metric that always breaches opens exactly
// one incident and one HMAC-signed webhook notification (alert-receiver service); changing the condition resolves it
// with exactly one resolve notification. Another organization sees neither the rule nor the incident.

type e2eSession struct {
	t    *testing.T
	http *http.Client
	csrf string
}

func (s *e2eSession) do(method, path string, body, out any, want int) {
	s.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, apiURL(path, nil), rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.csrf != "" {
		req.Header.Set("X-CSRF-Token", s.csrf)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		s.t.Fatalf("%s %s: HTTP %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			s.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
}

func ownerSession(t *testing.T) *e2eSession {
	jar, _ := cookiejar.New(nil)
	s := &e2eSession{t: t, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	var me struct {
		CSRFToken string `json:"csrf_token"`
	}
	s.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": env["OPENLOG_BOOTSTRAP_OWNER_EMAIL"], "password": env["OPENLOG_BOOTSTRAP_OWNER_PASSWORD"]}, &me, http.StatusOK)
	s.csrf = me.CSRFToken
	return s
}

type receivedNotification struct {
	Path           string `json:"path"`
	Event          string `json:"event"`
	IdempotencyKey string `json:"idempotency_key"`
	SignatureValid *bool  `json:"signature_valid"`
}

func alertReceiver() ([]receivedNotification, error) {
	resp, err := httpClient.Get("http://127.0.0.1:" + env["E2E_ALERT_RECEIVER_PORT"] + "/requests")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []receivedNotification
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func testAlerts(t *testing.T) {
	if env["E2E_ALERT_RECEIVER_PORT"] == "" {
		t.Skip("E2E_ALERT_RECEIVER_PORT not set")
	}
	s := ownerSession(t)
	var ch struct {
		ID string `json:"id"`
	}
	s.do(http.MethodPost, "/api/v1/alerts/channels", map[string]any{"name": "e2e webhook", "type": "webhook",
		"secrets": map[string]string{"url": "http://alert-receiver:8080/webhook", "hmac_secret": env["E2E_ALERT_HMAC_SECRET"]}}, &ch, http.StatusCreated)
	var test struct {
		Success bool `json:"success"`
	}
	s.do(http.MethodPost, "/api/v1/alerts/channels/"+ch.ID+"/test", nil, &test, http.StatusOK)
	if !test.Success {
		t.Fatal("test send to the receiver failed")
	}
	cond := map[string]any{"metric": "system.cpu.utilization", "aggregation": "count", "window_seconds": 60, "operator": "gt", "threshold": 0,
		"group_by": []string{"host"}, "filters": []map[string]any{{"field": "host.id", "op": "eq", "values": []string{targetID()}}}}
	rule := map[string]any{"name": "e2e always firing", "type": "metric_threshold", "severity": "warning", "interval_seconds": 10,
		"condition": cond, "channel_ids": []string{ch.ID}}
	var created struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	s.do(http.MethodPost, "/api/v1/alerts/rules", rule, &created, http.StatusCreated)

	// Preview through the tenant-scoped query layer sees the agent's data.
	var preview struct {
		Series []struct {
			Incidents []any `json:"incidents"`
		} `json:"series"`
	}
	s.do(http.MethodPost, "/api/v1/alerts/rules/preview", map[string]any{"rule": rule, "hours": 1}, &preview, http.StatusOK)
	if len(preview.Series) != 1 {
		t.Errorf("preview series = %d, want 1 (target host)", len(preview.Series))
	}

	var incidentID string
	eventually(t, 2*time.Minute, 3*time.Second, "alert incident opened", func() error {
		var list struct {
			Incidents []struct {
				ID     string `json:"id"`
				RuleID string `json:"rule_id"`
				State  string `json:"state"`
			} `json:"incidents"`
		}
		s.do(http.MethodGet, "/api/v1/alerts/incidents?state=open", nil, &list, http.StatusOK)
		for _, i := range list.Incidents {
			if i.RuleID == created.ID {
				incidentID = i.ID
				return nil
			}
		}
		return fmt.Errorf("no open incident yet")
	})
	eventually(t, time.Minute, 2*time.Second, "one signed opening webhook", func() error {
		got, err := alertReceiver()
		if err != nil {
			return err
		}
		opened := 0
		for _, r := range got {
			if r.Event == "incident.opened" {
				opened++
				if r.SignatureValid == nil || !*r.SignatureValid {
					return fmt.Errorf("invalid signature on %s", r.IdempotencyKey)
				}
			}
		}
		if opened != 1 {
			return fmt.Errorf("opened notifications = %d", opened)
		}
		return nil
	})

	// Tenant isolation: the other organization's API key sees neither the rule nor the incident.
	var others struct {
		Rules []struct {
			ID string `json:"id"`
		} `json:"rules"`
	}
	if _, err := apiGet(otherKey(), "/api/v1/alerts/rules", nil, &others); err != nil {
		t.Fatal(err)
	}
	for _, r := range others.Rules {
		if r.ID == created.ID {
			t.Error("other organization lists the e2e rule")
		}
	}
	if code, _ := apiGet(otherKey(), "/api/v1/alerts/incidents/"+incidentID, url.Values{}, nil); code != http.StatusNotFound {
		t.Errorf("other organization reads the incident: HTTP %d", code)
	}

	// Changing the condition resolves the incident (rule_changed) with exactly one resolve notification.
	cond["threshold"] = 1e12
	rule["version"] = created.Version
	s.do(http.MethodPut, "/api/v1/alerts/rules/"+created.ID, rule, nil, http.StatusOK)
	eventually(t, time.Minute, 2*time.Second, "resolve notification", func() error {
		got, err := alertReceiver()
		if err != nil {
			return err
		}
		counts := map[string]int{}
		for _, r := range got {
			counts[r.Event]++
		}
		if counts["incident.opened"] != 1 || counts["incident.resolved"] != 1 {
			return fmt.Errorf("notifications %v", counts)
		}
		return nil
	})
	var detail struct {
		State         string `json:"state"`
		ResolveReason string `json:"resolve_reason"`
		Deliveries    []struct {
			Status string `json:"status"`
		} `json:"deliveries"`
	}
	s.do(http.MethodGet, "/api/v1/alerts/incidents/"+incidentID, nil, &detail, http.StatusOK)
	if detail.State != "resolved" || detail.ResolveReason != "rule_changed" || len(detail.Deliveries) != 2 {
		t.Errorf("incident after rule change: %+v", detail)
	}
	s.do(http.MethodDelete, "/api/v1/alerts/rules/"+created.ID, nil, nil, http.StatusNoContent)
}
