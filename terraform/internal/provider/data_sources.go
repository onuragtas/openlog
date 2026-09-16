package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onuragtas/openlog/terraform/internal/client"
)

// Data sources look existing objects up, mostly to reference an id that something else created: a channel the
// on-call team manages by hand, or a dashboard imported through the web UI. Both accept either the id or the
// name; a name that matches several objects is an error rather than an arbitrary pick.

// ---- openlog_alert_channel ----

var (
	_ datasource.DataSource                     = (*alertChannelDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*alertChannelDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*alertChannelDataSource)(nil)
)

// NewAlertChannelDataSource returns the openlog_alert_channel data source.
func NewAlertChannelDataSource() datasource.DataSource { return &alertChannelDataSource{} }

type alertChannelDataSource struct{ client *client.Client }

type alertChannelDataModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Type        types.String `tfsdk:"type"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	SecretHints types.Map    `tfsdk:"secret_hints"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

func (d *alertChannelDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_channel"
}

func (d *alertChannelDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks a notification channel up by id or name, to reference a channel this configuration " +
			"does not manage. Secrets are never returned, only their masked hints.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Identifier of the channel. Set either this or `name`.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Name of the channel. Set either this or `id`; the name must be unique in the organization.",
			},
			"type":    schema.StringAttribute{Computed: true, MarkdownDescription: "Channel type."},
			"enabled": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether notifications are delivered."},
			"secret_hints": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Masked form of each stored secret.",
			},
			"created_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Creation time (RFC3339)."},
			"updated_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Time of the last change (RFC3339)."},
		},
	}
}

func (d *alertChannelDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *alertChannelDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = dataSourceClient(req, resp)
}

func (d *alertChannelDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg alertChannelDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var (
		ch  *client.Channel
		err error
	)
	if !cfg.ID.IsNull() {
		ch, err = d.client.GetChannel(ctx, cfg.ID.ValueString())
	} else {
		ch, err = d.client.FindChannelByName(ctx, cfg.Name.ValueString())
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "read the notification channel", err)
		return
	}
	cfg.ID = types.StringValue(ch.ID)
	cfg.Name = types.StringValue(ch.Name)
	cfg.Type = types.StringValue(ch.Type)
	cfg.Enabled = types.BoolValue(ch.Enabled)
	cfg.SecretHints = mapOfStrings(ctx, ch.SecretHints, &resp.Diagnostics)
	cfg.CreatedAt = types.StringValue(ch.CreatedAt)
	cfg.UpdatedAt = types.StringValue(ch.UpdatedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// ---- openlog_dashboard ----

var (
	_ datasource.DataSource                     = (*dashboardDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*dashboardDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*dashboardDataSource)(nil)
)

// NewDashboardDataSource returns the openlog_dashboard data source.
func NewDashboardDataSource() datasource.DataSource { return &dashboardDataSource{} }

type dashboardDataSource struct{ client *client.Client }

type dashboardDataModel struct {
	ID          types.String         `tfsdk:"id"`
	Name        types.String         `tfsdk:"name"`
	Description types.String         `tfsdk:"description"`
	Visibility  types.String         `tfsdk:"visibility"`
	Version     types.Int64          `tfsdk:"version"`
	Document    jsontypes.Normalized `tfsdk:"document"`
	CreatedAt   types.String         `tfsdk:"created_at"`
	UpdatedAt   types.String         `tfsdk:"updated_at"`
}

func (d *dashboardDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (d *dashboardDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks a dashboard up by id or name. Its variables and pages come back as one JSON " +
			"document, which `jsondecode` turns into values another resource can use.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Identifier of the dashboard. Set either this or `name`.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Name of the dashboard. Set either this or `id`; the name must be unique in the organization.",
			},
			"description": schema.StringAttribute{Computed: true, MarkdownDescription: "Description of the dashboard."},
			"visibility":  schema.StringAttribute{Computed: true, MarkdownDescription: "`org` or `private`."},
			"version":     schema.Int64Attribute{Computed: true, MarkdownDescription: "Version of the document."},
			"document": schema.StringAttribute{
				Computed:   true,
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "`{\"variables\": […], \"pages\": […]}` as stored, including the page and widget ids " +
					"openlog assigned.",
			},
			"created_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Creation time (RFC3339)."},
			"updated_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Time of the last change (RFC3339)."},
		},
	}
}

func (d *dashboardDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *dashboardDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = dataSourceClient(req, resp)
}

func (d *dashboardDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg dashboardDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var (
		db  *client.Dashboard
		err error
	)
	if !cfg.ID.IsNull() {
		db, err = d.client.GetDashboard(ctx, cfg.ID.ValueString())
	} else {
		db, err = d.client.FindDashboardByName(ctx, cfg.Name.ValueString())
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "read the dashboard", err)
		return
	}
	doc, jerr := json.Marshal(map[string]any{"variables": db.Variables, "pages": db.Pages})
	if jerr != nil {
		resp.Diagnostics.AddError("Could not read the dashboard", "encoding the dashboard document: "+jerr.Error())
		return
	}
	cfg.ID = types.StringValue(db.ID)
	cfg.Name = types.StringValue(db.Name)
	cfg.Description = types.StringValue(db.Description)
	cfg.Visibility = types.StringValue(db.Visibility)
	cfg.Version = types.Int64Value(int64(db.Version))
	cfg.Document = jsontypes.NewNormalizedValue(string(doc))
	cfg.CreatedAt = types.StringValue(db.CreatedAt)
	cfg.UpdatedAt = types.StringValue(db.UpdatedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
