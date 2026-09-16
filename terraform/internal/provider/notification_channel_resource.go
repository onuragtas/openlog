package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onuragtas/openlog/terraform/internal/client"
)

// openlog_notification_channel — where incidents are delivered (docs/contracts/alerting.md §5.3–§5.4).
//
// Secrets (a Slack or webhook URL, an HMAC secret, an SMTP password, a PagerDuty routing key, an Opsgenie API
// key) are write-only: openlog encrypts them with AES-256-GCM and never returns them, answering with masked
// secret_hints instead. The `secrets` attribute is therefore marked sensitive — Terraform prints
// `(sensitive value)` for it in a plan — and a read never touches it, because the API has nothing to compare
// against. The value does end up in the state file, so the state belongs in an encrypted backend.

var (
	_ resource.Resource                = (*notificationChannelResource)(nil)
	_ resource.ResourceWithConfigure   = (*notificationChannelResource)(nil)
	_ resource.ResourceWithImportState = (*notificationChannelResource)(nil)
)

// NewNotificationChannelResource returns the openlog_notification_channel resource.
func NewNotificationChannelResource() resource.Resource { return &notificationChannelResource{} }

type notificationChannelResource struct{ client *client.Client }

type channelModel struct {
	ID               types.String        `tfsdk:"id"`
	Name             types.String        `tfsdk:"name"`
	Type             types.String        `tfsdk:"type"`
	Enabled          types.Bool          `tfsdk:"enabled"`
	Config           *channelConfigModel `tfsdk:"config"`
	Secrets          types.Map           `tfsdk:"secrets"`
	SecretHints      types.Map           `tfsdk:"secret_hints"`
	GeneratedSecrets types.Map           `tfsdk:"generated_secrets"`
	CreatedByEmail   types.String        `tfsdk:"created_by_email"`
	CreatedAt        types.String        `tfsdk:"created_at"`
	UpdatedAt        types.String        `tfsdk:"updated_at"`
}

type channelConfigModel struct {
	To        types.List           `tfsdk:"to"`
	SMTP      *smtpModel           `tfsdk:"smtp"`
	PagerDuty *pagerDutyModel      `tfsdk:"pagerduty"`
	Opsgenie  *opsgenieConfigModel `tfsdk:"opsgenie"`
}

type smtpModel struct {
	Host     types.String `tfsdk:"host"`
	Port     types.Int64  `tfsdk:"port"`
	Username types.String `tfsdk:"username"`
	From     types.String `tfsdk:"from"`
	TLS      types.String `tfsdk:"tls"`
}

type pagerDutyModel struct {
	Region types.String `tfsdk:"region"`
}

type opsgenieConfigModel struct {
	Region     types.String             `tfsdk:"region"`
	Priority   types.String             `tfsdk:"priority"`
	Responders []opsgenieResponderModel `tfsdk:"responders"`
	Tags       types.List               `tfsdk:"tags"`
}

type opsgenieResponderModel struct {
	Type types.String `tfsdk:"type"`
	Name types.String `tfsdk:"name"`
	ID   types.String `tfsdk:"id"`
}

func (r *notificationChannelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_channel"
}

