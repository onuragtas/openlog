// Package querybuilder turns the structured filter model of the explorers (docs/contracts/api.md "Fields",
// POST /api/v1/logs/query, POST /api/v1/metrics/query; D-118) into ClickHouse conditions for internal/api/query.
//
// Every user-supplied key and value becomes a bound query parameter: SQL fragments only ever contain column names
// from the per-signal field tables below, fixed function names and {name:Type} placeholders. Keys are validated
// (length, no control characters) and values are bounded, so the fragments never trip the tenant-boundary checks of
// query.Select.
package querybuilder

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Signal is a telemetry signal with a field table.
type Signal string

const (
	Logs    Signal = "logs"
	Metrics Signal = "metrics"
	Traces  Signal = "traces"
)

// ParseSignal validates a signal name.
func ParseSignal(s string) (Signal, error) {
	switch Signal(s) {
	case Logs, Metrics, Traces:
		return Signal(s), nil
	}
	return "", invalid("signal must be one of logs, metrics, traces")
}

// Limits of one request.
const (
	MaxKeyBytes     = 256
	MaxValueBytes   = 1024
	MaxValues       = 100
	MaxConditions   = 50
	MaxGroups       = 10
	MaxRegexBytes   = 512
	MaxBodyPathKeys = 8
)

// Source is where a key is read from.
type Source string

const (
	SourceField     Source = "field"
	SourceAttribute Source = "attribute"
	SourceResource  Source = "resource"
	SourceBody      Source = "body"
	// SourceAny is a bare unknown key: the record attribute when present, else the resource attribute.
	SourceAny Source = "any"
)

// Type is the value type of a key.
type Type string

const (
	TString Type = "string"
	TNumber Type = "number"
	TBool   Type = "bool"
)

// Error is a validation error (HTTP 400).
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func invalid(format string, args ...any) error { return &Error{Msg: fmt.Sprintf(format, args...)} }

// AsError reports whether err is a validation error.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// FieldDef is a top-level column of a signal.
type FieldDef struct {
	Key  string
	SQL  string // column expression (no user input)
	Type Type
	// Filterable is false for columns that are only shown (timestamps: the time range filters them).
	Filterable bool
	// Lower lower-cases values of = / != / in / not_in (hex ids).
	Lower bool
}

func str(key, sql string) FieldDef {
	return FieldDef{Key: key, SQL: sql, Type: TString, Filterable: true}
}
func num(key, sql string) FieldDef {
	return FieldDef{Key: key, SQL: sql, Type: TNumber, Filterable: true}
}
func hexID(key, sql string) FieldDef {
	return FieldDef{Key: key, SQL: sql, Type: TString, Filterable: true, Lower: true}
}
func boolean(key, sql string) FieldDef {
	return FieldDef{Key: key, SQL: sql, Type: TBool, Filterable: true}
}
func shown(key, sql string) FieldDef { return FieldDef{Key: key, SQL: sql, Type: TString} }

// Fields are the top-level fields of each signal, in display order. Aliases are accepted in keys but not listed.
var Fields = map[Signal][]FieldDef{
	Logs: {
		shown("timestamp", "toString(timestamp)"),
		str("body", "body"),
		str("severity_text", "severity_text"),
		num("severity_number", "severity_number"),
		str("service.name", "service_name"),
		str("host.id", "host_id"),
		str("host.name", "host_name"),
		hexID("trace_id", "trace_id"),
		hexID("span_id", "span_id"),
		num("trace_flags", "trace_flags"),
		str("event.name", "event_name"),
		str("scope.name", "scope_name"),
		shown("observed_timestamp", "toString(observed_timestamp)"),
	},
	Traces: {
		shown("timestamp", "toString(timestamp)"),
		str("name", "name"),
		str("kind", "toString(kind)"),
		str("status_code", "toString(status_code)"),
		str("status_message", "status_message"),
		str("service.name", "service_name"),
		str("service.namespace", "service_namespace"),
		str("deployment.environment", "deployment_environment"),
		str("host.id", "host_id"),
		hexID("trace_id", "trace_id"),
		hexID("span_id", "span_id"),
		hexID("parent_span_id", "parent_span_id"),
		num("duration_ns", "duration_ns"),
		num("duration_ms", "divide(duration_ns, 1000000)"),
		num("http.status_code", "http_status_code"),
		boolean("is_entry", "is_entry"),
		boolean("error", "is_error"),
		str("transaction.name", "transaction_name"),
		str("transaction.type", "transaction_type"),
		str("db.system", "db_system"),
		str("peer.name", "peer_name"),
		str("scope.name", "scope_name"),
	},
	Metrics: {
		str("metric.name", "metric_name"),
		str("metric.type", "toString(metric_type)"),
		str("unit", "unit"),
		str("service.name", "service_name"),
		str("host.id", "host_id"),
		str("host.name", "host_name"),
		str("scope.name", "scope_name"),
		num("value", "value"),
	},
}

