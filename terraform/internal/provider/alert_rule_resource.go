package provider

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onuragtas/openlog/terraform/internal/client"
)

// openlog_alert_rule — one alert rule (docs/contracts/alerting.md §2). The condition is a JSON document
// rather than ten typed blocks: each of the ten rule types has its own condition shape, the API validates it
// field by field and reports the offending path, and a JSON attribute keeps the resource honest when openlog
// adds a rule type. The parsed and defaulted document the API stores is always readable in
// condition_effective.

// RuleTypes are the rule types the API accepts (alerting.md §2.1).
var RuleTypes = []string{
	"metric_threshold", "log_match", "no_data", "discovery", "apm",
	"apm_no_data", "apm_error", "oql", "slo_burn", "anomaly",
}

// lowerUUID matches the canonical lower-case UUID the API stores. Channel ids normally come from an
// openlog_notification_channel resource, which is already canonical; requiring the canonical form here turns
// an upper-case literal into a clear validation error instead of a confusing "inconsistent result after
// apply" from Terraform when the API lower-cases it.
var lowerUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var flappingAttrTypes = map[string]attr.Type{
	"enabled":        types.BoolType,
	"transitions":    types.Int64Type,
	"window_seconds": types.Int64Type,
	"hold_seconds":   types.Int64Type,
}

var (
	_ resource.Resource                = (*alertRuleResource)(nil)
	_ resource.ResourceWithConfigure   = (*alertRuleResource)(nil)
	_ resource.ResourceWithImportState = (*alertRuleResource)(nil)
)

// NewAlertRuleResource returns the openlog_alert_rule resource.
func NewAlertRuleResource() resource.Resource { return &alertRuleResource{} }

type alertRuleResource struct{ client *client.Client }

type alertRuleModel struct {
	ID                      types.String         `tfsdk:"id"`
	Name                    types.String         `tfsdk:"name"`
	Description             types.String         `tfsdk:"description"`
	Type                    types.String         `tfsdk:"type"`
	Severity                types.String         `tfsdk:"severity"`
	Enabled                 types.Bool           `tfsdk:"enabled"`
	IntervalSeconds         types.Int64          `tfsdk:"interval_seconds"`
	ForSeconds              types.Int64          `tfsdk:"for_seconds"`
	RecoveryForSeconds      types.Int64          `tfsdk:"recovery_for_seconds"`
	Condition               jsontypes.Normalized `tfsdk:"condition"`
	ConditionEffective      jsontypes.Normalized `tfsdk:"condition_effective"`
	ChannelIDs              types.Set            `tfsdk:"channel_ids"`
	RenotifyIntervalSeconds types.Int64          `tfsdk:"renotify_interval_seconds"`
	Flapping                types.Object         `tfsdk:"flapping"`
	RunbookURL              types.String         `tfsdk:"runbook_url"`
	Labels                  types.Map            `tfsdk:"labels"`
	Version                 types.Int64          `tfsdk:"version"`
	CreatedByEmail          types.String         `tfsdk:"created_by_email"`
	CreatedAt               types.String         `tfsdk:"created_at"`
	UpdatedAt               types.String         `tfsdk:"updated_at"`
}

func (r *alertRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_rule"
}

