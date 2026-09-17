package resource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"
)

// awsServer serves the IMDS endpoints; requireToken emulates an instance that enforces IMDSv2.
func awsServer(t *testing.T, requireToken bool, lifecycle string) *httptest.Server {
	t.Helper()
	const token = "tok-123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest/api/token" {
			if r.Method != http.MethodPut || r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !requireToken {
				w.WriteHeader(http.StatusNotFound) // IMDSv1-only instance
				return
			}
			w.Write([]byte(token))
			return
		}
		if requireToken && r.Header.Get("X-aws-ec2-metadata-token") != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/latest/dynamic/instance-identity/document":
			w.Write([]byte(`{"accountId":"123456789012","instanceType":"m5.large","region":"eu-central-1","availabilityZone":"eu-central-1a"}`))
		case "/latest/meta-data/instance-life-cycle":
			if lifecycle == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write([]byte(lifecycle))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// deadServer is an address nothing listens on, so a probe of it fails at once.
func deadServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

func TestProbeAWS(t *testing.T) {
	cases := []struct {
		name          string
		requireToken  bool
		lifecycle     string
		wantLifecycle string
	}{
		{"IMDSv2 on-demand", true, "on-demand", LifecycleOnDemand},
		{"IMDSv2 spot", true, "spot", LifecycleSpot},
		{"IMDSv1 without instance-life-cycle", false, "", LifecycleOnDemand},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := awsServer(t, c.requireToken, c.lifecycle)
			p := &cloudProber{aws: srv.URL, client: srv.Client()}
			f := p.probeAWS(context.Background())
			want := CloudFacts{Provider: ProviderAWS, Platform: "aws_ec2", InstanceType: "m5.large",
				Region: "eu-central-1", Zone: "eu-central-1a", AccountID: "123456789012", Lifecycle: c.wantLifecycle}
			if f != want {
				t.Errorf("facts = %+v, want %+v", f, want)
			}
		})
	}
}

func TestProbeGCP(t *testing.T) {
	provisioning, preemptible := "STANDARD", "FALSE"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/computeMetadata/v1/instance/machine-type":
			w.Write([]byte("projects/123456/machineTypes/n2-standard-4"))
		case "/computeMetadata/v1/instance/zone":
			w.Write([]byte("projects/123456/zones/europe-west1-b"))
		case "/computeMetadata/v1/project/project-id":
			w.Write([]byte("my-project"))
		case "/computeMetadata/v1/instance/scheduling/provisioning-model":
			w.Write([]byte(provisioning))
		case "/computeMetadata/v1/instance/scheduling/preemptible":
			w.Write([]byte(preemptible))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &cloudProber{gcp: srv.URL, client: srv.Client()}

	f := p.probeGCP(context.Background())
	want := CloudFacts{Provider: ProviderGCP, Platform: "gcp_compute_engine", InstanceType: "n2-standard-4",
		Region: "europe-west1", Zone: "europe-west1-b", AccountID: "my-project", Lifecycle: LifecycleOnDemand}
	if f != want {
		t.Errorf("facts = %+v, want %+v", f, want)
	}

	// The legacy preemptible flag is honored when the instance predates provisioning-model.
	preemptible = "TRUE"
	if got := p.probeGCP(context.Background()).Lifecycle; got != LifecyclePreemptible {
		t.Errorf("preemptible lifecycle = %q", got)
	}
	provisioning = "SPOT"
	if got := p.probeGCP(context.Background()).Lifecycle; got != LifecycleSpot {
		t.Errorf("spot lifecycle = %q", got)
	}
}

