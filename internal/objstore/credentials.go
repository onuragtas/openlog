package objstore

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNoCredentials is returned by a provider that is not configured in this environment (no variables, no metadata
// endpoint). A chain moves on to its next provider; S3 sends unsigned requests when the whole chain has none.
var ErrNoCredentials = errors.New("no AWS credentials found")

// Credentials are AWS credentials. SessionToken is set for temporary credentials; Expires is zero when they do not expire.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expires         time.Time
	// Source names the provider ("static", "env", "web-identity", "ecs", "imds").
	Source string
}

// CredentialsProvider returns credentials for signing.
type CredentialsProvider interface {
	Retrieve(ctx context.Context) (Credentials, error)
}

// StaticProvider returns fixed credentials.
type StaticProvider struct{ Credentials Credentials }

// Retrieve implements CredentialsProvider.
func (p StaticProvider) Retrieve(context.Context) (Credentials, error) {
	if p.Credentials.AccessKeyID == "" || p.Credentials.SecretAccessKey == "" {
		return Credentials{}, ErrNoCredentials
	}
	c := p.Credentials
	if c.Source == "" {
		c.Source = "static"
	}
	return c, nil
}

func getenvFunc(f func(string) string) func(string) string {
	if f != nil {
		return f
	}
	return os.Getenv
}

func httpClient(c *http.Client, timeout time.Duration) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: timeout}
}

// EnvProvider reads AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_SESSION_TOKEN.
type EnvProvider struct {
	Getenv func(string) string // default os.Getenv
}

// Retrieve implements CredentialsProvider.
func (p EnvProvider) Retrieve(context.Context) (Credentials, error) {
	getenv := getenvFunc(p.Getenv)
	id, secret := strings.TrimSpace(getenv("AWS_ACCESS_KEY_ID")), strings.TrimSpace(getenv("AWS_SECRET_ACCESS_KEY"))
	switch {
	case id == "" && secret == "":
		return Credentials{}, ErrNoCredentials
	case id == "" || secret == "":
		return Credentials{}, errors.New("env credentials: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set together")
	}
	return Credentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: strings.TrimSpace(getenv("AWS_SESSION_TOKEN")), Source: "env"}, nil
}

// WebIdentityProvider exchanges the token in AWS_WEB_IDENTITY_TOKEN_FILE for credentials of AWS_ROLE_ARN with STS
// AssumeRoleWithWebIdentity (EKS IRSA, EKS Pod Identity webhook). The request is unsigned; the token file is read again
// on every call because the kubelet rotates it.
type WebIdentityProvider struct {
	Getenv func(string) string // default os.Getenv
	// Region selects the regional STS endpoint when AWS_REGION and AWS_DEFAULT_REGION are unset.
	Region string
	// Endpoint overrides the STS endpoint URL (tests, VPC endpoints).
	Endpoint string
	Client   *http.Client
}

func stsEndpoint(region, mode string) string {
	if region == "" || strings.EqualFold(mode, "legacy") && legacyGlobalSTSRegion(region) {
		return "https://sts.amazonaws.com/"
	}
	host := "sts." + region + ".amazonaws.com"
	if strings.HasPrefix(region, "cn-") {
		host += ".cn"
	}
	return "https://" + host + "/"
}

// legacyGlobalSTSRegion lists the regions the SDKs send to the global endpoint with AWS_STS_REGIONAL_ENDPOINTS=legacy.
func legacyGlobalSTSRegion(region string) bool {
	switch region {
	case "ap-northeast-1", "ap-south-1", "ap-southeast-1", "ap-southeast-2", "aws-global", "ca-central-1", "eu-central-1",
		"eu-north-1", "eu-west-1", "eu-west-2", "eu-west-3", "sa-east-1", "us-east-1", "us-east-2", "us-west-1", "us-west-2":
		return true
	}
	return false
}

type stsCredentials struct {
	AccessKeyID     string `xml:"AccessKeyId"`
	SecretAccessKey string `xml:"SecretAccessKey"`
	SessionToken    string `xml:"SessionToken"`
	Expiration      string `xml:"Expiration"`
}

type stsResponse struct {
	Result struct {
		Credentials stsCredentials `xml:"Credentials"`
	} `xml:"AssumeRoleWithWebIdentityResult"`
}

