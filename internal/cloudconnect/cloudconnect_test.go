package cloudconnect

import (
	"errors"
	"strings"
	"testing"
)

// asError is errors.As with a readable call site in the provider tests.
func asError(err error, target any) bool { return errors.As(err, target) }

// fieldOf returns the field a ValidationError names ("" when err is not one).
func fieldOf(err error) string {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Field
	}
	return ""
}

func validAWSInput() Input {
	return Input{Name: "Production AWS", Provider: ProviderAWS, Scopes: []string{"eu-central-1"},
		Services: []string{"rds"}, Credentials: &Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}}
}

func TestValidateFillsDefaults(t *testing.T) {
	in := validAWSInput()
	if err := in.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if in.IngestMode != IngestPoll {
		t.Errorf("ingest_mode = %q, want %q", in.IngestMode, IngestPoll)
	}
	if in.PollIntervalSeconds != DefaultInterval {
		t.Errorf("poll_interval_seconds = %d, want %d", in.PollIntervalSeconds, DefaultInterval)
	}
	if in.MaxMetricsPerPoll != DefaultMetricsPerPoll || in.MaxAPICallsPerPoll != DefaultAPICallsPerPoll {
		t.Errorf("caps = %d/%d", in.MaxMetricsPerPoll, in.MaxAPICallsPerPoll)
	}
}

// Services are stored in catalog order and deduplicated, so a list reads the same however it was submitted.
func TestValidateNormalizesServicesAndScopes(t *testing.T) {
	in := validAWSInput()
	in.Services = []string{"lambda", "rds", "rds"}
	in.Scopes = []string{"eu-central-1", "eu-central-1", " us-east-1 "}
	if err := in.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(in.Services, ","); got != "rds,lambda" {
		t.Errorf("services = %q, want catalog order \"rds,lambda\"", got)
	}
	if got := strings.Join(in.Scopes, ","); got != "eu-central-1,us-east-1" {
		t.Errorf("scopes = %q", got)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*Input)
		field string
	}{
		{"empty name", func(in *Input) { in.Name = "  " }, "name"},
		{"unknown provider", func(in *Input) { in.Provider = "oracle" }, "provider"},
		{"unknown ingest mode", func(in *Input) { in.IngestMode = "stream" }, "ingest_mode"},
		{"no scopes", func(in *Input) { in.Scopes = nil }, "scopes"},
		{"scope with a slash", func(in *Input) { in.Scopes = []string{"eu/central"} }, "scopes[0]"},
		{"no services", func(in *Input) { in.Services = nil }, "services"},
		{"service of another provider", func(in *Input) { in.Services = []string{"azure_sql"} }, "services[0]"},
		{"interval too short", func(in *Input) { in.PollIntervalSeconds = 30 }, "poll_interval_seconds"},
		{"interval too long", func(in *Input) { in.PollIntervalSeconds = 90000 }, "poll_interval_seconds"},
		{"metric cap too low", func(in *Input) { in.MaxMetricsPerPoll = 1 }, "max_metrics_per_poll"},
		{"call cap too high", func(in *Input) { in.MaxAPICallsPerPoll = 99999 }, "max_api_calls_per_poll"},
		{"credentials of another provider", func(in *Input) {
			in.Credentials = &Credentials{TenantID: "t", ClientID: "c", ClientSecret: "s"}
		}, "credentials.access_key_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validAWSInput()
			tc.mut(&in)
			err := in.Validate()
			if err == nil {
				t.Fatal("accepted an invalid input")
			}
			if got := fieldOf(err); got != tc.field {
				t.Errorf("field = %q, want %q (%v)", got, tc.field, err)
			}
		})
	}
}

func TestCredentialsValidate(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		creds    Credentials
		field    string
	}{
		{"aws ok", ProviderAWS, Credentials{AccessKeyID: "A", SecretAccessKey: "S"}, ""},
		{"aws with a session token", ProviderAWS, Credentials{AccessKeyID: "A", SecretAccessKey: "S", SessionToken: "T"}, ""},
		{"aws without a secret", ProviderAWS, Credentials{AccessKeyID: "A"}, "credentials.secret_access_key"},
		{"aws with an azure field", ProviderAWS, Credentials{AccessKeyID: "A", SecretAccessKey: "S", TenantID: "T"}, "credentials.tenant_id"},
		{"azure ok", ProviderAzure, Credentials{TenantID: "T", ClientID: "C", ClientSecret: "S"}, ""},
		{"azure without a secret", ProviderAzure, Credentials{TenantID: "T", ClientID: "C"}, "credentials.client_secret"},
		{"gcp ok", ProviderGCP, Credentials{ClientEmail: "sa@x", PrivateKey: "pem"}, ""},
		{"gcp without a key", ProviderGCP, Credentials{ClientEmail: "sa@x"}, "credentials.private_key"},
		{"unknown provider", "oracle", Credentials{}, "provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.creds.Validate(tc.provider)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("rejected valid credentials: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted invalid credentials")
			}
			if got := fieldOf(err); got != tc.field {
				t.Errorf("field = %q, want %q (%v)", got, tc.field, err)
			}
		})
	}
}

