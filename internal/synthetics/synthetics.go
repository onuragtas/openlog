// Package synthetics implements scheduled outside-in checks (D-132): HTTP checks defined per organization in
// PostgreSQL (migrations/postgres/0090_synthetics.sql), run on schedule by the api leader, stored per run in
// ClickHouse (schema 0093_synthetic_runs) and mirrored as OTLP metric rows, so the existing metric alert rules
// and dashboards see them like any other metric (docs/contracts/alerting.md §2.2).
//
// Layout: synthetics.go (model + validation), checker.go (one run: SSRF guard, timings, assertions),
// scheduler.go (due selection, leader task, concurrency limits), results.go (ClickHouse writer and reader),
// pgstore.go (PostgreSQL store).
package synthetics

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Check types. Only http exists; the column and this list are the extension point (tcp, dns, browser).
const (
	TypeHTTP = "http"
)

// LocationLocal is the built-in location: the openlog server itself. Locations are stored as an array on the
// check and as one schedule row per check and location, so a remote runner can be added without changing the
// tables (D-132).
const LocationLocal = "local"

// Locations are the locations a check may use, in display order.
var Locations = []string{LocationLocal}

// Assertion kinds on the response body.
const (
	AssertNone        = "none"
	AssertContains    = "contains"
	AssertNotContains = "not_contains"
	AssertJSONPath    = "json_path"
)

// Methods a check may use. HEAD and GET carry no body.
var methods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true}

// Headers the client sets itself; a check must not override them.
var reservedHeaders = map[string]bool{"host": true, "content-length": true, "connection": true, "transfer-encoding": true, "upgrade": true}

// Limits of a definition. The timeout must fit inside the interval, so a check never overlaps itself.
const (
	MaxNameRunes     = 200
	MaxURLBytes      = 2048
	MaxHeaders       = 20
	MaxHeaderKey     = 128
	MaxHeaderValue   = 1024
	MaxBodyBytes     = 64 << 10
	MaxExpectStatus  = 10
	MaxAssertBytes   = 1024
	MinTimeoutMs     = 500
	MaxTimeoutMs     = 60000
	MinIntervalSecs  = 30
	MaxIntervalSecs  = 86400
	MaxLocations     = 10
	MaxPerOrg        = 100
	DefaultTimeoutMs = 10000
	DefaultInterval  = 300
)

var (
	// ErrNotFound is returned for an unknown check of the organization.
	ErrNotFound = errors.New("synthetic check not found")
	// ErrLimit reports that the organization already has MaxPerOrg checks.
	ErrLimit = errors.New("synthetic check limit reached")
)

// ValidationError is an invalid definition (400 invalid_argument with the field path).
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}

func invalid(field, msg string) error { return &ValidationError{Field: field, Msg: msg} }

// Actor is who changed a definition (audit log): a signed-in user, or an API key
// acting with its own role (D-133), in which case UserID and Email are empty.
type Actor struct {
	UserID     string
	Email      string
	IP         string
	APIKeyID   string
	APIKeyName string
}

// Input is the writable part of a check.
type Input struct {
	Name            string            `json:"name"`
	Type            string            `json:"type"`
	Enabled         bool              `json:"enabled"`
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	ExpectedStatus  []int             `json:"expected_status"`
	AssertionType   string            `json:"assertion_type"`
	AssertionPath   string            `json:"assertion_path"`
	AssertionValue  string            `json:"assertion_value"`
	TimeoutMs       int               `json:"timeout_ms"`
	IntervalSeconds int               `json:"interval_seconds"`
	Locations       []string          `json:"locations"`
}

// Check is a stored definition with the last run per location.
type Check struct {
	ID    string
	OrgID string
	Input
	CreatedByEmail string
	UpdatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// Status is the schedule row per location (the last run and when the next one is due).
	Status []LocationStatus
}

// LocationStatus is what the scheduler recorded for one check and location.
type LocationStatus struct {
	Location       string
	NextRunAt      time.Time
	LastRunAt      *time.Time
	LastSuccess    *bool
	LastStatusCode int
	LastDurationMs float64
	LastErrorKind  string
	LastError      string
}

// Timeout is the per-run timeout.
func (in Input) Timeout() time.Duration { return time.Duration(in.TimeoutMs) * time.Millisecond }

