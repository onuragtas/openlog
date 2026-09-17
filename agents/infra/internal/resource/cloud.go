package resource

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
)

// Cloud instance facts (semantic-conventions §1 "Cloud instance facts"). The agent asks the
// instance metadata service once at start-up; the answer becomes resource attributes on every
// payload, so the backend can price the machine (docs/contracts/cost.md).
//
// The link-local metadata address is not routable, so on a machine that is not in a cloud the
// probe fails immediately (connection refused) or, behind a firewall that drops the packets,
// at the deadline below. Nothing here can block start-up longer than CloudProbeTimeout, and a
// failure is never an error: the host simply has no cloud attributes and the backend falls back
// to per-vCPU/per-GB pricing.

// CloudProbeTimeout bounds the whole detection (all providers together).
const CloudProbeTimeout = 1500 * time.Millisecond

// cloudRequestTimeout bounds one metadata request.
const cloudRequestTimeout = 500 * time.Millisecond

// Metadata endpoints. GCP is addressed by IP, not metadata.google.internal, so detection never
// waits for DNS on a host where that name does not resolve.
const (
	awsMetadataURL   = "http://169.254.169.254"
	gcpMetadataURL   = "http://169.254.169.254"
	azureMetadataURL = "http://169.254.169.254"
)

// Provider names (OTel cloud.provider).
const (
	ProviderAWS   = "aws"
	ProviderGCP   = "gcp"
	ProviderAzure = "azure"
)

// Lifecycle values of openlog.host.lifecycle.
const (
	LifecycleOnDemand    = "on-demand"
	LifecycleSpot        = "spot"
	LifecyclePreemptible = "preemptible"
)

// CloudFacts are the instance facts of a cloud machine. Every field is empty when it could not
// be determined; a zero CloudFacts means "not in a cloud, or the metadata service did not answer".
type CloudFacts struct {
	Provider     string // aws, gcp, azure
	Platform     string // aws_ec2, gcp_compute_engine, azure_vm
	InstanceType string // m5.large, n2-standard-4, Standard_D4s_v5
	Region       string
	Zone         string
	AccountID    string // AWS account, GCP project, Azure subscription
	Lifecycle    string // on-demand, spot, preemptible
}

// Detected reports whether a provider answered.
func (c CloudFacts) Detected() bool { return c.Provider != "" }

// cloudProber holds the endpoints and the client; the fields exist so tests can point the
// probes at httptest servers.
type cloudProber struct {
	aws, gcp, azure string
	client          *http.Client
}

// newCloudProber builds the prober used in production. The transport takes no proxy from the
// environment (a metadata request must never leave the machine) and keeps no connections.
func newCloudProber() *cloudProber {
	return &cloudProber{
		aws: awsMetadataURL, gcp: gcpMetadataURL, azure: azureMetadataURL,
		client: &http.Client{
			Timeout: cloudRequestTimeout,
			Transport: &http.Transport{
				Proxy:               nil,
				DialContext:         (&net.Dialer{Timeout: cloudRequestTimeout}).DialContext,
				DisableKeepAlives:   true,
				TLSHandshakeTimeout: cloudRequestTimeout,
			},
		},
	}
}

// DetectCloud returns the instance facts of the machine, or a zero CloudFacts when no metadata
// service answers within CloudProbeTimeout. hint is the DMI system vendor (CloudHint) and only
// chooses which provider is asked first; a wrong or missing hint costs one extra probe, it never
// suppresses detection.
func DetectCloud(ctx context.Context, hint string) CloudFacts {
	return newCloudProber().detect(ctx, hint)
}