var aliases = map[Signal]map[string]string{
	Logs: {"severity": "severity_text", "message": "body", "trace.id": "trace_id", "span.id": "span_id",
		"service_name": "service.name", "host_id": "host.id", "host_name": "host.name", "event_name": "event.name", "scope_name": "scope.name"},
	Traces: {"trace.id": "trace_id", "span.id": "span_id", "parent.id": "parent_span_id", "service_name": "service.name",
		"host_id": "host.id", "span.name": "name", "scope_name": "scope.name", "duration.ms": "duration_ms", "status.code": "status_code",
		"status.message": "status_message", "service_namespace": "service.namespace", "deployment_environment": "deployment.environment",
		"http_status_code": "http.status_code", "is_error": "error", "entry": "is_entry", "transaction_name": "transaction.name",
		"transaction_type": "transaction.type", "db_system": "db.system", "peer_name": "peer.name"},
	Metrics: {"metricName": "metric.name", "metric_name": "metric.name", "service_name": "service.name", "host_id": "host.id",
		"host_name": "host.name", "scope_name": "scope.name"},
}

// Field is a resolved key.
type Field struct {
	Key    string // canonical key
	Name   string // key without the source prefix
	Source Source
	Type   Type
	def    *FieldDef
	path   []string // SourceBody: JSON path
}

// Def returns the top-level field definition (nil for map and body keys).
func (f *Field) Def() *FieldDef { return f.def }

// IsMap reports whether the field reads an attribute map.
func (f *Field) IsMap() bool {
	return f.Source == SourceAttribute || f.Source == SourceResource || f.Source == SourceAny
}

var bodyKeyRe = regexp.MustCompile(`^[A-Za-z0-9_\-@$:]+$`)

// ValidKey checks the length and characters of a key.
func ValidKey(key string) error {
	if key == "" {
		return invalid("key must not be empty")
	}
	if len(key) > MaxKeyBytes || !utf8.ValidString(key) || strings.ContainsFunc(key, unicode.IsControl) {
		return invalid("key %q must be at most %d bytes of UTF-8 without control characters", clip(key), MaxKeyBytes)
	}
	return nil
}

// Resolve maps a key of signal to a field: top-level fields (and aliases), attributes.<k>, resource.<k>, body.<k>
// (logs) or a bare attribute key.
func Resolve(sig Signal, key string) (*Field, error) {
	if err := ValidKey(key); err != nil {
		return nil, err
	}
	if a, ok := aliases[sig][key]; ok {
		key = a
	}
	for i := range Fields[sig] {
		if d := &Fields[sig][i]; d.Key == key {
			return &Field{Key: key, Name: key, Source: SourceField, Type: d.Type, def: d}, nil
		}
	}
	if strings.EqualFold(key, "tenant_id") {
		return nil, invalid("unknown key %q", key)
	}
	if k, ok := strings.CutPrefix(key, "attributes."); ok && k != "" {
		return &Field{Key: key, Name: k, Source: SourceAttribute, Type: TString}, nil
	}
	if k, ok := strings.CutPrefix(key, "attr."); ok && k != "" {
		return &Field{Key: "attributes." + k, Name: k, Source: SourceAttribute, Type: TString}, nil
	}
	if k, ok := strings.CutPrefix(key, "resource."); ok && k != "" {
		return &Field{Key: key, Name: k, Source: SourceResource, Type: TString}, nil
	}
	if k, ok := strings.CutPrefix(key, "body."); ok && k != "" {
		if sig != Logs {
			return nil, invalid("body keys are only available for logs")
		}
		path := strings.Split(k, ".")
		if len(path) > MaxBodyPathKeys {
			return nil, invalid("body key %q: at most %d path elements", clip(key), MaxBodyPathKeys)
		}
		for _, p := range path {
			if !bodyKeyRe.MatchString(p) {
				return nil, invalid("body key %q: path elements may contain letters, digits and _ - @ $ : only", clip(key))
			}
		}
		return &Field{Key: key, Name: k, Source: SourceBody, Type: TString, path: path}, nil
	}
	return &Field{Key: key, Name: key, Source: SourceAny, Type: TString}, nil
}

// Filter is one condition of the request body.
type Filter struct {
	Key    string            `json:"key"`
	Op     string            `json:"op"`
	Value  json.RawMessage   `json:"value,omitempty"`
	Values []json.RawMessage `json:"values,omitempty"`
}

// Ops lists the filter operators.
var Ops = []string{"=", "!=", "in", "not_in", "contains", "not_contains", "like", "not_like", "regex", "not_regex",
	"exists", "not_exists", ">", ">=", "<", "<="}

var opSet = func() map[string]bool {
	m := map[string]bool{}
	for _, o := range Ops {
		m[o] = true
	}
	return m
}()

// ParseFilters decodes a JSON array of filters (GET query parameters).
func ParseFilters(s string) ([]Filter, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var fs []Filter
	if err := json.Unmarshal([]byte(s), &fs); err != nil {
		return nil, invalid("filters must be a JSON array of {key, op, value | values}")
	}
	return fs, nil
}

// ParseGroups decodes a JSON array of filter arrays (OR of AND-groups, GET query parameters).
func ParseGroups(s string) ([][]Filter, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var gs [][]Filter
	if err := json.Unmarshal([]byte(s), &gs); err != nil {
		return nil, invalid("groups must be a JSON array of arrays of {key, op, value | values}")
	}
	return gs, nil
}

// scalar decodes a JSON string, number or boolean into its text.
func scalar(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strings.TrimSpace(string(raw)), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

func clip(s string) string {
	if len(s) <= 64 {
		return s
	}
	cut := 64
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
