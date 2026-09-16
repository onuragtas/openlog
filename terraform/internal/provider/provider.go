// Package provider implements the openlog Terraform provider on the Terraform Plugin Framework: the provider
// block itself (endpoint, API key, organization, timeout), the managed resources (alert rules, notification
// channels, routing rules, SLOs, dashboards) and the read-only data sources.
//
// Every resource maps one API object of docs/contracts/api.md. The rules the whole package follows:
//   - the API answer, never the plan, becomes the new state, so server-side defaulting and normalization show
//     up as a diff on the next plan instead of drifting silently;
//   - a 404 on read means the object is gone and the resource is removed from state;
//   - a validation error keeps the field path the API reports (condition.threshold) and is attached to that
//     attribute, so the message lands where the user has to fix it.
package provider

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onuragtas/openlog/terraform/internal/client"
)

// Environment variables the provider block falls back to.
const (
	envEndpoint = "OPENLOG_ENDPOINT"
	envAPIKey   = "OPENLOG_API_KEY"
	envOrgID    = "OPENLOG_ORG_ID"
)

// Ensure the implementation satisfies the framework interfaces.
var _ provider.Provider = (*openlogProvider)(nil)

type openlogProvider struct {
	// version is stamped at build time and travels in the User-Agent.
	version string
}

// New returns the provider factory the plugin server and the tests use.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &openlogProvider{version: version} }
}

type providerModel struct {
	Endpoint       types.String `tfsdk:"endpoint"`
	APIKey         types.String `tfsdk:"api_key"`
	OrgID          types.String `tfsdk:"org_id"`
	TimeoutSeconds types.Int64  `tfsdk:"timeout_seconds"`
}

func (p *openlogProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "openlog"
	resp.Version = p.version
}

func (p *openlogProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an openlog installation's alerting, SLO and dashboard configuration through its HTTP API " +
			"(`/api/v1`, docs/contracts/api.md).",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Base URL of the openlog API without `/api/v1`, for example `https://openlog.example.com`. " +
					"Falls back to the `" + envEndpoint + "` environment variable.",
			},
			"api_key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "API key (`ola_…`) sent as a Bearer token. Falls back to the `" + envAPIKey +
					"` environment variable, which is the recommended way to supply it: a key in the configuration ends up in " +
					"the state file. The key must be allowed to write — see docs/operations/terraform.md.",
			},
			"org_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Organization to act in (`X-Openlog-Org-Id`). Only needed when the credential belongs to " +
					"several organizations; it must match the key's organization. Falls back to `" + envOrgID + "`.",
			},
			"timeout_seconds": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Timeout of one API request in seconds (default 30). Raise it for large dashboard " +
					"documents on a slow link.",
			},
		},
	}
}

func (p *openlogProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A value that is still unknown at configure time comes from another resource's attribute; the provider
	// cannot be built from it, and the message has to say which attribute to make static.
	for name, v := range map[string]attrUnknown{
		"endpoint":        {cfg.Endpoint.IsUnknown()},
		"api_key":         {cfg.APIKey.IsUnknown()},
		"org_id":          {cfg.OrgID.IsUnknown()},
		"timeout_seconds": {cfg.TimeoutSeconds.IsUnknown()},
	} {
		if v.unknown {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Unknown provider configuration",
				"The provider cannot be configured while "+name+" is unknown. Set it statically, from a variable or from an "+
					"environment variable instead of from another resource's attribute.")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := firstNonEmpty(cfg.Endpoint.ValueString(), os.Getenv(envEndpoint))
	apiKey := firstNonEmpty(cfg.APIKey.ValueString(), os.Getenv(envAPIKey))
	orgID := firstNonEmpty(cfg.OrgID.ValueString(), os.Getenv(envOrgID))
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Missing openlog endpoint",
			"Set the provider's endpoint attribute or the "+envEndpoint+" environment variable to the base URL of the "+
				"openlog API, for example https://openlog.example.com.")
	}
	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing openlog API key",
			"Set the provider's api_key attribute or the "+envAPIKey+" environment variable to an API key (ola_…) that may "+
				"write. Prefer the environment variable: a key in the configuration is written to the state file.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	timeout := time.Duration(0)
	if !cfg.TimeoutSeconds.IsNull() {
		if s := cfg.TimeoutSeconds.ValueInt64(); s < 1 || s > 3600 {
			resp.Diagnostics.AddAttributeError(path.Root("timeout_seconds"), "Invalid timeout",
				"timeout_seconds must be between 1 and 3600, got "+strconv.FormatInt(s, 10)+".")
			return
		}
		timeout = time.Duration(cfg.TimeoutSeconds.ValueInt64()) * time.Second
	}

	c, err := client.New(client.Config{
		Endpoint:  endpoint,
		APIKey:    apiKey,
		OrgID:     orgID,
		Timeout:   timeout,
		UserAgent: "terraform-provider-openlog/" + p.version,
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid openlog provider configuration", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *openlogProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAlertRuleResource,
		NewNotificationChannelResource,
		NewAlertRoutingRuleResource,
		NewSLOResource,
		NewDashboardResource,
	}
}

func (p *openlogProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewAlertChannelDataSource,
		NewDashboardDataSource,
	}
}

// attrUnknown keeps the Configure loop above readable.
type attrUnknown struct{ unknown bool }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
