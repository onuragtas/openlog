package objstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3 stores objects under BaseURL (bucket and prefix included, ending with "/"): path-style
// "https://s3.example.com/bucket/prefix/" or virtual-hosted "https://bucket.s3.eu-west-1.amazonaws.com/prefix/".
// Requests are signed with AWS Signature Version 4 when AccessKeyID is set.
type S3 struct {
	BaseURL         string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	Client          *http.Client
	Now             func() time.Time
}

// Kind implements Store.
func (s *S3) Kind() string { return "s3" }

func (s *S3) objectURL(key string) (*url.URL, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	base, err := url.Parse(s.BaseURL)
	if err != nil || base.Host == "" || !strings.HasSuffix(base.Path, "/") {
		return nil, fmt.Errorf("invalid S3 base URL %q", s.BaseURL)
	}
	segs := strings.Split(strings.Trim(base.Path, "/")+"/"+key, "/")
	var raw strings.Builder
	var plain strings.Builder
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		raw.WriteString("/" + uriEncode(seg))
		plain.WriteString("/" + seg)
	}
	return &url.URL{Scheme: base.Scheme, Host: base.Host, Path: plain.String(), RawPath: raw.String()}, nil
}

func (s *S3) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s *S3) do(req *http.Request, payloadHash string) (*http.Response, error) {
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if s.AccessKeyID != "" {
		now := time.Now
		if s.Now != nil {
			now = s.Now
		}
		region := s.Region
		if region == "" {
			region = "us-east-1"
		}
		SignV4(req, payloadHash, s.AccessKeyID, s.SecretAccessKey, region, now())
	}
	return s.client().Do(req)
}

func responseError(op string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("s3 %s: HTTP %d: %s", op, resp.StatusCode, strings.TrimSpace(string(b)))
}

// Put implements Store. The payload is hashed first (a single signed PUT, at most 5 GiB).
func (s *S3) Put(ctx context.Context, key string, r io.ReadSeeker, size int64) error {
	u, err := s.objectURL(key)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(r, size)); err != nil {
		return err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), io.NopCloser(io.LimitReader(r, size)))
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/zip")
	resp, err := s.do(req, hex.EncodeToString(h.Sum(nil)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return responseError("put", resp)
	}
	return nil
}

// Open implements Store.
func (s *S3) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	u, err := s.objectURL(key)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := s.do(req, emptyHash)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, 0, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, 0, responseError("get", resp)
	}
	return resp.Body, resp.ContentLength, nil
}

// Delete implements Store.
func (s *S3) Delete(ctx context.Context, key string) error {
	u, err := s.objectURL(key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := s.do(req, emptyHash)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return responseError("delete", resp)
	}
	return nil
}

const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// uriEncode is the SigV4 URI encoding of one path segment (unreserved characters kept).
func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// SignV4 adds X-Amz-Date and the Authorization header of AWS Signature Version 4 (service s3) to req. The host,
// Range, Content-Type and every X-Amz-* header are signed; payloadHash is the hex sha256 of the body.
func SignV4(req *http.Request, payloadHash, accessKeyID, secretAccessKey, region string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)

	headers := map[string]string{"host": req.URL.Host}
	if req.Host != "" {
		headers["host"] = req.Host
	}
	for name, vals := range req.Header {
		ln := strings.ToLower(name)
		if strings.HasPrefix(ln, "x-amz-") || ln == "range" || ln == "content-type" || ln == "content-md5" {
			headers[ln] = strings.Join(strings.Fields(strings.Join(vals, ",")), " ")
		}
	}
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + headers[n] + "\n")
	}
	signed := strings.Join(names, ";")

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	q := req.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var query []string
	for _, k := range keys {
		vs := q[k]
		sort.Strings(vs)
		for _, v := range vs {
			query = append(query, uriEncode(k)+"="+uriEncode(v))
		}
	}
	canonical := strings.Join([]string{req.Method, path, strings.Join(query, "&"), canonHeaders.String(), signed, payloadHash}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	key := hmacSHA256(hmacSHA256(hmacSHA256(hmacSHA256([]byte("AWS4"+secretAccessKey), day), region), "s3"), "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}
