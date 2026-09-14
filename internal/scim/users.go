package scim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

type nameJSON struct {
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
	Formatted  string `json:"formatted,omitempty"`
}

type emailJSON struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// userIn is the accepted User representation.
type userIn struct {
	Schemas     []string    `json:"schemas"`
	UserName    string      `json:"userName"`
	ExternalID  string      `json:"externalId"`
	DisplayName string      `json:"displayName"`
	Name        *nameJSON   `json:"name"`
	Emails      []emailJSON `json:"emails"`
	Active      *bool       `json:"active"`
}

func clean(v string, max int, field string) (string, error) {
	v = strings.TrimSpace(v)
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) > max || strings.ContainsFunc(v, unicode.IsControl) {
		return "", errBadRequest("invalidValue", field+" is invalid or too long")
	}
	return v, nil
}

// primaryEmail returns the address the user is identified by: the primary (or first) e-mail, else userName.
func primaryEmail(in userIn) string {
	for _, e := range in.Emails {
		if e.Primary {
			return e.Value
		}
	}
	if len(in.Emails) > 0 {
		return in.Emails[0].Value
	}
	return in.UserName
}

func validEmail(v string) (string, bool) {
	v = auth.NormalizeEmail(v)
	a, err := mail.ParseAddress(v)
	return v, err == nil && a.Address == v && a.Name == "" && len(v) <= 320
}

func (h *Handler) userResource(ctx context.Context, q *request, su sso.SCIMUser) (map[string]any, error) {
	u, err := h.sso.Users().GetUser(ctx, su.UserID)
	if err != nil {
		return nil, err
	}
	groups, _, err := h.sso.Store().ListSCIMGroups(ctx, q.org.ID, sso.SCIMFilter{}, 0, maxCount)
	if err != nil {
		return nil, err
	}
	gs := []map[string]string{}
	for _, g := range groups {
		for _, m := range g.Members {
			if m == su.UserID {
				gs = append(gs, map[string]string{"value": g.ID, "display": g.DisplayName, "$ref": h.location("Groups", g.ID)})
			}
		}
	}
	res := map[string]any{
		"schemas": []string{schemaUser}, "id": su.UserID, "userName": su.UserName, "active": su.Active,
		"displayName": su.DisplayName,
		"name":        nameJSON{GivenName: su.GivenName, FamilyName: su.FamilyName, Formatted: strings.TrimSpace(su.GivenName + " " + su.FamilyName)},
		"emails":      []emailJSON{{Value: u.Email, Type: "work", Primary: true}},
		"groups":      gs,
		"meta": map[string]string{"resourceType": "User", "created": su.CreatedAt.UTC().Format(time.RFC3339),
			"lastModified": su.UpdatedAt.UTC().Format(time.RFC3339), "location": h.location("Users", su.UserID)},
	}
	if su.ExternalID != "" {
		res["externalId"] = su.ExternalID
	}
	return res, nil
}

func (h *Handler) loadUser(ctx context.Context, q *request, id string) (sso.SCIMUser, error) {
	su, err := h.sso.Store().GetSCIMUser(ctx, q.org.ID, id)
	if errors.Is(err, auth.ErrNotFound) {
		return su, errNotFound("user " + id + " not found")
	}
	return su, err
}

