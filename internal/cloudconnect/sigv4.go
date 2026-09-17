package cloudconnect

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 for the CloudWatch requests of aws.go.
//
// internal/objstore has a signer too, but it is bound to the s3 service (the credential scope and the signing
// key both hard-code it) and signs only the header set S3 needs. Rather than widen a working storage signer
// for a second caller, this one takes the service as a parameter and signs every header it is given, which is
// what the monitoring APIs require (they sign content-type on a POST body). The algorithm is the same;
// sigv4_test.go pins the signed header set, the credential scope and the fact that the signature follows the
// body and the query, so a change to any of them is caught.

// signV4 adds X-Amz-Date, the Authorization header and, for temporary credentials, X-Amz-Security-Token to
// req. payloadHash is the hex sha256 of the body; host, content-type and every x-amz-* header are signed.
func signV4(req *http.Request, payloadHash string, creds Credentials, service, region string, now time.Time) {
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}
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
		if strings.HasPrefix(ln, "x-amz-") || ln == "content-type" {
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
	canonical := strings.Join([]string{req.Method, path, canonicalQuery(req), canonHeaders.String(), signed, payloadHash}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	key := hmacSHA256(hmacSHA256(hmacSHA256(hmacSHA256([]byte("AWS4"+creds.SecretAccessKey), day), region), service), "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+creds.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
}

// canonicalQuery renders the query string sorted by name and then value, each part URI-encoded.
func canonicalQuery(req *http.Request) string {
	q := req.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k)+"="+uriEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode percent-encodes everything outside the unreserved set, as SigV4 requires.
func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
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

// sha256Hex is the payload hash of a signed request.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