// detect probes the providers concurrently and returns the first answer, preferring the hinted
// provider and otherwise the fixed order aws, gcp, azure, so the result does not depend on which
// probe happened to be quickest.
func (p *cloudProber) detect(ctx context.Context, hint string) CloudFacts {
	ctx, cancel := context.WithTimeout(ctx, CloudProbeTimeout)
	defer cancel()

	order := []string{ProviderAWS, ProviderGCP, ProviderAzure}
	if hint != "" {
		order = append([]string{hint}, order...)
	}
	probes := map[string]func(context.Context) CloudFacts{
		ProviderAWS: p.probeAWS, ProviderGCP: p.probeGCP, ProviderAzure: p.probeAzure,
	}

	results := make(map[string]chan CloudFacts, len(probes))
	started := map[string]bool{}
	for _, name := range order {
		probe, ok := probes[name]
		if !ok || started[name] {
			continue
		}
		started[name] = true
		ch := make(chan CloudFacts, 1)
		results[name] = ch
		go func() { ch <- probe(ctx) }()
	}
	for _, name := range order {
		ch, ok := results[name]
		if !ok {
			continue
		}
		delete(results, name)
		select {
		case f := <-ch:
			if f.Detected() {
				return f
			}
		case <-ctx.Done():
			return CloudFacts{}
		}
	}
	return CloudFacts{}
}

// get performs one metadata request and returns the trimmed body. Any failure (no route, refused,
// timeout, non-200) returns "" so a caller can simply treat it as "unknown".
func (p *cloudProber) get(ctx context.Context, method, url string, headers map[string]string) string {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return ""
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) //nolint:errcheck // drained only to reuse nothing
		return ""
	}
	// Metadata answers are small; a hostile or broken server must not stream forever.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---- AWS EC2 (IMDSv2, falling back to IMDSv1) ----

type awsIdentityDocument struct {
	AccountID        string `json:"accountId"`
	InstanceType     string `json:"instanceType"`
	Region           string `json:"region"`
	AvailabilityZone string `json:"availabilityZone"`
}

func (p *cloudProber) probeAWS(ctx context.Context) CloudFacts {
	headers := map[string]string{}
	// IMDSv2: a token is required on instances configured that way; instances that still allow
	// IMDSv1 answer the PUT with 404/405 and the unauthenticated GET below works.
	if token := p.get(ctx, http.MethodPut, p.aws+"/latest/api/token",
		map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "60"}); token != "" {
		headers["X-aws-ec2-metadata-token"] = token
	}
	body := p.get(ctx, http.MethodGet, p.aws+"/latest/dynamic/instance-identity/document", headers)
	if body == "" {
		return CloudFacts{}
	}
	var doc awsIdentityDocument
	if err := json.Unmarshal([]byte(body), &doc); err != nil || doc.InstanceType == "" {
		return CloudFacts{}
	}
	f := CloudFacts{
		Provider: ProviderAWS, Platform: "aws_ec2", InstanceType: doc.InstanceType,
		Region: doc.Region, Zone: doc.AvailabilityZone, AccountID: doc.AccountID,
		Lifecycle: LifecycleOnDemand,
	}
	if f.Region == "" {
		f.Region = regionOfZone(f.Zone)
	}
	// instance-life-cycle exists since 2019; an older instance simply stays on-demand.
	switch p.get(ctx, http.MethodGet, p.aws+"/latest/meta-data/instance-life-cycle", headers) {
	case "spot":
		f.Lifecycle = LifecycleSpot
	case "on-demand", "":
	case "scheduled", "capacity-block":
		f.Lifecycle = LifecycleOnDemand
	}
	return f
}

// ---- GCP Compute Engine ----

