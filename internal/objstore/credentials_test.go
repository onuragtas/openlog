package objstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These tests use httptest fakes of STS, IMDS and the container endpoint; nothing here talks to real AWS.

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

type providerFunc func(context.Context) (Credentials, error)

func (f providerFunc) Retrieve(ctx context.Context) (Credentials, error) { return f(ctx) }

func TestSignV4CredentialsSessionToken(t *testing.T) {
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	newReq := func() *http.Request {
		req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
		req.Header.Set("Range", "bytes=0-9")
		req.Header.Set("X-Amz-Content-Sha256", emptyHash)
		return req
	}
	creds := Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	// Without a session token the result is the AWS example signature.
	req := newReq()
	SignV4Credentials(req, emptyHash, creds, "us-east-1", at)
	if !strings.HasSuffix(req.Header.Get("Authorization"), "Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41") {
		t.Fatalf("static credentials: %s", req.Header.Get("Authorization"))
	}
	creds.SessionToken = "FwoGZXIvYXdzEXAMPLETOKEN"
	req = newReq()
	SignV4Credentials(req, emptyHash, creds, "us-east-1", at)
	auth := req.Header.Get("Authorization")
	if req.Header.Get("X-Amz-Security-Token") != creds.SessionToken {
		t.Fatal("X-Amz-Security-Token not set")
	}
	if !strings.Contains(auth, "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date;x-amz-security-token,") {
		t.Fatalf("session token not signed: %s", auth)
	}
	if strings.Contains(auth, "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41") {
		t.Fatal("signature must change with the session token")
	}
}

func TestEnvProvider(t *testing.T) {
	ctx := context.Background()
	if _, err := (EnvProvider{Getenv: envMap(nil)}).Retrieve(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("empty env: %v", err)
	}
	if _, err := (EnvProvider{Getenv: envMap(map[string]string{"AWS_ACCESS_KEY_ID": "a"})}).Retrieve(ctx); err == nil || errors.Is(err, ErrNoCredentials) {
		t.Fatalf("partial env: %v", err)
	}
	c, err := EnvProvider{Getenv: envMap(map[string]string{"AWS_ACCESS_KEY_ID": "a", "AWS_SECRET_ACCESS_KEY": "s", "AWS_SESSION_TOKEN": "t"})}.Retrieve(ctx)
	if err != nil || c.AccessKeyID != "a" || c.SecretAccessKey != "s" || c.SessionToken != "t" || c.Source != "env" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestWebIdentityProvider(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("jwt-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = r.ParseForm()
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" || r.Form.Get("Action") != "AssumeRoleWithWebIdentity" ||
			r.Form.Get("Version") != "2011-06-15" || r.Form.Get("RoleSessionName") != "sess" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if r.Form.Get("RoleArn") != "arn:aws:iam::123456789012:role/openlog" || r.Form.Get("WebIdentityToken") != "jwt-1" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>Not authorized</Message></Error></ErrorResponse>`)
			return
		}
		fmt.Fprint(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleWithWebIdentityResult>
    <Credentials>
      <AccessKeyId>ASIAEXAMPLE</AccessKeyId>
      <SecretAccessKey>secret</SecretAccessKey>
      <SessionToken>session</SessionToken>
      <Expiration>2030-01-02T03:04:05Z</Expiration>
    </Credentials>
  </AssumeRoleWithWebIdentityResult>
</AssumeRoleWithWebIdentityResponse>`)
	}))
	defer srv.Close()
	env := map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile, "AWS_ROLE_ARN": "arn:aws:iam::123456789012:role/openlog", "AWS_ROLE_SESSION_NAME": "sess"}
	p := WebIdentityProvider{Getenv: envMap(env), Endpoint: srv.URL, Client: srv.Client()}
	c, err := p.Retrieve(context.Background())
	if err != nil || c.AccessKeyID != "ASIAEXAMPLE" || c.SecretAccessKey != "secret" || c.SessionToken != "session" ||
		!c.Expires.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) || c.Source != "web-identity" {
		t.Fatalf("%+v %v", c, err)
	}
	env["AWS_ROLE_ARN"] = "arn:aws:iam::123456789012:role/other"
	if _, err := p.Retrieve(context.Background()); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("STS error: %v", err)
	}
	if _, err := (WebIdentityProvider{Getenv: envMap(nil)}).Retrieve(context.Background()); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("unconfigured: %v", err)
	}
	if _, err := (WebIdentityProvider{Getenv: envMap(map[string]string{"AWS_ROLE_ARN": "x"})}).Retrieve(context.Background()); err == nil || errors.Is(err, ErrNoCredentials) {
		t.Fatalf("partial: %v", err)
	}
	for _, tc := range []struct{ region, mode, want string }{
		{"eu-west-1", "", "https://sts.eu-west-1.amazonaws.com/"},
		{"eu-west-1", "regional", "https://sts.eu-west-1.amazonaws.com/"},
		{"eu-west-1", "legacy", "https://sts.amazonaws.com/"},
		{"me-south-1", "legacy", "https://sts.me-south-1.amazonaws.com/"},
		{"cn-north-1", "", "https://sts.cn-north-1.amazonaws.com.cn/"},
		{"", "", "https://sts.amazonaws.com/"},
	} {
		if got := stsEndpoint(tc.region, tc.mode); got != tc.want {
			t.Errorf("stsEndpoint(%q, %q) = %s, want %s", tc.region, tc.mode, got, tc.want)
		}
	}
}

