package savedview

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

// memStore is an in-memory Store.
type memStore struct{ views map[string]*View }

func (s *memStore) List(_ context.Context, orgID, viewerID string, admin bool, signal string) ([]View, error) {
	out := []View{}
	for _, v := range s.views {
		if v.OrgID == orgID && (signal == "" || v.Signal == signal) &&
			(v.Visibility == "org" || (v.CreatedBy != "" && v.CreatedBy == viewerID) || (v.CreatedBy == "" && admin)) {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *memStore) Get(_ context.Context, orgID, id string) (*View, error) {
	v, ok := s.views[id]
	if !ok || v.OrgID != orgID {
		return nil, ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (s *memStore) Create(_ context.Context, v *View, maxPerOrg int) error {
	n := 0
	for _, x := range s.views {
		if x.OrgID == v.OrgID {
			n++
		}
	}
	if n >= maxPerOrg {
		return ErrLimit
	}
	v.CreatedAt, v.UpdatedAt = time.Now(), time.Now()
	cp := *v
	s.views[v.ID] = &cp
	return nil
}

func (s *memStore) Update(_ context.Context, v *View) error {
	if _, ok := s.views[v.ID]; !ok {
		return ErrNotFound
	}
	cp := *v
	s.views[v.ID] = &cp
	return nil
}

func (s *memStore) Delete(_ context.Context, orgID, id string) error {
	if v, ok := s.views[id]; !ok || v.OrgID != orgID {
		return ErrNotFound
	}
	delete(s.views, id)
	return nil
}

const org = "00000000-0000-4000-8000-000000000001"

var (
	alice  = Viewer{UserID: "alice", CanWrite: true}
	bob    = Viewer{UserID: "bob", CanWrite: true}
	admin  = Viewer{UserID: "carol", CanWrite: true, Admin: true}
	viewer = Viewer{UserID: "dave"}
	apiKey = Viewer{Admin: false}
)

func input(name, visibility string) Input {
	return Input{Signal: "logs", Name: name, Visibility: visibility, State: json.RawMessage(`{ "filters": [ {"key":"service.name","op":"=","value":"api"} ], "columns": ["body"] }`)}
}

func TestPermissions(t *testing.T) {
	ctx := context.Background()
	m := NewManager(&memStore{views: map[string]*View{}})
	private, err := m.Create(ctx, org, alice, input("  mine  ", "private"))
	if err != nil {
		t.Fatal(err)
	}
	if private.Name != "mine" || string(private.State) != `{"filters":[{"key":"service.name","op":"=","value":"api"}],"columns":["body"]}` {
		t.Errorf("stored %q %s", private.Name, private.State)
	}
	shared, err := m.Create(ctx, org, alice, input("shared", "org"))
	if err != nil {
		t.Fatal(err)
	}
	// Private views are invisible to everyone but the creator (admins included while the creator exists).
	for _, v := range []Viewer{bob, admin, viewer, apiKey} {
		if _, err := m.Get(ctx, org, private.ID, v); !errors.Is(err, ErrNotFound) {
			t.Errorf("%+v read a private view: %v", v, err)
		}
		if list, _ := m.List(ctx, org, v, "logs"); len(list) != 1 || list[0].ID != shared.ID {
			t.Errorf("%+v list %+v", v, list)
		}
	}
	if list, _ := m.List(ctx, org, alice, ""); len(list) != 2 {
		t.Errorf("creator list %+v", list)
	}
	if list, _ := m.List(ctx, org, alice, "metrics"); len(list) != 0 {
		t.Errorf("signal filter %+v", list)
	}
	if _, err := m.List(ctx, org, alice, "events"); err == nil {
		t.Error("invalid signal accepted")
	}
	// Org-wide views: readable by all, editable by the creator and admins only.
	if _, err := m.Update(ctx, org, shared.ID, bob, input("x", "org")); !errors.Is(err, ErrForbidden) {
		t.Errorf("member edited another member's view: %v", err)
	}
	if _, err := m.Update(ctx, org, shared.ID, viewer, input("x", "org")); !errors.Is(err, ErrForbidden) {
		t.Errorf("viewer edited: %v", err)
	}
	updated, err := m.Update(ctx, org, shared.ID, admin, input("renamed", "org"))
	if err != nil || updated.Name != "renamed" || updated.CreatedBy != "alice" {
		t.Errorf("admin update: %+v %v", updated, err)
	}
	if !admin.CanEdit(updated) || bob.CanEdit(updated) || !alice.CanEdit(updated) {
		t.Error("CanEdit")
	}
	if _, err := m.Create(ctx, org, viewer, input("v", "org")); !errors.Is(err, ErrForbidden) {
		t.Errorf("viewer created: %v", err)
	}
	if _, err := m.Delete(ctx, org, private.ID, admin); !errors.Is(err, ErrNotFound) {
		t.Errorf("admin deleted a private view: %v", err)
	}
	if _, err := m.Delete(ctx, org, private.ID, alice); err != nil {
		t.Errorf("creator delete: %v", err)
	}
	if _, err := m.Get(ctx, org, "not-a-uuid", alice); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
	// Private views of deleted users are visible to admins.
	orphan := &View{ID: "00000000-0000-4000-8000-0000000000aa", OrgID: org, Signal: "logs", Name: "o", Visibility: "private", State: json.RawMessage(`{}`)}
	if !admin.CanRead(orphan) || !admin.CanEdit(orphan) || bob.CanRead(orphan) {
		t.Error("orphaned private view permissions")
	}
}

func TestValidation(t *testing.T) {
	ctx := context.Background()
	m := NewManager(&memStore{views: map[string]*View{}})
	bad := []Input{
		{Signal: "events", Name: "a", Visibility: "org", State: json.RawMessage(`{}`)},
		{Signal: "logs", Name: " ", Visibility: "org", State: json.RawMessage(`{}`)},
		{Signal: "logs", Name: strings.Repeat("ş", MaxNameRunes+1), Visibility: "org", State: json.RawMessage(`{}`)},
		{Signal: "logs", Name: "a", Description: strings.Repeat("d", MaxDescriptionRunes+1), Visibility: "org", State: json.RawMessage(`{}`)},
		{Signal: "logs", Name: "a", Visibility: "public", State: json.RawMessage(`{}`)},
		{Signal: "logs", Name: "a", Visibility: "org", State: json.RawMessage(`[]`)},
		{Signal: "logs", Name: "a", Visibility: "org"},
		{Signal: "logs", Name: "a", Visibility: "org", State: json.RawMessage(`{"a":` + strings.Repeat(`"x"`, 1) + strings.Repeat(" ", MaxStateBytes) + `}`)},
	}
	for _, in := range bad {
		var ve *ValidationError
		if _, err := m.Create(ctx, org, alice, in); !errors.As(err, &ve) {
			t.Errorf("%+v: %v", in.Name, err)
		}
	}
	if _, err := m.Create(ctx, org, alice, Input{Signal: "metrics", Name: strings.Repeat("ş", MaxNameRunes), Visibility: "org", State: json.RawMessage(`{}`)}); err != nil {
		t.Errorf("200-character name: %v", err)
	}
}

func TestLimit(t *testing.T) {
	ctx := context.Background()
	st := &memStore{views: map[string]*View{}}
	m := NewManager(st)
	for i := 0; i < MaxPerOrg; i++ {
		id := "00000000-0000-4000-8000-" + strings.Repeat("0", 8) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676%26)) + "0"
		st.views[id] = &View{ID: id, OrgID: org}
	}
	if _, err := m.Create(ctx, org, alice, input("one too many", "private")); !errors.Is(err, ErrLimit) {
		t.Errorf("limit: %v", err)
	}
}
