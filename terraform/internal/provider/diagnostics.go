package provider

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/onuragtas/openlog/terraform/internal/client"
)

// API errors → Terraform diagnostics. The API answers with {"error": {"code", "message"}} and puts the field
// path in front of validation messages ("condition.threshold: required", docs/contracts/api.md). A diagnostic
// that names the attribute is worth much more than one that repeats the HTTP status, so a 400 is attached to
// the attribute its field path resolves to, and the statuses with a standard cause (403 on a read-only
// credential, 409 on a stale version) explain that cause instead of the status text.

// addAPIError appends err to diags. action names what the provider was doing ("create the alert rule").
func addAPIError(diags *diag.Diagnostics, action string, err error) {
	e, ok := client.AsError(err)
	if !ok {
		diags.AddError("Could not "+action, err.Error())
		return
	}
	switch {
	case e.Invalid():
		summary := "openlog rejected the " + strings.TrimPrefix(action, "create the ")
		if p, ok := attributePath(e.Field); ok {
			diags.AddAttributeError(p, "Invalid value", e.Message)
			return
		}
		diags.AddError(summary, e.Message)
	case e.Forbidden():
		diags.AddError("Could not "+action+": the API key may not write",
			e.Message+"\n\nThe openlog API accepts writes from a credential with write permission; a read-only API key is "+
				"refused here. See docs/operations/terraform.md.")
	case e.Conflict():
		diags.AddError("Could not "+action+": the object changed in openlog",
			e.Message+"\n\nSomeone changed this object (or a limit was reached) since Terraform last read it. Run "+
				"'terraform refresh' (or plan again) and re-apply; this provider never overwrites a newer version blindly.")
	case e.NotFound():
		diags.AddError("Could not "+action+": not found",
			e.Message+"\n\nThe object does not exist in this organization. If it was deleted outside Terraform, remove it "+
				"from the state with 'terraform state rm' or let the next plan recreate it.")
	default:
		diags.AddError("Could not "+action, e.Error())
	}
}

// isGone reports whether err is the API's 404, which a read turns into "removed outside Terraform".
func isGone(err error) bool {
	e, ok := client.AsError(err)
	return ok && e.NotFound()
}

// attributePath turns an API field path ("condition.threshold", "config.to[0]", "secrets.url") into the path
// of the attribute that carries it. Only the first segment is used: it is the attribute the practitioner
// edits, and the message itself repeats the full path.
func attributePath(field string) (path.Path, bool) {
	if field == "" {
		return path.Empty(), false
	}
	head := field
	if i := strings.IndexAny(head, ".["); i >= 0 {
		head = head[:i]
	}
	if !knownAttributes[head] {
		return path.Empty(), false
	}
	return path.Root(head), true
}

// knownAttributes are the API field names that are also attribute names of a resource in this provider. A
// field the API reports but no resource has (because the provider does not expose it) falls back to a
// resource-level diagnostic rather than pointing at an attribute that does not exist.
var knownAttributes = map[string]bool{
	"name": true, "description": true, "type": true, "severity": true, "enabled": true,
	"interval_seconds": true, "for_seconds": true, "recovery_for_seconds": true, "condition": true,
	"channel_ids": true, "renotify_interval_seconds": true, "flapping": true, "runbook_url": true,
	"labels": true, "config": true, "secrets": true, "match": true, "position": true, "is_default": true,
	"service_name": true, "service_namespace": true, "environment": true, "sli_type": true,
	"latency_threshold_ms": true, "objective": true, "window_days": true, "visibility": true,
	"variables": true, "pages": true, "version": true,
}

// Condition drift.
//
// An alert rule's condition is a per-type document the API parses, defaults and echoes back in full: a
// configuration that sets three fields comes back with ten. Storing the answer would make every plan show a
// diff, and storing the configuration would hide real drift. The provider therefore keeps the configured
// document as long as the server agrees about every field the configuration actually sets, and replaces it
// with the server's document as soon as one of them differs — which is exactly when Terraform should show a
// diff. The server's full document is always available in the computed condition_effective attribute.

// conditionDrifted reports whether the server's condition disagrees with the configured one in a field the
// configuration sets. Unparsable input counts as drift so the answer becomes visible rather than silently
// ignored.
func conditionDrifted(configured, server []byte) bool {
	var want, got any
	if json.Unmarshal(configured, &want) != nil || json.Unmarshal(server, &got) != nil {
		return true
	}
	return !jsonSubset(want, got)
}

// jsonSubset reports whether every field want sets is present in got with an equal value. Objects recurse;
// arrays and scalars must be equal, because dropping or reordering an element of a filter list is a change.
func jsonSubset(want, got any) bool {
	wm, wok := want.(map[string]any)
	gm, gok := got.(map[string]any)
	if wok && gok {
		for k, wv := range wm {
			gv, ok := gm[k]
			if !ok || !jsonSubset(wv, gv) {
				return false
			}
		}
		return true
	}
	if wok != gok {
		return false
	}
	return reflect.DeepEqual(want, got)
}
