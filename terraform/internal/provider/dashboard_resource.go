package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
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

// openlog_dashboard — a dashboard of OQL widgets (docs/contracts/api.md "Dashboards").
//
// A dashboard is one document: the API replaces it whole and expects the version that was read, so the
// provider sends the version from state and a change made in the web UI in the meantime is refused with 409
// instead of being overwritten. Page and widget ids are assigned by openlog and kept when a write carries
// them, so the provider sends back the ids it last read: editing a widget keeps its identity (and with it the
// dashboard's version history), while inserting a page in the middle shifts the ids of the pages after it.

// Visualizations are the widget visualizations the API accepts.
var Visualizations = []string{"line", "area", "bar", "table", "billboard", "pie", "heatmap", "markdown"}

var (
	_ resource.Resource                = (*dashboardResource)(nil)
	_ resource.ResourceWithConfigure   = (*dashboardResource)(nil)
	_ resource.ResourceWithImportState = (*dashboardResource)(nil)
)

// NewDashboardResource returns the openlog_dashboard resource.
func NewDashboardResource() resource.Resource { return &dashboardResource{} }

type dashboardResource struct{ client *client.Client }

type dashboardModel struct {
	ID             types.String             `tfsdk:"id"`
	Name           types.String             `tfsdk:"name"`
	Description    types.String             `tfsdk:"description"`
	Visibility     types.String             `tfsdk:"visibility"`
	Variables      []dashboardVariableModel `tfsdk:"variables"`
	Pages          []dashboardPageModel     `tfsdk:"pages"`
	Version        types.Int64              `tfsdk:"version"`
	CreatedByEmail types.String             `tfsdk:"created_by_email"`
	CreatedAt      types.String             `tfsdk:"created_at"`
	UpdatedAt      types.String             `tfsdk:"updated_at"`
}

type dashboardVariableModel struct {
	Name       types.String `tfsdk:"name"`
	Label      types.String `tfsdk:"label"`
	Type       types.String `tfsdk:"type"`
	Query      types.String `tfsdk:"query"`
	Values     types.List   `tfsdk:"values"`
	Default    types.List   `tfsdk:"default"`
	Multi      types.Bool   `tfsdk:"multi"`
	IncludeAll types.Bool   `tfsdk:"include_all"`
}

type dashboardPageModel struct {
	ID      types.String           `tfsdk:"id"`
	Name    types.String           `tfsdk:"name"`
	Widgets []dashboardWidgetModel `tfsdk:"widgets"`
}

type dashboardWidgetModel struct {
	ID            types.String           `tfsdk:"id"`
	Title         types.String           `tfsdk:"title"`
	Visualization types.String           `tfsdk:"visualization"`
	Layout        *widgetLayoutModel     `tfsdk:"layout"`
	Query         types.String           `tfsdk:"query"`
	Markdown      types.String           `tfsdk:"markdown"`
	Unit          types.String           `tfsdk:"unit"`
	Thresholds    []widgetThresholdModel `tfsdk:"thresholds"`
	Options       *widgetOptionsModel    `tfsdk:"options"`
}

type widgetLayoutModel struct {
	X types.Int64 `tfsdk:"x"`
	Y types.Int64 `tfsdk:"y"`
	W types.Int64 `tfsdk:"w"`
	H types.Int64 `tfsdk:"h"`
}

type widgetThresholdModel struct {
	Value    types.Float64 `tfsdk:"value"`
	Severity types.String  `tfsdk:"severity"`
}

type widgetOptionsModel struct {
	Stacked types.Bool `tfsdk:"stacked"`
	Legend  types.Bool `tfsdk:"legend"`
}

