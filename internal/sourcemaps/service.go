package sourcemaps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/onuragtas/openlog/internal/objstore"
)

// MaxMapBytes bounds one uploaded document. Maps are the largest thing a user hands this product on
// purpose: a big single-page application's map runs to a few megabytes, and 32 MiB leaves room for one
// without letting an upload endpoint become a storage service.
const MaxMapBytes = 32 << 20

// ErrNotFound is returned when no map is stored for what was asked.
var ErrNotFound = errors.New("source map not found")

// Record is the stored metadata of one map (migrations/postgres/0094_source_maps.sql). The document itself
// lives in object storage under ObjectKey.
type Record struct {
	ID    string
	OrgID string
	App   string
	// Script is the generated file name a stack frame carries, e.g. "main.3f2a1b9c.js".
	Script         string
	Size           int64
	SHA256         []byte
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ObjectKey is where the document is stored. It is built from ids only, so no user-supplied name ever
// becomes part of a storage path (objstore.ValidKey would refuse many of them anyway).
func (r Record) ObjectKey() string { return "sourcemaps/" + r.OrgID + "/" + r.ID }

// MetaStore is the metadata half, implemented by internal/store/postgres.
type MetaStore interface {
	// PutSourceMap inserts or replaces the row for (OrgID, App, Script) and fills ID, CreatedAt, UpdatedAt.
	PutSourceMap(ctx context.Context, r *Record) error
	ListSourceMaps(ctx context.Context, orgID string) ([]Record, error)
	FindSourceMap(ctx context.Context, orgID, app, script string) (Record, error)
	// DeleteSourceMap removes the row and returns it, so the caller can delete the object too.
	DeleteSourceMap(ctx context.Context, orgID, id string) (Record, error)
}

// Service stores and resolves maps. It is the only thing that knows both halves — the index in PostgreSQL
// and the documents in object storage.
type Service struct {
	Meta    MetaStore
	Objects objstore.Store
}

// Upload validates the document and stores it, replacing any map already held for the same script.
//
// The map is parsed before it is stored, not when a stack needs it: a document that cannot be read is a
// mistake the person uploading can still fix, while the same failure at read time is a silent hole in an
// error nobody is looking at yet.
func (s *Service) Upload(ctx context.Context, r *Record, body []byte) error {
	if len(body) == 0 {
		return errors.New("the source map is empty")
	}
	if len(body) > MaxMapBytes {
		return fmt.Errorf("the source map is larger than %d MiB", MaxMapBytes>>20)
	}
	m, err := Parse(body)
	if err != nil {
		return err
	}
	if m.Segments() == 0 {
		return errors.New("the source map has no mappings")
	}
	sum := sha256.Sum256(body)
	r.Size, r.SHA256 = int64(len(body)), sum[:]
	if err := s.Meta.PutSourceMap(ctx, r); err != nil {
		return err
	}
	if err := s.Objects.Put(ctx, r.ObjectKey(), bytes.NewReader(body), r.Size); err != nil {
		// The row would otherwise promise a document that is not there, and the next upload of the same
		// script would reuse the id and look like a replacement of something that never landed.
		_, _ = s.Meta.DeleteSourceMap(ctx, r.OrgID, r.ID)
		return err
	}
	return nil
}

// List returns the maps of an organization, newest first.
func (s *Service) List(ctx context.Context, orgID string) ([]Record, error) {
	return s.Meta.ListSourceMaps(ctx, orgID)
}

// Delete removes the row and its document. A missing object is not an error: the row is the record of
// truth, and leaving it behind because storage already forgot the file would be worse.
func (s *Service) Delete(ctx context.Context, orgID, id string) error {
	rec, err := s.Meta.DeleteSourceMap(ctx, orgID, id)
	if err != nil {
		return err
	}
	if err := s.Objects.Delete(ctx, rec.ObjectKey()); err != nil && !errors.Is(err, objstore.ErrNotFound) {
		return err
	}
	return nil
}

// Load reads and parses one map.
func (s *Service) Load(ctx context.Context, orgID, app, script string) (*Map, error) {
	rec, err := s.Meta.FindSourceMap(ctx, orgID, app, script)
	if err != nil {
		return nil, err
	}
	rc, size, err := s.Objects.Open(ctx, rec.ObjectKey())
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if size > MaxMapBytes {
		return nil, fmt.Errorf("stored source map is %d bytes", size)
	}
	body, err := io.ReadAll(io.LimitReader(rc, MaxMapBytes+1))
	if err != nil {
		return nil, err
	}
	return Parse(body)
}

// Resolver returns a Resolver for one application, reading each script at most once per stack. Errors are
// swallowed into "no map": symbolication is an improvement on a stack, never a reason to fail the request
// that was asking for the error.
func (s *Service) Resolver(ctx context.Context, orgID, app string) Resolver {
	cache := map[string]*Map{}
	return func(script string) *Map {
		if m, ok := cache[script]; ok {
			return m
		}
		m, err := s.Load(ctx, orgID, app, script)
		if err != nil {
			m = nil
		}
		cache[script] = m
		return m
	}
}
