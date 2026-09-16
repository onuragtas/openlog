package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/alert"
	"github.com/onuragtas/openlog/internal/alert/notify"
	"github.com/onuragtas/openlog/internal/alert/secrets"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/config"
)

// fakeAlertStore is an in-memory alert.ManagerStore scoped by organization.
type fakeAlertStore struct {
	mu        sync.Mutex
	rules     map[string]*alert.RuleView
	channels  map[string]*alert.Channel
	mutes     map[string]*alert.Mute
	incidents map[string]*alert.Incident
	tests     int
	calendars map[string]*alert.HolidayCalendar // alert_calendars_test.go
	routes    map[string]*alert.RoutingRule     // alert_routes_test.go
}

func newFakeAlertStore() *fakeAlertStore {
	return &fakeAlertStore{rules: map[string]*alert.RuleView{}, channels: map[string]*alert.Channel{}, mutes: map[string]*alert.Mute{},
		incidents: map[string]*alert.Incident{}, routes: map[string]*alert.RoutingRule{}}
}

func (f *fakeAlertStore) CountRules(context.Context, string) (int, error) { return len(f.rules), nil }
func (f *fakeAlertStore) ListRules(_ context.Context, orgID string, _ alert.RuleFilter) ([]alert.RuleView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.RuleView{}
	for _, r := range f.rules {
		if r.OrgID == orgID {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (f *fakeAlertStore) GetRule(_ context.Context, orgID, id string) (*alert.RuleView, []alert.SeriesState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rules[id]
	if !ok || r.OrgID != orgID {
		return nil, nil, alert.ErrNotFound
	}
	c := *r
	return &c, nil, nil
}
func (f *fakeAlertStore) CreateRule(_ context.Context, orgID string, d *alert.Definition, actor alert.Actor) (*alert.RuleView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := &alert.RuleView{Rule: alert.Rule{Definition: *d, ID: uuid.NewString(), OrgID: orgID, Version: 1, CreatedBy: actor.UserID,
		CreatedByEmail: actor.Email, CreatedAt: time.Now(), UpdatedAt: time.Now()}, Status: alert.RuleStatus{State: "unknown"}}
	f.rules[v.ID] = v
	return v, nil
}
func (f *fakeAlertStore) UpdateRule(ctx context.Context, orgID, id string, d *alert.Definition, _ int, _ alert.Actor, _ string) (*alert.RuleView, error) {
	v, _, err := f.GetRule(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v.Definition = *d
	v.Version++
	f.rules[id] = v
	return v, nil
}
func (f *fakeAlertStore) SetRuleEnabled(ctx context.Context, orgID, id string, enabled bool, _ alert.Actor, _ string) (*alert.RuleView, error) {
	v, _, err := f.GetRule(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	v.Enabled = enabled
	f.mu.Lock()
	f.rules[id] = v
	f.mu.Unlock()
	return v, nil
}
func (f *fakeAlertStore) DeleteRule(ctx context.Context, orgID, id string, _ alert.Actor, _ string) error {
	if _, _, err := f.GetRule(ctx, orgID, id); err != nil {
		return err
	}
	f.mu.Lock()
	delete(f.rules, id)
	f.mu.Unlock()
	return nil
}
func (f *fakeAlertStore) ListIncidents(_ context.Context, orgID string, _ alert.IncidentFilter) ([]alert.Incident, string, alert.IncidentCounts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.Incident{}
	for _, i := range f.incidents {
		if i.OrgID == orgID {
			out = append(out, *i)
		}
	}
	return out, "", alert.IncidentCounts{Open: len(out)}, nil
}
func (f *fakeAlertStore) GetIncident(_ context.Context, orgID, id string) (*alert.Incident, []alert.IncidentEvent, []alert.DeliveryView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.incidents[id]
	if !ok || i.OrgID != orgID {
		return nil, nil, nil, alert.ErrNotFound
	}
	c := *i
	return &c, nil, nil, nil
}
func (f *fakeAlertStore) AcknowledgeIncident(ctx context.Context, orgID, id string, _ alert.Actor, _ string) (*alert.Incident, error) {
	i, _, _, err := f.GetIncident(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if i.State == alert.IncidentResolved {
		return nil, &alert.PreconditionError{Msg: "the incident is already resolved"}
	}
	f.mu.Lock()
	f.incidents[id].State = alert.IncidentAcknowledged
	f.mu.Unlock()
	i.State = alert.IncidentAcknowledged
	return i, nil
}
func (f *fakeAlertStore) ResolveIncident(ctx context.Context, orgID, id, _ string, _ alert.Actor, _ string) (*alert.Incident, error) {
	i, _, _, err := f.GetIncident(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.incidents[id].State = alert.IncidentResolved
	f.mu.Unlock()
	i.State = alert.IncidentResolved
	return i, nil
}
func (f *fakeAlertStore) AddIncidentNote(ctx context.Context, orgID, id, text string, actor alert.Actor) (*alert.IncidentEvent, error) {
	if _, _, _, err := f.GetIncident(ctx, orgID, id); err != nil {
		return nil, err
	}
	return &alert.IncidentEvent{ID: 1, IncidentID: id, Kind: alert.EventNote, Message: text, ActorEmail: actor.Email, At: time.Now()}, nil
}
func (f *fakeAlertStore) ListChannels(_ context.Context, orgID string) ([]alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.Channel{}
	for _, c := range f.channels {
		if c.OrgID == orgID {
			out = append(out, *c)
		}
	}
	return out, nil
}
func (f *fakeAlertStore) GetChannel(_ context.Context, orgID, id string) (*alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.channels[id]
	if !ok || c.OrgID != orgID {
		return nil, alert.ErrNotFound
	}
	cc := *c
	return &cc, nil
}
func (f *fakeAlertStore) CreateChannel(_ context.Context, orgID, id string, p *alert.PreparedChannel, stored, keyID string, _ alert.Actor) (*alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &alert.Channel{ID: id, OrgID: orgID, Name: p.Name, Type: p.Type, Enabled: p.Enabled, Config: p.Config, Secrets: stored,
		SecretsKeyID: keyID, SecretHints: p.Hints, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.channels[id] = c
	return c, nil
}
func (f *fakeAlertStore) UpdateChannel(ctx context.Context, orgID, id string, p *alert.PreparedChannel, stored, keyID string, a alert.Actor) (*alert.Channel, error) {
	if _, err := f.GetChannel(ctx, orgID, id); err != nil {
		return nil, err
	}
	return f.CreateChannel(ctx, orgID, id, p, stored, keyID, a)
}
func (f *fakeAlertStore) DeleteChannel(ctx context.Context, orgID, id string, _ alert.Actor) error {
	if _, err := f.GetChannel(ctx, orgID, id); err != nil {
		return err
	}
	f.mu.Lock()
	delete(f.channels, id)
	f.mu.Unlock()
	return nil
}
func (f *fakeAlertStore) RecordTest(context.Context, alert.Notification, alert.Attempt, alert.Actor) error {
	f.mu.Lock()
	f.tests++
	f.mu.Unlock()
	return nil
}
func (f *fakeAlertStore) OrgName(context.Context, string) (string, error) { return "Org", nil }
func (f *fakeAlertStore) ListMutes(_ context.Context, orgID string, _ bool) ([]alert.Mute, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.Mute{}
	for _, m := range f.mutes {
		if m.OrgID == orgID {
			out = append(out, *m)
		}
	}
	return out, nil
}
func (f *fakeAlertStore) GetMute(_ context.Context, orgID, id string) (*alert.Mute, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.mutes[id]
	if !ok || m.OrgID != orgID {
		return nil, alert.ErrNotFound
	}
	c := *m
	return &c, nil
}
func (f *fakeAlertStore) CreateMute(_ context.Context, orgID string, v *alert.ValidMute, actor alert.Actor) (*alert.Mute, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := &alert.Mute{ID: uuid.NewString(), OrgID: orgID, Name: v.Name, StartsAt: v.StartsAt, EndsAt: v.EndsAt, RuleIDs: v.RuleIDs,
		Matchers: v.Matchers, Schedule: v.Schedule, CreatedBy: actor.UserID}
	f.mutes[m.ID] = m
	return m, nil
}
func (f *fakeAlertStore) UpdateMute(ctx context.Context, orgID, id string, v *alert.ValidMute, _ alert.Actor) (*alert.Mute, error) {
	m, err := f.GetMute(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	m.Name = v.Name
	return m, nil
}
func (f *fakeAlertStore) DeleteMute(ctx context.Context, orgID, id string, _ alert.Actor) error {
	_, err := f.GetMute(ctx, orgID, id)
	return err
}
func (f *fakeAlertStore) ListDeliveries(context.Context, string, alert.DeliveryFilter) ([]alert.DeliveryView, error) {
	return []alert.DeliveryView{}, nil
}

// ---- routing rules (alert_routes_test.go) ----

func (f *fakeAlertStore) ListRoutingRules(_ context.Context, orgID string) ([]alert.RoutingRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.RoutingRule{}
	for _, r := range f.routes {
		if r.OrgID == orgID {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (f *fakeAlertStore) GetRoutingRule(_ context.Context, orgID, id string) (*alert.RoutingRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.routes[id]
	if !ok || r.OrgID != orgID {
		return nil, alert.ErrNotFound
	}
	c := *r
	return &c, nil
}

// checkRoutingWrite mirrors the store's checks: the channels exist in the organization and there is at most one
// default route (exceptID is the rule being updated).
func (f *fakeAlertStore) checkRoutingWrite(orgID, exceptID string, v *alert.ValidRoutingRule) error {
	for _, id := range v.ChannelIDs {
		if c, ok := f.channels[id]; !ok || c.OrgID != orgID {
			return &alert.ValidationError{Field: "channel_ids", Msg: "unknown channel"}
		}
	}
	if !v.IsDefault {
		return nil
	}
	for _, r := range f.routes {
		if r.OrgID == orgID && r.IsDefault && r.ID != exceptID {
			return &alert.PreconditionError{Msg: "the organization already has a default route"}
		}
	}
	return nil
}

func (f *fakeAlertStore) CreateRoutingRule(_ context.Context, orgID string, v *alert.ValidRoutingRule, actor alert.Actor) (*alert.RoutingRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.checkRoutingWrite(orgID, "", v); err != nil {
		return nil, err
	}
	r := &alert.RoutingRule{ID: uuid.NewString(), OrgID: orgID, Name: v.Name, Position: v.Position, Enabled: v.Enabled,
		IsDefault: v.IsDefault, Match: v.Match, ChannelIDs: v.ChannelIDs, CreatedBy: actor.UserID, CreatedByEmail: actor.Email,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.routes[r.ID] = r
	c := *r
	return &c, nil
}

func (f *fakeAlertStore) UpdateRoutingRule(_ context.Context, orgID, id string, v *alert.ValidRoutingRule, _ alert.Actor) (*alert.RoutingRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.routes[id]
	if !ok || r.OrgID != orgID {
		return nil, alert.ErrNotFound
	}
	if err := f.checkRoutingWrite(orgID, id, v); err != nil {
		return nil, err
	}
	r.Name, r.Position, r.Enabled, r.IsDefault, r.Match, r.ChannelIDs = v.Name, v.Position, v.Enabled, v.IsDefault, v.Match, v.ChannelIDs
	r.UpdatedAt = time.Now()
	c := *r
	return &c, nil
}

func (f *fakeAlertStore) DeleteRoutingRule(_ context.Context, orgID, id string, _ alert.Actor) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.routes[id]; !ok || r.OrgID != orgID {
		return alert.ErrNotFound
	}
	delete(f.routes, id)
	return nil
}

func (f *fakeAlertStore) ReorderRoutingRules(ctx context.Context, orgID string, ids []string, _ alert.Actor) ([]alert.RoutingRule, error) {
	f.mu.Lock()
	stored := map[string]bool{}
	for _, r := range f.routes {
		if r.OrgID == orgID {
			stored[r.ID] = true
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !stored[id] || seen[id] {
			f.mu.Unlock()
			return nil, &alert.ValidationError{Field: "ids", Msg: "must list every routing rule of the organization exactly once"}
		}
		seen[id] = true
	}
	if len(seen) != len(stored) {
		f.mu.Unlock()
		return nil, &alert.ValidationError{Field: "ids", Msg: "must list every routing rule of the organization exactly once"}
	}
	for i, id := range ids {
		f.routes[id].Position = i
	}
	f.mu.Unlock()
	return f.ListRoutingRules(ctx, orgID)
}

type okSender struct{}

func (okSender) Send(context.Context, notify.Target, notify.Event, string) notify.Result {
	return notify.Result{StatusCode: 200}
}

type alertEnv struct {
	*accountEnv
	store  *fakeAlertStore
	owner  *client
	orgA   string
	orgB   string
	otherB *client
}

func (e *alertEnv) member(t *testing.T, email string, role auth.Role) *client {
	t.Helper()
	rec := e.owner.do(http.MethodPost, "/api/v1/invitations", map[string]string{"email": email, "role": string(role)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite %s: %d %s", email, rec.Code, rec.Body)
	}
	token := decode[map[string]any](t, rec)["token"].(string)
	anon := &client{t: t, h: e.h}
	if rec := anon.do(http.MethodPost, "/api/v1/invitations/accept", map[string]string{"token": token, "password": ownerPassword, "name": email}); rec.Code != http.StatusOK {
		t.Fatalf("accept %s: %d %s", email, rec.Code, rec.Body)
	}
	return e.login(t, email, ownerPassword)
}

func newAlertEnv(t *testing.T) *alertEnv {
	t.Helper()
	st := memstore.New()
	svc := auth.NewService(st, auth.Config{CookieSecure: true, LoginMaxFailures: 5}, quietLog())
	for _, spec := range []auth.BootstrapSpec{
		{TenantID: "tenant-a", OrgName: "Org A", OwnerEmail: "owner@example.com", OwnerPassword: ownerPassword},
		{TenantID: "tenant-b", OrgName: "Org B", OwnerEmail: "other@example.com", OwnerPassword: ownerPassword},
	} {
		if _, err := svc.Bootstrap(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
	}
	conn := &recordingConn{}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), svc, quietLog(), nil)
	s.SetAccounts(svc)
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(make([]byte, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	fs := newFakeAlertStore()
	s.SetAlerts(alert.NewManager(fs, alert.ManagerOptions{Keys: kr, Sender: okSender{}}))
	e := &alertEnv{accountEnv: &accountEnv{h: s.srv.Handler, svc: svc, st: st, conn: conn}, store: fs}
	e.owner = e.login(t, "owner@example.com", ownerPassword)
	e.otherB = e.login(t, "other@example.com", ownerPassword)
	e.orgA = decode[orgJSON](t, e.owner.do(http.MethodGet, "/api/v1/orgs/current", nil)).ID
	e.orgB = decode[orgJSON](t, e.otherB.do(http.MethodGet, "/api/v1/orgs/current", nil)).ID
	return e
}

var cpuRule = map[string]any{
	"name": "High CPU", "type": "metric_threshold", "severity": "critical", "interval_seconds": 60,
	"condition": map[string]any{"metric": "system.cpu.utilization", "window_seconds": 60, "operator": "gt", "threshold": 0.9,
		"group_by": []string{"host"}, "filters": []map[string]any{{"field": "attr.cpu.mode", "op": "not_in", "values": []string{"idle"}}}},
}

func TestAlertAPIRoles(t *testing.T) {
	e := newAlertEnv(t)
	admin := e.member(t, "admin@example.com", auth.RoleAdmin)
	member := e.member(t, "member@example.com", auth.RoleMember)
	member2 := e.member(t, "member2@example.com", auth.RoleMember)
	viewer := e.member(t, "viewer@example.com", auth.RoleViewer)
	key := decode[map[string]any](t, e.owner.do(http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "ro"}))["key"].(string)
	apiKey := &client{t: t, h: e.h, bearer: key}

	// Reads: every role and API keys.
	for name, c := range map[string]*client{"viewer": viewer, "member": member, "api key": apiKey} {
		for _, path := range []string{"/api/v1/alerts/rules", "/api/v1/alerts/incidents", "/api/v1/alerts/channels", "/api/v1/alerts/mutes",
			"/api/v1/alerts/deliveries", "/api/v1/alerts/rule-types"} {
			rec := c.do(http.MethodGet, path, nil)
			if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s GET %s: %d %s", name, path, rec.Code, rec.Body)
			}
		}
	}

	// Rule writes: members and up (signed in); viewers and API keys are refused.
	for name, c := range map[string]*client{"viewer": viewer, "api key": apiKey} {
		if rec := c.do(http.MethodPost, "/api/v1/alerts/rules", cpuRule); rec.Code != http.StatusForbidden {
			t.Errorf("%s create rule: %d", name, rec.Code)
		}
	}
	noCSRF := *member
	noCSRF.csrf = ""
	if rec := noCSRF.do(http.MethodPost, "/api/v1/alerts/rules", cpuRule); rec.Code != http.StatusForbidden {
		t.Errorf("create rule without CSRF: %d", rec.Code)
	}
	rec := member.do(http.MethodPost, "/api/v1/alerts/rules", cpuRule)
	if rec.Code != http.StatusCreated {
		t.Fatalf("member create rule: %d %s", rec.Code, rec.Body)
	}
	memberRule := decode[map[string]any](t, rec)
	if memberRule["created_by_email"] != "member@example.com" || memberRule["status"].(map[string]any)["state"] != "unknown" {
		t.Errorf("rule response %v", memberRule)
	}
	adminRule := decode[map[string]any](t, admin.do(http.MethodPost, "/api/v1/alerts/rules", cpuRule))
	memberPath := "/api/v1/alerts/rules/" + memberRule["id"].(string)
	adminPath := "/api/v1/alerts/rules/" + adminRule["id"].(string)

	// Ownership: a member changes only their own rules; admins change any.
	if rec := member.do(http.MethodPost, memberPath+"/disable", nil); rec.Code != http.StatusOK {
		t.Errorf("member disables own rule: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		method, suffix string
		body           any
	}{{http.MethodPut, "", cpuRule}, {http.MethodPost, "/disable", nil}, {http.MethodDelete, "", nil}} {
		if rec := member.do(c.method, adminPath+c.suffix, c.body); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "members can only change") {
			t.Errorf("member %s admin rule%s: %d %s", c.method, c.suffix, rec.Code, rec.Body)
		}
		if rec := member2.do(c.method, memberPath+c.suffix, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("other member %s rule%s: %d", c.method, c.suffix, rec.Code)
		}
	}
	if rec := admin.do(http.MethodPut, memberPath, cpuRule); rec.Code != http.StatusOK {
		t.Errorf("admin updates member rule: %d %s", rec.Code, rec.Body)
	}

	// Channels and test sends: admins only.
	webhook := map[string]any{"name": "ops", "type": "webhook", "secrets": map[string]string{"url": "https://hooks.example.com/x"}}
	if rec := member.do(http.MethodPost, "/api/v1/alerts/channels", webhook); rec.Code != http.StatusForbidden {
		t.Errorf("member create channel: %d", rec.Code)
	}
	rec = admin.do(http.MethodPost, "/api/v1/alerts/channels", webhook)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin create channel: %d %s", rec.Code, rec.Body)
	}
	ch := decode[map[string]any](t, rec)
	if gen := ch["generated_secrets"].(map[string]any)["hmac_secret"].(string); len(gen) != 64 {
		t.Errorf("generated hmac secret %q", gen)
	}
	if strings.Contains(rec.Body.String(), "hooks.example.com/x") {
		t.Error("webhook URL returned in plaintext")
	}
	chPath := "/api/v1/alerts/channels/" + ch["id"].(string)
	got := e.owner.do(http.MethodGet, chPath, nil).Body.String()
	if strings.Contains(got, "hmac_secret\":\"") && !strings.Contains(got, "(set)") || strings.Contains(got, "generated_secrets") {
		t.Errorf("channel read leaks secrets: %s", got)
	}
	if rec := member.do(http.MethodPost, chPath+"/test", nil); rec.Code != http.StatusForbidden {
		t.Errorf("member test send: %d", rec.Code)
	}
	if rec := admin.do(http.MethodPost, chPath+"/test", nil); rec.Code != http.StatusOK || decode[map[string]any](t, rec)["success"] != true || e.store.tests != 1 {
		t.Errorf("admin test send: %d %s", rec.Code, rec.Body)
	}

	// Mutes: members create and change their own.
	mute := map[string]any{"name": "maintenance", "starts_at": "2026-09-13T00:00:00Z", "ends_at": "2026-09-14T00:00:00Z",
		"matchers": []map[string]string{{"label": "host.name", "op": "eq", "value": "web-1"}}}
	adminMute := decode[map[string]any](t, admin.do(http.MethodPost, "/api/v1/alerts/mutes", mute))
	if rec := member.do(http.MethodPost, "/api/v1/alerts/mutes", mute); rec.Code != http.StatusCreated {
		t.Errorf("member create mute: %d %s", rec.Code, rec.Body)
	}
	if rec := member.do(http.MethodDelete, "/api/v1/alerts/mutes/"+adminMute["id"].(string), nil); rec.Code != http.StatusForbidden {
		t.Errorf("member deletes admin mute: %d", rec.Code)
	}
	if rec := viewer.do(http.MethodPost, "/api/v1/alerts/mutes", mute); rec.Code != http.StatusForbidden {
		t.Errorf("viewer create mute: %d", rec.Code)
	}

	// Incidents: members acknowledge, resolve and add notes; viewers cannot.
	incID := uuid.NewString()
	e.store.incidents[incID] = &alert.Incident{ID: incID, OrgID: e.orgA, State: alert.IncidentOpen, RuleName: "High CPU", OpenedAt: time.Now()}
	incPath := "/api/v1/alerts/incidents/" + incID
	if rec := viewer.do(http.MethodPost, incPath+"/acknowledge", nil); rec.Code != http.StatusForbidden {
		t.Errorf("viewer ack: %d", rec.Code)
	}
	if rec := member.do(http.MethodPost, incPath+"/acknowledge", nil); rec.Code != http.StatusOK || decode[map[string]any](t, rec)["state"] != "acknowledged" {
		t.Errorf("member ack: %d %s", rec.Code, rec.Body)
	}
	if rec := member.do(http.MethodPost, incPath+"/notes", map[string]string{"text": "looking"}); rec.Code != http.StatusCreated {
		t.Errorf("member note: %d %s", rec.Code, rec.Body)
	}
	if rec := member.do(http.MethodPost, incPath+"/resolve", map[string]string{"note": "fixed"}); rec.Code != http.StatusOK {
		t.Errorf("member resolve: %d %s", rec.Code, rec.Body)
	}
	if rec := member.do(http.MethodPost, incPath+"/acknowledge", nil); rec.Code != http.StatusConflict {
		t.Errorf("ack of resolved incident: %d", rec.Code)
	}
}

func TestAlertAPITenantIsolationAndValidation(t *testing.T) {
	e := newAlertEnv(t)
	rule := decode[map[string]any](t, e.owner.do(http.MethodPost, "/api/v1/alerts/rules", cpuRule))
	path := "/api/v1/alerts/rules/" + rule["id"].(string)
	// Another organization's owner cannot see or change the rule (indistinguishable from unknown).
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		if rec := e.otherB.do(m, path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("org B %s org A rule: %d", m, rec.Code)
		}
	}
	if rec := e.otherB.do(http.MethodPut, path, cpuRule); rec.Code != http.StatusNotFound {
		t.Errorf("org B PUT org A rule: %d", rec.Code)
	}
	list := decode[map[string][]any](t, e.otherB.do(http.MethodGet, "/api/v1/alerts/rules", nil))
	if len(list["rules"]) != 0 {
		t.Errorf("org B sees %d rules", len(list["rules"]))
	}
	// A forged organization header is rejected by authentication.
	forged := *e.otherB
	forged.org = e.orgA
	if rec := forged.do(http.MethodGet, path, nil); rec.Code != http.StatusForbidden {
		t.Errorf("forged org header: %d", rec.Code)
	}

	bad := []map[string]any{
		{"name": "", "type": "metric_threshold", "condition": map[string]any{"metric": "m", "operator": "gt", "threshold": 1}},
		{"name": "x", "type": "metric_threshold", "condition": map[string]any{"metric": "m", "operator": "gt"}},
		{"name": "x", "type": "metric_threshold", "condition": map[string]any{"metric": "m", "operator": "gt", "threshold": 1,
			"filters": []map[string]any{{"field": "tenant_id", "op": "eq", "values": []string{"tenant-b"}}}}},
	}
	for _, b := range bad {
		if rec := e.owner.do(http.MethodPost, "/api/v1/alerts/rules", b); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_argument") {
			t.Errorf("invalid rule %v: %d %s", b, rec.Code, rec.Body)
		}
	}
	if rec := e.owner.do(http.MethodGet, "/api/v1/alerts/incidents?state=bogus", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad state filter: %d", rec.Code)
	}
}

// Preview reads telemetry through wrap: the tenant comes from the principal and every statement is scoped.
func TestAlertPreviewIsTenantScoped(t *testing.T) {
	e := newAlertEnv(t)
	viewer := e.member(t, "viewer@example.com", auth.RoleViewer)
	e.conn.mu.Lock()
	e.conn.sql = nil
	e.conn.mu.Unlock()
	for _, rule := range []map[string]any{cpuRule,
		{"name": "logs", "type": "log_match", "condition": map[string]any{"query": "error", "operator": "gte", "threshold": 1}},
		{"name": "apm", "type": "apm", "condition": map[string]any{"service_name": "checkout", "metric": "apdex", "operator": "lt", "threshold": 0.8}},
	} {
		body := map[string]any{"rule": rule, "hours": 1}
		rec := viewer.do(http.MethodPost, "/api/v1/alerts/rules/preview", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview %v: %d %s", rule["type"], rec.Code, rec.Body)
		}
		var res map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res["step_seconds"] == nil || res["series"] == nil {
			t.Fatalf("preview response %s", rec.Body)
		}
	}
	e.conn.mu.Lock()
	defer e.conn.mu.Unlock()
	if len(e.conn.sql) < 3 {
		t.Fatalf("only %d statements", len(e.conn.sql))
	}
	for _, sql := range e.conn.sql {
		for _, r := range tableRef.FindAllStringIndex(sql, -1) {
			if !strings.HasPrefix(sql[r[1]:], " WHERE (tenant_id = {tenant_id:String})") {
				t.Errorf("unscoped preview statement: %s", sql)
			}
		}
	}
	if rec := (&client{t: t, h: e.h}).do(http.MethodPost, "/api/v1/alerts/rules/preview", map[string]any{"rule": cpuRule}); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous preview: %d", rec.Code)
	}
}
