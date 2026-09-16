package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onuragtas/openlog/terraform/internal/client"
)

// openlog_alert_routing_rule — which channels an incident reaches when it opens (alerting.md §5.6). The rules
// of an organization form one ordered list: the first enabled route whose match applies wins, then the route
// marked default. Order comes from `position`, so several independently declared routes still have a defined
// order without a separate reorder resource.

var (
	_ resource.Resource                = (*alertRoutingRuleResource)(nil)
	_ resource.ResourceWithConfigure   = (*alertRoutingRuleResource)(nil)
	_ resource.ResourceWithImportState = (*alertRoutingRuleResource)(nil)
)

// NewAlertRoutingRuleResource returns the openlog_alert_routing_rule resource.
func NewAlertRoutingRuleResource() resource.Resource { return &alertRoutingRuleResource{} }

type alertRoutingRuleResource struct{ client *client.Client }

type routingRuleModel struct {
	ID             types.String     `tfsdk:"id"`
	Name           types.String     `tfsdk:"name"`
	Position       types.Int64      `tfsdk:"position"`
	Enabled        types.Bool       `tfsdk:"enabled"`
	IsDefault      types.Bool       `tfsdk:"is_default"`
	ChannelIDs     types.Set        `tfsdk:"channel_ids"`
	Match          *routeMatchModel `tfsdk:"match"`
	CreatedByEmail types.String     `tfsdk:"created_by_email"`
	CreatedAt      types.String     `tfsdk:"created_at"`
	UpdatedAt      types.String     `tfsdk:"updated_at"`
}

type routeMatchModel struct {
	Severities types.Set           `tfsdk:"severities"`
	Services   types.Set           `tfsdk:"services"`
	RuleTypes  types.Set           `tfsdk:"rule_types"`
	Labels     []routeMatcherModel `tfsdk:"labels"`
	TimeWindow *routeWindowModel   `tfsdk:"time_window"`
}

type routeMatcherModel struct {
	Label types.String `tfsdk:"label"`
	Op    types.String `tfsdk:"op"`
	Value types.String `tfsdk:"value"`
}

type routeWindowModel struct {
	Timezone  types.String `tfsdk:"timezone"`
	Days      types.Set    `tfsdk:"days"`
	StartTime types.String `tfsdk:"start_time"`
	EndTime   types.String `tfsdk:"end_time"`
}

func (r *alertRoutingRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_routing_rule"
}

func (r *alertRoutingRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A notification routing rule: the first enabled route whose match applies decides which " +
			"channels an incident reaches when it opens (docs/contracts/alerting.md §5.6). At most 100 per organization.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier of the routing rule, assigned by openlog.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the routing rule, 1–200 characters.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 200)},
			},
			"position": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(0),
				MarkdownDescription: "Evaluation order, 0 first (0–100). Routes with the same position keep the order " +
					"openlog stored them in, so give each route its own position when the order matters.",
				Validators: []validator.Int64{int64validator.Between(0, 100)},
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the route is evaluated; a disabled route is skipped.",
			},
			"is_default": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "The default route: it matches every incident and is evaluated after all others. " +
					"At most one per organization, and its `match` must be empty.",
			},
			"channel_ids": schema.SetAttribute{
				Required:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Channels this route delivers to, 1–20. Disabled channels are dropped at delivery time.",
				Validators: []validator.Set{
					setvalidator.SizeBetween(1, 20),
					setvalidator.ValueStringsAre(stringvalidator.RegexMatches(lowerUUID,
						"must be a channel id in canonical lower-case UUID form, normally openlog_notification_channel.<name>.id")),
				},
			},
			"match": schema.SingleNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Condition of the route. Its parts are AND-ed and an empty part matches everything, " +
					"so a route without a match reaches every incident.",
				Attributes: map[string]schema.Attribute{
					"severities": schema.SetAttribute{
						Optional:            true,
						Computed:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "`critical`, `warning`, `info`; empty matches any severity.",
						Validators:          []validator.Set{setvalidator.ValueStringsAre(stringvalidator.OneOf("critical", "warning", "info"))},
					},
					"services": schema.SetAttribute{
						Optional:            true,
						Computed:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "Exact `service.name` values of the incident labels; empty matches any service.",
					},
					"rule_types": schema.SetAttribute{
						Optional:            true,
						Computed:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "Alert rule types; empty matches any type.",
						Validators:          []validator.Set{setvalidator.ValueStringsAre(stringvalidator.OneOf(RuleTypes...))},
					},
					"labels": schema.ListNestedAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Matchers on the incident labels, at most 20, AND-ed.",
						Validators:          []validator.List{listvalidator.SizeAtMost(20)},
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"label": schema.StringAttribute{Required: true, MarkdownDescription: "Label name, for example `env`."},
								"op": schema.StringAttribute{
									Required:            true,
									MarkdownDescription: "`eq`, `neq` or `contains` (case-insensitive substring).",
									Validators:          []validator.String{stringvalidator.OneOf("eq", "neq", "contains")},
								},
								"value": schema.StringAttribute{Required: true, MarkdownDescription: "Value to compare against."},
							},
						},
					},
					"time_window": schema.SingleNestedAttribute{
						Optional:            true,
						MarkdownDescription: "Local window the route applies in, for example office hours.",
						Attributes: map[string]schema.Attribute{
							"timezone": schema.StringAttribute{
								Optional:            true,
								Computed:            true,
								MarkdownDescription: "IANA time zone name; `UTC` by default.",
							},
							"days": schema.SetAttribute{
								Optional:            true,
								Computed:            true,
								ElementType:         types.StringType,
								MarkdownDescription: "`mon` … `sun`; empty means every day.",
								Validators: []validator.Set{setvalidator.ValueStringsAre(
									stringvalidator.OneOf("mon", "tue", "wed", "thu", "fri", "sat", "sun"))},
							},
							"start_time": schema.StringAttribute{Required: true, MarkdownDescription: "Local start as `HH:MM`."},
							"end_time": schema.StringAttribute{
								Required:            true,
								MarkdownDescription: "Local end as `HH:MM`; at or before `start_time` means the next day.",
							},
						},
					},
				},
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