// Interval is how often the check runs per location.
func (in Input) Interval() time.Duration { return time.Duration(in.IntervalSeconds) * time.Second }

// HasBody reports whether the method sends the body.
func (in Input) HasBody() bool { return in.Method != "GET" && in.Method != "HEAD" }

// ExpectsStatus reports whether code is one of the expected codes.
func (in Input) ExpectsStatus(code int) bool {
	for _, c := range in.ExpectedStatus {
		if c == code {
			return true
		}
	}
	return false
}

// Validate checks and normalizes an input (the API and the store both call it; the database repeats the
// bounds as CHECK constraints).
func (in *Input) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > MaxNameRunes || !utf8.ValidString(in.Name) {
		return invalid("name", "must be 1-200 characters")
	}
	if in.Type == "" {
		in.Type = TypeHTTP
	}
	if in.Type != TypeHTTP {
		return invalid("type", "must be http")
	}
	if err := in.validateRequest(); err != nil {
		return err
	}
	if err := in.validateAssertion(); err != nil {
		return err
	}
	if in.TimeoutMs == 0 {
		in.TimeoutMs = DefaultTimeoutMs
	}
	if in.TimeoutMs < MinTimeoutMs || in.TimeoutMs > MaxTimeoutMs {
		return invalid("timeout_ms", "must be between 500 and 60000 milliseconds")
	}
	if in.IntervalSeconds == 0 {
		in.IntervalSeconds = DefaultInterval
	}
	if in.IntervalSeconds < MinIntervalSecs || in.IntervalSeconds > MaxIntervalSecs {
		return invalid("interval_seconds", "must be between 30 and 86400 seconds")
	}
	// A run must finish before the next one is due, so a slow target cannot pile up runs.
	if in.Timeout() > in.Interval() {
		return invalid("timeout_ms", "must not be longer than interval_seconds")
	}
	return in.validateLocations()
}

func (in *Input) validateRequest() error {
	in.URL = strings.TrimSpace(in.URL)
	if _, err := ParseTargetURL(in.URL); err != nil {
		return invalid("url", err.Error())
	}
	in.Method = strings.ToUpper(strings.TrimSpace(in.Method))
	if in.Method == "" {
		in.Method = "GET"
	}
	if !methods[in.Method] {
		return invalid("method", "must be one of GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
	}
	if len(in.Headers) > MaxHeaders {
		return invalid("headers", "at most 20 headers")
	}
	clean := map[string]string{}
	for k, v := range in.Headers {
		k = strings.TrimSpace(k)
		if k == "" || len(k) > MaxHeaderKey || !headerToken(k) {
			return invalid("headers", "header names must be 1-128 characters of a-z, A-Z, 0-9 and -_")
		}
		if reservedHeaders[strings.ToLower(k)] {
			return invalid("headers", "the header "+k+" is set by openlog and cannot be overridden")
		}
		if len(v) > MaxHeaderValue || strings.ContainsAny(v, "\r\n") {
			return invalid("headers", "header values must be at most 1024 characters without line breaks")
		}
		clean[k] = v
	}
	in.Headers = clean
	if len(in.Body) > MaxBodyBytes {
		return invalid("body", "at most 65536 bytes")
	}
	if in.Body != "" && !in.HasBody() {
		return invalid("body", "a "+in.Method+" request sends no body")
	}
	if len(in.ExpectedStatus) == 0 {
		in.ExpectedStatus = []int{200}
	}
	if len(in.ExpectedStatus) > MaxExpectStatus {
		return invalid("expected_status", "at most 10 status codes")
	}
	seen := map[int]bool{}
	codes := make([]int, 0, len(in.ExpectedStatus))
	for _, c := range in.ExpectedStatus {
		if c < 100 || c > 599 {
			return invalid("expected_status", "status codes must be between 100 and 599")
		}
		if !seen[c] {
			seen[c] = true
			codes = append(codes, c)
		}
	}
	sort.Ints(codes)
	in.ExpectedStatus = codes
	return nil
}

func (in *Input) validateAssertion() error {
	if in.AssertionType == "" {
		in.AssertionType = AssertNone
	}
	in.AssertionPath = strings.TrimSpace(in.AssertionPath)
	switch in.AssertionType {
	case AssertNone:
		in.AssertionPath, in.AssertionValue = "", ""
		return nil
	case AssertContains, AssertNotContains:
		if in.AssertionValue == "" {
			return invalid("assertion_value", "required for a "+in.AssertionType+" assertion")
		}
		in.AssertionPath = ""
	case AssertJSONPath:
		if in.AssertionPath == "" {
			return invalid("assertion_path", "required for a json_path assertion")
		}
		if !jsonPathValid(in.AssertionPath) {
			return invalid("assertion_path", "must be a dotted path such as data.items.0.status")
		}
	default:
		return invalid("assertion_type", "must be none, contains, not_contains or json_path")
	}
	if len(in.AssertionValue) > MaxAssertBytes || len(in.AssertionPath) > MaxAssertBytes {
		return invalid("assertion_value", "at most 1024 bytes")
	}
	return nil
}

func (in *Input) validateLocations() error {
	if len(in.Locations) == 0 {
		in.Locations = []string{LocationLocal}
	}
	if len(in.Locations) > MaxLocations {
		return invalid("locations", "at most 10 locations")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in.Locations))
	for _, l := range in.Locations {
		if !knownLocation(l) {
			return invalid("locations", "unknown location "+strconv.Quote(l)+" ("+strings.Join(Locations, ", ")+")")
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	in.Locations = out
	return nil
}

func knownLocation(l string) bool {
	for _, k := range Locations {
		if k == l {
			return true
		}
	}
	return false
}

func headerToken(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// ParseTargetURL validates the URL a check requests: absolute http(s), no credentials, no fragment. Whether
// the address it resolves to may be requested is decided per connection by the checker (SSRF guard).
func ParseTargetURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("required")
	}
	if len(raw) > MaxURLBytes {
		return nil, errors.New("the URL is too long")
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, errors.New("invalid URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, errors.New("must be an http or https URL")
	case u.Host == "":
		return nil, errors.New("must be an absolute URL")
	case u.User != nil:
		return nil, errors.New("must not contain credentials")
	case u.Fragment != "":
		return nil, errors.New("must not contain a fragment")
	}
	return u, nil
}

// jsonPathValid accepts dotted paths of object keys and array indexes (data.items.0.status).
func jsonPathValid(path string) bool {
	if strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
		return false
	}
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			return false
		}
	}
	return true
}

