package scim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

type memberIn struct {
	Value string `json:"value"`
}

type groupIn struct {
	Schemas     []string   `json:"schemas"`
	DisplayName string     `json:"displayName"`
	ExternalID  string     `json:"externalId"`
	Members     []memberIn `json:"members"`
}

func (h *Handler) groupResource(ctx context.Context, q *request, g sso.SCIMGroup, withMembers bool) (map[string]any, error) {
	res := map[string]any{"schemas": []string{schemaGroup}, "id": g.ID, "displayName": g.DisplayName,
		"meta": map[string]string{"resourceType": "Group", "created": g.CreatedAt.UTC().Format(time.RFC3339),
			"lastModified": g.UpdatedAt.UTC().Format(time.RFC3339), "location": h.location("Groups", g.ID)}}
	if g.ExternalID != "" {
		res["externalId"] = g.ExternalID
	}
	if withMembers {
		ms := make([]map[string]string, 0, len(g.Members))
		for _, id := range g.Members {
			m := map[string]string{"value": id, "$ref": h.location("Users", id)}
			if su, err := h.sso.Store().GetSCIMUser(ctx, q.org.ID, id); err == nil {
				m["display"] = su.UserName
			} else if !errors.Is(err, auth.ErrNotFound) {
				return nil, err
			}
			ms = append(ms, m)
		}
		res["members"] = ms
	}
	return res, nil
}

func excludesMembers(r *http.Request) bool {
	for _, a := range strings.Split(r.URL.Query().Get("excludedAttributes"), ",") {
		if strings.EqualFold(strings.TrimSpace(a), "members") {
			return true
		}
	}
	return false
}

func (h *Handler) loadGroup(ctx context.Context, q *request, id string) (sso.SCIMGroup, error) {
	g, err := h.sso.Store().GetSCIMGroup(ctx, q.org.ID, id)
	if errors.Is(err, auth.ErrNotFound) {
		return g, errNotFound("group " + id + " not found")
	}
	return g, err
}

