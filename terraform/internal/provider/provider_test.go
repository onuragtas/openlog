package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onuragtas/openlog/terraform/internal/client"
)

// Unit tests of the schemas and of the mapping between the framework's values and the API's Go types. They
// need neither a terraform binary nor a server: the acceptance tests in acceptance_test.go cover the plugin
// protocol end of things against a fake API.

// TestResourceSchemas checks every resource schema against the framework's own implementation rules
// (attribute names, required/computed combinations, nested object types).
func TestResourceSchemas(t *testing.T) {
	ctx := context.Background()
	for name, newResource := range map[string]func() fwresource.Resource{
		"openlog_alert_rule":           NewAlertRuleResource,
		"openlog_notification_channel": NewNotificationChannelResource,
		"openlog_alert_routing_rule":   NewAlertRoutingRuleResource,
		"openlog_slo":                  NewSLOResource,
		"openlog_dashboard":            NewDashboardResource,
	} {
		t.Run(name, func(t *testing.T) {
			resp := &fwresource.SchemaResponse{}
			newResource().Schema(ctx, fwresource.SchemaRequest{}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("Schema: %v", resp.Diagnostics)
			}
			if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
				t.Fatalf("ValidateImplementation: %v", d)
			}
		})
	}
}

func TestDataSourceSchemas(t *testing.T) {
	ctx := context.Background()
	for name, newDataSource := range map[string]func() fwdatasource.DataSource{
		"openlog_alert_channel": NewAlertChannelDataSource,
		"openlog_dashboard":     NewDashboardDataSource,
	} {
		t.Run(name, func(t *testing.T) {
			resp := &fwdatasource.SchemaResponse{}
			newDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("Schema: %v", resp.Diagnostics)
			}
			if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
				t.Fatalf("ValidateImplementation: %v", d)
			}
		})
	}
}

func TestProviderSchema(t *testing.T) {
	ctx := context.Background()
	p := New("test")()
	metaResp := &fwprovider.MetadataResponse{}
	p.Metadata(ctx, fwprovider.MetadataRequest{}, metaResp)
	if metaResp.TypeName != "openlog" {
		t.Fatalf("TypeName = %q, want openlog", metaResp.TypeName)
	}
	resp := &fwprovider.SchemaResponse{}
	p.Schema(ctx, fwprovider.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema: %v", resp.Diagnostics)
	}
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("ValidateImplementation: %v", d)
	}
	// The API key must never be printed by Terraform.
	if !resp.Schema.Attributes["api_key"].IsSensitive() {
		t.Error("api_key is not marked sensitive")
	}
	if got, want := len(p.Resources(ctx)), 5; got != want {
		t.Errorf("Resources = %d, want %d", got, want)
	}
	if got, want := len(p.DataSources(ctx)), 2; got != want {
		t.Errorf("DataSources = %d, want %d", got, want)
	}
}

// Secrets must be sensitive wherever they appear, so that a plan shows "(sensitive value)" and never the key
// itself.
func TestChannelSecretsAreSensitive(t *testing.T) {
	ctx := context.Background()
	resp := &fwresource.SchemaResponse{}
	NewNotificationChannelResource().Schema(ctx, fwresource.SchemaRequest{}, resp)
	for _, name := range []string{"secrets", "generated_secrets"} {
		if !resp.Schema.Attributes[name].IsSensitive() {
			t.Errorf("%s is not marked sensitive", name)
		}
	}
	// The masked hints are deliberately not sensitive: they carry no secret and are useful in a plan.
	if resp.Schema.Attributes["secret_hints"].IsSensitive() {
		t.Error("secret_hints is marked sensitive although it only carries masked values")
	}
}

