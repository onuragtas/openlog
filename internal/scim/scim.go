// Package scim implements SCIM 2.0 provisioning (RFC 7643/7644 subset, docs/contracts/api.md "SCIM", D-078) at
// /api/scim/v2: Users and Groups of one organization, authenticated with an organization's SCIM bearer token.
//
// A SCIM user is a member of the organization: creating an active user adds the membership (the openlog user is
// created when the address is new), deactivating or deleting it removes the membership, revokes the user's SSO
// sessions of the organization and the API keys they created there. Group memberships map to roles through the
// organization's role mappings (internal/sso). Owners are never changed by SCIM.
package scim

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

// BasePath is where the handler is mounted.
const BasePath = "/api/scim/v2"

const (
	schemaUser      = "urn:ietf:params:scim:schemas:core:2.0:User"
	schemaGroup     = "urn:ietf:params:scim:schemas:core:2.0:Group"
	schemaList      = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaError     = "urn:ietf:params:scim:api:messages:2.0:Error"
	schemaPatch     = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	schemaSPConfig  = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	schemaResType   = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	schemaSchema    = "urn:ietf:params:scim:schemas:core:2.0:Schema"
	contentType     = "application/scim+json"
	maxBody         = 1 << 20
	defaultCount    = 100
	maxCount        = 500
	maxGroupMembers = 10000
)

// Handler serves the SCIM API.
type Handler struct {
	sso  *sso.Service
	meta func(*http.Request) auth.ClientMeta
	log  *slog.Logger
}

// NewHandler creates the handler; meta extracts the client IP (auth.Service.Meta).
func NewHandler(svc *sso.Service, meta func(*http.Request) auth.ClientMeta, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Handler{sso: svc, meta: meta, log: log}
}

// scimError is an RFC 7644 §3.12 error.
type scimError struct {
	status   int
	scimType string
	detail   string
}

func (e *scimError) Error() string { return e.detail }

func errBadRequest(scimType, detail string) error {
	return &scimError{http.StatusBadRequest, scimType, detail}
}

func errNotFound(detail string) error { return &scimError{http.StatusNotFound, "", detail} }

func errConflict(detail string) error { return &scimError{http.StatusConflict, "uniqueness", detail} }

