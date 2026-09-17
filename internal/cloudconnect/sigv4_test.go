package cloudconnect

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// SigV4 is what makes a CloudWatch request acceptable at all, so the parts a change could silently break are
// pinned: which headers are signed, the credential scope (service and region), and that the signature follows
// the body and the query rather than being computed over the URL alone.

func signedRequest(t *testing.T, body string, query string, creds Credentials) *http.Request {
	t.Helper()
	url := "https://monitoring.eu-central-1.amazonaws.com/"
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", awsJSONContent)
	req.Header.Set("X-Amz-Target", awsTargetPrefix+"GetMetricData")
	signV4(req, sha256Hex([]byte(body)), creds, "monitoring", "eu-central-1",
		time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC))
	return req
}

func authParts(t *testing.T, req *http.Request) map[string]string {
	t.Helper()
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("Authorization = %q", auth)
	}
	out := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ", ") {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func TestSignV4SignsTheRequiredHeaders(t *testing.T) {
	req := signedRequest(t, `{"a":1}`, "", Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"})
	parts := authParts(t, req)

	// The credential scope names the day, the region and the service; a wrong service is the most common
	// cause of a rejected monitoring request.
	if want := "AKIDEXAMPLE/20260917/eu-central-1/monitoring/aws4_request"; parts["Credential"] != want {
		t.Errorf("Credential = %q, want %q", parts["Credential"], want)
	}
	// content-type must be signed: the JSON protocol sends it and AWS includes it in the signature.
	if want := "content-type;host;x-amz-date;x-amz-target"; parts["SignedHeaders"] != want {
		t.Errorf("SignedHeaders = %q, want %q", parts["SignedHeaders"], want)
	}
	if len(parts["Signature"]) != 64 {
		t.Errorf("Signature = %q, want 64 hex characters", parts["Signature"])
	}
	if req.Header.Get("X-Amz-Date") != "20260917T100000Z" {
		t.Errorf("X-Amz-Date = %q", req.Header.Get("X-Amz-Date"))
	}
	// No temporary credentials: no security token header.
	if req.Header.Get("X-Amz-Security-Token") != "" {
		t.Error("a security token header was set without a session token")
	}
}

// The same inputs always produce the same signature; a different body produces a different one.
func TestSignV4FollowsTheBody(t *testing.T) {
	creds := Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}
	a := authParts(t, signedRequest(t, `{"a":1}`, "", creds))["Signature"]
	again := authParts(t, signedRequest(t, `{"a":1}`, "", creds))["Signature"]
	b := authParts(t, signedRequest(t, `{"a":2}`, "", creds))["Signature"]

	if a != again {
		t.Errorf("signing is not deterministic: %q then %q", a, again)
	}
	if a == b {
		t.Error("the signature did not change with the body")
	}
}

// A different secret key produces a different signature (the signing key is derived from it).
func TestSignV4FollowsTheKey(t *testing.T) {
	a := authParts(t, signedRequest(t, `{}`, "", Credentials{AccessKeyID: "A", SecretAccessKey: "one"}))["Signature"]
	b := authParts(t, signedRequest(t, `{}`, "", Credentials{AccessKeyID: "A", SecretAccessKey: "two"}))["Signature"]
	if a == b {
		t.Error("the signature did not change with the secret key")
	}
}

// Temporary credentials are sent and signed as X-Amz-Security-Token.
func TestSignV4SignsTheSessionToken(t *testing.T) {
	req := signedRequest(t, `{}`, "", Credentials{AccessKeyID: "A", SecretAccessKey: "S", SessionToken: "TOKEN"})
	if req.Header.Get("X-Amz-Security-Token") != "TOKEN" {
		t.Fatalf("X-Amz-Security-Token = %q", req.Header.Get("X-Amz-Security-Token"))
	}
	if headers := authParts(t, req)["SignedHeaders"]; !strings.Contains(headers, "x-amz-security-token") {
		t.Errorf("SignedHeaders = %q, want the security token signed", headers)
	}
}

// The canonical query is sorted by name and value and percent-encoded with the SigV4 rules.
func TestCanonicalQuery(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com/?b=2&a=1&a=0&c=a+b%2Fc", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := canonicalQuery(req), "a=0&a=1&b=2&c=a%20b%2Fc"; got != want {
		t.Errorf("canonicalQuery = %q, want %q", got, want)
	}
	// A signature over a different query must differ.
	creds := Credentials{AccessKeyID: "A", SecretAccessKey: "S"}
	a := authParts(t, signedRequest(t, `{}`, "x=1", creds))["Signature"]
	b := authParts(t, signedRequest(t, `{}`, "x=2", creds))["Signature"]
	if a == b {
		t.Error("the signature did not change with the query")
	}
}

func TestURIEncode(t *testing.T) {
	cases := map[string]string{
		"simple":       "simple",
		"a b":          "a%20b",
		"a/b":          "a%2Fb",
		"-_.~":         "-_.~",
		"eu-central-1": "eu-central-1",
		"AWS/RDS":      "AWS%2FRDS",
	}
	for in, want := range cases {
		if got := uriEncode(in); got != want {
			t.Errorf("uriEncode(%q) = %q, want %q", in, got, want)
		}
	}
}
