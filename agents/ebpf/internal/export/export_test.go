package export

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type call struct {
	url  string
	hdr  http.Header
	body []byte
}

type fake struct {
	calls   []call
	replies []int
	err     error
}

func (f *fake) Do(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, call{url: r.URL.String(), hdr: r.Header.Clone(), body: b})
	if f.err != nil {
		return nil, f.err
	}
	code := 200
	if len(f.replies) > 0 {
		code = f.replies[0]
		f.replies = f.replies[1:]
	}
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(strings.NewReader("")),
		Header: http.Header{}}, nil
}

func newTest(t *testing.T, f *fake, o Options) *Exporter {
	t.Helper()
	if o.Endpoint == "" {
		o.Endpoint = "https://ingest.example.com:4318"
	}
	o.Client = f
	o.Sleep = func(time.Duration) {} // a backoff must cost no wall clock in a test
	if o.Elapsed == 0 {
		o.Elapsed = time.Minute
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPostsToTheProfilesEndpointWithTheKey(t *testing.T) {
	f := &fake{}
	e := newTest(t, f, Options{Headers: map[string]string{"openlog-license-key": "olk_test"}})
	if err := e.Send(context.Background(), []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("%d requests", len(f.calls))
	}
	c := f.calls[0]
	if c.url != "https://ingest.example.com:4318/v1/profiles" {
		t.Errorf("url = %s", c.url)
	}
	if got := c.hdr.Get("Content-Type"); got != "application/x-protobuf" {
		t.Errorf("content-type = %q", got)
	}
	if got := c.hdr.Get("openlog-license-key"); got != "olk_test" {
		t.Errorf("key header = %q", got)
	}
	if string(c.body) != "payload" {
		t.Errorf("body = %q", c.body)
	}
}

// A trailing slash on the endpoint must not produce //v1/profiles: the ingest would answer 404 and the
// failure would look like a misconfigured server rather than a misconfigured agent.
func TestEndpointPathIsJoinedCleanly(t *testing.T) {
	f := &fake{}
	e := newTest(t, f, Options{Endpoint: "https://ingest.example.com:4318/"})
	_ = e.Send(context.Background(), []byte("x"))
	if got := f.calls[0].url; got != "https://ingest.example.com:4318/v1/profiles" {
		t.Errorf("url = %s", got)
	}
}

func TestGzipBodyIsReadableAndDeclared(t *testing.T) {
	f := &fake{}
	e := newTest(t, f, Options{Gzip: true})
	if err := e.Send(context.Background(), []byte("hello profiles")); err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	if got := c.hdr.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("content-encoding = %q", got)
	}
	// A body that says it is gzip and is not would be rejected as a decode failure.
	zr, err := gzip.NewReader(strings.NewReader(string(c.body)))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != "hello profiles" {
		t.Errorf("decompressed to %q", got)
	}
}

func TestRetriesTheStatusesTheContractMarksRetryable(t *testing.T) {
	f := &fake{replies: []int{503, 429, 200}}
	e := newTest(t, f, Options{})
	if err := e.Send(context.Background(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 {
		t.Errorf("made %d attempts, want 3", len(f.calls))
	}
}

// 401 and 413 will not differ next time, so retrying them only delays the next window's profile.
func TestDoesNotRetryWhatCannotSucceed(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusRequestEntityTooLarge, http.StatusBadRequest} {
		f := &fake{replies: []int{code}}
		e := newTest(t, f, Options{})
		if err := e.Send(context.Background(), []byte("x")); err == nil {
			t.Errorf("status %d reported success", code)
		}
		if len(f.calls) != 1 {
			t.Errorf("status %d was attempted %d times, want 1", code, len(f.calls))
		}
	}
}

// The budget is what stops a dead ingest from pushing out the next profile for ever.
func TestRetriesAreBounded(t *testing.T) {
	f := &fake{}
	for i := 0; i < 50; i++ {
		f.replies = append(f.replies, 503)
	}
	e := newTest(t, f, Options{Elapsed: 5 * time.Second})
	var fakeNow = time.Now()
	e.now = func() time.Time { fakeNow = fakeNow.Add(2 * time.Second); return fakeNow }
	if err := e.Send(context.Background(), []byte("x")); err == nil {
		t.Fatal("a permanently failing export reported success")
	}
	if len(f.calls) >= 50 {
		t.Errorf("attempted %d times: the budget did not stop it", len(f.calls))
	}
}

func TestTransportErrorIsRetriedThenReported(t *testing.T) {
	f := &fake{err: errors.New("connection refused")}
	e := newTest(t, f, Options{Elapsed: time.Second})
	var fakeNow = time.Now()
	e.now = func() time.Time { fakeNow = fakeNow.Add(time.Second); return fakeNow }
	if err := e.Send(context.Background(), []byte("x")); err == nil {
		t.Fatal("a refused connection reported success")
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	var waited []time.Duration
	f := &fake{}
	e := newTest(t, f, Options{})
	e.sleep = func(d time.Duration) { waited = append(waited, d) }
	e.client = &retryAfterOnce{inner: f}
	if err := e.Send(context.Background(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(waited) != 1 || waited[0] != 7*time.Second {
		t.Errorf("waited %v, want the server's 7s", waited)
	}
}

type retryAfterOnce struct {
	inner *fake
	sent  bool
}

func (r *retryAfterOnce) Do(req *http.Request) (*http.Response, error) {
	if !r.sent {
		r.sent = true
		_, _ = r.inner.Do(req)
		h := http.Header{}
		h.Set("Retry-After", "7")
		return &http.Response{StatusCode: 503, Status: "503", Body: io.NopCloser(strings.NewReader("")), Header: h}, nil
	}
	return r.inner.Do(req)
}
