package symbols

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/onuragtas/openlog/internal/objstore"
)

// Kinds of a symbol file. The key is what a stack is matched by: the version for a mapping, which a Java stack cannot otherwise
// be tied to, and the image id for native symbols, which is exact.
const (
	KindR8     = "r8"
	KindNative = "native"
)

// MaxUploadBytes bounds one compressed upload.
const MaxUploadBytes = 128 << 20

// ErrNotFound is returned when nothing is stored for what was asked.
var ErrNotFound = errors.New("symbol file not found")

// Record is the stored metadata of one file (0106_crash_symbols). The file itself lives in object storage.
type Record struct {
	ID        string
	OrgID     string
	App       string
	Kind      string
	Key       string
	Size      int64
	SHA256    []byte
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ObjectKey is built from ids only, so nothing a user typed becomes part of a storage path.
func (r Record) ObjectKey() string { return "symbols/" + r.OrgID + "/" + r.ID }

// MetaStore is the index, implemented by internal/store/postgres.
type MetaStore interface {
	// PutSymbolFile inserts or replaces the row for (OrgID, App, Kind, Key) and fills ID and CreatedAt.
	PutSymbolFile(ctx context.Context, r *Record) error
	ListSymbolFiles(ctx context.Context, orgID string) ([]Record, error)
	FindSymbolFile(ctx context.Context, orgID, app, kind, key string) (Record, error)
	DeleteSymbolFile(ctx context.Context, orgID, id string) (Record, error)
}

// Service stores files and hands out parsed ones.
type Service struct {
	Meta    MetaStore
	Objects objstore.Store

	mu    sync.Mutex
	cache map[string]*cacheEntry
	order []string
}

// maxCached bounds the parsed files held in memory. The crash screen reads a handful of builds, and each can be large.
const maxCached = 16

// cacheTTL is how late a replaced file can be noticed by another API process, which has no other way to hear of it.
const cacheTTL = 10 * time.Minute

type cacheEntry struct {
	table   *Table
	mapping *Mapping
	loaded  time.Time
}

// Validate parses a body the way a later read will. A file that cannot be read is a mistake the uploader can still fix, while
// the same failure at read time is a silent hole in a crash nobody is looking at yet.
func Validate(kind string, body []byte) error {
	switch kind {
	case KindR8:
		_, err := ParseMapping(body)
		return err
	case KindNative:
		t, err := ParseTable(body)
		if err != nil {
			return err
		}
		if t.Functions() == 0 {
			return errors.New("symbols: the table has no functions: the binary may be stripped")
		}
		return nil
	}
	return fmt.Errorf("symbols: unknown kind %q", kind)
}

// Upload stores a file, replacing any held for the same key.
func (s *Service) Upload(ctx context.Context, r *Record, body []byte) error {
	if err := Validate(r.Kind, body); err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	r.Size, r.SHA256 = int64(len(body)), sum[:]
	if err := s.Meta.PutSymbolFile(ctx, r); err != nil {
		return err
	}
	if err := s.Objects.Put(ctx, r.ObjectKey(), bytes.NewReader(body), r.Size); err != nil {
		// Only a row this call created: when replacing, the earlier file is still there and still right.
		if r.CreatedAt.Equal(r.UpdatedAt) {
			_, _ = s.Meta.DeleteSymbolFile(ctx, r.OrgID, r.ID)
		}
		return err
	}
	s.mu.Lock()
	delete(s.cache, s.cacheKey(r.OrgID, r.App, r.Kind, r.Key))
	s.mu.Unlock()
	return nil
}

func (s *Service) List(ctx context.Context, orgID string) ([]Record, error) {
	return s.Meta.ListSymbolFiles(ctx, orgID)
}

// Delete removes the row and its file. A missing object is not an error: the row is the record of truth.
func (s *Service) Delete(ctx context.Context, orgID, id string) error {
	rec, err := s.Meta.DeleteSymbolFile(ctx, orgID, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, s.cacheKey(rec.OrgID, rec.App, rec.Kind, rec.Key))
	s.mu.Unlock()
	if err := s.Objects.Delete(ctx, rec.ObjectKey()); err != nil && !errors.Is(err, objstore.ErrNotFound) {
		return err
	}
	return nil
}

func (s *Service) cacheKey(orgID, app, kind, key string) string {
	return orgID + "\x00" + app + "\x00" + kind + "\x00" + key
}

func (s *Service) load(ctx context.Context, orgID, app, kind, key string) *cacheEntry {
	ck := s.cacheKey(orgID, app, kind, key)
	s.mu.Lock()
	e := s.cache[ck]
	s.mu.Unlock()
	if e != nil && time.Since(e.loaded) < cacheTTL {
		return e
	}
	rec, err := s.Meta.FindSymbolFile(ctx, orgID, app, kind, key)
	if err != nil {
		return nil
	}
	rc, _, err := s.Objects.Open(ctx, rec.ObjectKey())
	if err != nil {
		return nil
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, MaxUploadBytes+1))
	if err != nil {
		return nil
	}
	e = &cacheEntry{loaded: time.Now()}
	switch kind {
	case KindR8:
		if e.mapping, err = ParseMapping(body); err != nil {
			return nil
		}
	case KindNative:
		if e.table, err = ParseTable(body); err != nil {
			return nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[string]*cacheEntry{}
	}
	if len(s.cache) >= maxCached {
		for len(s.order) > 0 && len(s.cache) >= maxCached {
			delete(s.cache, s.order[0])
			s.order = s.order[1:]
		}
	}
	if _, ok := s.cache[ck]; !ok {
		s.order = append(s.order, ck)
	}
	s.cache[ck] = e
	return e
}

// Resolver reads what one application and version uploaded, and remembers it for the stacks of one request: a list of crashes
// asks about the same few images over and over. Absence is remembered too, or each frame would be a query.
func (s *Service) Resolver(ctx context.Context, orgID, app, version string) Resolver {
	images := map[string]*Table{}
	var mapping *Mapping
	mappingAsked := false
	return Resolver{
		Mapping: func() *Mapping {
			if !mappingAsked {
				mappingAsked = true
				if e := s.load(ctx, orgID, app, KindR8, version); e != nil {
					mapping = e.mapping
				}
			}
			return mapping
		},
		Image: func(id string) *Table {
			if t, ok := images[id]; ok {
				return t
			}
			var t *Table
			if e := s.load(ctx, orgID, app, KindNative, id); e != nil {
				t = e.table
			}
			images[id] = t
			return t
		},
	}
}
