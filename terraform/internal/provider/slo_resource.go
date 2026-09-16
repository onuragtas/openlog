package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onuragtas/openlog/terraform/internal/client"
)

// openlog_slo — a service level objective of one APM service (docs/contracts/slo.md). An SLO is a definition
// only: the error budget and the burn rates are computed at query time, and the alerting on it is a separate
// openlog_alert_rule of type slo_burn that names this SLO's id.

var (
	_ resource.Resource                = (*sloResource)(nil)
	_ resource.ResourceWithConfigure   = (*sloResource)(nil)
	_ resource.ResourceWithImportState = (*sloResource)(nil)
)

// NewSLOResource returns the openlog_slo resource.
func NewSLOResource() resource.Resource { return &sloResource{} }

type sloResource struct{ client *client.Client }

type sloModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	ServiceName types.String `tfsdk:"service_name"`
	// ServiceNamespace and Environment keep the API's three-way meaning: null = every namespace/environment
	// (aggregated), "" = the empty one exactly, any other string = that one exactly (apm.md §1).
	ServiceNamespace   types.String  `tfsdk:"service_namespace"`
	Environment        types.String  `tfsdk:"environment"`
	SLIType            types.String  `tfsdk:"sli_type"`
	LatencyThresholdMs types.Int64   `tfsdk:"latency_threshold_ms"`
	Objective          types.Float64 `tfsdk:"objective"`
	WindowDays         types.Int64   `tfsdk:"window_days"`
	CreatedByEmail     types.String  `tfsdk:"created_by_email"`
	UpdatedByEmail     types.String  `tfsdk:"updated_by_email"`
	CreatedAt          types.String  `tfsdk:"created_at"`
	UpdatedAt          types.String  `tfsdk:"updated_at"`
}

func (r *sloResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_slo"
}

func (r *sloResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A service level objective: an SLI, a target and a rolling window over one APM service " +
			"(docs/contracts/slo.md). At most 200 per organization.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier of the SLO, assigned by openlog.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the SLO, 1–200 characters.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 200)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(""),
				MarkdownDescription: "Free text, at most 2000 characters.",
				Validators:          []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"service_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Exact `service.name` of the APM service this objective is about.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 512)},
			},
			"service_namespace": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Exact `service.namespace`. Leave unset to aggregate every namespace; set it to `\"\"` " +
					"to select the services that have no namespace.",
				Validators: []validator.String{stringvalidator.LengthAtMost(512)},
			},
			"environment": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Exact environment. Leave unset to aggregate every environment; set it to `\"\"` to " +
					"select the services that report none.",
				Validators: []validator.String{stringvalidator.LengthAtMost(512)},
			},
			"sli_type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "`availability` (non-error requests are good) or `latency` (requests faster than " +
					"`latency_threshold_ms` are good, errors included).",
				Validators: []validator.String{stringvalidator.OneOf("availability", "latency")},
			},
			"latency_threshold_ms": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Latency budget in milliseconds, 1–600000. Required for `sli_type = \"latency\"` and " +
					"rejected for `availability`.",
				Validators: []validator.Int64{int64validator.Between(1, 600000)},
			},
			"objective": schema.Float64Attribute{
				Required:            true,
				MarkdownDescription: "Target in percent, at least 50 and below 100 (for example `99.9`).",
				Validators:          []validator.Float64{float64validator.AtLeast(50), belowHundred{}},
			},
			"window_days": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Rolling window in days: `7`, `28` or `30`.",
				Validators:          []validator.Int64{int64validator.OneOf(7, 28, 30)},
			},
			"created_by_email": schema.StringAttribute{Computed: true, MarkdownDescription: "E-mail address of the creator."},
			"updated_by_email": schema.StringAttribute{Computed: true, MarkdownDescription: "E-mail address of the last editor."},
			"created_at":       schema.StringAttribute{Computed: true, MarkdownDescription: "Creation time (RFC3339)."},
			"updated_at":       schema.StringAttribute{Computed: true, MarkdownDescription: "Time of the last change (RFC3339)."},
		},
	}
}

func (r *sloResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = resourceClient(req, resp)
}

// apiSLO maps the model to the API body.
func (m *sloModel) apiSLO() client.SLO {
	in := client.SLO{
		Name:             m.Name.ValueString(),
		Description:      m.Description.ValueString(),
		ServiceName:      m.ServiceName.ValueString(),
		ServiceNamespace: stringPtr(m.ServiceNamespace),
		Environment:      stringPtr(m.Environment),
		SLIType:          m.SLIType.ValueString(),
		Objective:        m.Objective.ValueFloat64(),
		WindowDays:       int(m.WindowDays.ValueInt64()),
	}
	if !m.LatencyThresholdMs.IsNull() && !m.LatencyThresholdMs.IsUnknown() {
		ms := float64(m.LatencyThresholdMs.ValueInt64())
		in.LatencyThresholdMs = &ms
	}
	return in
}

// applySLO writes the API's answer into the model, which becomes the new state.
func (m *sloModel) applySLO(x *client.SLO) {
	m.ID = types.StringValue(x.ID)
	m.Name = types.StringValue(x.Name)
	m.Description = types.StringValue(x.Description)
	m.ServiceName = types.StringValue(x.ServiceName)
	m.ServiceNamespace = ptrString(x.ServiceNamespace)
	m.Environment = ptrString(x.Environment)
	m.SLIType = types.StringValue(x.SLIType)
	m.LatencyThresholdMs = types.Int64Null()
	if x.LatencyThresholdMs != nil {
		m.LatencyThresholdMs = types.Int64Value(int64(*x.LatencyThresholdMs))
	}
	m.Objective = types.Float64Value(x.Objective)
	m.WindowDays = types.Int64Value(int64(x.WindowDays))
	m.CreatedByEmail = types.StringValue(x.CreatedByEmail)
	m.UpdatedByEmail = types.StringValue(x.UpdatedByEmail)
	m.CreatedAt = types.StringValue(x.CreatedAt)
	m.UpdatedAt = types.StringValue(x.UpdatedAt)
}

func (r *sloResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sloModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateSLO(ctx, plan.apiSLO())
	if err != nil {
		addAPIError(&resp.Diagnostics, "create the SLO", err)
		return
	}
	plan.applySLO(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sloResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sloModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GetSLO(ctx, state.ID.ValueString())
	if err != nil {
		if isGone(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "read the SLO", err)
		return
	}
	state.applySLO(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sloResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state sloModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.UpdateSLO(ctx, state.ID.ValueString(), plan.apiSLO())
	if err != nil {
		addAPIError(&resp.Diagnostics, "update the SLO", err)
		return
	}
	plan.applySLO(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sloResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sloModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// An SLO that is already gone is the state Terraform wants; only a real failure is reported.
	if err := r.client.DeleteSLO(ctx, state.ID.ValueString()); err != nil && !isGone(err) {
		addAPIError(&resp.Diagnostics, "delete the SLO", err)
	}
}

func (r *sloResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importByID(ctx, req, resp)
}

// belowHundred enforces the open upper bound of an objective. An objective of exactly 100 % allows no failed
// request at all, so openlog rejects it; the framework's validators only offer a closed AtMost.
type belowHundred struct{}

func (belowHundred) Description(context.Context) string { return "must be below 100" }

func (v belowHundred) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (belowHundred) ValidateFloat64(_ context.Context, req validator.Float64Request, resp *validator.Float64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if req.ConfigValue.ValueFloat64() >= 100 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid objective",
			"An objective is a percentage below 100: 100 would allow no failed request at all and leave no error "+
				"budget. Use 99.9 or 99.99 instead.")
	}
}