func TestConditionDrifted(t *testing.T) {
	tests := []struct {
		name               string
		configured, server string
		want               bool
	}{
		{
			name:       "server adds its defaults",
			configured: `{"metric":"cpu","operator":"gt","threshold":0.9}`,
			server:     `{"metric":"cpu","operator":"gt","threshold":0.9,"aggregation":"avg","window_seconds":300}`,
			want:       false,
		},
		{
			name:       "only the formatting differs",
			configured: `{"threshold":0.9,  "metric":"cpu"}`,
			server:     `{"metric":"cpu","threshold":0.9}`,
			want:       false,
		},
		{
			name:       "a configured field was changed elsewhere",
			configured: `{"metric":"cpu","threshold":0.9}`,
			server:     `{"metric":"cpu","threshold":0.95,"aggregation":"avg"}`,
			want:       true,
		},
		{
			name:       "a configured field disappeared",
			configured: `{"metric":"cpu","threshold":0.9}`,
			server:     `{"metric":"cpu"}`,
			want:       true,
		},
		{
			name:       "nested objects recurse",
			configured: `{"windows":{"fast":{"factor":14.4}}}`,
			server:     `{"windows":{"fast":{"factor":14.4,"long_seconds":3600}}}`,
			want:       false,
		},
		{
			name:       "a filter removed from a list is drift",
			configured: `{"filters":[{"field":"host.id","op":"eq","values":["a"]}]}`,
			server:     `{"filters":[]}`,
			want:       true,
		},
		{
			name:       "an element added to a list is drift",
			configured: `{"group_by":["host"]}`,
			server:     `{"group_by":["host","service"]}`,
			want:       true,
		},
		{
			name:       "unparsable input counts as drift",
			configured: `not json`,
			server:     `{"metric":"cpu"}`,
			want:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conditionDrifted([]byte(tt.configured), []byte(tt.server)); got != tt.want {
				t.Errorf("conditionDrifted(%s, %s) = %v, want %v", tt.configured, tt.server, got, tt.want)
			}
		})
	}
}

func TestAttributePath(t *testing.T) {
	tests := []struct {
		field    string
		wantOK   bool
		wantPath string
	}{
		{"condition.threshold", true, "condition"},
		{"condition", true, "condition"},
		{"config.to[0]", true, "config"},
		{"secrets.url", true, "secrets"},
		{"channel_ids[3]", true, "channel_ids"},
		{"", false, ""},
		// A field the provider does not expose must not be turned into a path that does not exist.
		{"schedule.rrule", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			p, ok := attributePath(tt.field)
			if ok != tt.wantOK {
				t.Fatalf("attributePath(%q) ok = %v, want %v", tt.field, ok, tt.wantOK)
			}
			if ok && p.String() != tt.wantPath {
				t.Errorf("attributePath(%q) = %q, want %q", tt.field, p.String(), tt.wantPath)
			}
		})
	}
}

// applyRule keeps the configured condition while openlog only adds defaults, and adopts openlog's document as
// soon as a configured field disagrees. Without this the resource would either diff forever or hide drift.
func TestApplyRuleConditionHandling(t *testing.T) {
	ctx := context.Background()
	configured := `{"metric":"system.cpu.utilization","operator":"gt","threshold":0.9}`

	t.Run("defaults do not change the configuration", func(t *testing.T) {
		m := &alertRuleModel{Condition: jsontypes.NewNormalizedValue(configured)}
		var diags diagnostics
		m.applyRule(ctx, &client.AlertRule{
			ID:        "r1",
			Condition: []byte(`{"metric":"system.cpu.utilization","operator":"gt","threshold":0.9,"aggregation":"avg","window_seconds":300}`),
		}, &diags)
		if diags.HasError() {
			t.Fatalf("applyRule: %v", diags)
		}
		if m.Condition.ValueString() != configured {
			t.Errorf("condition = %s, want the configured document unchanged", m.Condition.ValueString())
		}
		if !contains(m.ConditionEffective.ValueString(), "window_seconds") {
			t.Errorf("condition_effective = %s, want the full stored document", m.ConditionEffective.ValueString())
		}
	})

	t.Run("a changed field becomes a diff", func(t *testing.T) {
		m := &alertRuleModel{Condition: jsontypes.NewNormalizedValue(configured)}
		var diags diagnostics
		m.applyRule(ctx, &client.AlertRule{
			ID:        "r1",
			Condition: []byte(`{"metric":"system.cpu.utilization","operator":"gt","threshold":0.5}`),
		}, &diags)
		if !contains(m.Condition.ValueString(), "0.5") {
			t.Errorf("condition = %s, want openlog's document so the next plan shows the drift", m.Condition.ValueString())
		}
	})

	t.Run("an import has no configured condition", func(t *testing.T) {
		m := &alertRuleModel{}
		var diags diagnostics
		m.applyRule(ctx, &client.AlertRule{ID: "r1", Condition: []byte(`{"metric":"x"}`)}, &diags)
		if m.Condition.IsNull() {
			t.Error("condition is still null after an import; want openlog's document")
		}
	})
}

