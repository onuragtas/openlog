package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/cloudconnect"
	"github.com/onuragtas/openlog/internal/cloudconnect/cloudconnecttest"
	"github.com/onuragtas/openlog/internal/config"
)

// The cloud connection endpoints: the CRUD shapes, that a credential never comes back out, the test action
// and the role that each route needs.

const cloudPath = "/api/v1/cloud/connections"

// decodeBody unmarshals a response body into v (decode[T] cannot infer an anonymous struct).
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
}

// fakeCloudTester stands in for the collector's provider check.
type fakeCloudTester struct {
	err  error
	last struct {
		provider string
		scope    string
		creds    cloudconnect.Credentials
	}
}

func (f *fakeCloudTester) Test(_ context.Context, provider string, creds cloudconnect.Credentials, scope string) error {
	f.last.provider, f.last.scope, f.last.creds = provider, scope, creds
	return f.err
}

type cloudEnv struct {
	h      http.Handler
	store  *cloudconnecttest.MemStore
	tester *fakeCloudTester
	owner  *client
}

func newCloudEnv(t *testing.T) *cloudEnv {
	t.Helper()
	st := memstore.New()
	svc := auth.NewService(st, auth.Config{CookieSecure: true, LoginMaxFailures: 5}, quietLog())
	if _, err := svc.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-a", OrgName: "Org A",
		OwnerEmail: "owner@example.com", OwnerPassword: ownerPassword}); err != nil {
		t.Fatal(err)
	}
	conn := &recordingConn{}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), svc, quietLog(), nil)
	s.SetAccounts(svc)
	store := cloudconnecttest.New()
	tester := &fakeCloudTester{}
	s.SetCloudConnect(cloudconnect.NewManager(store, cloudconnect.ManagerOptions{
		Keys: apiKeyring(t), Tester: tester}))

	e := &cloudEnv{h: s.srv.Handler, store: store, tester: tester}
	c := &client{t: t, h: e.h}
	rec := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "owner@example.com", "password": ownerPassword})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.DefaultCookieName {
			c.cookie = &http.Cookie{Name: ck.Name, Value: ck.Value}
		}
	}
	me := decode[meJSON](t, rec)
	if me.CSRFToken == nil {
		t.Fatal("login response without csrf_token")
	}
	c.csrf = *me.CSRFToken
	e.owner = c
	return e
}

const cloudSecret = "top-secret-access-key"

func validCloudBody() map[string]any {
	return map[string]any{
		"name": "Prod AWS", "provider": "aws",
		"scopes": []string{"eu-central-1"}, "services": []string{"rds", "lambda"},
		"credentials": map[string]any{"access_key_id": "AKIDEXAMPLE", "secret_access_key": cloudSecret},
	}
}

func TestCloudConnectionCRUD(t *testing.T) {
	e := newCloudEnv(t)

	rec := e.owner.do(http.MethodPost, cloudPath, validCloudBody())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	// The credential must not travel back, in plaintext or as a ciphertext.
	if body := rec.Body.String(); strings.Contains(body, cloudSecret) || strings.Contains(body, "ol1:") {
		t.Fatalf("the response carries a credential: %s", body)
	}
	created := decode[cloudConnectionJSON](t, rec)
	if !created.CredentialsSet || created.CredentialsKeyID == "" {
		t.Errorf("credentials_set/key id = %v/%q", created.CredentialsSet, created.CredentialsKeyID)
	}
	if created.IngestMode != "poll" || created.PollIntervalSeconds != 300 || created.MaxMetricsPerPoll != 5000 {
		t.Errorf("defaults not applied: %+v", created)
	}
	if len(created.Status) != 1 || created.Status[0].Scope != "eu-central-1" {
		t.Errorf("schedule rows = %+v", created.Status)
	}
	// What was stored is a ciphertext, not the secret.
	if stored := e.store.StoredCredentials(created.ID); stored == "" || strings.Contains(stored, cloudSecret) {
		t.Errorf("stored credentials = %q", stored)
	}

	rec = e.owner.do(http.MethodGet, cloudPath, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), cloudSecret) {
		t.Fatal("the list carries a credential")
	}

	rec = e.owner.do(http.MethodGet, cloudPath+"/"+created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}

	// An update without credentials keeps the stored ones.
	body := validCloudBody()
	delete(body, "credentials")
	body["name"] = "Prod AWS (eu)"
	rec = e.owner.do(http.MethodPut, cloudPath+"/"+created.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	updated := decode[cloudConnectionJSON](t, rec)
	if updated.Name != "Prod AWS (eu)" || !updated.CredentialsSet {
		t.Errorf("update lost the credentials: %+v", updated)
	}

	if rec := e.owner.do(http.MethodDelete, cloudPath+"/"+created.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodGet, cloudPath+"/"+created.ID, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", rec.Code)
	}
}