func (h *Handler) listUsers(ctx context.Context, w http.ResponseWriter, q *request) error {
	f, err := parseFilter(q.r.URL.Query().Get("filter"), false)
	if err != nil {
		return err
	}
	offset, limit, start, err := paging(q.r)
	if err != nil {
		return err
	}
	users, total, err := h.sso.Store().ListSCIMUsers(ctx, q.org.ID, f, offset, limit)
	if err != nil {
		return err
	}
	out := make([]any, 0, len(users))
	for _, su := range users {
		res, err := h.userResource(ctx, q, su)
		if err != nil {
			return err
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, listResponse(out, total, start))
	return nil
}

func (h *Handler) getUser(ctx context.Context, w http.ResponseWriter, q *request, id string) error {
	su, err := h.loadUser(ctx, q, id)
	if err != nil {
		return err
	}
	res, err := h.userResource(ctx, q, su)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// applyFields copies the mutable attributes of in onto su.
func applyFields(su *sso.SCIMUser, in userIn) error {
	var err error
	if su.UserName, err = clean(in.UserName, 320, "userName"); err != nil {
		return err
	}
	if su.UserName == "" {
		return errBadRequest("invalidValue", "userName is required")
	}
	if su.ExternalID, err = clean(in.ExternalID, 512, "externalId"); err != nil {
		return err
	}
	if su.DisplayName, err = clean(in.DisplayName, 200, "displayName"); err != nil {
		return err
	}
	su.GivenName, su.FamilyName = "", ""
	if in.Name != nil {
		if su.GivenName, err = clean(in.Name.GivenName, 200, "name.givenName"); err != nil {
			return err
		}
		if su.FamilyName, err = clean(in.Name.FamilyName, 200, "name.familyName"); err != nil {
			return err
		}
	}
	return nil
}

func displayName(su sso.SCIMUser) string {
	if su.DisplayName != "" {
		return su.DisplayName
	}
	return strings.TrimSpace(su.GivenName + " " + su.FamilyName)
}

func (h *Handler) createUser(ctx context.Context, w http.ResponseWriter, q *request) error {
	var in userIn
	if err := decode(q.r, &in); err != nil {
		return err
	}
	su := sso.SCIMUser{OrgID: q.org.ID, Active: in.Active == nil || *in.Active}
	if err := applyFields(&su, in); err != nil {
		return err
	}
	email, ok := validEmail(primaryEmail(in))
	if !ok {
		return errBadRequest("invalidValue", "userName or the primary e-mail must be a valid e-mail address")
	}
	if err := h.requireDomain(ctx, q, email); err != nil {
		return err
	}
	if _, total, err := h.sso.Store().ListSCIMUsers(ctx, q.org.ID, sso.SCIMFilter{UserName: su.UserName}, 0, 1); err != nil {
		return err
	} else if total > 0 {
		return errConflict("a user with this userName already exists")
	}
	users := h.sso.Users()
	now := h.sso.Now()
	u, err := users.GetUserByEmail(ctx, email)
	if errors.Is(err, auth.ErrNotFound) {
		name := displayName(su)
		if utf8.RuneCountInString(name) > 200 {
			name = ""
		}
		u = auth.User{Email: email, Name: name, CreatedAt: now, EmailVerifiedAt: &now}
		if err := users.CreateUser(ctx, &u); errors.Is(err, auth.ErrAlreadyExists) {
			u, err = users.GetUserByEmail(ctx, email)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := h.sso.Store().GetSCIMUser(ctx, q.org.ID, u.ID); err == nil {
		return errConflict("this e-mail address is already provisioned")
	} else if !errors.Is(err, auth.ErrNotFound) {
		return err
	}
	su.UserID = u.ID
	if err := h.sso.Store().CreateSCIMUser(ctx, &su); err != nil {
		if errors.Is(err, auth.ErrAlreadyExists) {
			return errConflict("userName or externalId is already used")
		}
		return err
	}
	details := map[string]any{"email": email, "active": su.Active}
	if su.Active {
		role, added, err := h.ensureMember(ctx, q, u.ID)
		if err != nil {
			return err
		}
		details["role"], details["membership_added"] = role, added
	}
	h.audit(ctx, q, "scim.user.create", "user", u.ID, details)
	res, err := h.userResource(ctx, q, su)
	if err != nil {
		return err
	}
	w.Header().Set("Location", h.location("Users", su.UserID))
	writeJSON(w, http.StatusCreated, res)
	return nil
}

// requireDomain refuses addresses outside the organization's verified domains (as SSO sign-in does, D-078).
func (h *Handler) requireDomain(ctx context.Context, q *request, email string) error {
	_, domain, _ := strings.Cut(email, "@")
	d, err := h.sso.Store().FindVerifiedDomain(ctx, domain)
	if errors.Is(err, auth.ErrNotFound) || (err == nil && d.OrgID != q.org.ID) {
		return errBadRequest("invalidValue", "the e-mail domain "+domain+" is not a verified domain of the organization")
	}
	return err
}

// ensureMember adds the membership of an active SCIM user when missing (role from groups or the default).
func (h *Handler) ensureMember(ctx context.Context, q *request, userID string) (auth.Role, bool, error) {
	users := h.sso.Users()
	if m, err := users.GetMembership(ctx, q.org.ID, userID); err == nil {
		return m.Role, false, nil
	} else if !errors.Is(err, auth.ErrNotFound) {
		return "", false, err
	}
	def, err := h.sso.SCIMDefaultRole(ctx, q.org.ID)
	if err != nil {
		return "", false, err
	}
	groups, err := h.sso.Store().SCIMGroupNames(ctx, q.org.ID, userID)
	if err != nil {
		return "", false, err
	}
	role, err := h.sso.RoleForGroups(ctx, q.org.ID, groups, def)
	if err != nil {
		return "", false, err
	}
	if err := h.sso.Auth().CheckMemberLimit(ctx, q.org.ID, false); err != nil { // plan users limit (SaaS mode, D-105)
		var ae *auth.Error
		if errors.As(err, &ae) && ae.Code == auth.CodeQuotaExceeded {
			return "", false, &scimError{http.StatusForbidden, "", ae.Message}
		}
		return "", false, err
	}
	if err := users.AddMember(ctx, q.org.ID, userID, role); err != nil && !errors.Is(err, auth.ErrAlreadyExists) {
		return "", false, err
	}
	h.audit(ctx, q, "member.add", "user", userID, map[string]any{"role": role, "via": "scim"})
	return role, true, nil
}

// deprovision removes the membership of userID and revokes the user's SSO sessions of the organization and the API
// keys they created there. Owners cannot be deprovisioned through SCIM.
func (h *Handler) deprovision(ctx context.Context, q *request, userID string) (map[string]any, error) {
	users := h.sso.Users()
	details := map[string]any{}
	m, err := users.GetMembership(ctx, q.org.ID, userID)
	if errors.Is(err, auth.ErrNotFound) {
		return details, nil
	}
	if err != nil {
		return nil, err
	}
	if m.Role == auth.RoleOwner {
		return nil, errMutability("owners are managed in openlog and cannot be deactivated through SCIM")
	}
	// Dashboard report recipient lists lose the member's address in the same transaction (D-096).
	cleanup, err := auth.RemoveMembership(ctx, users, q.org.ID, userID)
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		return nil, err
	}
	details["role"] = m.Role
	if rd := map[string]any{"user_id": userID, "via": "scim"}; cleanup.Details(rd) {
		details["report_recipient_removed"] = len(cleanup.ReportsUpdated)
		h.audit(ctx, q, "dashboard.report.recipient_remove", "user", userID, rd)
	}
	if n, err := h.sso.RevokeOrgSessions(ctx, q.org.ID, userID); err != nil {
		h.log.Error("cannot revoke sessions of a deprovisioned user", "org_id", q.org.ID, "user_id", userID, "err", err)
	} else if n > 0 {
		details["revoked_sessions"] = n
	}
	if n, err := users.RevokeAPIKeysCreatedBy(ctx, q.org.ID, userID, "", h.sso.Now()); err != nil {
		h.log.Error("cannot revoke API keys of a deprovisioned user", "org_id", q.org.ID, "user_id", userID, "err", err)
	} else if n > 0 {
		details["revoked_api_keys"] = n
	}
	h.audit(ctx, q, "member.remove", "user", userID, map[string]any{"role": m.Role, "via": "scim"})
	return details, nil
}

// setActive applies an active change and returns the audit action ("" = unchanged).
func (h *Handler) setActive(ctx context.Context, q *request, su *sso.SCIMUser, active bool) (string, map[string]any, error) {
	if su.Active == active {
		if active {
			_, _, err := h.ensureMember(ctx, q, su.UserID)
			return "", nil, err
		}
		return "", nil, nil
	}
	if active {
		role, _, err := h.ensureMember(ctx, q, su.UserID)
		su.Active = true
		return "scim.user.activate", map[string]any{"role": role}, err
	}
	details, err := h.deprovision(ctx, q, su.UserID)
	if err != nil {
		return "", nil, err
	}
	su.Active = false
	return "scim.user.deactivate", details, nil
}

// emailOf returns the primary (or first) address of an emails list ("" = none).
func emailOf(emails []emailJSON) string {
	for _, e := range emails {
		if e.Primary {
			return e.Value
		}
	}
	if len(emails) > 0 {
		return emails[0].Value
	}
	return ""
}

// saveUser stores a PUT/PATCH. emailReq is the address the request's emails ask for ("" = none); otherwise a
// changed userName is the new address when the previous userName was the account's address.
func (h *Handler) saveUser(ctx context.Context, w http.ResponseWriter, q *request, su sso.SCIMUser, prev sso.SCIMUser, active bool, emailReq string) error {
	u, err := h.sso.Users().GetUser(ctx, su.UserID)
	if err != nil {
		return err
	}
	target := strings.TrimSpace(emailReq)
	if target == "" && !strings.EqualFold(su.UserName, prev.UserName) && strings.EqualFold(prev.UserName, u.Email) && strings.Contains(su.UserName, "@") {
		target = su.UserName
	}
	if !strings.EqualFold(su.UserName, prev.UserName) {
		if _, total, err := h.sso.Store().ListSCIMUsers(ctx, q.org.ID, sso.SCIMFilter{UserName: su.UserName}, 0, 1); err != nil {
			return err
		} else if total > 0 {
			return errConflict("a user with this userName already exists")
		}
	}
	var emailChange map[string]any
	if target != "" && !strings.EqualFold(auth.NormalizeEmail(target), u.Email) {
		if emailChange, err = h.changeEmail(ctx, q, u, target); err != nil {
			return err
		}
	}
	action, details, err := h.setActive(ctx, q, &su, active)
	if err != nil {
		return err
	}
	if err := h.sso.Store().UpdateSCIMUser(ctx, &su); err != nil {
		if errors.Is(err, auth.ErrAlreadyExists) {
			return errConflict("userName or externalId is already used")
		}
		return err
	}
	if emailChange != nil {
		h.audit(ctx, q, "scim.user.email_change", "user", su.UserID, emailChange)
	}
	if action == "" {
		action = "scim.user.update"
	}
	h.audit(ctx, q, action, "user", su.UserID, details)
	res, err := h.userResource(ctx, q, su)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// changeEmail changes the account's e-mail address (D-089): the new address must be valid, in a verified domain of
// the organization and not used by another account (409 uniqueness); the current address must be in a verified
// domain of the organization as well, and owners' addresses are managed in openlog.
func (h *Handler) changeEmail(ctx context.Context, q *request, u auth.User, target string) (map[string]any, error) {
	email, ok := validEmail(target)
	if !ok {
		return nil, errBadRequest("invalidValue", "the new e-mail address is not valid")
	}
	if err := h.requireDomain(ctx, q, email); err != nil {
		return nil, err
	}
	if err := h.requireDomain(ctx, q, u.Email); err != nil {
		var se *scimError
		if errors.As(err, &se) {
			return nil, errMutability("the current e-mail address is not in a verified domain of the organization")
		}
		return nil, err
	}
	if m, err := h.sso.Users().GetMembership(ctx, q.org.ID, u.ID); err == nil && m.Role == auth.RoleOwner {
		return nil, errMutability("the e-mail address of an owner is managed in openlog")
	} else if err != nil && !errors.Is(err, auth.ErrNotFound) {
		return nil, err
	}
	if other, err := h.sso.Users().GetUserByEmail(ctx, email); err == nil && other.ID != u.ID {
		return nil, errConflict("the e-mail address is already used by another account")
	} else if err != nil && !errors.Is(err, auth.ErrNotFound) {
		return nil, err
	}
	if err := h.sso.Users().SetUserEmail(ctx, u.ID, email); err != nil {
		if errors.Is(err, auth.ErrAlreadyExists) {
			return nil, errConflict("the e-mail address is already used by another account")
		}
		return nil, err
	}
	return map[string]any{"from": u.Email, "to": email}, nil
}

func (h *Handler) replaceUser(ctx context.Context, w http.ResponseWriter, q *request, id string) error {
	prev, err := h.loadUser(ctx, q, id)
	if err != nil {
		return err
	}
	var in userIn
	if err := decode(q.r, &in); err != nil {
		return err
	}
	su := prev
	if err := applyFields(&su, in); err != nil {
		return err
	}
	active := in.Active == nil || *in.Active
	return h.saveUser(ctx, w, q, su, prev, active, emailOf(in.Emails))
}

func (h *Handler) patchUser(ctx context.Context, w http.ResponseWriter, q *request, id string) error {
	prev, err := h.loadUser(ctx, q, id)
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
	su := prev
	active := prev.Active
	email := ""
	for _, op := range p.Operations {
		kind := strings.ToLower(op.Op)
		if kind != "add" && kind != "replace" && kind != "remove" {
			return errBadRequest("invalidSyntax", "unsupported op "+op.Op)
		}
		if op.Path == "" {
			if kind == "remove" {
				return errBadRequest("noTarget", "remove requires a path")
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(op.Value, &obj); err != nil {
				return errBadRequest("invalidValue", "value must be an object when path is omitted")
			}
			for k, v := range obj {
				if err := patchUserAttr(&su, &active, &email, k, v, false); err != nil {
					return err
				}
			}
			continue
		}
		if err := patchUserAttr(&su, &active, &email, op.Path, op.Value, kind == "remove"); err != nil {
			return err
		}
	}
	if su.UserName == "" {
		return errBadRequest("invalidValue", "userName is required")
	}
	return h.saveUser(ctx, w, q, su, prev, active, email)
}

// patchEmail reads the new address of an emails operation: the emails array, or the value of
// emails[primary eq true].value / emails[type eq "work"].value.
func patchEmail(lp string, raw json.RawMessage, email *string) error {
	if lp == "emails" {
		var list []emailJSON
		if err := json.Unmarshal(raw, &list); err != nil {
			return errBadRequest("invalidValue", "emails must be an array")
		}
		*email = emailOf(list)
		return nil
	}
	if strings.HasPrefix(lp, "emails[") && strings.HasSuffix(lp, "].value") {
		filter := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(lp, "emails["), "].value"), " ", "")
		if filter == "primaryeqtrue" || filter == `typeeq"work"` {
			v, err := stringValue(raw)
			if err != nil {
				return err
			}
			*email = v
		}
	}
	return nil
}

func patchUserAttr(su *sso.SCIMUser, active *bool, email *string, path string, raw json.RawMessage, remove bool) error {
	str := func(max int, field string) (string, error) {
		if remove {
			return "", nil
		}
		v, err := stringValue(raw)
		if err != nil {
			return "", err
		}
		return clean(v, max, field)
	}
	var err error
	switch strings.ToLower(path) {
	case "active":
		if remove {
			return errMutability("active cannot be removed")
		}
		*active, err = boolValue(raw)
	case "username":
		if remove {
			return errMutability("userName is required")
		}
		su.UserName, err = str(320, "userName")
	case "externalid":
		su.ExternalID, err = str(512, "externalId")
	case "displayname":
		su.DisplayName, err = str(200, "displayName")
	case "name.givenname":
		su.GivenName, err = str(200, "name.givenName")
	case "name.familyname":
		su.FamilyName, err = str(200, "name.familyName")
	case "name":
		if remove {
			su.GivenName, su.FamilyName = "", ""
			return nil
		}
		var n nameJSON
		if json.Unmarshal(raw, &n) != nil {
			return errBadRequest("invalidValue", "name must be an object")
		}
		if su.GivenName, err = clean(n.GivenName, 200, "name.givenName"); err == nil {
			su.FamilyName, err = clean(n.FamilyName, 200, "name.familyName")
		}
	default:
		// E-mail addresses change the account's address (saveUser); phone numbers, enterprise extension
		// attributes …: accepted and ignored.
		lp := strings.ToLower(path)
		if strings.HasPrefix(lp, "emails") {
			if remove {
				return nil
			}
			return patchEmail(lp, raw, email)
		}
		if strings.HasPrefix(lp, "phonenumbers") || strings.HasPrefix(lp, "addresses") ||
			strings.HasPrefix(lp, "title") || strings.HasPrefix(lp, "preferredlanguage") || strings.HasPrefix(lp, "locale") ||
			strings.HasPrefix(lp, "timezone") || strings.HasPrefix(lp, "nickname") || strings.HasPrefix(lp, "urn:") ||
			strings.HasPrefix(lp, "schemas") || strings.HasPrefix(lp, "name.") || strings.HasPrefix(lp, "usertype") {
			return nil
		}
		return errBadRequest("invalidPath", "unsupported path "+path)
	}
	return err
}

func (h *Handler) deleteUser(ctx context.Context, w http.ResponseWriter, q *request, id string) error {
	su, err := h.loadUser(ctx, q, id)
	if err != nil {
		return err
	}
	details, err := h.deprovision(ctx, q, su.UserID)
	if err != nil {
		return err
	}
	if err := h.sso.Store().DeleteSCIMUser(ctx, q.org.ID, su.UserID); err != nil && !errors.Is(err, auth.ErrNotFound) {
		return err
	}
	h.audit(ctx, q, "scim.user.delete", "user", su.UserID, details)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