func (h *Handler) listGroups(ctx context.Context, w http.ResponseWriter, q *request) error {
	f, err := parseFilter(q.r.URL.Query().Get("filter"), true)
	if err != nil {
		return err
	}
	offset, limit, start, err := paging(q.r)
	if err != nil {
		return err
	}
	groups, total, err := h.sso.Store().ListSCIMGroups(ctx, q.org.ID, f, offset, limit)
	if err != nil {
		return err
	}
	out := make([]any, 0, len(groups))
	for _, g := range groups {
		res, err := h.groupResource(ctx, q, g, !excludesMembers(q.r))
		if err != nil {
			return err
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, listResponse(out, total, start))
	return nil
}

func (h *Handler) getGroup(ctx context.Context, w http.ResponseWriter, q *request, id string) error {
	g, err := h.loadGroup(ctx, q, id)
	if err != nil {
		return err
	}
	res, err := h.groupResource(ctx, q, g, !excludesMembers(q.r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// memberIDs validates that every member is a SCIM user of the organization.
func (h *Handler) memberIDs(ctx context.Context, q *request, in []memberIn) ([]string, error) {
	if len(in) > maxGroupMembers {
		return nil, errBadRequest("tooMany", "too many members")
	}
	out := []string{}
	for _, m := range in {
		id := strings.TrimSpace(m.Value)
		if slices.Contains(out, id) {
			continue
		}
		if _, err := h.sso.Store().GetSCIMUser(ctx, q.org.ID, id); errors.Is(err, auth.ErrNotFound) {
			return nil, errBadRequest("invalidValue", "member "+id+" is not a provisioned user")
		} else if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// syncRoles recomputes the roles of users whose groups changed.
func (h *Handler) syncRoles(ctx context.Context, q *request, userIDs ...[]string) {
	seen := map[string]bool{}
	for _, ids := range userIDs {
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			if _, err := h.sso.SyncSCIMRoles(ctx, q.org.ID, id, q.meta.IP); err != nil {
				h.log.Error("cannot sync SCIM role", "org_id", q.org.ID, "user_id", id, "err", err)
			}
		}
	}
}

func (h *Handler) groupFields(in groupIn) (string, string, error) {
	name, err := clean(in.DisplayName, 512, "displayName")
	if err != nil {
		return "", "", err
	}
	if name == "" {
		return "", "", errBadRequest("invalidValue", "displayName is required")
	}
	ext, err := clean(in.ExternalID, 512, "externalId")
	return name, ext, err
}

func (h *Handler) createGroup(ctx context.Context, w http.ResponseWriter, q *request) error {
	var in groupIn
	if err := decode(q.r, &in); err != nil {
		return err
	}
	name, ext, err := h.groupFields(in)
	if err != nil {
		return err
	}
	members, err := h.memberIDs(ctx, q, in.Members)
	if err != nil {
		return err
	}
	g := sso.SCIMGroup{OrgID: q.org.ID, DisplayName: name, ExternalID: ext, Members: members}
	if err := h.sso.Store().CreateSCIMGroup(ctx, &g); err != nil {
		if errors.Is(err, auth.ErrAlreadyExists) {
			return errConflict("a group with this displayName already exists")
		}
		return err
	}
	h.syncRoles(ctx, q, members)
	h.audit(ctx, q, "scim.group.create", "scim_group", g.ID, map[string]any{"display_name": name, "members": len(members)})
	res, err := h.groupResource(ctx, q, g, true)
	if err != nil {
		return err
	}
	w.Header().Set("Location", h.location("Groups", g.ID))
	writeJSON(w, http.StatusCreated, res)
	return nil
}

func (h *Handler) saveGroup(ctx context.Context, w http.ResponseWriter, q *request, prev, g sso.SCIMGroup, members []string, action string) error {
	if err := h.sso.Store().UpdateSCIMGroup(ctx, &g, members); err != nil {
		if errors.Is(err, auth.ErrAlreadyExists) {
			return errConflict("a group with this displayName already exists")
		}
		return err
	}
	h.syncRoles(ctx, q, prev.Members, g.Members)
	h.audit(ctx, q, action, "scim_group", g.ID, map[string]any{"display_name": g.DisplayName, "members": len(g.Members)})
	res, err := h.groupResource(ctx, q, g, true)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) replaceGroup(ctx context.Context, w http.ResponseWriter, q *request, id string) error {
	prev, err := h.loadGroup(ctx, q, id)
	if err != nil {
		return err
	}
	var in groupIn
	if err := decode(q.r, &in); err != nil {
		return err
	}
	name, ext, err := h.groupFields(in)
	if err != nil {
		return err
	}
	members, err := h.memberIDs(ctx, q, in.Members)
	if err != nil {
		return err
	}
	g := prev
	g.DisplayName, g.ExternalID = name, ext
	return h.saveGroup(ctx, w, q, prev, g, members, "scim.group.update")
}

func (h *Handler) patchGroup(ctx context.Context, w http.ResponseWriter, q *request, id string) error {
	prev, err := h.loadGroup(ctx, q, id)
	if err != nil {
		return err
	}
	var p patchRequest
	if err := decode(q.r, &p); err != nil {
		return err
	}
	if err := checkPatchSchema(p); err != nil {
		return err
	}
	g := prev
	members := slices.Clone(prev.Members)
	membersChanged := false
	for _, op := range p.Operations {
		kind := strings.ToLower(op.Op)
		path := strings.TrimSpace(op.Path)
		lpath := strings.ToLower(path)
		switch {
		case kind != "add" && kind != "replace" && kind != "remove":
			return errBadRequest("invalidSyntax", "unsupported op "+op.Op)
		case path == "" && kind == "remove":
			return errBadRequest("noTarget", "remove requires a path")
		case path == "":
			var in struct {
				DisplayName *string    `json:"displayName"`
				ExternalID  *string    `json:"externalId"`
				Members     []memberIn `json:"members"`
			}
			if err := json.Unmarshal(op.Value, &in); err != nil {
				return errBadRequest("invalidValue", "value must be an object when path is omitted")
			}
			if in.DisplayName != nil {
				if g.DisplayName, err = clean(*in.DisplayName, 512, "displayName"); err != nil {
					return err
				}
			}
			if in.ExternalID != nil {
				if g.ExternalID, err = clean(*in.ExternalID, 512, "externalId"); err != nil {
					return err
				}
			}
			if in.Members != nil {
				ids, err := h.memberIDs(ctx, q, in.Members)
				if err != nil {
					return err
				}
				if kind == "replace" {
					members = ids
				} else {
					members = appendUnique(members, ids)
				}
				membersChanged = true
			}
		case lpath == "displayname":
			if kind == "remove" {
				return errMutability("displayName is required")
			}
			v, err := stringValue(op.Value)
			if err != nil {
				return err
			}
			if g.DisplayName, err = clean(v, 512, "displayName"); err != nil {
				return err
			}
		case lpath == "externalid":
			v := ""
			if kind != "remove" {
				if v, err = stringValue(op.Value); err != nil {
					return err
				}
			}
			if g.ExternalID, err = clean(v, 512, "externalId"); err != nil {
				return err
			}
		case lpath == "members":
			var in []memberIn
			if len(op.Value) > 0 && string(op.Value) != "null" {
				if err := json.Unmarshal(op.Value, &in); err != nil {
					return errBadRequest("invalidValue", "members must be an array")
				}
			}
			switch kind {
			case "remove":
				if len(in) == 0 {
					members = []string{}
				} else {
					for _, m := range in {
						members = slices.DeleteFunc(members, func(x string) bool { return x == strings.TrimSpace(m.Value) })
					}
				}
			case "add", "replace":
				ids, err := h.memberIDs(ctx, q, in)
				if err != nil {
					return err
				}
				if kind == "replace" {
					members = ids
				} else {
					members = appendUnique(members, ids)
				}
			}
			membersChanged = true
		case memberPathRe.MatchString(path) && kind == "remove":
			mid := memberPathRe.FindStringSubmatch(path)[1]
			members = slices.DeleteFunc(members, func(x string) bool { return x == mid })
			membersChanged = true
		default:
			return errBadRequest("invalidPath", "unsupported path "+path)
		}
	}
	if g.DisplayName == "" {
		return errBadRequest("invalidValue", "displayName is required")
	}
	if !membersChanged {
		members = nil
	}
	return h.saveGroup(ctx, w, q, prev, g, members, "scim.group.update")
}

func appendUnique(dst, add []string) []string {
	for _, id := range add {
		if !slices.Contains(dst, id) {
			dst = append(dst, id)
		}
	}
	return dst
}

func (h *Handler) deleteGroup(ctx context.Context, w http.ResponseWriter, q *request, id string) error {
	g, err := h.sso.Store().DeleteSCIMGroup(ctx, q.org.ID, id)
	if errors.Is(err, auth.ErrNotFound) {
		return errNotFound("group " + id + " not found")
	}
	if err != nil {
		return err
	}
	h.syncRoles(ctx, q, g.Members)
	h.audit(ctx, q, "scim.group.delete", "scim_group", g.ID, map[string]any{"display_name": g.DisplayName})
	w.WriteHeader(http.StatusNoContent)
	return nil
}
