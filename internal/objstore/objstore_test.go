package objstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// AWS Signature Version 4 example "GET Object" (docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html).
func TestSignV4AWSExample(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	req.Header.Set("X-Amz-Content-Sha256", emptyHash)
	SignV4(req, emptyHash, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1",
		time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization\n got %s\nwant %s", got, want)
	}
}

func TestValidKey(t *testing.T) {
	for key, ok := range map[string]bool{
		"exports/org/abc.zip": true, "a": true,
		"": false, "/abs": false, "../x": false, "a/../b": false, "a//b": false, "a/": false, "a b": false,
	} {
		if ValidKey(key) != ok {
			t.Errorf("ValidKey(%q) = %v", key, !ok)
		}
	}
}

func exercise(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	body := []byte("PK\x03\x04 archive bytes")
	if err := s.Put(ctx, "exports/o1/e1.zip", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	rc, n, err := s.Open(ctx, "exports/o1/e1.zip")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, body) || n != int64(len(body)) {
		t.Fatalf("read %q (%d)", got, n)
	}
	if err := s.Delete(ctx, "exports/o1/e1.zip"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "exports/o1/e1.zip"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if _, _, err := s.Open(ctx, "exports/o1/e1.zip"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("open deleted: %v", err)
	}
	if err := s.Put(ctx, "../escape", bytes.NewReader(body), 1); err == nil {
		t.Fatal("unsafe key accepted")
	}
}

func TestLocal(t *testing.T) {
	exercise(t, Local{Dir: t.TempDir()})
}

// fakeS3 is a path-style bucket that requires a signed request.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=ak/") || r.Header.Get("X-Amz-Content-Sha256") == "" {
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[r.URL.Path] = b
	case http.MethodGet:
		b, ok := f.objects[r.URL.Path]
		if !ok {
			http.Error(w, "NoSuchKey", http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		delete(f.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestS3(t *testing.T) {
	fake := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := &S3{BaseURL: srv.URL + "/bucket/openlog-exports/", Region: "eu-west-1", AccessKeyID: "ak", SecretAccessKey: "sk", Client: srv.Client()}
	ctx := context.Background()
	if err := s.Put(ctx, "k.zip", bytes.NewReader([]byte("x")), 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.objects["/bucket/openlog-exports/k.zip"]; !ok {
		t.Fatalf("objects: %v", fake.objects)
	}
	exercise(t, s)
}
