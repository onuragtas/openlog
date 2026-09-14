package scim

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/internal/sso"
)

// filterRe matches the supported filter form: attribute eq "value".
var filterRe = regexp.MustCompile(`(?i)^\s*([a-z][a-z0-9.]*)\s+eq\s+"((?:[^"\\]|\\.)*)"\s*$`)

// parseFilter supports `userName eq "…"`, `externalId eq "…"` (Users) and `displayName eq "…"`,
// `externalId eq "…"` (Groups).
func parseFilter(v string, group bool) (sso.SCIMFilter, error) {
	var f sso.SCIMFilter
	if strings.TrimSpace(v) == "" {
		return f, nil
	}
	m := filterRe.FindStringSubmatch(v)
	if m == nil {
		return f, errBadRequest("invalidFilter", `only filters of the form attribute eq "value" are supported`)
	}
	val, err := strconv.Unquote(`"` + m[2] + `"`)
	if err != nil {
		return f, errBadRequest("invalidFilter", "invalid filter value")
	}
	switch attr := strings.ToLower(m[1]); {
	case attr == "externalid":
		f.ExternalID = val
	case !group && (attr == "username" || attr == "emails.value"):
		f.UserName = val
	case group && attr == "displayname":
		f.DisplayName = val
	default:
		return f, errBadRequest("invalidFilter", "filtering on "+m[1]+" is not supported")
	}
	return f, nil
}

// patchRequest is a PatchOp message.
type patchRequest struct {
	Schemas    []string    `json:"schemas"`
	Operations []patchOpIn `json:"Operations"`
}

type patchOpIn struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// memberPathRe matches members[value eq "id"].
var memberPathRe = regexp.MustCompile(`(?i)^members\[\s*value\s+eq\s+"([^"]+)"\s*\]$`)

// boolValue accepts JSON booleans and the strings "true"/"false" (sent by some IdPs).
func boolValue(raw json.RawMessage) (bool, error) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
			return v, nil
		}
	}
	return false, errBadRequest("invalidValue", "a boolean is required")
}

func stringValue(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", errBadRequest("invalidValue", "a string is required")
	}
	return s, nil
}

func checkPatchSchema(p patchRequest) error {
	for _, s := range p.Schemas {
		if s == schemaPatch {
			if len(p.Operations) == 0 {
				return errBadRequest("invalidValue", "Operations must not be empty")
			}
			if len(p.Operations) > 1000 {
				return errBadRequest("tooMany", "too many operations")
			}
			return nil
		}
	}
	return errBadRequest("invalidSyntax", "schemas must contain "+schemaPatch)
}