// fakeIMDS is an IMDSv2-only metadata service: every metadata GET needs the session token.
func fakeIMDS(t *testing.T, role string) (*httptest.Server, *atomic.Int32) {
	var rejected atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest/api/token" {
			if r.Method != http.MethodPut || r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "" {
				http.Error(w, "", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, "imds-token")
			return
		}
		if r.Header.Get("X-aws-ec2-metadata-token") != "imds-token" {
			rejected.Add(1)
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/latest/meta-data/iam/security-credentials/":
			if role == "" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, role+"\n")
		case "/latest/meta-data/iam/security-credentials/" + role:
			fmt.Fprint(w, `{"Code":"Success","Type":"AWS-HMAC","AccessKeyId":"ASIAIMDS","SecretAccessKey":"s","Token":"t","Expiration":"2030-01-01T00:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &rejected
}

func TestIMDSProvider(t *testing.T) {
	ctx := context.Background()
	srv, rejected := fakeIMDS(t, "openlog-role")
	// The fake rejects IMDSv1 (no token).
	if resp, err := http.Get(srv.URL + "/latest/meta-data/iam/security-credentials/"); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("IMDSv1 request: %v %v", resp, err)
	}
	c, err := IMDSProvider{Getenv: envMap(nil), Endpoint: srv.URL}.Retrieve(ctx)
	if err != nil || c.AccessKeyID != "ASIAIMDS" || c.SessionToken != "t" || c.Source != "imds" || c.Expires.Year() != 2030 {
		t.Fatalf("%+v %v", c, err)
	}
	if rejected.Load() != 1 {
		t.Fatalf("provider sent %d requests without the token", rejected.Load()-1)
	}
	if _, err := (IMDSProvider{Getenv: envMap(map[string]string{"AWS_EC2_METADATA_DISABLED": "true"}), Endpoint: srv.URL}).Retrieve(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("disabled: %v", err)
	}
	noRole, _ := fakeIMDS(t, "")
	if _, err := (IMDSProvider{Getenv: envMap(nil), Endpoint: noRole.URL}).Retrieve(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("no instance profile: %v", err)
	}
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	if _, err := (IMDSProvider{Getenv: envMap(nil), Endpoint: down.URL}).Retrieve(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestECSProvider(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/credentials/abc" || (r.URL.Path == "/full" && r.Header.Get("Authorization") == "Bearer pod-token") {
			fmt.Fprint(w, `{"AccessKeyId":"ASIAECS","SecretAccessKey":"s","Token":"t","Expiration":"2030-01-01T00:00:00Z","RoleArn":"arn"}`)
			return
		}
		http.Error(w, `{"code":"denied"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	c, err := ECSProvider{Getenv: envMap(map[string]string{"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/v2/credentials/abc"}), Endpoint: srv.URL}.Retrieve(ctx)
	if err != nil || c.AccessKeyID != "ASIAECS" || c.SessionToken != "t" || c.Source != "ecs" {
		t.Fatalf("relative: %+v %v", c, err)
	}
	full := map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL + "/full", "AWS_CONTAINER_AUTHORIZATION_TOKEN": "Bearer pod-token"}
	if c, err := (ECSProvider{Getenv: envMap(full)}).Retrieve(ctx); err != nil || c.AccessKeyID != "ASIAECS" {
		t.Fatalf("full with token: %+v %v", c, err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(tokenFile, []byte("Bearer pod-token\n"), 0o600)
	full["AWS_CONTAINER_AUTHORIZATION_TOKEN"] = "wrong"
	full["AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"] = tokenFile // the file wins
	if c, err := (ECSProvider{Getenv: envMap(full)}).Retrieve(ctx); err != nil || c.AccessKeyID != "ASIAECS" {
		t.Fatalf("full with token file: %+v %v", c, err)
	}
	delete(full, "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE")
	if _, err := (ECSProvider{Getenv: envMap(full)}).Retrieve(ctx); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("wrong token: %v", err)
	}
	for uri, ok := range map[string]bool{
		"http://169.254.170.2/creds": true, "http://169.254.170.23/v1/credentials": true, "http://[fd00:ec2::23]/v1": true,
		"http://127.0.0.1:8080/": true, "http://localhost/": true, "https://creds.example.com/": true,
		"http://creds.example.com/": false, "http://10.0.0.1/": false, "file:///etc/passwd": false,
	} {
		u, _ := url.Parse(uri)
		if allowedFullURIHost(u) != ok {
			t.Errorf("allowedFullURIHost(%s) = %v", uri, !ok)
		}
	}
	if _, err := (ECSProvider{Getenv: envMap(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://creds.example.com/"})}).Retrieve(ctx); err == nil || errors.Is(err, ErrNoCredentials) {
		t.Fatalf("disallowed host: %v", err)
	}
	if _, err := (ECSProvider{Getenv: envMap(nil)}).Retrieve(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("unconfigured: %v", err)
	}
}

func TestChainOrder(t *testing.T) {
	ctx := context.Background()
	var order []string
	step := func(name string, err error) CredentialsProvider {
		return providerFunc(func(context.Context) (Credentials, error) {
			order = append(order, name)
			if err != nil {
				return Credentials{}, err
			}
			return Credentials{AccessKeyID: name, SecretAccessKey: "s", Source: name}, nil
		})
	}
	c, err := ChainProvider{step("env", ErrNoCredentials), step("web-identity", nil), step("ecs", nil)}.Retrieve(ctx)
	if err != nil || c.Source != "web-identity" || strings.Join(order, ",") != "env,web-identity" {
		t.Fatalf("%+v %v %v", c, err, order)
	}
	order = nil
	boom := errors.New("token file unreadable")
	if _, err := (ChainProvider{step("env", ErrNoCredentials), step("web-identity", boom), step("ecs", nil)}).Retrieve(ctx); !errors.Is(err, boom) || len(order) != 2 {
		t.Fatalf("a real error must stop the chain: %v %v", err, order)
	}
	if _, err := (ChainProvider{step("env", ErrNoCredentials)}).Retrieve(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("empty chain: %v", err)
	}

	// DefaultChain order: environment beats web identity, container and IMDS.
	imds, _ := fakeIMDS(t, "role")
	env := map[string]string{"AWS_ACCESS_KEY_ID": "AKIAENV", "AWS_SECRET_ACCESS_KEY": "s", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/x"}
	chain := DefaultChain(ChainOptions{Getenv: envMap(env), IMDSEndpoint: imds.URL, ECSEndpoint: "http://127.0.0.1:1"})
	if c, err := chain.Retrieve(ctx); err != nil || c.Source != "env" {
		t.Fatalf("env first: %+v %v", c, err)
	}
	chain = DefaultChain(ChainOptions{Getenv: envMap(nil), IMDSEndpoint: imds.URL})
	if c, err := chain.Retrieve(ctx); err != nil || c.Source != "imds" {
		t.Fatalf("imds last: %+v %v", c, err)
	}
}

func TestCachedProviderRefresh(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var calls int
	var fail error
	c := &CachedProvider{Now: func() time.Time { return now }, Provider: providerFunc(func(context.Context) (Credentials, error) {
		calls++
		if fail != nil {
			return Credentials{}, fail
		}
		return Credentials{AccessKeyID: fmt.Sprintf("k%d", calls), SecretAccessKey: "s", Expires: now.Add(time.Hour)}, nil
	})}
	get := func() Credentials {
		t.Helper()
		creds, err := c.Retrieve(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return creds
	}
	if get().AccessKeyID != "k1" || get().AccessKeyID != "k1" || calls != 1 {
		t.Fatalf("cached: calls=%d", calls)
	}
	now = now.Add(54 * time.Minute) // 6 minutes left: still cached
	if get().AccessKeyID != "k1" {
		t.Fatal("refreshed too early")
	}
	now = now.Add(2 * time.Minute) // 4 minutes left: refresh
	if get().AccessKeyID != "k2" || calls != 2 {
		t.Fatalf("not refreshed before expiry: calls=%d", calls)
	}

	// Refresh fails: the still-valid credentials are served, retried after RetryInterval, an error once expired.
	now = now.Add(57 * time.Minute) // k2 has 3 minutes left
	fail = errors.New("sts down")
	if get().AccessKeyID != "k2" || calls != 3 {
		t.Fatalf("serve stale: calls=%d", calls)
	}
	if get().AccessKeyID != "k2" || calls != 3 {
		t.Fatalf("retried before RetryInterval: calls=%d", calls)
	}
	now = now.Add(11 * time.Second)
	if get().AccessKeyID != "k2" || calls != 4 {
		t.Fatalf("no retry after RetryInterval: calls=%d", calls)
	}
	now = now.Add(3 * time.Minute) // expired
	if _, err := c.Retrieve(ctx); !errors.Is(err, fail) {
		t.Fatalf("expired credentials must not be served: %v", err)
	}
	fail = nil
	if get().AccessKeyID != "k6" {
		t.Fatal("recovery")
	}

	// ErrNoCredentials is cached for NegativeTTL; non-expiring credentials are never refreshed.
	calls = 0
	n := &CachedProvider{Now: func() time.Time { return now }, Provider: providerFunc(func(context.Context) (Credentials, error) {
		calls++
		return Credentials{}, ErrNoCredentials
	})}
	for range 3 {
		if _, err := n.Retrieve(ctx); !errors.Is(err, ErrNoCredentials) {
			t.Fatal(err)
		}
	}
	now = now.Add(61 * time.Second)
	_, _ = n.Retrieve(ctx)
	if calls != 2 {
		t.Fatalf("negative cache: calls=%d", calls)
	}
	calls = 0
	s := &CachedProvider{Provider: providerFunc(func(context.Context) (Credentials, error) {
		calls++
		return Credentials{AccessKeyID: "a", SecretAccessKey: "s"}, nil
	})}
	for range 3 {
		_, _ = s.Retrieve(ctx)
	}
	if calls != 1 {
		t.Fatalf("static: calls=%d", calls)
	}
}

func TestCachedProviderConcurrentRefresh(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	c := &CachedProvider{Provider: providerFunc(func(context.Context) (Credentials, error) {
		calls.Add(1)
		<-release
		return Credentials{AccessKeyID: "a", SecretAccessKey: "s", Expires: time.Now().Add(time.Hour)}, nil
	})}
	done := make(chan error)
	for range 8 {
		go func() { _, err := c.Retrieve(context.Background()); done <- err }()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent callers refreshed %d times", calls.Load())
	}
}

func TestS3WithCredentialsProvider(t *testing.T) {
	var sawToken atomic.Int32
	fake := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Security-Token") != "session" || !strings.Contains(r.Header.Get("Authorization"), "x-amz-security-token") {
			http.Error(w, "missing signed session token", http.StatusForbidden)
			return
		}
		sawToken.Add(1)
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	creds := StaticProvider{Credentials: Credentials{AccessKeyID: "ak", SecretAccessKey: "sk", SessionToken: "session"}}
	s := &S3{BaseURL: srv.URL + "/bucket/p/", Region: "eu-west-1", Credentials: creds, Client: srv.Client()}
	exercise(t, s)
	if sawToken.Load() == 0 {
		t.Fatal("no request carried the session token")
	}

	// A provider error fails the request; ErrNoCredentials sends it unsigned.
	s.Credentials = providerFunc(func(context.Context) (Credentials, error) { return Credentials{}, errors.New("sts down") })
	if err := s.Put(context.Background(), "k.zip", bytes.NewReader([]byte("x")), 1); err == nil || !strings.Contains(err.Error(), "sts down") {
		t.Fatalf("provider error: %v", err)
	}
	var unsigned atomic.Bool
	anon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unsigned.Store(r.Header.Get("Authorization") == "")
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer anon.Close()
	s = &S3{BaseURL: anon.URL + "/bucket/", Credentials: ChainProvider{EnvProvider{Getenv: envMap(nil)}}, Client: anon.Client()}
	if err := s.Put(context.Background(), "k.zip", bytes.NewReader([]byte("x")), 1); err != nil || !unsigned.Load() {
		t.Fatalf("unsigned: %v %v", err, unsigned.Load())
	}
}