func errMutability(detail string) error {
	return &scimError{http.StatusBadRequest, "mutability", detail}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	var se *scimError
	if !errors.As(err, &se) {
		var ae *auth.Error
		switch {
		case errors.As(err, &ae) && ae.Code == auth.CodeUnauthenticated:
			se = &scimError{http.StatusUnauthorized, "", ae.Message}
		case errors.As(err, &ae) && ae.Code == auth.CodeNotFound:
			se = &scimError{http.StatusNotFound, "", ae.Message}
		case errors.As(err, &ae) && (ae.Code == auth.CodeAlreadyExists):
			se = &scimError{http.StatusConflict, "uniqueness", ae.Message}
		case errors.As(err, &ae) && (ae.Code == auth.CodeFailedPrecondition || ae.Code == auth.CodeInvalidArgument):
			se = &scimError{http.StatusBadRequest, "invalidValue", ae.Message}
		case errors.As(err, &ae) && ae.Code == auth.CodePermissionDenied:
			se = &scimError{http.StatusForbidden, "", ae.Message}
		default:
			h.log.Error("scim request failed", "err", err)
			se = &scimError{http.StatusServiceUnavailable, "", "provisioning backend unavailable"}
		}
	}
	if se.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="openlog-scim"`)
	}
	if se.status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "30")
	}
	body := map[string]any{"schemas": []string{schemaError}, "status": strconv.Itoa(se.status), "detail": se.detail}
	if se.scimType != "" {
		body["scimType"] = se.scimType
	}
	writeJSON(w, se.status, body)
}

// request is one authenticated SCIM request.
type request struct {
	org   auth.Organization
	token sso.SCIMToken
	meta  auth.ClientMeta
	r     *http.Request
}

func (q *request) actor() string { return "scim:" + q.token.Name }

func (h *Handler) audit(ctx context.Context, q *request, action, targetType, targetID string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	details["token_id"] = q.token.ID
	h.sso.Audit(ctx, q.org.ID, q.actor(), q.meta.IP, action, targetType, targetID, details)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.sso.SCIMEnabled() {
		h.writeError(w, &scimError{http.StatusNotFound, "", "SCIM provisioning is disabled on this server"})
		return
	}
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(raw) < 8 || !strings.EqualFold(raw[:7], "bearer ") {
		h.writeError(w, &scimError{http.StatusUnauthorized, "", "missing bearer token"})
		return
	}
	tok, org, err := h.sso.AuthenticateSCIM(r.Context(), raw[7:])
	if err != nil {
		h.writeError(w, err)
		return
	}
	q := &request{org: org, token: tok, meta: h.meta(r), r: r}
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, BasePath), "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) > 2 || parts[0] == "" {
		h.writeError(w, errNotFound("no such SCIM endpoint"))
		return
	}
	resource, id := parts[0], ""
	if len(parts) == 2 {
		id = parts[1]
	}
	if err := h.route(w, q, r.Method, resource, id); err != nil {
		h.writeError(w, err)
	}
}

func (h *Handler) route(w http.ResponseWriter, q *request, method, resource, id string) error {
	ctx := q.r.Context()
	switch {
	case resource == "ServiceProviderConfig" && id == "" && method == http.MethodGet:
		writeJSON(w, http.StatusOK, h.serviceProviderConfig())
	case resource == "ResourceTypes" && method == http.MethodGet:
		return h.resourceTypes(w, id)
	case resource == "Schemas" && method == http.MethodGet:
		return h.schemas(w, id)
	case resource == "Users" && id == "" && method == http.MethodGet:
		return h.listUsers(ctx, w, q)
	case resource == "Users" && id == "" && method == http.MethodPost:
		return h.createUser(ctx, w, q)
	case resource == "Users" && id != "" && method == http.MethodGet:
		return h.getUser(ctx, w, q, id)
	case resource == "Users" && id != "" && method == http.MethodPut:
		return h.replaceUser(ctx, w, q, id)
	case resource == "Users" && id != "" && method == http.MethodPatch:
		return h.patchUser(ctx, w, q, id)
	case resource == "Users" && id != "" && method == http.MethodDelete:
		return h.deleteUser(ctx, w, q, id)
	case resource == "Groups" && id == "" && method == http.MethodGet:
		return h.listGroups(ctx, w, q)
	case resource == "Groups" && id == "" && method == http.MethodPost:
		return h.createGroup(ctx, w, q)
	case resource == "Groups" && id != "" && method == http.MethodGet:
		return h.getGroup(ctx, w, q, id)
	case resource == "Groups" && id != "" && method == http.MethodPut:
		return h.replaceGroup(ctx, w, q, id)
	case resource == "Groups" && id != "" && method == http.MethodPatch:
		return h.patchGroup(ctx, w, q, id)
	case resource == "Groups" && id != "" && method == http.MethodDelete:
		return h.deleteGroup(ctx, w, q, id)
	case resource == "Users" || resource == "Groups" || resource == "ServiceProviderConfig":
		return &scimError{http.StatusMethodNotAllowed, "", "method not allowed"}
	default:
		return errNotFound("no such SCIM endpoint")
	}
	return nil
}

func decode(r *http.Request, v any) error {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != contentType && ct != "application/json" {
		return errBadRequest("invalidSyntax", "Content-Type must be application/scim+json")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return errBadRequest("invalidSyntax", "invalid JSON body")
	}
	return nil
}

func (h *Handler) location(resource, id string) string {
	return h.sso.SCIMBaseURL() + "/" + resource + "/" + id
}

// paging parses startIndex (1-based) and count.
func paging(r *http.Request) (offset, limit, start int, err error) {
	start, limit = 1, defaultCount
	if v := r.URL.Query().Get("startIndex"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil {
			return 0, 0, 0, errBadRequest("invalidValue", "startIndex must be an integer")
		}
		start = max(1, n)
	}
	if v := r.URL.Query().Get("count"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil {
			return 0, 0, 0, errBadRequest("invalidValue", "count must be an integer")
		}
		limit = min(max(0, n), maxCount)
	}
	return start - 1, limit, start, nil
}

func listResponse(resources []any, total, start int) map[string]any {
	if resources == nil {
		resources = []any{}
	}
	return map[string]any{"schemas": []string{schemaList}, "totalResults": total, "startIndex": start,
		"itemsPerPage": len(resources), "Resources": resources}
}

func (h *Handler) serviceProviderConfig() map[string]any {
	return map[string]any{
		"schemas":          []string{schemaSPConfig},
		"documentationUri": "https://github.com/onuragtas/openlog/blob/main/docs/operations/sso.md",
		"patch":            map[string]bool{"supported": true},
		"bulk":             map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":           map[string]any{"supported": true, "maxResults": maxCount},
		"changePassword":   map[string]bool{"supported": false},
		"sort":             map[string]bool{"supported": false},
		"etag":             map[string]bool{"supported": false},
		"authenticationSchemes": []map[string]any{{"type": "oauthbearertoken", "name": "OAuth Bearer Token",
			"description": "SCIM token created under Settings → Single sign-on (ols_…)", "primary": true}},
		"meta": map[string]string{"resourceType": "ServiceProviderConfig", "location": h.sso.SCIMBaseURL() + "/ServiceProviderConfig"},
	}
}

func (h *Handler) resourceTypes(w http.ResponseWriter, id string) error {
	types := map[string]map[string]any{
		"User": {"schemas": []string{schemaResType}, "id": "User", "name": "User", "endpoint": "/Users", "schema": schemaUser,
			"meta": map[string]string{"resourceType": "ResourceType", "location": h.sso.SCIMBaseURL() + "/ResourceTypes/User"}},
		"Group": {"schemas": []string{schemaResType}, "id": "Group", "name": "Group", "endpoint": "/Groups", "schema": schemaGroup,
			"meta": map[string]string{"resourceType": "ResourceType", "location": h.sso.SCIMBaseURL() + "/ResourceTypes/Group"}},
	}
	if id != "" {
		t, ok := types[id]
		if !ok {
			return errNotFound("resource type not found")
		}
		writeJSON(w, http.StatusOK, t)
		return nil
	}
	writeJSON(w, http.StatusOK, listResponse([]any{types["User"], types["Group"]}, 2, 1))
	return nil
}

func attr(name, typ string, multi, required bool, uniqueness string) map[string]any {
	return map[string]any{"name": name, "type": typ, "multiValued": multi, "required": required, "caseExact": false,
		"mutability": "readWrite", "returned": "default", "uniqueness": uniqueness}
}

func (h *Handler) schemas(w http.ResponseWriter, id string) error {
	user := map[string]any{"schemas": []string{schemaSchema}, "id": schemaUser, "name": "User", "attributes": []any{
		attr("userName", "string", false, true, "server"), attr("externalId", "string", false, false, "none"),
		attr("displayName", "string", false, false, "none"), attr("active", "boolean", false, false, "none"),
		attr("name", "complex", false, false, "none"), attr("emails", "complex", true, false, "none"),
	}}
	group := map[string]any{"schemas": []string{schemaSchema}, "id": schemaGroup, "name": "Group", "attributes": []any{
		attr("displayName", "string", false, true, "server"), attr("externalId", "string", false, false, "none"),
		attr("members", "complex", true, false, "none"),
	}}
	switch id {
	case "":
		writeJSON(w, http.StatusOK, listResponse([]any{user, group}, 2, 1))
	case schemaUser:
		writeJSON(w, http.StatusOK, user)
	case schemaGroup:
		writeJSON(w, http.StatusOK, group)
	default:
		return errNotFound("schema not found")
	}
	return nil
}
