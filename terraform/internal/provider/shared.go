package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onuragtas/openlog/terraform/internal/client"
)

// Helpers shared by the resources and data sources: wiring the configured client through, importing by id,
// and the conversions between the framework's null-aware values and the API's Go types.

// diagnostics shortens the framework's collection type, which every conversion below appends to.
type diagnostics = diag.Diagnostics

// resourceClient type-asserts the client the provider's Configure put into the resource data. A nil provider
// data is normal: the framework calls Configure before the provider is configured during validation.
func resourceClient(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *client.Client {
	if req.ProviderData == nil {
		return nil
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			"The openlog provider passed something other than *client.Client to a resource. This is a bug in the provider.")
		return nil
	}
	return c
}

// dataSourceClient is resourceClient for data sources.
func dataSourceClient(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *client.Client {
	if req.ProviderData == nil {
		return nil
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			"The openlog provider passed something other than *client.Client to a data source. This is a bug in the provider.")
		return nil
	}
	return c
}

// importByID imports a resource from its openlog id; Read fills the rest of the state.
func importByID(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// stringPtr maps a framework string to the API's *string: null (and unknown) mean "not set", while an empty
// string is a value of its own — the distinction the SLO and APM selectors rely on.
func stringPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

// ptrString is the inverse of stringPtr.
func ptrString(p *string) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(*p)
}

// stringsFromSet reads a framework set of strings. A null or unknown set is no value at all, which the API
// reads as "none".
func stringsFromSet(ctx context.Context, v types.Set, diags *diag.Diagnostics) []string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}

// stringsFromList reads a framework list of strings.
func stringsFromList(ctx context.Context, v types.List, diags *diag.Diagnostics) []string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}

// setOfStrings builds a framework set from API strings; a nil slice becomes an empty set, because the API
// answers with [] rather than null for these lists.
func setOfStrings(ctx context.Context, v []string, diags *diag.Diagnostics) types.Set {
	if v == nil {
		v = []string{}
	}
	s, d := types.SetValueFrom(ctx, types.StringType, v)
	diags.Append(d...)
	return s
}

// listOfStrings builds a framework list from API strings, empty rather than null for the same reason.
func listOfStrings(ctx context.Context, v []string, diags *diag.Diagnostics) types.List {
	if v == nil {
		v = []string{}
	}
	l, d := types.ListValueFrom(ctx, types.StringType, v)
	diags.Append(d...)
	return l
}

// mapOfStrings builds a framework map from API labels or secret hints.
func mapOfStrings(ctx context.Context, v map[string]string, diags *diag.Diagnostics) types.Map {
	if v == nil {
		v = map[string]string{}
	}
	m, d := types.MapValueFrom(ctx, types.StringType, v)
	diags.Append(d...)
	return m
}

// stringsFromMap reads a framework map of strings.
func stringsFromMap(ctx context.Context, v types.Map, diags *diag.Diagnostics) map[string]string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}

// boolPtr maps a framework bool to *bool, keeping "unset" apart from "false".
func boolPtr(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	b := v.ValueBool()
	return &b
}

// ptrBool is the inverse of boolPtr.
func ptrBool(p *bool) types.Bool {
	if p == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*p)
}