func (r *notificationChannelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A notification channel: Slack, e-mail, a generic webhook, Microsoft Teams, PagerDuty or " +
			"Opsgenie (docs/contracts/alerting.md §5.3). Needs `OPENLOG_SECRETS_KEY` on the server.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier of the channel, assigned by openlog. Use it in `channel_ids`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the channel, 1–200 characters.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 200)},
			},
			"type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "`slack`, `email`, `webhook`, `teams`, `pagerduty` or `opsgenie`. openlog does not " +
					"allow the type of a channel to change, so changing it here replaces the channel.",
				Validators:    []validator.String{stringvalidator.OneOf("slack", "email", "webhook", "teams", "pagerduty", "opsgenie")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether notifications are delivered. A disabled channel fails the rows queued for it.",
			},
			"secrets": schema.MapAttribute{
				Optional:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				MarkdownDescription: "Write-only secrets of the channel, by name: `url` (slack, teams, webhook), " +
					"`hmac_secret` (webhook), `smtp_password` (email), `routing_key` (pagerduty), `api_key` (opsgenie). " +
					"openlog stores them encrypted and never returns them, so Terraform cannot detect a change made " +
					"elsewhere; the masked `secret_hints` show which ones are set. A webhook created without an " +
					"`hmac_secret` gets a generated one in `generated_secrets`.",
			},
			"config": schema.SingleNestedAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Non-secret settings. Each section belongs to one channel type; Slack, Teams and webhook channels have none.",
				Attributes: map[string]schema.Attribute{
					"to": schema.ListAttribute{
						Optional:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "`email` channels: 1–50 recipient addresses.",
						Validators:          []validator.List{listvalidator.SizeBetween(1, 50)},
					},
					"smtp": schema.SingleNestedAttribute{
						Optional: true,
						MarkdownDescription: "`email` channels: an SMTP server for this channel instead of the global " +
							"`OPENLOG_SMTP_*` one. Its password is the `smtp_password` secret.",
						Attributes: map[string]schema.Attribute{
							"host": schema.StringAttribute{Required: true, MarkdownDescription: "Host name of the SMTP server."},
							"port": schema.Int64Attribute{
								Optional:            true,
								Computed:            true,
								MarkdownDescription: "Port; openlog defaults to 587, or 465 with `tls = \"tls\"`.",
								Validators:          []validator.Int64{int64validator.Between(1, 65535)},
							},
							"username": schema.StringAttribute{Optional: true, MarkdownDescription: "User name for PLAIN authentication over TLS."},
							"from":     schema.StringAttribute{Required: true, MarkdownDescription: "Envelope and header sender address."},
							"tls": schema.StringAttribute{
								Optional: true,
								Computed: true,
								MarkdownDescription: "`starttls` (default), `tls` (implicit TLS) or `none` (plaintext, " +
									"authentication refused).",
								Validators: []validator.String{stringvalidator.OneOf("starttls", "tls", "none")},
							},
						},
					},
					"pagerduty": schema.SingleNestedAttribute{
						Optional:            true,
						MarkdownDescription: "`pagerduty` channels. The Events API v2 integration key is the `routing_key` secret.",
						Attributes: map[string]schema.Attribute{
							"region": schema.StringAttribute{
								Optional:            true,
								Computed:            true,
								MarkdownDescription: "Service region: `us` (default) or `eu`.",
								Validators:          []validator.String{stringvalidator.OneOf("us", "eu")},
							},
						},
					},
					"opsgenie": schema.SingleNestedAttribute{
						Optional:            true,
						MarkdownDescription: "`opsgenie` channels. The Alerts API key is the `api_key` secret.",
						Attributes: map[string]schema.Attribute{
							"region": schema.StringAttribute{
								Optional:            true,
								Computed:            true,
								MarkdownDescription: "Service region: `us` (default) or `eu`.",
								Validators:          []validator.String{stringvalidator.OneOf("us", "eu")},
							},
							"priority": schema.StringAttribute{
								Optional: true,
								MarkdownDescription: "`P1`–`P5`, overriding the mapping from the rule severity " +
									"(critical → P1, warning → P3, info → P5).",
								Validators: []validator.String{stringvalidator.OneOf("P1", "P2", "P3", "P4", "P5")},
							},
							"responders": schema.ListNestedAttribute{
								Optional:            true,
								MarkdownDescription: "Teams, users, escalations or schedules the alert is assigned to, at most 20.",
								Validators:          []validator.List{listvalidator.SizeAtMost(20)},
								NestedObject: schema.NestedAttributeObject{
									Attributes: map[string]schema.Attribute{
										"type": schema.StringAttribute{
											Required:            true,
											MarkdownDescription: "`team`, `user`, `escalation` or `schedule`.",
											Validators:          []validator.String{stringvalidator.OneOf("team", "user", "escalation", "schedule")},
										},
										"name": schema.StringAttribute{Optional: true, MarkdownDescription: "Name of the responder; set exactly one of name and id."},
										"id":   schema.StringAttribute{Optional: true, MarkdownDescription: "Identifier of the responder; set exactly one of name and id."},
									},
								},
							},
							"tags": schema.ListAttribute{
								Optional:    true,
								ElementType: types.StringType,
								MarkdownDescription: "Tags added to the alert, at most 18 (openlog adds `openlog` and " +
									"`severity:<severity>` itself).",
								Validators: []validator.List{listvalidator.SizeAtMost(18)},
							},
						},
					},
				},
			},
			"secret_hints": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Masked form of each stored secret, as openlog returns it (for example `••••••  (set)`).",
			},
			"generated_secrets": schema.MapAttribute{
				Computed:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				MarkdownDescription: "Secrets openlog generated when the channel was created — a webhook's `hmac_secret` " +
					"when none was given. Returned once, at creation; empty afterwards and after an import.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
			},
			"created_by_email": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "E-mail address of the creator.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation time (RFC3339).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Time of the last change (RFC3339)."},
		},
	}
}

func (r *notificationChannelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = resourceClient(req, resp)
}

// apiChannel maps the model to the API body.
func (m *channelModel) apiChannel(ctx context.Context, diags *diagnostics) client.Channel {
	in := client.Channel{
		Name:    m.Name.ValueString(),
		Type:    m.Type.ValueString(),
		Enabled: m.Enabled.ValueBool(),
		Secrets: stringsFromMap(ctx, m.Secrets, diags),
	}
	c := m.Config
	if c == nil {
		return in
	}
	in.Config.To = stringsFromList(ctx, c.To, diags)
	if s := c.SMTP; s != nil {
		in.Config.SMTP = &client.SMTPOverride{
			Host:     s.Host.ValueString(),
			Port:     int(s.Port.ValueInt64()),
			Username: s.Username.ValueString(),
			From:     s.From.ValueString(),
			TLS:      s.TLS.ValueString(),
		}
	}
	if p := c.PagerDuty; p != nil {
		in.Config.PagerDuty = &client.PagerDutyConfig{Region: p.Region.ValueString()}
	}
	if o := c.Opsgenie; o != nil {
		cfg := &client.OpsgenieConfig{
			Region:   o.Region.ValueString(),
			Priority: o.Priority.ValueString(),
			Tags:     stringsFromList(ctx, o.Tags, diags),
		}
		for _, rp := range o.Responders {
			cfg.Responders = append(cfg.Responders, client.OpsgenieResponder{
				Type: rp.Type.ValueString(), Name: rp.Name.ValueString(), ID: rp.ID.ValueString(),
			})
		}
		in.Config.Opsgenie = cfg
	}
	return in
}