func TestApplyRuleMapsFields(t *testing.T) {
	ctx := context.Background()
	var diags diagnostics
	m := &alertRuleModel{}
	m.applyRule(ctx, &client.AlertRule{
		ID: "r1", Name: "High CPU", Type: "metric_threshold", Severity: "critical", Enabled: true,
		IntervalSeconds: 60, ForSeconds: 300, Version: 3, Condition: []byte(`{"metric":"x"}`),
		ChannelIDs: []string{"c1"}, Labels: map[string]string{"team": "infra"},
		Flapping: &client.Flapping{Enabled: true, Transitions: 4, WindowSeconds: 3600, HoldSeconds: 600},
	}, &diags)
	if diags.HasError() {
		t.Fatalf("applyRule: %v", diags)
	}
	if m.ID.ValueString() != "r1" || m.Version.ValueInt64() != 3 || m.Severity.ValueString() != "critical" {
		t.Errorf("scalars not mapped: %+v", m)
	}
	if m.Flapping.IsNull() {
		t.Error("flapping is null although openlog returned one")
	}
	if len(m.ChannelIDs.Elements()) != 1 || len(m.Labels.Elements()) != 1 {
		t.Errorf("channel_ids = %v, labels = %v", m.ChannelIDs, m.Labels)
	}

	// A rule without flapping (openlog omitted it) must not invent one.
	var diags2 diagnostics
	m2 := &alertRuleModel{}
	m2.applyRule(ctx, &client.AlertRule{ID: "r2", Condition: []byte(`{}`)}, &diags2)
	if !m2.Flapping.IsNull() {
		t.Error("flapping was invented although openlog returned none")
	}
}

// The empty string and "not set" must stay apart in both directions: they select different APM services.
func TestSLOModelRoundTrip(t *testing.T) {
	empty := ""
	prod := "prod"
	tests := []struct {
		name      string
		namespace *string
	}{
		{"aggregated", nil},
		{"the empty namespace", &empty},
		{"a namespace", &prod},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &sloModel{}
			m.applySLO(&client.SLO{
				ID: "s1", Name: "Checkout", ServiceName: "checkout", ServiceNamespace: tt.namespace,
				SLIType: "availability", Objective: 99.9, WindowDays: 28,
			})
			if (tt.namespace == nil) != m.ServiceNamespace.IsNull() {
				t.Fatalf("service_namespace null = %v, want %v", m.ServiceNamespace.IsNull(), tt.namespace == nil)
			}
			back := m.apiSLO()
			switch {
			case tt.namespace == nil && back.ServiceNamespace != nil:
				t.Errorf("service_namespace = %q, want nil", *back.ServiceNamespace)
			case tt.namespace != nil && (back.ServiceNamespace == nil || *back.ServiceNamespace != *tt.namespace):
				t.Errorf("service_namespace = %v, want %q", back.ServiceNamespace, *tt.namespace)
			}
			if back.WindowDays != 28 || back.Objective != 99.9 {
				t.Errorf("window_days = %d, objective = %v", back.WindowDays, back.Objective)
			}
		})
	}
}

// A latency SLO carries its threshold; an availability SLO must not send one at all.
func TestSLOLatencyThreshold(t *testing.T) {
	m := &sloModel{SLIType: types.StringValue("latency"), LatencyThresholdMs: types.Int64Value(300)}
	if in := m.apiSLO(); in.LatencyThresholdMs == nil || *in.LatencyThresholdMs != 300 {
		t.Errorf("latency_threshold_ms = %v, want 300", in.LatencyThresholdMs)
	}
	m2 := &sloModel{SLIType: types.StringValue("availability"), LatencyThresholdMs: types.Int64Null()}
	if in := m2.apiSLO(); in.LatencyThresholdMs != nil {
		t.Errorf("latency_threshold_ms = %v, want nil for an availability SLI", *in.LatencyThresholdMs)
	}
}

// The dashboard write must carry the page and widget ids that are already known, so openlog keeps them (and
// with them the version history), while a page it has not seen yet is sent without an id.
func TestDashboardSendsKnownIDs(t *testing.T) {
	ctx := context.Background()
	var diags diagnostics
	m := &dashboardModel{
		Name: types.StringValue("Checkout"),
		Pages: []dashboardPageModel{
			{
				ID:   types.StringValue("page-1"),
				Name: types.StringValue("Overview"),
				Widgets: []dashboardWidgetModel{
					{
						ID: types.StringValue("widget-1"), Title: types.StringValue("Errors"),
						Visualization: types.StringValue("line"),
						Layout: &widgetLayoutModel{
							X: types.Int64Value(0), Y: types.Int64Value(0), W: types.Int64Value(6), H: types.Int64Value(3),
						},
						Query:   types.StringValue("SELECT count(*) FROM Log TIMESERIES"),
						Options: &widgetOptionsModel{Stacked: types.BoolValue(false), Legend: types.BoolNull()},
					},
					// A widget Terraform just added: its id is still unknown.
					{
						ID: types.StringUnknown(), Title: types.StringValue("New"),
						Visualization: types.StringValue("table"),
						Layout: &widgetLayoutModel{
							X: types.Int64Value(6), Y: types.Int64Value(0), W: types.Int64Value(6), H: types.Int64Value(3),
						},
					},
				},
			},
		},
	}
	in := m.apiDashboard(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("apiDashboard: %v", diags)
	}
	if len(in.Pages) != 1 || in.Pages[0].ID != "page-1" {
		t.Fatalf("page id = %q, want page-1", in.Pages[0].ID)
	}
	if in.Pages[0].Widgets[0].ID != "widget-1" {
		t.Errorf("widget id = %q, want widget-1", in.Pages[0].Widgets[0].ID)
	}
	if in.Pages[0].Widgets[1].ID != "" {
		t.Errorf("new widget id = %q, want it empty so openlog assigns one", in.Pages[0].Widgets[1].ID)
	}
	// An unset legend must stay unset rather than becoming false.
	if in.Pages[0].Widgets[0].Options.Legend != nil {
		t.Error("legend was sent although it is unset")
	}
}