type stsError struct {
	Error struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

// Retrieve implements CredentialsProvider.
func (p WebIdentityProvider) Retrieve(ctx context.Context) (Credentials, error) {
	getenv := getenvFunc(p.Getenv)
	tokenFile, role := strings.TrimSpace(getenv("AWS_WEB_IDENTITY_TOKEN_FILE")), strings.TrimSpace(getenv("AWS_ROLE_ARN"))
	if tokenFile == "" && role == "" {
		return Credentials{}, ErrNoCredentials
	}
	if tokenFile == "" || role == "" {
		return Credentials{}, errors.New("web identity credentials: AWS_WEB_IDENTITY_TOKEN_FILE and AWS_ROLE_ARN must be set together")
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return Credentials{}, fmt.Errorf("web identity credentials: %w", err)
	}
	session := strings.TrimSpace(getenv("AWS_ROLE_SESSION_NAME"))
	if session == "" {
		session = "openlog-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	endpoint := p.Endpoint
	if endpoint == "" {
		region := strings.TrimSpace(getenv("AWS_REGION"))
		if region == "" {
			region = strings.TrimSpace(getenv("AWS_DEFAULT_REGION"))
		}
		if region == "" {
			region = p.Region
		}
		endpoint = stsEndpoint(region, strings.TrimSpace(getenv("AWS_STS_REGIONAL_ENDPOINTS")))
	}
	form := url.Values{
		"Action":           {"AssumeRoleWithWebIdentity"},
		"Version":          {"2011-06-15"},
		"RoleArn":          {role},
		"RoleSessionName":  {session},
		"WebIdentityToken": {strings.TrimSpace(string(token))},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Credentials{}, fmt.Errorf("web identity credentials: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Accept", "application/xml")
	resp, err := httpClient(p.Client, 30*time.Second).Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("web identity credentials: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Credentials{}, fmt.Errorf("web identity credentials: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var e stsError
		if xml.Unmarshal(body, &e) == nil && e.Error.Code != "" {
			return Credentials{}, fmt.Errorf("web identity credentials: STS HTTP %d: %s: %s", resp.StatusCode, e.Error.Code, e.Error.Message)
		}
		return Credentials{}, fmt.Errorf("web identity credentials: STS HTTP %d", resp.StatusCode)
	}
	var r stsResponse
	if err := xml.Unmarshal(body, &r); err != nil {
		return Credentials{}, fmt.Errorf("web identity credentials: STS response: %w", err)
	}
	c := r.Result.Credentials
	return toCredentials("web-identity", c.AccessKeyID, c.SecretAccessKey, c.SessionToken, c.Expiration)
}

func toCredentials(source, id, secret, token, expiration string) (Credentials, error) {
	if id == "" || secret == "" {
		return Credentials{}, fmt.Errorf("%s credentials: response without access key", source)
	}
	c := Credentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: token, Source: source}
	if expiration != "" {
		t, err := time.Parse(time.RFC3339, expiration)
		if err != nil {
			return Credentials{}, fmt.Errorf("%s credentials: expiration %q: %w", source, expiration, err)
		}
		c.Expires = t
	}
	return c, nil
}

// jsonCredentials is the credential document of the ECS/EKS container endpoint and of IMDS.
type jsonCredentials struct {
	Code            string `json:"Code"`
	Message         string `json:"Message"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	Token           string `json:"Token"`
	Expiration      string `json:"Expiration"`
}

// ECSProvider reads container credentials: AWS_CONTAINER_CREDENTIALS_RELATIVE_URI against http://169.254.170.2 (ECS
// task role), or AWS_CONTAINER_CREDENTIALS_FULL_URI (EKS Pod Identity, local credential servers) with the
// Authorization header from AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE or AWS_CONTAINER_AUTHORIZATION_TOKEN. A full URI must
// be https, or http to a loopback address, "localhost" or the ECS/EKS link-local hosts (169.254.170.2,
// 169.254.170.23, fd00:ec2::23); host names other than localhost are not resolved to check them.
type ECSProvider struct {
	Getenv func(string) string // default os.Getenv
	// Endpoint overrides the base of the relative URI (default http://169.254.170.2; tests).
	Endpoint string
	Client   *http.Client
}

func allowedFullURIHost(u *url.URL) bool {
	if u.Scheme == "https" {
		return u.Host != ""
	}
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.Equal(net.ParseIP("169.254.170.2")) || ip.Equal(net.ParseIP("169.254.170.23")) || ip.Equal(net.ParseIP("fd00:ec2::23"))
}

// Retrieve implements CredentialsProvider.
func (p ECSProvider) Retrieve(ctx context.Context) (Credentials, error) {
	getenv := getenvFunc(p.Getenv)
	var target string
	if rel := strings.TrimSpace(getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")); rel != "" {
		base := p.Endpoint
		if base == "" {
			base = "http://169.254.170.2"
		}
		target = strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(rel, "/")
	} else if full := strings.TrimSpace(getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")); full != "" {
		u, err := url.Parse(full)
		if err != nil || !allowedFullURIHost(u) {
			return Credentials{}, fmt.Errorf("container credentials: AWS_CONTAINER_CREDENTIALS_FULL_URI %q must be https or http to a loopback or ECS/EKS link-local address", full)
		}
		target = full
	} else {
		return Credentials{}, ErrNoCredentials
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Credentials{}, fmt.Errorf("container credentials: %w", err)
	}
	auth := ""
	if f := strings.TrimSpace(getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE")); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return Credentials{}, fmt.Errorf("container credentials: %w", err)
		}
		auth = strings.TrimSpace(string(b))
	} else {
		auth = strings.TrimSpace(getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN"))
	}
	if strings.ContainsAny(auth, "\r\n") {
		return Credentials{}, errors.New("container credentials: authorization token contains a line break")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Accept", "application/json")
	var doc jsonCredentials
	if err := getJSON(httpClient(p.Client, 10*time.Second), req, &doc); err != nil {
		return Credentials{}, fmt.Errorf("container credentials: %w", err)
	}
	return toCredentials("ecs", doc.AccessKeyID, doc.SecretAccessKey, doc.Token, doc.Expiration)
}

func getJSON(client *http.Client, req *http.Request, v any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 256)])))
	}
	return json.Unmarshal(body, v)
}

// IMDSProvider reads the instance profile credentials of an EC2 instance through IMDSv2 (session token first). It is
// disabled by AWS_EC2_METADATA_DISABLED=true. A metadata service that cannot be reached, or an instance without a
// role, is ErrNoCredentials.
type IMDSProvider struct {
	Getenv func(string) string // default os.Getenv
	// Endpoint overrides the metadata service (default AWS_EC2_METADATA_SERVICE_ENDPOINT, else http://169.254.169.254).
	Endpoint string
	Client   *http.Client
}

// Retrieve implements CredentialsProvider.
func (p IMDSProvider) Retrieve(ctx context.Context) (Credentials, error) {
	getenv := getenvFunc(p.Getenv)
	if v, _ := strconv.ParseBool(strings.TrimSpace(getenv("AWS_EC2_METADATA_DISABLED"))); v {
		return Credentials{}, ErrNoCredentials
	}
	base := p.Endpoint
	if base == "" {
		base = strings.TrimSpace(getenv("AWS_EC2_METADATA_SERVICE_ENDPOINT"))
	}
	if base == "" {
		base = "http://169.254.169.254"
	}
	base = strings.TrimSuffix(base, "/")
	client := httpClient(p.Client, 5*time.Second)

	tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodPut, base+"/latest/api/token", nil)
	if err != nil {
		return Credentials{}, fmt.Errorf("instance profile credentials: %w", err)
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Credentials{}, ctx.Err()
		}
		return Credentials{}, ErrNoCredentials // not on EC2 (or the hop limit blocks the token)
	}
	tokenBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusNotFound {
			return Credentials{}, ErrNoCredentials
		}
		return Credentials{}, fmt.Errorf("instance profile credentials: token: HTTP %d", resp.StatusCode)
	}
	token := strings.TrimSpace(string(tokenBody))

	get := func(path string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-aws-ec2-metadata-token", token)
		return client.Do(req)
	}
	resp, err = get("/latest/meta-data/iam/security-credentials/")
	if err != nil {
		return Credentials{}, fmt.Errorf("instance profile credentials: %w", err)
	}
	roles, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Credentials{}, ErrNoCredentials // no instance profile
	}
	if resp.StatusCode/100 != 2 {
		return Credentials{}, fmt.Errorf("instance profile credentials: role list: HTTP %d", resp.StatusCode)
	}
	role, _, _ := strings.Cut(strings.TrimSpace(string(roles)), "\n")
	if role = strings.TrimSpace(role); role == "" {
		return Credentials{}, ErrNoCredentials
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/latest/meta-data/iam/security-credentials/"+url.PathEscape(role), nil)
	if err != nil {
		return Credentials{}, fmt.Errorf("instance profile credentials: %w", err)
	}
	req.Header.Set("X-aws-ec2-metadata-token", token)
	var doc jsonCredentials
	if err := getJSON(client, req, &doc); err != nil {
		return Credentials{}, fmt.Errorf("instance profile credentials: %w", err)
	}
	if doc.Code != "" && doc.Code != "Success" {
		return Credentials{}, fmt.Errorf("instance profile credentials: %s: %s", doc.Code, doc.Message)
	}
	return toCredentials("imds", doc.AccessKeyID, doc.SecretAccessKey, doc.Token, doc.Expiration)
}

// ChainProvider returns the credentials of the first provider that is configured: ErrNoCredentials moves on, any
// other error stops the chain (a misconfigured source is reported instead of silently using the next one).
type ChainProvider []CredentialsProvider

// Retrieve implements CredentialsProvider.
func (c ChainProvider) Retrieve(ctx context.Context) (Credentials, error) {
	for _, p := range c {
		creds, err := p.Retrieve(ctx)
		if errors.Is(err, ErrNoCredentials) {
			continue
		}
		return creds, err
	}
	return Credentials{}, ErrNoCredentials
}

// ChainOptions configures DefaultChain; the zero value uses the process environment and the AWS endpoints.
type ChainOptions struct {
	Getenv func(string) string
	// Region is the fallback region of the STS endpoint (the S3 signing region).
	Region                                    string
	STSEndpoint, ECSEndpoint, IMDSEndpoint    string
	Client                                    *http.Client
	RefreshBefore, RetryInterval, NegativeTTL time.Duration
}

// DefaultChain is the cached chain environment → web identity → container (ECS/EKS) → EC2 instance profile (IMDSv2).
// Shared config/credentials files, SSO and process credentials are not supported (D-116).
func DefaultChain(o ChainOptions) *CachedProvider {
	return &CachedProvider{
		Provider: ChainProvider{
			EnvProvider{Getenv: o.Getenv},
			WebIdentityProvider{Getenv: o.Getenv, Region: o.Region, Endpoint: o.STSEndpoint, Client: o.Client},
			ECSProvider{Getenv: o.Getenv, Endpoint: o.ECSEndpoint, Client: o.Client},
			IMDSProvider{Getenv: o.Getenv, Endpoint: o.IMDSEndpoint, Client: o.Client},
		},
		RefreshBefore: o.RefreshBefore, RetryInterval: o.RetryInterval, NegativeTTL: o.NegativeTTL,
	}
}

// CachedProvider caches expiring credentials and refreshes them RefreshBefore their expiry. One refresh runs at a
// time; while a refresh runs, or when it fails, callers get the cached credentials as long as they have not expired.
// ErrNoCredentials is remembered for NegativeTTL so an environment without credentials is not probed on every request.
type CachedProvider struct {
	Provider      CredentialsProvider
	RefreshBefore time.Duration    // default 5m
	RetryInterval time.Duration    // minimum time between refresh attempts while cached credentials are valid; default 10s
	NegativeTTL   time.Duration    // default 1m
	Now           func() time.Time // default time.Now

	refresh sync.Mutex // held during Provider.Retrieve
	mu      sync.Mutex // guards the fields below
	creds   Credentials
	have    bool
	tried   time.Time // last refresh attempt
	noneTil time.Time // ErrNoCredentials cached until
}

func (c *CachedProvider) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// state reports the cached credentials: usable (not expired) and fresh (no refresh needed).
func (c *CachedProvider) state(now time.Time) (creds Credentials, usable, fresh, none bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.have {
		return Credentials{}, false, false, now.Before(c.noneTil)
	}
	if c.creds.Expires.IsZero() {
		return c.creds, true, true, false
	}
	usable = now.Before(c.creds.Expires)
	fresh = c.creds.Expires.Sub(now) > orDefault(c.RefreshBefore, 5*time.Minute) ||
		usable && now.Sub(c.tried) < orDefault(c.RetryInterval, 10*time.Second)
	return c.creds, usable, fresh, false
}

// Retrieve implements CredentialsProvider.
func (c *CachedProvider) Retrieve(ctx context.Context) (Credentials, error) {
	now := c.now()
	creds, usable, fresh, none := c.state(now)
	if fresh {
		return creds, nil
	}
	if none {
		return Credentials{}, ErrNoCredentials
	}
	if usable {
		if !c.refresh.TryLock() {
			return creds, nil // another caller is refreshing
		}
	} else {
		c.refresh.Lock()
	}
	defer c.refresh.Unlock()
	// A refresh that finished while this caller waited may already have produced fresh credentials.
	now = c.now()
	if creds, usable, fresh, none = c.state(now); fresh {
		return creds, nil
	} else if none {
		return Credentials{}, ErrNoCredentials
	}
	got, err := c.Provider.Retrieve(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tried = c.now()
	switch {
	case err == nil:
		c.creds, c.have, c.noneTil = got, true, time.Time{}
		return got, nil
	case usable && c.now().Before(creds.Expires):
		return creds, nil // serve still-valid credentials; retry after RetryInterval
	case errors.Is(err, ErrNoCredentials):
		c.have, c.creds = false, Credentials{}
		c.noneTil = c.tried.Add(orDefault(c.NegativeTTL, time.Minute))
	}
	return Credentials{}, err
}
