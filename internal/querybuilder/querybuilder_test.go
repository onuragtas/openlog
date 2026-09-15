package querybuilder

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/api/query"
)

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func flt(key, op string, v any) Filter {
	f := Filter{Key: key, Op: op}
	if v != nil {
		f.Value = raw(v)
	}
	return f
}

func fltIn(key, op string, vs ...any) Filter {
	f := Filter{Key: key, Op: op}
	for _, v := range vs {
		f.Values = append(f.Values, raw(v))
	}
	return f
}

func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		sig       Signal
		key       string
		canonical string
		source    Source
		typ       Type
	}{
		{Logs, "service.name", "service.name", SourceField, TString},
		{Logs, "service_name", "service.name", SourceField, TString},
		{Logs, "severity", "severity_text", SourceField, TString},
		{Logs, "severity_number", "severity_number", SourceField, TNumber},
		{Logs, "attributes.http.route", "attributes.http.route", SourceAttribute, TString},
		{Logs, "attr.http.route", "attributes.http.route", SourceAttribute, TString},
		{Logs, "resource.k8s.pod.name", "resource.k8s.pod.name", SourceResource, TString},
		{Logs, "body.user.id", "body.user.id", SourceBody, TString},
		{Logs, "http.route", "http.route", SourceAny, TString},
		{Traces, "duration_ns", "duration_ns", SourceField, TNumber},
		{Traces, "duration.ms", "duration_ms", SourceField, TNumber},
		{Traces, "http.status_code", "http.status_code", SourceField, TNumber},
		{Traces, "is_error", "error", SourceField, TBool},
		{Traces, "transaction_name", "transaction.name", SourceField, TString},
		{Traces, "http.route", "http.route", SourceAny, TString},
		{Metrics, "metricName", "metric.name", SourceField, TString},
		{Metrics, "state", "state", SourceAny, TString},
	} {
		f, err := Resolve(tc.sig, tc.key)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.sig, tc.key, err)
		}
		if f.Key != tc.canonical || f.Source != tc.source || f.Type != tc.typ {
			t.Errorf("%s %s: got %+v", tc.sig, tc.key, f)
		}
	}
	for _, tc := range []struct {
		sig Signal
		key string
	}{
		{Logs, ""},
		{Logs, strings.Repeat("k", MaxKeyBytes+1)},
		{Logs, "a\x00b"},
		{Logs, "tenant_id"},
		{Logs, "body.a'b"},
		{Traces, "body.x"},
		{Logs, "body." + strings.Repeat("a.", MaxBodyPathKeys) + "z"},
	} {
		if _, err := Resolve(tc.sig, tc.key); err == nil {
			t.Errorf("%s %q: expected an error", tc.sig, tc.key)
		}
	}
}

// render builds the condition into a real query so the tenant boundary checks of query.Select run too.
func render(t *testing.T, sig Signal, filters []Filter, groups [][]Filter) (string, map[string]string) {
	t.Helper()
	b := NewBuilder(sig, "qb")
	c, err := b.Where(filters, groups, "")
	if err != nil {
		t.Fatal(err)
	}
	db := query.New(nil, "openlog", 0)
	sc, _ := db.Scope("t1")
	q := sc.From(query.Logs).Columns("count()")
	if c != "" {
		q.Where(c)
	}
	sql, params, err := b.Bind(q).Build()
	if err != nil {
		t.Fatalf("%s: %v", c, err)
	}
	return sql, params
}

