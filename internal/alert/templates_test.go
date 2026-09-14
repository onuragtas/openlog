package alert

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// sampleParams returns parameters that satisfy every required parameter of t.
func sampleParams(t *Template) map[string]any {
	p := map[string]any{}
	for _, x := range t.Params {
		switch x.Key {
		case "service_name":
			if x.Required {
				p[x.Key] = "checkout"
			}
		case "match":
			p[x.Key] = "redis"
		case "host_id":
			p[x.Key] = "host-1"
		case "host_name":
			p[x.Key] = "web-1"
		case "discovery_id":
			p[x.Key] = "redis"
		case "instance":
			p[x.Key] = "/usr/bin/redis-server"
		}
	}
	return p
}

func fixedResolver(v float64, ok bool) referenceResolver {
	return func(context.Context, string, []Filter) (float64, bool, error) { return v, ok, nil }
}

func TestTemplateCatalog(t *testing.T) {
	ids := map[string]bool{}
	for _, tpl := range Templates("", "") {
		if ids[tpl.ID] {
			t.Errorf("duplicate template %s", tpl.ID)
		}
		ids[tpl.ID] = true
		if tpl.Name["en"] == "" || tpl.Name["tr"] == "" || tpl.Description["en"] == "" || tpl.Description["tr"] == "" {
			t.Errorf("%s: missing en/tr texts", tpl.ID)
		}
		for _, p := range tpl.Params {
			if p.Label["en"] == "" || p.Label["tr"] == "" {
				t.Errorf("%s.%s: missing label", tpl.ID, p.Key)
			}
		}
		switch tpl.Category {
		case TemplateHost, TemplateContainer, TemplateAPM, TemplateKubernetes:
		case TemplateIntegration:
			if tpl.Integration == "" {
				t.Errorf("%s: integration template without integration", tpl.ID)
			}
		default:
			t.Errorf("%s: category %q", tpl.ID, tpl.Category)
		}
		// Every template renders to a valid rule with defaults and a target.
		r, err := renderTemplate(context.Background(), tpl.ID, TemplateRenderInput{Params: sampleParams(&tpl), Language: "tr"}, fixedResolver(1000, true))
		if err != nil {
			t.Errorf("%s: render: %v", tpl.ID, err)
			continue
		}
		if r.Rule.Type != tpl.RuleType || r.Rule.Labels["openlog.template"] != tpl.ID || !strings.HasPrefix(r.Rule.Name, tpl.Name["tr"]) {
			t.Errorf("%s: rule %+v", tpl.ID, r.Rule)
		}
		if _, err := r.Rule.Validate(); err != nil {
			t.Errorf("%s: rendered rule invalid: %v", tpl.ID, err)
		}
	}
	for _, want := range []string{"host_disk_full", "container_restarts", "apm_error_rate", "apm_apdex_low", "redis_memory_high", "mysql_replica_lag", "postgresql_connections_high"} {
		if !ids[want] {
			t.Errorf("catalog lacks %s", want)
		}
	}
	if n := len(Templates(TemplateIntegration, "redis")); n < 3 {
		t.Errorf("redis templates: %d", n)
	}
}