// A credential value must never appear in the audit detail of a change.
func TestAuditDetailsCarryNoCredentials(t *testing.T) {
	in := validAWSInput()
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	details := string(auditDetails(in))
	if strings.Contains(details, "AKIDEXAMPLE") || strings.Contains(details, "secret") {
		t.Fatalf("audit details carry a credential: %s", details)
	}
	if !strings.Contains(details, `"credentials_changed":true`) {
		t.Errorf("audit details do not record that credentials changed: %s", details)
	}
}

func TestSnakeName(t *testing.T) {
	cases := map[string]string{
		"CPUUtilization":               "cpu_utilization",
		"DatabaseConnections":          "database_connections",
		"FreeStorageSpace":             "free_storage_space",
		"ReadIOPS":                     "read_iops",
		"HTTPCode_Target_5XX_Count":    "http_code_target_5xx_count",
		"NumberOfObjects":              "number_of_objects",
		"ConcurrentExecutions":         "concurrent_executions",
		"Percentage CPU":               "percentage_cpu",
		"Available Memory Bytes":       "available_memory_bytes",
		"cpu/utilization":              "cpu_utilization",
		"network/received_bytes_count": "network_received_bytes_count",
		"connection_successful":        "connection_successful",
		"Http5xx":                      "http_5xx",
		"SuccessE2ELatency":            "success_e2e_latency",
		"UsedCapacity":                 "used_capacity",
	}
	for in, want := range cases {
		if got := snakeName(in); got != want {
			t.Errorf("snakeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The metric name and the attributes are the contract with the Metrics Explorer and the alert rules
// (semantic-conventions.md §9), so they are pinned here.
func TestPointNameAndAttributes(t *testing.T) {
	p := Point{Provider: ProviderAWS, Service: "rds", Platform: "aws_rds", MetricName: "CPUUtilization",
		Unit: "1", Stat: "Average", Region: "eu-central-1", AccountID: "123456789012",
		ResourceName: "orders-db", Dimensions: map[string]string{"cloud.aws.dimension.engine": "postgres"}}

	if got, want := p.Name(), "cloud.aws.rds.cpu_utilization"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	res := p.ResourceAttributes("conn-1", "Prod AWS")
	for k, want := range map[string]string{
		"cloud.provider": "aws", "cloud.platform": "aws_rds", "cloud.region": "eu-central-1",
		"cloud.account.id": "123456789012", "cloud.resource.name": "orders-db",
		"openlog.entity.type": "cloud_resource", "openlog.cloud.connection.id": "conn-1",
		"openlog.cloud.connection.name": "Prod AWS", "openlog.cloud.service": "rds",
	} {
		if res[k] != want {
			t.Errorf("resource attribute %s = %q, want %q", k, res[k], want)
		}
	}
	// An empty value is left out rather than stored as "".
	if _, ok := res["cloud.resource.id"]; ok {
		t.Error("an empty resource id was stored as an attribute")
	}
	attrs := p.Attributes()
	if attrs["cloud.metric.name"] != "CPUUtilization" || attrs["cloud.metric.stat"] != "Average" ||
		attrs["cloud.aws.dimension.engine"] != "postgres" {
		t.Errorf("attributes = %v", attrs)
	}
}

// Every catalog entry must be usable: a provider, a namespace, a platform and at least one metric.
func TestServiceCatalogIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, provider := range Providers() {
		services := Services(provider)
		if len(services) == 0 {
			t.Errorf("provider %s has no services", provider)
		}
		for _, s := range services {
			key := provider + "/" + s.ID
			if seen[key] {
				t.Errorf("duplicate service %s", key)
			}
			seen[key] = true
			if s.Provider != provider || s.Namespace == "" || s.Platform == "" || len(s.Metrics) == 0 {
				t.Errorf("service %s is incomplete: %+v", key, s)
			}
			for _, m := range s.Metrics {
				if m.Name == "" || m.Unit == "" || m.Stat == "" {
					t.Errorf("service %s has an incomplete metric %+v", key, m)
				}
			}
			if got, ok := ServiceByID(provider, s.ID); !ok || got.ID != s.ID {
				t.Errorf("ServiceByID(%s, %s) did not return the service", provider, s.ID)
			}
		}
	}
}

func TestBudgetCaps(t *testing.T) {
	b := NewBudget(2, 1)
	if !b.TakeCall() {
		t.Fatal("the first call was refused")
	}
	if b.TakeCall() {
		t.Fatal("a call beyond the cap was allowed")
	}
	if !b.AddMetric() || !b.AddMetric() {
		t.Fatal("a metric within the cap was refused")
	}
	if b.AddMetric() {
		t.Fatal("a metric beyond the cap was allowed")
	}
	b.Throttle()
	metrics, calls, throttled, capped := b.Stats()
	if metrics != 2 || calls != 1 || throttled != 1 {
		t.Errorf("stats = %d/%d/%d", metrics, calls, throttled)
	}
	// The first cap that was hit is the one reported.
	if capped != "api_calls" {
		t.Errorf("capped at %q, want \"api_calls\"", capped)
	}
	if b.MetricsLeft() != 0 {
		t.Errorf("MetricsLeft = %d", b.MetricsLeft())
	}
}