// JSONPath returns the value at a dotted path of a decoded JSON document as a string ("true", "3", "x"),
// and whether the path exists. Objects and arrays are returned as their compact JSON.
func JSONPath(doc any, path string) (string, bool) {
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[seg]
			if !ok {
				return "", false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return "", false
			}
			cur = node[i]
		default:
			return "", false
		}
	}
	switch v := cur.(type) {
	case nil:
		return "null", true
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}

// Store persists check definitions per organization (PostgreSQL: PGStore).
type Store interface {
	// List returns the checks of the organization ordered by name, each with its schedule rows.
	List(ctx context.Context, orgID string) ([]Check, error)
	// Get returns one check (ErrNotFound when it belongs to another organization).
	Get(ctx context.Context, orgID, id string) (*Check, error)
	// Create stores a new check (ErrLimit at MaxPerOrg) and writes the audit event synthetic_check.create.
	Create(ctx context.Context, orgID string, in Input, actor Actor) (*Check, error)
	// Update replaces the writable fields and writes the audit event synthetic_check.update.
	Update(ctx context.Context, orgID, id string, in Input, actor Actor) (*Check, error)
	// Delete removes a check and writes the audit event synthetic_check.delete.
	Delete(ctx context.Context, orgID, id string, actor Actor) error
}

// Due is one claimed run: the definition, the tenant its results belong to and the location to run it from.
type Due struct {
	Check    Check
	TenantID string
	Location string
}

// ScheduleStore is the scheduler's half of the store (scheduler.go).
type ScheduleStore interface {
	// Claim moves the next_run_at of at most limit due schedule rows forward by their interval and returns
	// them. Claiming and advancing happen in one statement, so two schedulers never run the same check twice.
	Claim(ctx context.Context, now time.Time, limit int) ([]Due, error)
	// Record stores the outcome of a run on its schedule row (the list view's current status).
	Record(ctx context.Context, r Result) error
}
