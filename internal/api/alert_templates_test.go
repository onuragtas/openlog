package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
)

func TestAlertTemplatesAPI(t *testing.T) {
	e := newAlertEnv(t)
	viewer := e.member(t, "tpl-viewer@example.com", auth.RoleViewer)

	rec := viewer.do(http.MethodGet, "/api/v1/alerts/templates?category=integration&integration=redis", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list templates: %d %s", rec.Code, rec.Body)
	}
	list := decode[map[string][]map[string]any](t, rec)["templates"]
	if len(list) < 3 {
		t.Fatalf("redis templates: %d", len(list))
	}
	for _, tpl := range list {
		if tpl["integration"] != "redis" || tpl["name"].(map[string]any)["tr"] == "" || tpl["params"] == nil {
			t.Errorf("template %v", tpl)
		}
	}

	// Viewers may render (like previews); the result validates as a rule that a member can create.
	body := map[string]any{"language": "en", "params": map[string]any{"host_id": "h1", "host_name": "web-1", "threshold": 0.95}}
	rec = viewer.do(http.MethodPost, "/api/v1/alerts/templates/host_disk_full/render", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("render: %d %s", rec.Code, rec.Body)
	}
	out := decode[map[string]any](t, rec)
	rule := out["rule"].(map[string]any)
	if rule["name"] != "Disk almost full – web-1" || rule["type"] != "metric_threshold" || out["reference"] != nil {
		t.Errorf("rendered %v", out)
	}
	if rec := e.owner.do(http.MethodPost, "/api/v1/alerts/rules", rule); rec.Code != http.StatusCreated {
		t.Errorf("create rendered rule: %d %s", rec.Code, rec.Body)
	}

	if rec := viewer.do(http.MethodPost, "/api/v1/alerts/templates/host_disk_full/render", map[string]any{"params": map[string]any{"threshold": 7}}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "params.threshold") {
		t.Errorf("invalid param: %d %s", rec.Code, rec.Body)
	}
	if rec := viewer.do(http.MethodPost, "/api/v1/alerts/templates/nope/render", map[string]any{}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown template: %d", rec.Code)
	}
	// Ratio templates need one instance.
	if rec := viewer.do(http.MethodPost, "/api/v1/alerts/templates/redis_memory_high/render", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("ratio without instance: %d %s", rec.Code, rec.Body)
	}
}