// applyChannel writes the API's answer into the model. The configured secrets are left untouched: openlog
// never returns them, so state keeps what Terraform sent.
func (m *channelModel) applyChannel(ctx context.Context, x *client.Channel, diags *diagnostics) {
	m.ID = types.StringValue(x.ID)
	m.Name = types.StringValue(x.Name)
	m.Type = types.StringValue(x.Type)
	m.Enabled = types.BoolValue(x.Enabled)
	m.SecretHints = mapOfStrings(ctx, x.SecretHints, diags)
	m.CreatedByEmail = types.StringValue(x.CreatedByEmail)
	m.CreatedAt = types.StringValue(x.CreatedAt)
	m.UpdatedAt = types.StringValue(x.UpdatedAt)

	cfg := &channelConfigModel{To: types.ListNull(types.StringType)}
	if len(x.Config.To) > 0 {
		cfg.To = listOfStrings(ctx, x.Config.To, diags)
	}
	if s := x.Config.SMTP; s != nil {
		cfg.SMTP = &smtpModel{
			Host:     types.StringValue(s.Host),
			Port:     types.Int64Value(int64(s.Port)),
			Username: types.StringValue(s.Username),
			From:     types.StringValue(s.From),
			TLS:      types.StringValue(s.TLS),
		}
		if s.Username == "" {
			cfg.SMTP.Username = types.StringNull()
		}
	}
	if p := x.Config.PagerDuty; p != nil {
		cfg.PagerDuty = &pagerDutyModel{Region: types.StringValue(p.Region)}
	}
	if o := x.Config.Opsgenie; o != nil {
		og := &opsgenieConfigModel{
			Region:   types.StringValue(o.Region),
			Priority: types.StringNull(),
			Tags:     types.ListNull(types.StringType),
		}
		if o.Priority != "" {
			og.Priority = types.StringValue(o.Priority)
		}
		if len(o.Tags) > 0 {
			og.Tags = listOfStrings(ctx, o.Tags, diags)
		}
		for _, rp := range o.Responders {
			og.Responders = append(og.Responders, opsgenieResponderModel{
				Type: types.StringValue(rp.Type),
				Name: optionalString(rp.Name),
				ID:   optionalString(rp.ID),
			})
		}
		cfg.Opsgenie = og
	}
	m.Config = cfg
}

// optionalString keeps an empty API string null, so an attribute the configuration never set stays unset.
func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func (r *notificationChannelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan channelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.apiChannel(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateChannel(ctx, in)
	if err != nil {
		addAPIError(&resp.Diagnostics, "create the notification channel", err)
		return
	}
	plan.applyChannel(ctx, out, &resp.Diagnostics)
	// generated_secrets is returned once, by the create response only.
	plan.GeneratedSecrets = mapOfStrings(ctx, out.GeneratedSecrets, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *notificationChannelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state channelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GetChannel(ctx, state.ID.ValueString())
	if err != nil {
		if isGone(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "read the notification channel", err)
		return
	}
	state.applyChannel(ctx, out, &resp.Diagnostics)
	if state.GeneratedSecrets.IsNull() || state.GeneratedSecrets.IsUnknown() {
		// After an import there is nothing to carry over; the API never returns generated secrets on a read.
		state.GeneratedSecrets = mapOfStrings(ctx, nil, &resp.Diagnostics)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *notificationChannelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state channelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.apiChannel(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.UpdateChannel(ctx, state.ID.ValueString(), in)
	if err != nil {
		addAPIError(&resp.Diagnostics, "update the notification channel", err)
		return
	}
	plan.applyChannel(ctx, out, &resp.Diagnostics)
	plan.GeneratedSecrets = state.GeneratedSecrets
	if plan.GeneratedSecrets.IsNull() || plan.GeneratedSecrets.IsUnknown() {
		plan.GeneratedSecrets = mapOfStrings(ctx, nil, &resp.Diagnostics)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *notificationChannelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state channelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteChannel(ctx, state.ID.ValueString()); err != nil && !isGone(err) {
		addAPIError(&resp.Diagnostics, "delete the notification channel", err)
	}
}

// ImportState imports a channel by id. Its secrets cannot be imported — openlog never returns them — so the
// configuration must carry them and the first apply after an import re-sends them.
func (r *notificationChannelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importByID(ctx, req, resp)
}
