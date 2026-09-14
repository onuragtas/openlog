// Package objstore stores data export archives in a local directory or an S3-compatible bucket (AWS S3, MinIO, …;
// path-style or virtual-hosted base URLs, AWS Signature Version 4 with static credentials). It has no dependency on
// an SDK: exports need only PUT, GET and DELETE of single objects (docs/operations/saas.md "Data subject requests").
package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// ErrNotFound is returned by Open for a missing object.
var ErrNotFound = errors.New("object not found")

// Store is an object store.
type Store interface {
	// Kind is "local" or "s3".
	Kind() string
	// Put stores size bytes of r under key (replacing an existing object).
	Put(ctx context.Context, key string, r io.ReadSeeker, size int64) error
	// Open returns the object and its size; ErrNotFound when it does not exist.
	Open(ctx context.Context, key string) (io.ReadCloser, int64, error)
	// Delete removes the object; a missing object is not an error.
	Delete(ctx context.Context, key string) error
}

var keyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-/]{0,510}$`)

// ValidKey reports whether key is a safe relative object key (no "..", no empty segments).
func ValidKey(key string) bool {
	if !keyRe.MatchString(key) || strings.HasSuffix(key, "/") {
		return false
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func checkKey(key string) error {
	if !ValidKey(key) {
		return fmt.Errorf("invalid object key %q", key)
	}
	return nil
}