func (r *dashboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (r *dashboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A dashboard of OQL widgets (docs/contracts/api.md \"Dashboards\"). The whole document is " +
			"managed here: 1–20 pages of at most 100 widgets each on a 12-column grid.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier of the dashboard, assigned by openlog.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the dashboard, 1–200 characters.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 200)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(""),
				MarkdownDescription: "Free text, at most 2000 characters.",
				Validators:          []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"visibility": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "`org` (everyone in the organization can read it) or `private` (only its creator). " +
					"A dashboard created by Terraform belongs to the account whose API key the provider uses.",
				Validators: []validator.String{stringvalidator.OneOf("org", "private")},
			},
			"variables": schema.ListNestedAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Template variables usable as `{{name}}` in widget queries, at most 10.",
				Validators:          []validator.List{listvalidator.SizeAtMost(10)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Variable name, matching `[A-Za-z_][A-Za-z0-9_]{0,63}`.",
						},
						"label": schema.StringAttribute{
							Optional: true, Computed: true, Default: stringdefault.StaticString(""),
							MarkdownDescription: "Label shown in the web UI; the name is used when empty.",
						},
						"type": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "`query` (values from an OQL query with `FACET`), `list` or `text`.",
							Validators:          []validator.String{stringvalidator.OneOf("query", "list", "text")},
						},
						"query": schema.StringAttribute{
							Optional: true, Computed: true, Default: stringdefault.StaticString(""),
							MarkdownDescription: "OQL query with a `FACET` whose first facet gives the values (`query` variables).",
						},
						"values": schema.ListAttribute{
							Optional: true, Computed: true, ElementType: types.StringType,
							MarkdownDescription: "Fixed values, at most 200 (`list` variables).",
						},
						"default": schema.ListAttribute{
							Optional: true, Computed: true, ElementType: types.StringType,
							MarkdownDescription: "Values selected when the dashboard opens.",
						},
						"multi": schema.BoolAttribute{
							Optional: true, Computed: true,
							MarkdownDescription: "Whether several values can be selected at once.",
						},
						"include_all": schema.BoolAttribute{
							Optional: true, Computed: true,
							MarkdownDescription: "Whether the variable offers an \"All\" entry.",
						},
					},
				},
			},
			"pages": schema.ListNestedAttribute{
				Required:            true,
				MarkdownDescription: "Pages of the dashboard, 1–20, in the order they appear.",
				Validators:          []validator.List{listvalidator.SizeBetween(1, 20)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed: true,
							MarkdownDescription: "Identifier of the page, assigned by openlog and kept across updates as " +
								"long as the page keeps its position in the list.",
							PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
						},
						"name": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Name of the page, 1–100 characters.",
							Validators:          []validator.String{stringvalidator.LengthBetween(1, 100)},
						},
						"widgets": schema.ListNestedAttribute{
							Required:            true,
							MarkdownDescription: "Widgets of the page, at most 100.",
							Validators:          []validator.List{listvalidator.SizeAtMost(100)},
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"id": schema.StringAttribute{
										Computed:            true,
										MarkdownDescription: "Identifier of the widget, assigned by openlog.",
										PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
									},
									"title": schema.StringAttribute{
										Optional: true, Computed: true, Default: stringdefault.StaticString(""),
										MarkdownDescription: "Title shown above the widget.",
									},
									"visualization": schema.StringAttribute{
										Required: true,
										MarkdownDescription: "`line`, `area`, `bar`, `table`, `billboard`, `pie`, `heatmap` " +
											"or `markdown`.",
										Validators: []validator.String{stringvalidator.OneOf(Visualizations...)},
									},
									"layout": schema.SingleNestedAttribute{
										Required:            true,
										MarkdownDescription: "Position on the 12-column grid.",
										Attributes: map[string]schema.Attribute{
											"x": schema.Int64Attribute{Required: true, MarkdownDescription: "Column, `0 ≤ x` and `x + w ≤ 12`."},
											"y": schema.Int64Attribute{Required: true, MarkdownDescription: "Row, 0–10000."},
											"w": schema.Int64Attribute{Required: true, MarkdownDescription: "Width in columns, 1–12."},
											"h": schema.Int64Attribute{Required: true, MarkdownDescription: "Height in rows, 1–50."},
										},
									},
									"query": schema.StringAttribute{
										Optional: true, Computed: true, Default: stringdefault.StaticString(""),
										MarkdownDescription: "OQL query of the widget; required for every visualization but `markdown`.",
									},
									"markdown": schema.StringAttribute{
										Optional: true, Computed: true, Default: stringdefault.StaticString(""),
										MarkdownDescription: "Text of a `markdown` widget, at most 20000 characters.",
									},
									"unit": schema.StringAttribute{
										Optional: true, Computed: true, Default: stringdefault.StaticString(""),
										MarkdownDescription: "`number`, `percent`, `bytes`, `bytesPerSec`, `ms`, `s`, or empty.",
										Validators: []validator.String{
											stringvalidator.OneOf("", "number", "percent", "bytes", "bytesPerSec", "ms", "s"),
										},
									},
									"thresholds": schema.ListNestedAttribute{
										Optional: true, Computed: true,
										MarkdownDescription: "Value thresholds that colour the widget, at most 10.",
										Validators:          []validator.List{listvalidator.SizeAtMost(10)},
										NestedObject: schema.NestedAttributeObject{
											Attributes: map[string]schema.Attribute{
												"value": schema.Float64Attribute{Required: true, MarkdownDescription: "Value the threshold starts at."},
												"severity": schema.StringAttribute{
													Required:            true,
													MarkdownDescription: "`warning` or `critical`.",
													Validators:          []validator.String{stringvalidator.OneOf("warning", "critical")},
												},
											},
										},
									},
									"options": schema.SingleNestedAttribute{
										Optional: true, Computed: true,
										MarkdownDescription: "Visualization options.",
										Attributes: map[string]schema.Attribute{
											"stacked": schema.BoolAttribute{
												Optional: true, Computed: true,
												MarkdownDescription: "Stack the series of a line, area or bar widget.",
											},
											"legend": schema.BoolAttribute{
												Optional:            true,
												MarkdownDescription: "Show the legend; unset leaves the web UI's own default.",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			"version": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "Version of the document, raised on every save. The provider sends it back on " +
					"update, so a dashboard changed in the web UI meanwhile is not overwritten.",
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

func (r *dashboardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = resourceClient(req, resp)
}

// apiDashboard maps the model to the API body, carrying the page and widget ids that are already known so
// openlog keeps them.
func (m *dashboardModel) apiDashboard(ctx context.Context, diags *diagnostics) client.Dashboard {
	in := client.Dashboard{
		Name:        m.Name.ValueString(),
		Description: m.Description.ValueString(),
		Visibility:  m.Visibility.ValueString(),
		Variables:   []client.Variable{},
		Pages:       []client.Page{},
	}
	for _, v := range m.Variables {
		in.Variables = append(in.Variables, client.Variable{
			Name:       v.Name.ValueString(),
			Label:      v.Label.ValueString(),
			Type:       v.Type.ValueString(),
			Query:      v.Query.ValueString(),
			Values:     stringsFromList(ctx, v.Values, diags),
			Default:    stringsFromList(ctx, v.Default, diags),
			Multi:      v.Multi.ValueBool(),
			IncludeAll: v.IncludeAll.ValueBool(),
		})
	}
	for _, p := range m.Pages {
		page := client.Page{ID: knownString(p.ID), Name: p.Name.ValueString(), Widgets: []client.Widget{}}
		for _, w := range p.Widgets {
			widget := client.Widget{
				ID:            knownString(w.ID),
				Title:         w.Title.ValueString(),
				Visualization: w.Visualization.ValueString(),
				Query:         w.Query.ValueString(),
				Markdown:      w.Markdown.ValueString(),
				Unit:          w.Unit.ValueString(),
				Thresholds:    []client.Threshold{},
			}
			if l := w.Layout; l != nil {
				widget.Layout = client.Layout{
					X: int(l.X.ValueInt64()), Y: int(l.Y.ValueInt64()),
					W: int(l.W.ValueInt64()), H: int(l.H.ValueInt64()),
				}
			}
			for _, t := range w.Thresholds {
				widget.Thresholds = append(widget.Thresholds, client.Threshold{
					Value: t.Value.ValueFloat64(), Severity: t.Severity.ValueString(),
				})
			}
			if o := w.Options; o != nil {
				widget.Options = client.WidgetOptions{Stacked: o.Stacked.ValueBool(), Legend: boolPtr(o.Legend)}
			}
			page.Widgets = append(page.Widgets, widget)
		}
		in.Pages = append(in.Pages, page)
	}
	return in
}

// knownString is the value of a framework string, or "" while it is unknown (a page or widget openlog has not
// assigned an id to yet).
func knownString(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func (m *dashboardModel) applyDashboard(ctx context.Context, x *client.Dashboard, diags *diagnostics) {
	m.ID = types.StringValue(x.ID)
	m.Name = types.StringValue(x.Name)
	m.Description = types.StringValue(x.Description)
	m.Visibility = types.StringValue(x.Visibility)
	m.Version = types.Int64Value(int64(x.Version))
	m.CreatedByEmail = types.StringValue(x.CreatedByEmail)
	m.CreatedAt = types.StringValue(x.CreatedAt)
	m.UpdatedAt = types.StringValue(x.UpdatedAt)

	m.Variables = []dashboardVariableModel{}
	for _, v := range x.Variables {
		m.Variables = append(m.Variables, dashboardVariableModel{
			Name:       types.StringValue(v.Name),
			Label:      types.StringValue(v.Label),
			Type:       types.StringValue(v.Type),
			Query:      types.StringValue(v.Query),
			Values:     listOfStrings(ctx, v.Values, diags),
			Default:    listOfStrings(ctx, v.Default, diags),
			Multi:      types.BoolValue(v.Multi),
			IncludeAll: types.BoolValue(v.IncludeAll),
		})
	}
	m.Pages = []dashboardPageModel{}
	for _, p := range x.Pages {
		page := dashboardPageModel{
			ID:      types.StringValue(p.ID),
			Name:    types.StringValue(p.Name),
			Widgets: []dashboardWidgetModel{},
		}
		for _, w := range p.Widgets {
			widget := dashboardWidgetModel{
				ID:            types.StringValue(w.ID),
				Title:         types.StringValue(w.Title),
				Visualization: types.StringValue(w.Visualization),
				Layout: &widgetLayoutModel{
					X: types.Int64Value(int64(w.Layout.X)), Y: types.Int64Value(int64(w.Layout.Y)),
					W: types.Int64Value(int64(w.Layout.W)), H: types.Int64Value(int64(w.Layout.H)),
				},
				Query:      types.StringValue(w.Query),
				Markdown:   types.StringValue(w.Markdown),
				Unit:       types.StringValue(w.Unit),
				Thresholds: []widgetThresholdModel{},
				Options: &widgetOptionsModel{
					Stacked: types.BoolValue(w.Options.Stacked),
					Legend:  ptrBool(w.Options.Legend),
				},
			}
			for _, t := range w.Thresholds {
				widget.Thresholds = append(widget.Thresholds, widgetThresholdModel{
					Value: types.Float64Value(t.Value), Severity: types.StringValue(t.Severity),
				})
			}
			page.Widgets = append(page.Widgets, widget)
		}
		m.Pages = append(m.Pages, page)
	}
}

func (r *dashboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.apiDashboard(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.CreateDashboard(ctx, in)
	if err != nil {
		addAPIError(&resp.Diagnostics, "create the dashboard", err)
		return
	}
	plan.applyDashboard(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.GetDashboard(ctx, state.ID.ValueString())
	if err != nil {
		if isGone(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "read the dashboard", err)
		return
	}
	state.applyDashboard(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *dashboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.apiDashboard(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.UpdateDashboard(ctx, state.ID.ValueString(), in, int(state.Version.ValueInt64()))
	if err != nil {
		addAPIError(&resp.Diagnostics, "update the dashboard", err)
		return
	}
	plan.applyDashboard(ctx, out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dashboardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDashboard(ctx, state.ID.ValueString()); err != nil && !isGone(err) {
		addAPIError(&resp.Diagnostics, "delete the dashboard", err)
	}
}

func (r *dashboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importByID(ctx, req, resp)
}
