package renderer

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
)

const sharedToken = "renderer-shared-secret-0123456789abcdef"

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type fakeBrowser struct {
	mu      sync.Mutex
	urls    []string
	res     *Response
	err     error
	block   chan struct{}
	started chan struct{}
}

func (f *fakeBrowser) Render(ctx context.Context, pageURL string, _ Request) (*Response, error) {
	f.mu.Lock()
	f.urls = append(f.urls, pageURL)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.res, f.err
}

func validToken(t *testing.T) string {
	t.Helper()
	tok, err := NewReportToken(KeyFromSecret("server-secret-server-secret-server-secret"), testClaims(), time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestRequestNormalize(t *testing.T) {
	tok := validToken(t)
	ok := Request{Path: "/print/dashboard", Token: tok}
	if err := ok.Normalize(); err != nil || ok.Width != DefaultWidth || ok.Scale != DefaultScale {
		t.Fatalf("defaults: %+v %v", ok, err)
	}
	if got := ok.PageURL("http://openlog-api:8080/"); got != "http://openlog-api:8080/print/dashboard#token="+tok {
		t.Errorf("page url %q", got)
	}
	for name, r := range map[string]Request{
		"absolute url":   {Path: "http://evil.example/print/x", Token: tok},
		"other route":    {Path: "/dashboards/1", Token: tok},
		"dot segments":   {Path: "/print/../settings", Token: tok},
		"query":          {Path: "/print/dashboard?x=1", Token: tok},
		"double slash":   {Path: "/print//dashboard", Token: tok},
		"protocol rel":   {Path: "//evil.example/print/x", Token: tok},
		"not a token":    {Path: "/print/dashboard", Token: "olds_abc"},
		"token fragment": {Path: "/print/dashboard", Token: tok + "#x"},
		"narrow":         {Path: "/print/dashboard", Token: tok, Width: 100},
		"scale":          {Path: "/print/dashboard", Token: tok, Scale: 8},
	} {
		if err := r.Normalize(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for _, o := range []string{"http://openlog-api:8080", "https://ui.example.com"} {
		if CheckOrigin(o) != nil {
			t.Errorf("origin %s refused", o)
		}
	}
	for _, o := range []string{"", "ftp://x", "http://x/path", "http://user@x", "http://x?q", "openlog-api:8080"} {
		if CheckOrigin(o) == nil {
			t.Errorf("origin %q accepted", o)
		}
	}
}

func newTestServer(t *testing.T, b Browser, mut func(*ServerOptions)) *httptest.Server {
	t.Helper()
	o := ServerOptions{Token: sharedToken, Origin: "http://openlog-api:8080", Browser: b, MaxImageBytes: 4096, QueueTimeout: time.Second}
	if mut != nil {
		mut(&o)
	}
	s, err := NewServer(o)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestServerAuthValidationAndCaps(t *testing.T) {
	small := pngOf(t, 4, 2)
	b := &fakeBrowser{res: &Response{
		Images: []Image{{ID: "w1", Width: 4, Height: 2, PNG: small}, {ID: "w2", PNG: make([]byte, 5000)}, {ID: "../bad", PNG: small}},
		Errors: []ElementError{{ID: "w3", Error: "the widget could not be loaded"}},
	}}
	ts := newTestServer(t, b, nil)
	tok := validToken(t)

	post := func(auth, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/render", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	body := `{"path":"/print/dashboard","token":"` + tok + `"}`
	if r := post("", body); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth: %d", r.StatusCode)
	}
	if r := post("Bearer "+sharedToken+"x", body); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong auth: %d", r.StatusCode)
	}
	if r := post("Bearer "+sharedToken, `{"path":"/print/dashboard","token":"`+tok+`","url":"http://evil"}`); r.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field: %d", r.StatusCode)
	}
	if r := post("Bearer "+sharedToken, `{"path":"/settings","token":"`+tok+`"}`); r.StatusCode != http.StatusBadRequest {
		t.Errorf("bad path: %d", r.StatusCode)
	}
	if len(b.urls) != 0 {
		t.Fatalf("browser called for rejected requests: %v", b.urls)
	}

	client, err := NewClient(ts.URL, sharedToken, nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Render(context.Background(), Request{Path: "/print/dashboard", Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 || res.Images[0].ID != "w1" || !bytes.Equal(res.Images[0].PNG, small) {
		t.Errorf("images %+v", res.Images)
	}
	if len(res.Errors) != 2 || res.Errors[0].ID != "w2" || res.Errors[0].Error != "image too large" || res.Errors[1].ID != "w3" {
		t.Errorf("errors %+v", res.Errors)
	}
	if len(b.urls) != 1 || b.urls[0] != "http://openlog-api:8080/print/dashboard#token="+tok {
		t.Errorf("browser urls %v", b.urls)
	}

	b.err = errors.New("chromium crashed")
	if _, err := client.Render(context.Background(), Request{Path: "/print/dashboard", Token: tok}); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("browser failure: %v", err)
	}
	bad, _ := NewClient(ts.URL, strings.Repeat("z", 40), nil, 5*time.Second)
	if _, err := bad.Render(context.Background(), Request{Path: "/print/dashboard", Token: tok}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("client with wrong token: %v", err)
	}
	if _, err := NewClient("ftp://x", sharedToken, nil, 0); err == nil {
		t.Error("client with bad url")
	}
	if _, err := NewServer(ServerOptions{Token: "short", Origin: "http://x", Browser: b}); err == nil {
		t.Error("server with short token")
	}
}

func TestServerConcurrencyLimit(t *testing.T) {
	b := &fakeBrowser{res: &Response{}, block: make(chan struct{}), started: make(chan struct{}, 4)}
	ts := newTestServer(t, b, func(o *ServerOptions) { o.MaxConcurrency = 1; o.QueueTimeout = 200 * time.Millisecond })
	client, _ := NewClient(ts.URL, sharedToken, nil, 5*time.Second)
	tok := validToken(t)
	done := make(chan error, 1)
	go func() {
		_, err := client.Render(context.Background(), Request{Path: "/print/dashboard", Token: tok})
		done <- err
	}()
	<-b.started
	if _, err := client.Render(context.Background(), Request{Path: "/print/dashboard", Token: tok}); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("second render while busy: %v", err)
	}
	close(b.block)
	if err := <-done; err != nil {
		t.Errorf("first render: %v", err)
	}
}

func TestServerRenderTimeout(t *testing.T) {
	b := &fakeBrowser{res: &Response{}, block: make(chan struct{})}
	ts := newTestServer(t, b, func(o *ServerOptions) { o.RenderTimeout = 50 * time.Millisecond })
	client, _ := NewClient(ts.URL, sharedToken, nil, 5*time.Second)
	_, err := client.Render(context.Background(), Request{Path: "/print/dashboard", Token: validToken(t)})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("timeout: %v", err)
	}
}

type fakeRenderer struct {
	req Request
	res *Response
	err error
}

func (f *fakeRenderer) Render(_ context.Context, req Request) (*Response, error) {
	f.req = req
	return f.res, f.err
}

func TestReportImages(t *testing.T) {
	key := KeyFromSecret("server-secret-server-secret-server-secret")
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	fr := &fakeRenderer{res: &Response{Images: []Image{
		{ID: "w1", PNG: pngOf(t, 1440, 560)},
		{ID: "w2", PNG: []byte("not a png")},
		{ID: "bad id!", PNG: pngOf(t, 2, 2)},
	}}}
	ri := &ReportImages{Renderer: fr, Key: key, Now: func() time.Time { return now }}
	sr := &dashboard.ScheduledReport{Report: dashboard.Report{ID: testClaims().ReportID, OrgID: "org-1", DashboardID: testClaims().DashboardID}, TenantID: "tenant-1"}
	from, to := now.Add(-24*time.Hour), now
	images, err := ri.Images(context.Background(), sr, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images["w1"].Width != 720 || images["w1"].Height != 280 {
		t.Errorf("images %+v", images)
	}
	if fr.req.Path != PrintPath || fr.req.Width != DefaultWidth {
		t.Errorf("request %+v", fr.req)
	}
	c, err := VerifyToken(key, fr.req.Token, now.Add(time.Minute))
	if err != nil || c.TenantID != "tenant-1" || c.ReportID != sr.ID || c.From != from.UnixMilli() || c.To != to.UnixMilli() {
		t.Errorf("token claims %+v %v", c, err)
	}
	fr.err = errors.New("down")
	if _, err := ri.Images(context.Background(), sr, from, to); err == nil {
		t.Error("renderer error swallowed")
	}
}