func TestProbeAzure(t *testing.T) {
	priority := "Regular"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata") != "true" || r.URL.Query().Get("api-version") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"compute":{"vmSize":"Standard_D4s_v5","location":"westeurope","zone":"2",` +
			`"subscriptionId":"sub-1","priority":"` + priority + `"}}`))
	}))
	defer srv.Close()
	p := &cloudProber{azure: srv.URL, client: srv.Client()}

	f := p.probeAzure(context.Background())
	want := CloudFacts{Provider: ProviderAzure, Platform: "azure_vm", InstanceType: "Standard_D4s_v5",
		Region: "westeurope", Zone: "westeurope-2", AccountID: "sub-1", Lifecycle: LifecycleOnDemand}
	if f != want {
		t.Errorf("facts = %+v, want %+v", f, want)
	}

	priority = "Spot"
	if got := p.probeAzure(context.Background()).Lifecycle; got != LifecycleSpot {
		t.Errorf("spot lifecycle = %q", got)
	}
}

// A host that is not in a cloud must come back empty and quickly, not hang or panic.
func TestDetectNotInCloud(t *testing.T) {
	dead := deadServer(t)
	p := &cloudProber{aws: dead, gcp: dead, azure: dead, client: &http.Client{Timeout: cloudRequestTimeout}}
	start := time.Now()
	f := p.detect(context.Background(), "")
	if f.Detected() {
		t.Errorf("facts = %+v, want none", f)
	}
	if d := time.Since(start); d > CloudProbeTimeout {
		t.Errorf("detection took %s, want at most %s", d, CloudProbeTimeout)
	}
}

// A metadata service that never answers must not hold up the agent past the deadline.
func TestDetectHangingMetadataService(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	p := &cloudProber{aws: srv.URL, gcp: srv.URL, azure: srv.URL,
		client: &http.Client{Timeout: 100 * time.Millisecond}}
	start := time.Now()
	if f := p.detect(context.Background(), ""); f.Detected() {
		t.Errorf("facts = %+v, want none", f)
	}
	if d := time.Since(start); d > CloudProbeTimeout {
		t.Errorf("detection took %s, want at most %s", d, CloudProbeTimeout)
	}
}

// With several providers reachable the result is the fixed order, not whichever answered first.
func TestDetectPrefersHintedProvider(t *testing.T) {
	aws := awsServer(t, true, "on-demand")
	azure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"compute":{"vmSize":"Standard_D4s_v5","location":"westeurope"}}`))
	}))
	defer azure.Close()

	p := &cloudProber{aws: aws.URL, gcp: deadServer(t), azure: azure.URL, client: aws.Client()}
	if got := p.detect(context.Background(), "").Provider; got != ProviderAWS {
		t.Errorf("without hint: provider = %q, want aws", got)
	}
	if got := p.detect(context.Background(), ProviderAzure).Provider; got != ProviderAzure {
		t.Errorf("with azure hint: provider = %q, want azure", got)
	}
}

func TestCloudHint(t *testing.T) {
	cases := map[string]string{
		"Amazon EC2":            ProviderAWS,
		"Google":                ProviderGCP,
		"Microsoft Corporation": ProviderAzure,
		"QEMU":                  "",
		"Dell Inc.":             "",
	}
	for vendor, want := range cases {
		fs := hostfstest.Build(t, map[string]string{"/sys/class/dmi/id/sys_vendor": vendor + "\n"})
		if got := CloudHint(fs); got != want {
			t.Errorf("CloudHint(%q) = %q, want %q", vendor, got, want)
		}
	}
	// No DMI (macOS, Windows, a container without /sys) is not an error.
	if got := CloudHint(hostfstest.Build(t, map[string]string{})); got != "" {
		t.Errorf("CloudHint without DMI = %q", got)
	}
}

func TestRegionOfZone(t *testing.T) {
	cases := map[string]string{
		"eu-central-1a":  "eu-central-1",
		"us-east-1f":     "us-east-1",
		"europe-west1-b": "europe-west1",
		"us-central1-a":  "us-central1",
		"":               "",
	}
	for zone, want := range cases {
		if got := regionOfZone(zone); got != want {
			t.Errorf("regionOfZone(%q) = %q, want %q", zone, got, want)
		}
	}
}
