package synthetics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// mustCheck validates an input and returns the check it becomes.
func mustCheck(t *testing.T, in Input) Check {
	t.Helper()
	if err := in.Validate(); err != nil {
		t.Fatalf("invalid test input: %v", err)
	}
	return Check{ID: "c1", OrgID: "o1", Input: in}
}

// localChecker may reach the test server: httptest listens on loopback, which the SSRF guard blocks by
// default (TestRunSSRFGuard covers the guard itself).
func localChecker(o CheckerOptions) *Checker {
	o.AllowPrivateNetworks = true
	return NewChecker(o)
}

func run(t *testing.T, c *Checker, chk Check) Result {
	t.Helper()
	return c.Run(context.Background(), Due{Check: chk, TenantID: "tenant-a", Location: LocationLocal})
}

func TestRunSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pong")
	}))
	defer srv.Close()

	chk := mustCheck(t, Input{Name: "ping", URL: srv.URL, AssertionType: AssertContains, AssertionValue: "pong"})
	res := run(t, localChecker(CheckerOptions{}), chk)

	if !res.Success || res.ErrorKind != ErrorNone || res.Error != "" {
		t.Fatalf("failed: %+v", res)
	}
	if res.StatusCode != 200 || res.ResponseBytes != 4 {
		t.Errorf("status %d, %d bytes", res.StatusCode, res.ResponseBytes)
	}
	if res.DurationMs <= 0 || res.FirstByteMs <= 0 {
		t.Errorf("timings: total %v, first byte %v", res.DurationMs, res.FirstByteMs)
	}
	// The definition travels with the result, so the stored run describes itself.
	if res.CheckID != "c1" || res.TenantID != "tenant-a" || res.Location != LocationLocal ||
		res.Name != "ping" || res.URL != srv.URL || res.Method != "GET" {
		t.Errorf("definition not copied: %+v", res)
	}
}

func TestRunStatusCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/created" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := localChecker(CheckerOptions{})

	bad := run(t, c, mustCheck(t, Input{Name: "x", URL: srv.URL}))
	if bad.Success || bad.ErrorKind != ErrorStatus || bad.StatusCode != 500 {
		t.Fatalf("500 accepted: %+v", bad)
	}
	if !strings.Contains(bad.Error, "500") {
		t.Errorf("message %q", bad.Error)
	}
	// A check may expect any of several codes.
	ok := run(t, c, mustCheck(t, Input{Name: "x", URL: srv.URL + "/created", ExpectedStatus: []int{200, 204}}))
	if !ok.Success || ok.StatusCode != 204 {
		t.Fatalf("204 rejected: %+v", ok)
	}
}