func (p *cloudProber) probeGCP(ctx context.Context) CloudFacts {
	h := map[string]string{"Metadata-Flavor": "Google"}
	base := p.gcp + "/computeMetadata/v1/instance/"
	// machine-type is "projects/<num>/machineTypes/n2-standard-4"; zone is "projects/<num>/zones/<zone>".
	machine := lastPathSegment(p.get(ctx, http.MethodGet, base+"machine-type", h))
	if machine == "" {
		return CloudFacts{}
	}
	zone := lastPathSegment(p.get(ctx, http.MethodGet, base+"zone", h))
	f := CloudFacts{
		Provider: ProviderGCP, Platform: "gcp_compute_engine", InstanceType: machine,
		Zone: zone, Region: regionOfZone(zone), Lifecycle: LifecycleOnDemand,
		AccountID: p.get(ctx, http.MethodGet, p.gcp+"/computeMetadata/v1/project/project-id", h),
	}
	// Spot VMs are the successor of preemptible VMs; both are reported, so the price table can
	// distinguish them if it ever prices them differently.
	switch strings.ToUpper(p.get(ctx, http.MethodGet, base+"scheduling/provisioning-model", h)) {
	case "SPOT":
		f.Lifecycle = LifecycleSpot
	case "STANDARD", "":
		if strings.EqualFold(p.get(ctx, http.MethodGet, base+"scheduling/preemptible", h), "TRUE") {
			f.Lifecycle = LifecyclePreemptible
		}
	}
	return f
}

// ---- Azure ----

type azureInstanceMetadata struct {
	Compute struct {
		VMSize         string `json:"vmSize"`
		Location       string `json:"location"`
		Zone           string `json:"zone"`
		SubscriptionID string `json:"subscriptionId"`
		Priority       string `json:"priority"`
	} `json:"compute"`
}

func (p *cloudProber) probeAzure(ctx context.Context) CloudFacts {
	body := p.get(ctx, http.MethodGet, p.azure+"/metadata/instance?api-version=2021-02-01",
		map[string]string{"Metadata": "true"})
	if body == "" {
		return CloudFacts{}
	}
	var doc azureInstanceMetadata
	if err := json.Unmarshal([]byte(body), &doc); err != nil || doc.Compute.VMSize == "" {
		return CloudFacts{}
	}
	c := doc.Compute
	f := CloudFacts{
		Provider: ProviderAzure, Platform: "azure_vm", InstanceType: c.VMSize,
		Region: c.Location, AccountID: c.SubscriptionID, Lifecycle: LifecycleOnDemand,
	}
	// Availability zone is the bare number ("1") and only set for zonal VMs.
	if c.Zone != "" && c.Location != "" {
		f.Zone = c.Location + "-" + c.Zone
	}
	if strings.EqualFold(c.Priority, "Spot") {
		f.Lifecycle = LifecycleSpot
	}
	return f
}

// ---- helpers ----

// regionOfZone strips the zone suffix: "eu-central-1a" → "eu-central-1", "europe-west1-b" → "europe-west1".
func regionOfZone(zone string) string {
	if zone == "" {
		return ""
	}
	if i := strings.LastIndex(zone, "-"); i > 0 {
		// GCP zones end in "-<letter>"; AWS zones end in a letter without a separator.
		if len(zone)-i == 2 {
			return zone[:i]
		}
	}
	if last := zone[len(zone)-1]; last >= 'a' && last <= 'z' {
		return zone[:len(zone)-1]
	}
	return zone
}

// lastPathSegment returns the part after the last "/" ("projects/1/machineTypes/n2-standard-4" → "n2-standard-4").
func lastPathSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// CloudHint reads the DMI system vendor and maps it to a provider name, or "" when the machine
// does not say. It is only a hint for probe ordering (see detect); the file does not exist on
// macOS, Windows or inside a container without /sys, which is not an error.
func CloudHint(fs *hostfs.FS) string {
	vendor, err := fs.ReadString("/sys/class/dmi/id/sys_vendor")
	if err != nil {
		return ""
	}
	switch v := strings.ToLower(strings.TrimSpace(vendor)); {
	case strings.Contains(v, "amazon"):
		return ProviderAWS
	case strings.Contains(v, "google"):
		return ProviderGCP
	case strings.Contains(v, "microsoft"):
		return ProviderAzure
	}
	return ""
}