func TestCloudConnectionValidation(t *testing.T) {
	e := newCloudEnv(t)
	cases := []struct {
		name string
		mut  func(map[string]any)
	}{
		{"unknown provider", func(b map[string]any) { b["provider"] = "oracle" }},
		{"no scopes", func(b map[string]any) { b["scopes"] = []string{} }},
		{"unknown service", func(b map[string]any) { b["services"] = []string{"bigquery"} }},
		{"interval too short", func(b map[string]any) { b["poll_interval_seconds"] = 10 }},
		{"credentials of another provider", func(b map[string]any) {
			b["credentials"] = map[string]any{"tenant_id": "t", "client_id": "c", "client_secret": "s"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := validCloudBody()
			tc.mut(body)
			rec := e.owner.do(http.MethodPost, cloudPath, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("create = %d %s, want 400", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), "invalid_argument") {
				t.Errorf("body = %s", rec.Body)
			}
		})
	}
}

func TestCloudConnectionTestAction(t *testing.T) {
	e := newCloudEnv(t)

	// With inline credentials, before anything is saved.
	rec := e.owner.do(http.MethodPost, cloudPath+"/test", map[string]any{
		"provider": "aws", "scope": "eu-central-1",
		"credentials": map[string]any{"access_key_id": "AKIDEXAMPLE", "secret_access_key": cloudSecret}})
	if rec.Code != http.StatusOK {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	got := decode[map[string]any](t, rec)
	if got["ok"] != true || got["error"] != "" {
		t.Errorf("test result = %v", got)
	}
	if e.tester.last.provider != "aws" || e.tester.last.scope != "eu-central-1" ||
		e.tester.last.creds.SecretAccessKey != cloudSecret {
		t.Errorf("the tester was called with %+v", e.tester.last)
	}

	// A rejected credential is a normal outcome: 200 with ok=false, so the form can show it inline.
	e.tester.err = errors.New("the credentials were rejected")
	rec = e.owner.do(http.MethodPost, cloudPath+"/test", map[string]any{
		"provider": "aws", "scope": "eu-central-1",
		"credentials": map[string]any{"access_key_id": "A", "secret_access_key": "S"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("failing test: %d %s", rec.Code, rec.Body)
	}
	got = decode[map[string]any](t, rec)
	if got["ok"] != false || !strings.Contains(got["error"].(string), "rejected") {
		t.Errorf("test result = %v", got)
	}

	// A saved connection can be tested without re-sending the secret.
	e.tester.err = nil
	rec = e.owner.do(http.MethodPost, cloudPath, validCloudBody())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode[cloudConnectionJSON](t, rec)
	e.tester.last.creds = cloudconnect.Credentials{}
	rec = e.owner.do(http.MethodPost, cloudPath+"/test", map[string]any{"connection_id": created.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("test of a stored connection: %d %s", rec.Code, rec.Body)
	}
	if e.tester.last.creds.SecretAccessKey != cloudSecret {
		t.Errorf("the stored credentials were not decrypted for the test: %+v", e.tester.last.creds)
	}
	if e.tester.last.scope != "eu-central-1" {
		t.Errorf("scope = %q, want the connection's first scope", e.tester.last.scope)
	}

	// An unknown connection is a 404, not an ok=false answer.
	if rec := e.owner.do(http.MethodPost, cloudPath+"/test",
		map[string]any{"connection_id": "53000000-0000-4000-8000-0000000000ff"}); rec.Code != http.StatusNotFound {
		t.Fatalf("test of an unknown connection: %d %s", rec.Code, rec.Body)
	}
}

func TestCloudConnectionRuns(t *testing.T) {
	e := newCloudEnv(t)
	rec := e.owner.do(http.MethodPost, cloudPath, validCloudBody())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode[cloudConnectionJSON](t, rec)
	e.store.AddRun(created.ID, cloudconnect.Run{ID: 1, Scope: "eu-central-1", StartedAt: time.Now().UTC(),
		Status: cloudconnect.StatusPartial, Metrics: 120, APICalls: 4, Throttled: 1,
		Error:    "rds: 1 of 3 resources could not be read",
		Services: []cloudconnect.ServiceRun{{Service: "rds", Metrics: 120, Error: "1 of 3 resources could not be read"}}})

	rec = e.owner.do(http.MethodGet, cloudPath+"/"+created.ID+"/runs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("runs: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Connection cloudConnectionJSON `json:"connection"`
		Runs       []cloudRunJSON      `json:"runs"`
	}
	decodeBody(t, rec, &out)
	if len(out.Runs) != 1 {
		t.Fatalf("runs = %+v", out.Runs)
	}
	r := out.Runs[0]
	if r.Status != cloudconnect.StatusPartial || r.Metrics != 120 || r.APICalls != 4 || r.Throttled != 1 {
		t.Errorf("run = %+v", r)
	}
	if len(r.Services) != 1 || r.Services[0].Service != "rds" || r.Services[0].Error == "" {
		t.Errorf("per-service outcome = %+v", r.Services)
	}

	if rec := e.owner.do(http.MethodGet, cloudPath+"/"+created.ID+"/runs?limit=0", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("limit=0: %d", rec.Code)
	}
}

func TestCloudProviderCatalog(t *testing.T) {
	e := newCloudEnv(t)
	rec := e.owner.do(http.MethodGet, "/api/v1/cloud/providers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("providers: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Providers []struct {
			ID          string `json:"id"`
			ScopeLabel  string `json:"scope_label"`
			Credentials []struct {
				Key      string `json:"key"`
				Required bool   `json:"required"`
				Secret   bool   `json:"secret"`
			} `json:"credentials"`
			Services []struct {
				ID string `json:"id"`
			} `json:"services"`
		} `json:"providers"`
		SecretsConfigured bool `json:"secrets_configured"`
		TestSupported     bool `json:"test_supported"`
	}
	decodeBody(t, rec, &out)
	if len(out.Providers) != 3 {
		t.Fatalf("providers = %d, want aws, azure and gcp", len(out.Providers))
	}
	if !out.SecretsConfigured || !out.TestSupported {
		t.Errorf("secrets_configured/test_supported = %v/%v", out.SecretsConfigured, out.TestSupported)
	}
	byID := map[string]int{}
	for i, p := range out.Providers {
		byID[p.ID] = i
	}
	aws := out.Providers[byID["aws"]]
	if aws.ScopeLabel != "region" || len(aws.Services) == 0 {
		t.Errorf("aws = %+v", aws)
	}
	// The secret fields are marked, so the form renders them as password inputs and never prefills them.
	var secretMarked bool
	for _, f := range aws.Credentials {
		if f.Key == "secret_access_key" {
			secretMarked = f.Secret && f.Required
		}
	}
	if !secretMarked {
		t.Errorf("secret_access_key is not marked required and secret: %+v", aws.Credentials)
	}
	if out.Providers[byID["azure"]].ScopeLabel != "subscription" || out.Providers[byID["gcp"]].ScopeLabel != "project" {
		t.Errorf("scope labels = %q/%q", out.Providers[byID["azure"]].ScopeLabel, out.Providers[byID["gcp"]].ScopeLabel)
	}
}

// Reading a connection is reading configuration; changing one stores cloud credentials, so it needs an admin.
func TestCloudConnectionRoles(t *testing.T) {
	for _, tc := range []struct {
		role      auth.Role
		canRead   bool
		canManage bool
	}{
		{auth.RoleViewer, true, false},
		{auth.RoleMember, true, false},
		{auth.RoleAdmin, true, true},
		{auth.RoleOwner, true, true},
	} {
		if got := tc.role.Can(auth.ActReadCloudConnections); got != tc.canRead {
			t.Errorf("%s may read = %v, want %v", tc.role, got, tc.canRead)
		}
		if got := tc.role.Can(auth.ActManageCloudConnections); got != tc.canManage {
			t.Errorf("%s may manage = %v, want %v", tc.role, got, tc.canManage)
		}
	}
}
