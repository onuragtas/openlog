package cloudconnect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Shared HTTP behaviour of the three provider clients: one place that turns a provider answer into the error
// kinds of provider.go, and one retry loop that backs off when a provider rate-limits us.

// Retry policy of one provider request.
const (
	// maxAttempts includes the first try, so a throttled request is retried at most three times.
	maxAttempts = 4
	baseBackoff = 500 * time.Millisecond
	maxBackoff  = 8 * time.Second
	// maxErrorBody is how much of an error response is read to build the message.
	maxErrorBody = 4 << 10
	// maxResponseBytes caps a provider answer; these APIs page, so a single page is small.
	maxResponseBytes = 16 << 20
)

// doJSON sends req and decodes a JSON answer into out. It takes an API-call slot from the budget first, so a
// poll cannot exceed its request cap, and classifies every non-2xx answer as a ThrottleError, an AuthError or
// an APIError.
func doJSON(ctx context.Context, client *http.Client, b *Budget, provider string, req *http.Request, out any) error {
	if b != nil && !b.TakeCall() {
		return fmt.Errorf("%w: the API call cap of this poll is reached", ErrBudget)
	}
	res, err := client.Do(req.WithContext(ctx))
	if err != nil {
		// A transport failure is not a provider answer; it is worth one retry like a 5xx.
		return &APIError{Provider: provider, Status: 0, Msg: transportMessage(err)}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxErrorBody))
		_ = res.Body.Close()
	}()
	if res.StatusCode/100 != 2 {
		return responseError(provider, res)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(out); err != nil {
		return &APIError{Provider: provider, Status: res.StatusCode, Msg: "the answer could not be decoded: " + err.Error()}
	}
	return nil
}

// transportMessage keeps a dial or TLS failure short and free of the request URL (which carries the account).
func transportMessage(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "the request timed out"
	}
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return inner.Error()
		}
	}
	return err.Error()
}

// responseError classifies a non-2xx answer. The message is the provider's own error text, never the request:
// a URL or a header could carry an account id or a signature.
func responseError(provider string, res *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	msg := providerMessage(body)
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		return &ThrottleError{Provider: provider, RetryAfter: retryAfter(res), Msg: msg}
	case res.StatusCode == http.StatusUnauthorized, res.StatusCode == http.StatusForbidden:
		return &AuthError{Provider: provider, Msg: authMessage(res.StatusCode, msg)}
	}
	// AWS answers a throttled request with 400 and an error code rather than 429.
	if isThrottleCode(msg) || isThrottleCode(res.Header.Get("x-amzn-ErrorType")) {
		return &ThrottleError{Provider: provider, RetryAfter: retryAfter(res), Msg: msg}
	}
	return &APIError{Provider: provider, Status: res.StatusCode, Msg: msg}
}

func authMessage(status int, msg string) string {
	if msg != "" {
		return msg
	}
	if status == http.StatusUnauthorized {
		return "the credentials were rejected"
	}
	return "the credentials are not allowed to read these metrics"
}

// throttleCodes are the provider error codes that mean "slow down" outside a 429.
var throttleCodes = []string{"throttling", "throttled", "requestlimitexceeded", "toomanyrequests",
	"rate_limit", "ratelimitexceeded", "slowdown", "resource_exhausted"}

func isThrottleCode(s string) bool {
	l := strings.ToLower(s)
	for _, c := range throttleCodes {
		if strings.Contains(l, c) {
			return true
		}
	}
	return false
}

// retryAfter reads the provider's Retry-After hint (seconds, or an HTTP date).
func retryAfter(res *http.Response) time.Duration {
	v := strings.TrimSpace(res.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// providerErrorBody covers the error shapes of the three providers: AWS JSON ({"__type","message"}), Azure
// ({"error":{"code","message"}}) and GCP ({"error":{"status","message"}}).
type providerErrorBody struct {
	Type    string `json:"__type"`
	Message string `json:"message"`
	Msg     string `json:"Message"`
	Code    string `json:"code"`
	Error   struct {
		Code    any    `json:"code"`
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

// providerMessage extracts a short, quotable message from an error body.
func providerMessage(body []byte) string {
	var b providerErrorBody
	if err := json.Unmarshal(body, &b); err == nil {
		for _, s := range []string{b.Error.Message, b.Message, b.Msg} {
			if s = strings.TrimSpace(s); s != "" {
				return withCode(errorCode(b), s)
			}
		}
		if c := errorCode(b); c != "" {
			return c
		}
	}
	// Not JSON (an XML or HTML error page): keep one short line.
	line := strings.TrimSpace(string(body))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return truncate(line, 200)
}

func errorCode(b providerErrorBody) string {
	switch c := b.Error.Code.(type) {
	case string:
		if c != "" {
			return c
		}
	case float64:
		// GCP reports a numeric code next to the status.
		if b.Error.Status != "" {
			return b.Error.Status
		}
	}
	if b.Error.Status != "" {
		return b.Error.Status
	}
	if b.Code != "" {
		return b.Code
	}
	// AWS puts the code in __type, sometimes prefixed ("com.amazonaws...#Throttling").
	if t := b.Type; t != "" {
		if i := strings.LastIndexByte(t, '#'); i >= 0 {
			return t[i+1:]
		}
		return t
	}
	return ""
}

func withCode(code, msg string) string {
	if code == "" || strings.Contains(msg, code) {
		return truncate(msg, 200)
	}
	return truncate(code+": "+msg, 200)
}

// retryRequest runs fn until it succeeds, until the error is not worth retrying, or until the attempts run
// out. A throttle waits for the provider's Retry-After when it gave one, else an exponential backoff with
// jitter, so many connections that are throttled at once do not come back in lockstep.
func retryRequest(ctx context.Context, b *Budget, fn func() error) error {
	var err error
	for attempt := range maxAttempts {
		if err = fn(); err == nil {
			return nil
		}
		// A budget refusal is final: waiting does not give the poll more requests.
		if errors.Is(err, ErrBudget) || !retryable(err) || attempt == maxAttempts-1 {
			return err
		}
		var te *ThrottleError
		if errors.As(err, &te) && b != nil {
			b.Throttle()
		}
		wait := backoffFor(err, attempt)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
	return err
}

// backoffFor is the provider's own hint when it gave one, else base * 2^attempt with up to 25% jitter,
// capped at maxBackoff.
func backoffFor(err error, attempt int) time.Duration {
	var te *ThrottleError
	if errors.As(err, &te) && te.RetryAfter > 0 {
		return min(te.RetryAfter, maxBackoff)
	}
	d := min(baseBackoff<<attempt, maxBackoff)
	return d + time.Duration(rand.Int64N(int64(d/4)+1))
}

// jsonRequest builds a POST with a JSON body and returns the body bytes for signing.
func jsonRequest(method, url string, body any) (*http.Request, []byte, error) {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		raw = b
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, raw, nil
}