func TestConditions(t *testing.T) {
	for _, tc := range []struct {
		f      Filter
		sql    string
		params map[string]string
	}{
		{flt("service.name", "=", "api"), "(service_name = {qb_0:String})", map[string]string{"qb_0": "api"}},
		{flt("severity_number", ">=", 13), "(severity_number >= {qb_0:Float64})", map[string]string{"qb_0": "13"}},
		{flt("severity_number", "=", "17"), "(severity_number = {qb_0:Float64})", map[string]string{"qb_0": "17"}},
		{flt("trace_id", "=", "ABCDEF"), "(trace_id = {qb_0:String})", map[string]string{"qb_0": "abcdef"}},
		{flt("attributes.http.status_code", ">", 499), "(toFloat64OrNull(attributes[{qb_0:String}]) > {qb_1:Float64})",
			map[string]string{"qb_0": "http.status_code", "qb_1": "499"}},
		{flt("resource.k8s.pod.name", "contains", "Web"), "(positionCaseInsensitiveUTF8(resource_attributes[{qb_0:String}], {qb_1:String}) > 0)",
			map[string]string{"qb_0": "k8s.pod.name", "qb_1": "Web"}},
		{flt("http.route", "exists", nil), "((mapContains(attributes, {qb_0:String}) OR mapContains(resource_attributes, {qb_0:String})))",
			map[string]string{"qb_0": "http.route"}},
		{flt("body", "not_contains", "health"), "(NOT (positionCaseInsensitiveUTF8(body, {qb_0:String}) > 0))", map[string]string{"qb_0": "health"}},
		{flt("body.user.id", "=", 42), "(if(JSONType(body, {qb_0:String}, {qb_1:String}) = 'String', JSONExtractString(body, {qb_0:String}, {qb_1:String}), JSONExtractRaw(body, {qb_0:String}, {qb_1:String})) = {qb_2:String})",
			map[string]string{"qb_0": "user", "qb_1": "id", "qb_2": "42"}},
		{fltIn("severity_text", "in", "ERROR", "WARN"), "(has({qb_0:Array(String)}, severity_text))", map[string]string{"qb_0": "['ERROR','WARN']"}},
		{fltIn("severity_number", "not_in", 17, 21), "(NOT (severity_number IN ({qb_0:Float64}, {qb_1:Float64})))", map[string]string{"qb_0": "17", "qb_1": "21"}},
		{flt("attributes.path", "like", "/api/%"), "(attributes[{qb_0:String}] LIKE {qb_1:String})", map[string]string{"qb_1": "/api/%"}},
		{flt("host.name", "not_regex", "^db-[0-9]+$"), "(NOT (match(host_name, {qb_0:String})))", map[string]string{"qb_0": "^db-[0-9]+$"}},
		{flt("attributes.ok", "!=", true), "(attributes[{qb_0:String}] != {qb_1:String})", map[string]string{"qb_1": "true"}},
	} {
		sql, params := render(t, Logs, []Filter{tc.f}, nil)
		if !strings.Contains(sql, tc.sql) {
			t.Errorf("%+v:\n got %s\nwant %s", tc.f, sql, tc.sql)
		}
		for k, v := range tc.params {
			if params[k] != v {
				t.Errorf("%+v: param %s = %q, want %q", tc.f, k, params[k], v)
			}
		}
	}
}

func TestTraceConditions(t *testing.T) {
	for _, tc := range []struct {
		f      Filter
		sql    string
		params map[string]string
	}{
		{flt("duration_ms", ">=", 250), "(divide(duration_ns, 1000000) >= {qb_0:Float64})", map[string]string{"qb_0": "250"}},
		{flt("error", "=", true), "(toString(is_error) = {qb_0:String})", map[string]string{"qb_0": "true"}},
		{fltIn("kind", "in", "server", "consumer"), "(has({qb_0:Array(String)}, toString(kind)))", map[string]string{"qb_0": "['server','consumer']"}},
		{flt("http.status_code", ">", 499), "(http_status_code > {qb_0:Float64})", map[string]string{"qb_0": "499"}},
		{flt("parent_span_id", "not_exists", nil), "(NOT (parent_span_id != ''))", nil},
		{flt("attributes.http.route", "contains", "API"), "(positionCaseInsensitiveUTF8(attributes[{qb_0:String}], {qb_1:String}) > 0)", map[string]string{"qb_1": "API"}},
	} {
		sql, params := render(t, Traces, []Filter{tc.f}, nil)
		if !strings.Contains(sql, tc.sql) {
			t.Errorf("%+v:\n got %s\nwant %s", tc.f, sql, tc.sql)
		}
		for k, v := range tc.params {
			if params[k] != v {
				t.Errorf("%+v: param %s = %q, want %q", tc.f, k, params[k], v)
			}
		}
	}
	if _, err := Resolve(Traces, "body.x"); err == nil {
		t.Error("body keys accepted for traces")
	}
}

