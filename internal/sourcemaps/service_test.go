package sourcemaps

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/objstore"
)

// fakeMeta is the PostgreSQL half in memory, keyed the way the unique index is: (org, app, script).
type fakeMeta struct {
	rows map[string]Record
	next int
}

func newFakeMeta() *fakeMeta { return &fakeMeta{rows: map[string]Record{}} }

func metaKey(org, app, script string) string { return org + "\x00" + app + "\x00" + script }

func (f *fakeMeta) PutSourceMap(_ context.Context, r *Record) error {
	k := metaKey(r.OrgID, r.App, r.Script)
	if old, ok := f.rows[k]; ok {
		r.ID = old.ID // a replacement keeps the id, and with it the object key
	} else {
		f.next++
		r.ID = string(rune('a'+f.next-1)) + "0000000-0000-4000-8000-000000000000"
	}
	f.rows[k] = *r
	return nil
}

func (f *fakeMeta) ListSourceMaps(_ context.Context, org string) ([]Record, error) {
	out := []Record{}
	for _, r := range f.rows {
		if r.OrgID == org {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeMeta) FindSourceMap(_ context.Context, org, app, script string) (Record, error) {
	r, ok := f.rows[metaKey(org, app, script)]
	if !ok {
		return Record{}, ErrNotFound
	}
	return r, nil
}

func (f *fakeMeta) DeleteSourceMap(_ context.Context, org, id string) (Record, error) {
	for k, r := range f.rows {
		if r.OrgID == org && r.ID == id {
			delete(f.rows, k)
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}

func (f *fakeMeta) StaleSourceMaps(_ context.Context, before time.Time, limit int) ([]Record, error) {
	out := []Record{}
	for _, r := range f.rows {
		if r.UpdatedAt.Before(before) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func newService(t *testing.T) (*Service, *fakeMeta) {
	t.Helper()
	meta := newFakeMeta()
	return &Service{Meta: meta, Objects: objstore.Local{Dir: t.TempDir()}}, meta
}

func TestUploadStoresAndResolves(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	rec := &Record{OrgID: "11111111-1111-4111-8111-111111111111", App: "shop-web", Script: "main.3f2a1b9c.js"}
	if err := svc.Upload(ctx, rec, fixture(t)); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if rec.ID == "" || rec.Size == 0 || len(rec.SHA256) != 32 {
		t.Fatalf("record not filled: %+v", rec)
	}

	// The whole point: a stack from that bundle now reads in the developer's own files.
	stack := "TypeError: boom\n    at n (https://shop.example.com/assets/main.3f2a1b9c.js:1:11)"
	out, n := Symbolicate(stack, svc.Resolver(ctx, rec.OrgID, "shop-web"))
	if n != 1 || !strings.Contains(out, "at boom (src/app.ts:5:3)") {
		t.Errorf("symbolicate = %d %q", n, out)
	}
}

func TestUploadRefusesDocumentsThatCannotBeRead(t *testing.T) {
	svc, meta := newService(t)
	ctx := context.Background()
	rec := &Record{OrgID: "o", App: "shop-web", Script: "main.js"}
	for _, body := range [][]byte{
		nil,
		[]byte("not json"),
		[]byte(`{"version":3,"sections":[]}`),
		// Parses, but maps nothing: storing it would answer "there is a map" forever.
		[]byte(`{"version":3,"sources":[],"names":[],"mappings":""}`),
	} {
		if err := svc.Upload(ctx, rec, body); err == nil {
			t.Errorf("accepted an unusable document: %q", body)
		}
	}
	if len(meta.rows) != 0 {
		t.Errorf("a refused upload left %d rows", len(meta.rows))
	}
}

func TestUploadReplacesKeepingTheObjectKey(t *testing.T) {
	svc, meta := newService(t)
	ctx := context.Background()
	org := "11111111-1111-4111-8111-111111111111"
	first := &Record{OrgID: org, App: "shop-web", Script: "main.js"}
	if err := svc.Upload(ctx, first, fixture(t)); err != nil {
		t.Fatal(err)
	}
	second := &Record{OrgID: org, App: "shop-web", Script: "main.js"}
	if err := svc.Upload(ctx, second, fixture(t)); err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Errorf("replacement changed the id (%s -> %s), orphaning the stored document", first.ID, second.ID)
	}
	if len(meta.rows) != 1 {
		t.Errorf("%d rows after a replacement", len(meta.rows))
	}
}

func TestDeleteRemovesRowAndDocument(t *testing.T) {
	svc, meta := newService(t)
	ctx := context.Background()
	org := "11111111-1111-4111-8111-111111111111"
	rec := &Record{OrgID: org, App: "shop-web", Script: "main.js"}
	if err := svc.Upload(ctx, rec, fixture(t)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, org, rec.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(meta.rows) != 0 {
		t.Error("the row survived the delete")
	}
	if _, _, err := svc.Objects.Open(ctx, rec.ObjectKey()); !errors.Is(err, objstore.ErrNotFound) {
		t.Errorf("the document survived the delete: %v", err)
	}
	if err := svc.Delete(ctx, org, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestResolverMissesAreNotErrors(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	// Nothing uploaded at all: symbolication has to be a no-op, never a failure of the error screen.
	out, n := Symbolicate("Error: x\n    at n (https://shop.example.com/main.js:1:1)", svc.Resolver(ctx, "o", "shop-web"))
	if n != 0 || !strings.Contains(out, "main.js:1:1") {
		t.Errorf("resolver miss changed the stack: %d %q", n, out)
	}
}

func TestUploadRollsBackWhenStorageFails(t *testing.T) {
	meta := newFakeMeta()
	// A store whose Put always fails, standing in for a bucket that refuses the write.
	svc := &Service{Meta: meta, Objects: objstore.Local{Dir: "/proc/openlog-not-a-directory"}}
	rec := &Record{OrgID: "o", App: "shop-web", Script: "main.js"}
	if err := svc.Upload(context.Background(), rec, fixture(t)); err == nil {
		t.Fatal("upload reported success without storing the document")
	}
	if len(meta.rows) != 0 {
		t.Error("the row survived a failed upload, promising a document that is not there")
	}
}

// put stores one map with an explicit last-upload time.
func put(t *testing.T, s *Service, meta *fakeMeta, app, script string, updated time.Time) Record {
	t.Helper()
	r := &Record{OrgID: "11111111-1111-4111-8111-111111111111", App: app, Script: script,
		CreatedAt: updated, UpdatedAt: updated}
	if err := s.Upload(context.Background(), r, fixture(t)); err != nil {
		t.Fatalf("upload %s: %v", script, err)
	}
	// Upload does not carry the timestamps through the fake, so set them as the database would.
	k := metaKey(r.OrgID, app, script)
	rec := meta.rows[k]
	rec.CreatedAt, rec.UpdatedAt = updated, updated
	meta.rows[k] = rec
	return rec
}

func TestPruneRemovesStaleMapsAndTheirDocuments(t *testing.T) {
	s, meta := newService(t)
	old := put(t, s, meta, "web", "main.old.js", time.Now().Add(-200*24*time.Hour))
	n, err := s.Prune(context.Background(), time.Now().Add(-90*24*time.Hour), 100)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	if len(meta.rows) != 0 {
		t.Fatalf("row survived: %+v", meta.rows)
	}
	if rc, _, err := s.Objects.Open(context.Background(), old.ObjectKey()); err == nil {
		rc.Close()
		t.Fatal("the document is still in storage")
	} else if !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("unexpected error reading the deleted document: %v", err)
	}
}

func TestPruneLeavesMapsStillBeingDeployed(t *testing.T) {
	s, meta := newService(t)
	put(t, s, meta, "web", "main.fresh.js", time.Now().Add(-3*24*time.Hour))
	n, err := s.Prune(context.Background(), time.Now().Add(-90*24*time.Hour), 100)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 0 || len(meta.rows) != 1 {
		t.Fatalf("pruned %d and left %d rows; a recent map must survive", n, len(meta.rows))
	}
}

func TestPruneReadsTheLastUploadNotTheFirst(t *testing.T) {
	// A bundler that does not hash names re-uploads "main.js" every deploy: the row is old, the upload is not,
	// and deleting it would un-minify nothing while the build is still live.
	s, meta := newService(t)
	r := put(t, s, meta, "web", "main.js", time.Now().Add(-400*24*time.Hour))
	k := metaKey(r.OrgID, "web", "main.js")
	rec := meta.rows[k]
	rec.UpdatedAt = time.Now().Add(-2 * 24 * time.Hour)
	meta.rows[k] = rec

	n, err := s.Prune(context.Background(), time.Now().Add(-90*24*time.Hour), 100)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 0 {
		t.Fatalf("pruned a map that was re-uploaded two days ago")
	}
}

func TestPruneStopsAtItsLimit(t *testing.T) {
	s, meta := newService(t)
	for i := range 5 {
		put(t, s, meta, "web", "main."+string(rune('a'+i))+".js", time.Now().Add(-time.Duration(200+i)*24*time.Hour))
	}
	n, err := s.Prune(context.Background(), time.Now().Add(-90*24*time.Hour), 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 2 || len(meta.rows) != 3 {
		t.Fatalf("pruned %d and left %d rows, want 2 and 3", n, len(meta.rows))
	}
}