func TestApplyDashboardMapsDocument(t *testing.T) {
	ctx := context.Background()
	var diags diagnostics
	legend := true
	m := &dashboardModel{}
	m.applyDashboard(ctx, &client.Dashboard{
		ID: "d1", Name: "Checkout", Visibility: "org", Version: 2,
		Variables: []client.Variable{{Name: "host", Type: "query", Values: []string{"web-1"}}},
		Pages: []client.Page{{
			ID: "p1", Name: "Overview",
			Widgets: []client.Widget{{
				ID: "w1", Title: "Errors", Visualization: "line",
				Layout:     client.Layout{X: 0, Y: 0, W: 6, H: 3},
				Thresholds: []client.Threshold{{Value: 100, Severity: "warning"}},
				Options:    client.WidgetOptions{Stacked: true, Legend: &legend},
			}},
		}},
	}, &diags)
	if diags.HasError() {
		t.Fatalf("applyDashboard: %v", diags)
	}
	if m.Version.ValueInt64() != 2 || len(m.Pages) != 1 || len(m.Pages[0].Widgets) != 1 {
		t.Fatalf("document not mapped: %+v", m)
	}
	w := m.Pages[0].Widgets[0]
	if w.ID.ValueString() != "w1" || w.Layout.W.ValueInt64() != 6 || len(w.Thresholds) != 1 {
		t.Errorf("widget not mapped: %+v", w)
	}
	if !w.Options.Stacked.ValueBool() || !w.Options.Legend.ValueBool() {
		t.Errorf("options not mapped: %+v", w.Options)
	}
	if len(m.Variables) != 1 || m.Variables[0].Name.ValueString() != "host" {
		t.Errorf("variables not mapped: %+v", m.Variables)
	}
}

func TestApplyRoutingRuleFillsEmptyMatch(t *testing.T) {
	ctx := context.Background()
	var diags diagnostics
	m := &routingRuleModel{}
	m.applyRoutingRule(ctx, &client.RoutingRule{
		ID: "rr1", Name: "default", Position: 0, Enabled: true, IsDefault: true, ChannelIDs: []string{"c1"},
	}, &diags)
	if diags.HasError() {
		t.Fatalf("applyRoutingRule: %v", diags)
	}
	// The API answers with empty lists, so the match object exists in state with empty sets rather than nulls.
	if m.Match == nil || m.Match.Severities.IsNull() || len(m.Match.Severities.Elements()) != 0 {
		t.Errorf("match = %+v, want an empty (not null) match", m.Match)
	}
	if m.Match.TimeWindow != nil {
		t.Error("time_window was invented although openlog returned none")
	}
}

func TestChannelReadKeepsSecrets(t *testing.T) {
	ctx := context.Background()
	var diags diagnostics
	secrets, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"url": "https://hooks.example/x"})
	diags.Append(d...)
	m := &channelModel{Secrets: secrets}
	// A read answers with masked hints and no secrets at all.
	m.applyChannel(ctx, &client.Channel{
		ID: "c1", Name: "ops", Type: "webhook", Enabled: true,
		SecretHints: map[string]string{"url": "https://hooks…/•••f3a9"},
	}, &diags)
	if diags.HasError() {
		t.Fatalf("applyChannel: %v", diags)
	}
	if len(m.Secrets.Elements()) != 1 {
		t.Errorf("secrets = %v, want the configured secrets untouched by a read", m.Secrets)
	}
	if len(m.SecretHints.Elements()) != 1 {
		t.Errorf("secret_hints = %v, want the masked hints", m.SecretHints)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