// contains matches the needle literally (no LIKE wildcards, no escaping needed) and case-insensitively; OQL CONTAINS
// uses the same fragment (D-122).
func TestContainsIsLiteral(t *testing.T) {
	if got := ContainsSQL("body", "{p:String}"); got != "positionCaseInsensitiveUTF8(body, {p:String}) > 0" {
		t.Errorf("ContainsSQL: %s", got)
	}
	sql, params := render(t, Logs, []Filter{flt("body", "contains", `50%_off\`)}, nil)
	if strings.Contains(sql, "LIKE") || params["qb_0"] != `50%_off\\` && params["qb_0"] != `50%_off\` {
		t.Errorf("contains pattern: %s %v", sql, params)
	}
}

func TestGroups(t *testing.T) {
	sql, _ := render(t, Logs,
		[]Filter{flt("service.name", "=", "api")},
		[][]Filter{{flt("severity_text", "=", "ERROR"), flt("host.id", "=", "h1")}, {flt("body", "contains", "panic")}})
	want := "((service_name = {qb_0:String}) AND (((severity_text = {qb_1:String}) AND (host_id = {qb_2:String})) OR ((positionCaseInsensitiveUTF8(body, {qb_3:String}) > 0))))"
	if !strings.Contains(sql, want) {
		t.Errorf("got %s", sql)
	}
	// An empty group matches everything.
	sql, _ = render(t, Logs, nil, [][]Filter{{flt("host.id", "=", "h1")}, {}})
	if strings.Contains(sql, "host_id") {
		t.Errorf("empty group must drop the OR: %s", sql)
	}
	b := NewBuilder(Logs, "qb")
	c, err := b.Where([]Filter{flt("severity_text", "=", "ERROR"), flt("service.name", "=", "api")}, nil, "severity_text")
	if err != nil || strings.Contains(c, "severity_text") || !strings.Contains(c, "service_name") {
		t.Errorf("skip key: %q %v", c, err)
	}
}

func TestInjectionStaysInParameters(t *testing.T) {
	evil := []string{"x') OR 1=1 --", "a`b", "tenant_id", "x FROM openlog.logs_local", "settings max_execution_time=1", "}{qb_0:String}"}
	for _, e := range evil {
		for _, key := range []string{"attributes." + e, "resource." + e, e} {
			if _, err := Resolve(Logs, key); err != nil {
				continue // rejected keys are fine too
			}
			sql, params := render(t, Logs, []Filter{flt(key, "=", e), fltIn(key, "in", e, "b"), flt(key, "regex", "a|b")}, nil)
			user := sql[strings.Index(sql, "{tenant_id:String})")+len("{tenant_id:String})"):]
			if strings.Contains(user, e) {
				t.Errorf("%q leaked into SQL: %s", e, sql)
			}
			found := false
			for _, v := range params {
				if strings.Contains(v, e) || strings.Contains(v, strings.ReplaceAll(e, "'", `\'`)) {
					found = true
				}
			}
			if !found {
				t.Errorf("%q not bound as a parameter: %v", e, params)
			}
		}
	}
}

func TestValidation(t *testing.T) {
	b := NewBuilder(Logs, "qb")
	bad := []Filter{
		{Key: "service.name", Op: "~"},
		flt("service.name", "exists", "x"),
		fltIn("service.name", "in"),
		flt("service.name", "in", "x"),
		flt("service.name", "=", nil),
		flt("service.name", "=", map[string]string{"a": "b"}),
		flt("service.name", "=", strings.Repeat("v", MaxValueBytes+1)),
		flt("severity_number", "=", "high"),
		flt("attributes.code", ">", "abc"),
		flt("body", "regex", "(unclosed"),
		flt("timestamp", "=", "2026"),
		fltIn("service.name", "=", "a", "b"),
	}
	for _, f := range bad {
		if _, err := b.Condition(f); err == nil {
			t.Errorf("%+v: expected an error", f)
		} else if _, ok := AsError(err); !ok {
			t.Errorf("%+v: not a validation error: %v", f, err)
		}
	}
	many := make([]Filter, MaxConditions+1)
	for i := range many {
		many[i] = flt("host.id", "=", "h")
	}
	if _, err := b.Where(many, nil, ""); err == nil {
		t.Error("too many conditions accepted")
	}
	if _, err := b.Where(nil, make([][]Filter, MaxGroups+1), ""); err == nil {
		t.Error("too many groups accepted")
	}
	vals := make([]any, MaxValues+1)
	for i := range vals {
		vals[i] = "v"
	}
	if _, err := b.Condition(fltIn("host.id", "in", vals...)); err == nil {
		t.Error("too many values accepted")
	}
}

func TestParseFilters(t *testing.T) {
	fs, err := ParseFilters(`[{"key":"service.name","op":"=","value":"api"},{"key":"a","op":"in","values":["x",1]}]`)
	if err != nil || len(fs) != 2 || fs[1].Op != "in" || len(fs[1].Values) != 2 {
		t.Fatalf("%+v %v", fs, err)
	}
	if fs, err := ParseFilters(" "); err != nil || fs != nil {
		t.Errorf("empty: %v %v", fs, err)
	}
	if _, err := ParseFilters(`{"key":"a"}`); err == nil {
		t.Error("object accepted")
	}
}