func (r *alertRoutingRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = resourceClient(req, resp)
}

func (m *routingRuleModel) apiRoutingRule(ctx context.Context, diags *diagnostics) client.RoutingRule {
	in := client.RoutingRule{
		Name:       m.Name.ValueString(),
		Position:   int(m.Position.ValueInt64()),
		Enabled:    m.Enabled.ValueBool(),
		IsDefault:  m.IsDefault.ValueBool(),
		ChannelIDs: stringsFromSet(ctx, m.ChannelIDs, diags),
	}
	mt := m.Match
	if mt == nil {
		return in
	}
	in.Match.Severities = stringsFromSet(ctx, mt.Severities, diags)
	in.Match.Services = stringsFromSet(ctx, mt.Services, diags)
	in.Match.RuleTypes = stringsFromSet(ctx, mt.RuleTypes, diags)
	for _, l := range mt.Labels {
		in.Match.Labels = append(in.Match.Labels, client.RouteMatcher{
			Label: l.Label.ValueString(), Op: l.Op.ValueString(), Value: l.Value.ValueString(),
		})
	}
	if w := mt.TimeWindow; w != nil {
		in.Match.TimeWindow = &client.RouteWindow{
			Timezone:  w.Timezone.ValueString(),
			Days:      stringsFromSet(ctx, w.Days, diags),
			StartTime: w.StartTime.ValueString(),
			EndTime:   w.EndTime.ValueString(),
		}
	}
	return in
}

func (m *routingRuleModel) applyRoutingRule(ctx context.Context, x *client.RoutingRule, diags *diagnostics) {
	m.ID = types.StringValue(x.ID)
	m.Name = types.StringValue(x.Name)
	m.Position = types.Int64Value(int64(x.Position))
	m.Enabled = types.BoolValue(x.Enabled)
	m.IsDefault = types.BoolValue(x.IsDefault)
	m.ChannelIDs = setOfStrings(ctx, x.ChannelIDs, diags)
	m.CreatedByEmail = types.StringValue(x.CreatedByEmail)
	m.CreatedAt = types.StringValue(x.CreatedAt)
	m.UpdatedAt = types.StringValue(x.UpdatedAt)

	// The API answers with empty lists rather than nulls, so the match object always exists in state.
	mt := &routeMatchModel{
		Severities: setOfStrings(ctx, x.Match.Severities, diags),
		Services:   setOfStrings(ctx, x.Match.Services, diags),
		RuleTypes:  setOfStrings(ctx, x.Match.RuleTypes, diags),
		Labels:     []routeMatcherModel{},
	}
	for _, l := range x.Match.Labels {
		mt.Labels = append(mt.Labels, routeMatcherModel{
			Label: types.StringValue(l.Label), Op: types.StringValue(l.Op), Value: types.StringValue(l.Value),
		})
	}
	if w := x.Match.TimeWindow; w != nil {
		mt.TimeWindow = &routeWindowModel{
			Timezone:  types.StringValue(w.Timezone),
			Days:      setOfStrings(ctx, w.Days, diags),
			StartTime: types.StringValue(w.StartTime),
			EndTime:   types.StringValue(w.EndTime),
		}
	}
	m.Match = mt
}

func (r *alertRoutingRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan routingRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.apiRoutingRule(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateRoutingRule(ctx, in)
	if err != nil {
		addAPIError(&resp.Diagnostics, "create the routing rule", err)
		return
	}
	plan.applyRoutingRule(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertRoutingRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state routingRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GetRoutingRule(ctx, state.ID.ValueString())
	if err != nil {
		if isGone(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "read the routing rule", err)
		return
	}
	state.applyRoutingRule(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *alertRoutingRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state routingRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.apiRoutingRule(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.UpdateRoutingRule(ctx, state.ID.ValueString(), in)
	if err != nil {
		addAPIError(&resp.Diagnostics, "update the routing rule", err)
		return
	}
	plan.applyRoutingRule(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertRoutingRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state routingRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteRoutingRule(ctx, state.ID.ValueString()); err != nil && !isGone(err) {
		addAPIError(&resp.Diagnostics, "delete the routing rule", err)
	}
}

func (r *alertRoutingRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importByID(ctx, req, resp)
}