func (r *alertRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An alert rule: what openlog evaluates, how often, and which notification channels its " +
			"incidents reach (docs/contracts/alerting.md §2).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier of the rule, assigned by openlog.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the rule, 1–200 characters.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 200)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(""),
				MarkdownDescription: "Free text, at most 2000 characters.",
				Validators:          []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Rule type, which decides the shape of `condition`: one of `" +
					"metric_threshold`, `log_match`, `no_data`, `discovery`, `apm`, `apm_no_data`, `apm_error`, `oql`, " +
					"`slo_burn`, `anomaly`. Changing the type resolves the rule's open incidents with reason `rule_changed`.",
				Validators: []validator.String{stringvalidator.OneOf(RuleTypes...)},
			},
			"severity": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("warning"),
				MarkdownDescription: "`critical`, `warning` (default) or `info`.",
				Validators:          []validator.String{stringvalidator.OneOf("critical", "warning", "info")},
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the rule is evaluated. Disabling it resolves its open incidents (`rule_disabled`).",
			},
			"interval_seconds": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Evaluation period in seconds, 10–3600. Left unset, openlog picks the default of the " +
					"rule type (60 seconds for most, 300 for `discovery`).",
				Validators:    []validator.Int64{int64validator.Between(10, 3600)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"for_seconds": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(0),
				MarkdownDescription: "How long the condition must hold before the rule fires (0–86400, `pending` until " +
					"then). The `discovery` and `apm_error` rule types ignore it and openlog stores 0.",
				Validators: []validator.Int64{int64validator.Between(0, 86400)},
			},
			"recovery_for_seconds": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(0),
				MarkdownDescription: "How long the recovery condition must hold before the incident resolves (0–86400).",
				Validators:          []validator.Int64{int64validator.Between(0, 86400)},
			},
			"condition": schema.StringAttribute{
				Required:   true,
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "The condition as a JSON document, whose fields depend on `type` " +
					"(docs/contracts/alerting.md §2.2–§2.12). Write it with `jsonencode({…})`. openlog fills in the " +
					"defaults of the fields it does not carry; the stored document is `condition_effective`, and this " +
					"attribute only changes when openlog disagrees about a field the configuration sets.",
			},
			"condition_effective": schema.StringAttribute{
				Computed:   true,
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "The complete condition openlog stores, including every default it filled in. " +
					"Read-only; useful to see what a short `condition` actually means.",
			},
			"channel_ids": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Notification channels of the incidents this rule opens, at most 20. A matching " +
					"`openlog_alert_routing_rule` overrides them when the incident opens.",
				Validators: []validator.Set{
					setvalidator.SizeAtMost(20),
					setvalidator.ValueStringsAre(stringvalidator.RegexMatches(lowerUUID,
						"must be a channel id in canonical lower-case UUID form, normally openlog_notification_channel.<name>.id")),
				},
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
			},
			"renotify_interval_seconds": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(0),
				MarkdownDescription: "Repeat the notification while the incident is open and unacknowledged: 0 (off, the " +
					"default) or 300–604800 seconds.",
				Validators: []validator.Int64{int64validator.Any(int64validator.OneOf(0), int64validator.Between(300, 604800))},
			},
			"flapping": schema.SingleNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Flapping protection (alerting.md §3.4). Left unset, openlog uses its defaults: " +
					"enabled, 4 transitions within 3600 seconds, held for 600 seconds.",
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Required:            true,
						MarkdownDescription: "Whether flapping protection is on.",
					},
					"transitions": schema.Int64Attribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Transitions into firing that make a series flapping, 2–20.",
						Validators:          []validator.Int64{int64validator.Between(2, 20)},
					},
					"window_seconds": schema.Int64Attribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Window the transitions are counted in, 300–86400 seconds.",
						Validators:          []validator.Int64{int64validator.Between(300, 86400)},
					},
					"hold_seconds": schema.Int64Attribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "How long a flapping series must stay recovered before its incident resolves, 60–86400 seconds.",
						Validators:          []validator.Int64{int64validator.Between(60, 86400)},
					},
				},
				Validators:    []validator.Object{flappingValidator{}},
				PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
			},
			"runbook_url": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(""),
				MarkdownDescription: "An `http(s)` URL shown on the incident and sent with the notification.",
				Validators:          []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"labels": schema.MapAttribute{
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Labels added to every incident of this rule, at most 20. Routing rules can match on them.",
				PlanModifiers:       []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
			},
			"version": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "Version of the rule, raised by openlog on every change. The provider sends it back " +
					"on update, so a change made elsewhere in the meantime is refused instead of overwritten.",
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

func (r *alertRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = resourceClient(req, resp)
}

// apiRule maps the model to the API body.
func (m *alertRuleModel) apiRule(ctx context.Context, diags *diagnostics) client.AlertRule {
	in := client.AlertRule{
		Name:                    m.Name.ValueString(),
		Description:             m.Description.ValueString(),
		Type:                    m.Type.ValueString(),
		Severity:                m.Severity.ValueString(),
		Enabled:                 m.Enabled.ValueBool(),
		IntervalSeconds:         int(m.IntervalSeconds.ValueInt64()),
		ForSeconds:              int(m.ForSeconds.ValueInt64()),
		RecoveryForSeconds:      int(m.RecoveryForSeconds.ValueInt64()),
		Condition:               json.RawMessage(m.Condition.ValueString()),
		ChannelIDs:              stringsFromSet(ctx, m.ChannelIDs, diags),
		RenotifyIntervalSeconds: int(m.RenotifyIntervalSeconds.ValueInt64()),
		RunbookURL:              m.RunbookURL.ValueString(),
		Labels:                  stringsFromMap(ctx, m.Labels, diags),
	}
	if in.ChannelIDs == nil {
		in.ChannelIDs = []string{}
	}
	if in.Labels == nil {
		in.Labels = map[string]string{}
	}
	in.Flapping = flappingFromObject(m.Flapping)
	return in
}

// flappingFromObject reads the flapping block; an unset or partly unknown block is omitted so the API applies
// its own defaults.
func flappingFromObject(o types.Object) *client.Flapping {
	if o.IsNull() || o.IsUnknown() {
		return nil
	}
	attrs := o.Attributes()
	enabled, _ := attrs["enabled"].(types.Bool)
	if enabled.IsNull() || enabled.IsUnknown() {
		return nil
	}
	f := &client.Flapping{Enabled: enabled.ValueBool()}
	// The API validates the three numbers only while flapping is enabled and stores its defaults otherwise.
	for name, dst := range map[string]*int{
		"transitions":    &f.Transitions,
		"window_seconds": &f.WindowSeconds,
		"hold_seconds":   &f.HoldSeconds,
	} {
		if v, ok := attrs[name].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
			*dst = int(v.ValueInt64())
		}
	}
	if f.Enabled && (f.Transitions == 0 || f.WindowSeconds == 0 || f.HoldSeconds == 0) {
		// A block that enables flapping without all three numbers would fail the API's range checks; let the
		// API apply its whole default set instead.
		return &client.Flapping{Enabled: true, Transitions: 4, WindowSeconds: 3600, HoldSeconds: 600}
	}
	return f
}