func condition(t *testing.T, r *TemplateRender) map[string]any {
	t.Helper()
	var c map[string]any
	if err := json.Unmarshal(r.Rule.Condition, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRenderTemplateScopes(t *testing.T) {
	ctx := context.Background()
	// One instance: host + instance resource filters, one series per host.
	r, err := renderTemplate(ctx, "mysql_replica_lag", TemplateRenderInput{Params: map[string]any{"host_id": "h1", "host_name": "db-1",
		"discovery_id": "mariadb", "instance": "/usr/sbin/mariadbd", "threshold": "45"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := condition(t, r)
	fs, _ := json.Marshal(c["filters"])
	if !strings.Contains(string(fs), `"resource.openlog.discovery.instance","op":"eq","values":["/usr/sbin/mariadbd"]`) ||
		!strings.Contains(string(fs), `"host.id"`) || c["threshold"] != 45.0 || r.Rule.Name != "MySQL replication lag – db-1 / /usr/sbin/mariadbd" {
		t.Errorf("instance scope: %s %v %q", fs, c, r.Rule.Name)
	}
	// Every instance of the integration: one series per host and instance.
	r, err = renderTemplate(ctx, "mysql_replica_lag", TemplateRenderInput{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c = condition(t, r)
	if g, _ := json.Marshal(c["group_by"]); string(g) != `["host","resource.openlog.discovery.instance"]` {
		t.Errorf("fleet group_by %s", g)
	}
	if fs, _ := json.Marshal(c["filters"]); !strings.Contains(string(fs), `"resource.openlog.integration.id","op":"eq","values":["mysql"]`) {
		t.Errorf("fleet filters %s", fs)
	}
	// APM: environment empty = all (null).
	r, err = renderTemplate(ctx, "apm_apdex_low", TemplateRenderInput{Params: map[string]any{"service_name": "checkout", "threshold": 0.5}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := condition(t, r); c["environment"] != nil || c["metric"] != "apdex" || c["operator"] != "lt" || c["threshold"] != 0.5 {
		t.Errorf("apm condition %v", c)
	}
	// Container restarts: rate threshold just below n restarts per window.
	r, err = renderTemplate(ctx, "container_restarts", TemplateRenderInput{Params: map[string]any{"restarts": 3, "window_seconds": 600}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := condition(t, r); c["threshold"].(float64)*600 != 2.5 {
		t.Errorf("restart threshold %v", c["threshold"])
	}
}

func TestRenderTemplateRatio(t *testing.T) {
	ctx := context.Background()
	inst := map[string]any{"host_id": "h1", "discovery_id": "redis", "instance": "/usr/bin/redis-server", "ratio": 0.9}
	var gotMetric string
	var gotFilters []Filter
	resolve := func(_ context.Context, metric string, fs []Filter) (float64, bool, error) {
		gotMetric, gotFilters = metric, fs
		return 1 << 30, true, nil
	}
	r, err := renderTemplate(ctx, "redis_memory_high", TemplateRenderInput{Params: inst}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if c := condition(t, r); c["threshold"] != 966367642.0 || gotMetric != "redis.maxmemory" || len(gotFilters) != 3 {
		t.Errorf("ratio threshold %v, resolved %s %v", c["threshold"], gotMetric, gotFilters)
	}
	if r.Reference == nil || r.Reference.Value != 1<<30 || r.Reference.Ratio != 0.9 {
		t.Errorf("reference %+v", r.Reference)
	}
	// maxmemory 0 (unlimited) or no data: failed precondition.
	var pe *PreconditionError
	if _, err := renderTemplate(ctx, "redis_memory_high", TemplateRenderInput{Params: inst}, fixedResolver(0, true)); !errors.As(err, &pe) {
		t.Errorf("maxmemory 0: %v", err)
	}
	// Without an instance the ratio cannot be resolved.
	var ve *ValidationError
	if _, err := renderTemplate(ctx, "postgresql_connections_high", TemplateRenderInput{}, resolve); !errors.As(err, &ve) {
		t.Errorf("no instance: %v", err)
	}
}

func TestRenderTemplateParams(t *testing.T) {
	ctx := context.Background()
	bad := []struct {
		id     string
		params map[string]any
	}{
		{"apm_error_rate", map[string]any{}},                                               // service_name required
		{"host_cpu_high", map[string]any{"threshold": 2}},                                  // above max
		{"host_cpu_high", map[string]any{"threshold": "lots"}},                             // not a number
		{"host_cpu_high", map[string]any{"bogus": 1}},                                      // unknown
		{"redis_evicted_keys", map[string]any{"instance": "/usr/bin/redis-server"}},        // discovery_id missing
		{"service_disappeared", map[string]any{"match": ""}},                               // required
		{"host_not_reporting", map[string]any{"window_seconds": 10}},                       // below min
		{"host_cpu_high", map[string]any{"host_id": strings.Repeat("x", maxValueBytes+1)}}, // too long
	}
	for _, b := range bad {
		var ve *ValidationError
		if _, err := renderTemplate(ctx, b.id, TemplateRenderInput{Params: b.params}, nil); !errors.As(err, &ve) {
			t.Errorf("%s %v: %v", b.id, b.params, err)
		}
	}
	if _, err := renderTemplate(ctx, "nope", TemplateRenderInput{}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown template: %v", err)
	}
	r, err := renderTemplate(ctx, "host_cpu_high", TemplateRenderInput{Name: "Custom", Params: map[string]any{"for_seconds": 120}}, nil)
	if err != nil || r.Rule.Name != "Custom" || r.Rule.ForSeconds != 120 || r.Rule.Description == "" {
		t.Errorf("custom name: %+v %v", r, err)
	}
}