func TestRunAssertions(t *testing.T) {
	const body = `{"status":"ok","data":{"items":[{"state":"up"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/text" {
			_, _ = io.WriteString(w, "not json")
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	c := localChecker(CheckerOptions{})

	cases := []struct {
		name    string
		path    string
		in      Input
		success bool
	}{
		{"contains hit", "", Input{AssertionType: AssertContains, AssertionValue: `"status":"ok"`}, true},
		{"contains miss", "", Input{AssertionType: AssertContains, AssertionValue: "missing"}, false},
		{"not_contains hit", "", Input{AssertionType: AssertNotContains, AssertionValue: "error"}, true},
		{"not_contains miss", "", Input{AssertionType: AssertNotContains, AssertionValue: "status"}, false},
		{"json_path match", "", Input{AssertionType: AssertJSONPath, AssertionPath: "status", AssertionValue: "ok"}, true},
		{"json_path nested", "", Input{AssertionType: AssertJSONPath, AssertionPath: "data.items.0.state", AssertionValue: "up"}, true},
		{"json_path mismatch", "", Input{AssertionType: AssertJSONPath, AssertionPath: "status", AssertionValue: "down"}, false},
		{"json_path missing", "", Input{AssertionType: AssertJSONPath, AssertionPath: "nope", AssertionValue: "x"}, false},
		{"json_path presence only", "", Input{AssertionType: AssertJSONPath, AssertionPath: "status"}, true},
		{"json_path on text", "/text", Input{AssertionType: AssertJSONPath, AssertionPath: "status", AssertionValue: "ok"}, false},
		{"no assertion", "/text", Input{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.Name, in.URL = tc.name, srv.URL+tc.path
			res := run(t, c, mustCheck(t, in))
			if res.Success != tc.success {
				t.Fatalf("success = %v (%s: %s)", res.Success, res.ErrorKind, res.Error)
			}
			if !tc.success && res.ErrorKind != ErrorAssertion {
				t.Errorf("error kind %q, want %q", res.ErrorKind, ErrorAssertion)
			}
		})
	}
}

func TestRunTimeout(t *testing.T) {
	// The handler returns as soon as the client gives up, so Close does not wait for it.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	// Built without Validate: the timeout is below the minimum a stored check may have, to keep the test fast.
	chk := Check{ID: "c1", Input: Input{Name: "slow", URL: srv.URL, Method: "GET", ExpectedStatus: []int{200}, TimeoutMs: 100}}
	res := run(t, localChecker(CheckerOptions{}), chk)
	if res.Success || res.ErrorKind != ErrorTimeout {
		t.Fatalf("expected a timeout: %+v", res)
	}
}

func TestRunRedirectCap(t *testing.T) {
	var hops int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer srv.Close()

	chk := mustCheck(t, Input{Name: "loop", URL: srv.URL})
	res := run(t, localChecker(CheckerOptions{MaxRedirects: 2}), chk)
	if res.Success || res.ErrorKind != ErrorRedirect {
		t.Fatalf("expected a redirect failure: %+v", res)
	}
	if hops > 3 {
		t.Errorf("followed %d hops with a cap of 2", hops)
	}
}

func TestRunFollowsRedirectWithinCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			_, _ = io.WriteString(w, "done")
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer srv.Close()

	chk := mustCheck(t, Input{Name: "hop", URL: srv.URL, AssertionType: AssertContains, AssertionValue: "done"})
	if res := run(t, localChecker(CheckerOptions{MaxRedirects: 3}), chk); !res.Success {
		t.Fatalf("redirect within the cap failed: %+v", res)
	}
}

func TestRunResponseSizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 5000))
	}))
	defer srv.Close()

	chk := mustCheck(t, Input{Name: "big", URL: srv.URL})
	res := run(t, localChecker(CheckerOptions{MaxResponseBytes: 1000}), chk)
	if res.Success || res.ErrorKind != ErrorBody {
		t.Fatalf("expected the size cap to fail the run: %+v", res)
	}
	// The run reports the cap, not the real size: nothing beyond it was read.
	if res.ResponseBytes != 1000 {
		t.Errorf("response bytes %d, want the cap 1000", res.ResponseBytes)
	}
}

// The guard is the SSRF protection: a member of an organization chooses the URL, so without
// OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS a check must not reach loopback or private addresses.
func TestRunSSRFGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secret")
	}))
	defer srv.Close()
	chk := mustCheck(t, Input{Name: "internal", URL: srv.URL})

	blocked := run(t, NewChecker(CheckerOptions{}), chk)
	if blocked.Success || blocked.ErrorKind != ErrorBlocked {
		t.Fatalf("loopback was not blocked: %+v", blocked)
	}
	if allowed := run(t, localChecker(CheckerOptions{}), chk); !allowed.Success {
		t.Fatalf("loopback rejected although private networks are allowed: %+v", allowed)
	}
}

func TestPublicAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "::1", "10.0.0.5", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1", "::ffff:127.0.0.1"} {
		if a, err := netip.ParseAddr(addr); err != nil || publicAddr(a) {
			t.Errorf("%s treated as public", addr)
		}
	}
	for _, addr := range []string{"8.8.8.8", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		if a, err := netip.ParseAddr(addr); err != nil || !publicAddr(a) {
			t.Errorf("%s treated as private", addr)
		}
	}
}

func TestRunSendsRequest(t *testing.T) {
	var (
		gotMethod string
		gotHeader string
		gotBody   string
		gotAgent  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMethod, gotHeader, gotBody, gotAgent = r.Method, r.Header.Get("X-Token"), string(b), r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	chk := mustCheck(t, Input{Name: "post", URL: srv.URL, Method: "POST", Body: `{"a":1}`,
		Headers: map[string]string{"X-Token": "abc"}, ExpectedStatus: []int{201}})
	if res := run(t, localChecker(CheckerOptions{UserAgent: "openlog-synthetics/test"}), chk); !res.Success {
		t.Fatalf("failed: %+v", res)
	}
	if gotMethod != "POST" || gotHeader != "abc" || gotBody != `{"a":1}` || gotAgent != "openlog-synthetics/test" {
		t.Fatalf("request: %s %q %q %q", gotMethod, gotHeader, gotBody, gotAgent)
	}
}

func TestRunDNSFailure(t *testing.T) {
	chk := mustCheck(t, Input{Name: "dns", URL: "https://openlog-synthetics.invalid/health", TimeoutMs: 5000, IntervalSeconds: 30})
	res := run(t, localChecker(CheckerOptions{}), chk)
	if res.Success || (res.ErrorKind != ErrorDNS && res.ErrorKind != ErrorConnect) {
		t.Fatalf("expected a resolution failure: %+v", res)
	}
}