// flappingObject renders the flapping the API stored.
func flappingObject(f *client.Flapping, diags *diagnostics) types.Object {
	if f == nil {
		return types.ObjectNull(flappingAttrTypes)
	}
	o, d := types.ObjectValue(flappingAttrTypes, map[string]attr.Value{
		"enabled":        types.BoolValue(f.Enabled),
		"transitions":    types.Int64Value(int64(f.Transitions)),
		"window_seconds": types.Int64Value(int64(f.WindowSeconds)),
		"hold_seconds":   types.Int64Value(int64(f.HoldSeconds)),
	})
	diags.Append(d...)
	return o
}

// applyRule writes the API's answer into the model. The configured condition is kept as long as openlog
// agrees about every field it sets (see conditionDrifted), so server-side defaulting does not show up as a
// permanent diff while real drift still does.
func (m *alertRuleModel) applyRule(ctx context.Context, x *client.AlertRule, diags *diagnostics) {
	m.ID = types.StringValue(x.ID)
	m.Name = types.StringValue(x.Name)
	m.Description = types.StringValue(x.Description)
	m.Type = types.StringValue(x.Type)
	m.Severity = types.StringValue(x.Severity)
	m.Enabled = types.BoolValue(x.Enabled)
	m.IntervalSeconds = types.Int64Value(int64(x.IntervalSeconds))
	m.ForSeconds = types.Int64Value(int64(x.ForSeconds))
	m.RecoveryForSeconds = types.Int64Value(int64(x.RecoveryForSeconds))
	m.RenotifyIntervalSeconds = types.Int64Value(int64(x.RenotifyIntervalSeconds))
	m.RunbookURL = types.StringValue(x.RunbookURL)
	m.ChannelIDs = setOfStrings(ctx, x.ChannelIDs, diags)
	m.Labels = mapOfStrings(ctx, x.Labels, diags)
	m.Flapping = flappingObject(x.Flapping, diags)
	m.Version = types.Int64Value(int64(x.Version))
	m.CreatedByEmail = types.StringValue(x.CreatedByEmail)
	m.CreatedAt = types.StringValue(x.CreatedAt)
	m.UpdatedAt = types.StringValue(x.UpdatedAt)

	server := []byte(x.Condition)
	if len(server) == 0 {
		server = []byte("{}")
	}
	m.ConditionEffective = jsontypes.NewNormalizedValue(string(server))
	if m.Condition.IsNull() || m.Condition.IsUnknown() || conditionDrifted([]byte(m.Condition.ValueString()), server) {
		m.Condition = jsontypes.NewNormalizedValue(string(server))
	}
}

func (r *alertRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan alertRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.apiRule(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateAlertRule(ctx, in)
	if err != nil {
		addAPIError(&resp.Diagnostics, "create the alert rule", err)
		return
	}
	plan.applyRule(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state alertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GetAlertRule(ctx, state.ID.ValueString())
	if err != nil {
		if isGone(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "read the alert rule", err)
		return
	}
	state.applyRule(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *alertRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state alertRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.apiRule(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// The version last read travels with the write: openlog refuses a stale one with 409 rather than
	// overwriting whoever changed the rule in between (alerting.md §4).
	out, err := r.client.UpdateAlertRule(ctx, state.ID.ValueString(), in, int(state.Version.ValueInt64()))
	if err != nil {
		addAPIError(&resp.Diagnostics, "update the alert rule", err)
		return
	}
	plan.applyRule(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state alertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAlertRule(ctx, state.ID.ValueString()); err != nil && !isGone(err) {
		addAPIError(&resp.Diagnostics, "delete the alert rule", err)
	}
}

func (r *alertRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importByID(ctx, req, resp)
}

// flappingValidator rejects the one combination the API silently rewrites: numbers configured next to
// enabled = false, which openlog replaces with its defaults. Without this check Terraform would report the
// rewrite as an inconsistent result after apply, which says nothing about the cause.
type flappingValidator struct{}

func (flappingValidator) Description(context.Context) string {
	return "transitions, window_seconds and hold_seconds may only be set while flapping is enabled"
}

func (v flappingValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (flappingValidator) ValidateObject(_ context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	attrs := req.ConfigValue.Attributes()
	enabled, ok := attrs["enabled"].(types.Bool)
	if !ok || enabled.IsNull() || enabled.IsUnknown() || enabled.ValueBool() {
		return
	}
	for _, name := range []string{"transitions", "window_seconds", "hold_seconds"} {
		v, ok := attrs[name].(types.Int64)
		if ok && !v.IsNull() && !v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(req.Path.AtName(name), "Unused flapping setting",
				"With flapping.enabled = false openlog stores its own defaults for transitions, window_seconds and "+
					"hold_seconds and ignores what is configured here. Remove "+name+" or enable flapping.")
		}
	}
}
