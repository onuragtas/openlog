package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The read-only tool set (docs/operations/mcp.md §3). Every tool is one or two GET/POST requests to
// /api/v1 with the caller's credentials; none of them writes. Descriptions are read by a model, so they
// name the units, the defaults and the limits a query runs into.

// ---- argument types ----

type runOQLArgs struct {
	Query string `json:"query" jsonschema:"OQL query, e.g. \"SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name SINCE 1 hour ago\". Every selected item must be an aggregate (count, sum, average, min, max, uniqueCount, percentile, median, latest, earliest, rate, filter, histogram); raw event listing is not possible - use search_logs for records. Event types: Log, Span, Transaction, Metric, Host, Container. Clauses: WHERE, FACET (max 5 attributes), SINCE, UNTIL, TIMESERIES, LIMIT, COMPARE WITH. Call oql_schema for the attributes of an event type."`
	From  string `json:"from,omitempty" jsonschema:"Start of the range as RFC3339 (2026-09-16T10:00:00Z) or unix milliseconds, overriding SINCE. Must be given together with 'to'."`
	To    string `json:"to,omitempty" jsonschema:"End of the range as RFC3339 or unix milliseconds, overriding UNTIL. Must be given together with 'from'."`
	// Variables bind {{name}} placeholders; several values turn = into IN (oql.md §3).
	Variables map[string][]string `json:"variables,omitempty" jsonschema:"Values for {{name}} placeholders in the query. Each name maps to a list of values; several values turn = into IN. A missing or empty variable makes its predicate true (no filter)."`
}

type oqlSchemaArgs struct {
	EventType string `json:"event_type,omitempty" jsonschema:"Event type whose live attribute keys should be sampled: Log, Span, Transaction, Metric, Host or Container. Without it only the static schema (event types, attributes, functions, keywords) is returned, which needs no query."`
}

type listServicesArgs struct {
	From        string `json:"from,omitempty" jsonschema:"Start of the range. RFC3339 or unix milliseconds. Defaults to the last hour."`
	To          string `json:"to,omitempty" jsonschema:"End of the range. RFC3339 or unix milliseconds. Defaults to now."`
	Namespace   string `json:"namespace,omitempty" jsonschema:"Exact service.namespace. Omit for every namespace (aggregated); an empty string matches services without a namespace."`
	Environment string `json:"environment,omitempty" jsonschema:"Exact deployment.environment, e.g. prod. Omit for every environment (aggregated)."`
	Query       string `json:"q,omitempty" jsonschema:"Case-insensitive substring of the service name."`
}

type serviceSummaryArgs struct {
	Service     string `json:"service" jsonschema:"Exact service.name, as returned by list_services."`
	From        string `json:"from,omitempty" jsonschema:"Start of the range. RFC3339 or unix milliseconds. Defaults to the last hour."`
	To          string `json:"to,omitempty" jsonschema:"End of the range. RFC3339 or unix milliseconds. Defaults to now."`
	Namespace   string `json:"namespace,omitempty" jsonschema:"Exact service.namespace. Omit for every namespace of the service."`
	Environment string `json:"environment,omitempty" jsonschema:"Exact deployment.environment. Omit for every environment of the service."`
	Transaction string `json:"transaction,omitempty" jsonschema:"Restrict the RED totals to one transaction name, e.g. \"GET /orders/{id}\"."`
	Limit       int    `json:"limit,omitempty" jsonschema:"How many transactions to return, most time consumed first. Default 10."`
}

type listIncidentsArgs struct {
	State    []string `json:"state,omitempty" jsonschema:"Incident states to return: open, acknowledged, resolved. Default: every state, newest first."`
	RuleID   string   `json:"rule_id,omitempty" jsonschema:"Only incidents of this alert rule id."`
	Severity string   `json:"severity,omitempty" jsonschema:"Only incidents of this severity: critical, warning or info."`
	Limit    int      `json:"limit,omitempty" jsonschema:"How many incidents to return, newest first. Default 50."`
	Cursor   string   `json:"cursor,omitempty" jsonschema:"next_cursor of a previous call, to read the following page."`
}

type getIncidentArgs struct {
	ID string `json:"id" jsonschema:"Incident id, as returned by list_incidents."`
}

// logFilterArg is one condition of the log query language (api.md "Fields" › filter conditions).
type logFilterArg struct {
	Key    string   `json:"key" jsonschema:"Field to filter on: a top-level field (service.name, severity_text, severity_number, host.name, trace_id, body, ...), attributes.<key>, resource.<key> or body.<path> for a value inside a JSON body. A bare key reads the record attribute, else the resource attribute."`
	Op     string   `json:"op" jsonschema:"One of =, !=, in, not_in, contains, not_contains, like, not_like, regex, not_regex, exists, not_exists, >, >=, <, <=. 'contains' is a case-insensitive substring match; 'like' uses SQL wildcards % and _."`
	Value  string   `json:"value,omitempty" jsonschema:"Value for the single-value operators. A value that parses as a number or as true/false is sent as that type, so numeric comparisons work."`
	Values []string `json:"values,omitempty" jsonschema:"Values for 'in' and 'not_in' (1-100)."`
}

type searchLogsArgs struct {
	From    string         `json:"from,omitempty" jsonschema:"Start of the range. RFC3339 or unix milliseconds. Defaults to the last hour."`
	To      string         `json:"to,omitempty" jsonschema:"End of the range. RFC3339 or unix milliseconds. Defaults to now."`
	Query   string         `json:"q,omitempty" jsonschema:"Case-insensitive substring searched in the log body (at most 1024 bytes)."`
	Filters []logFilterArg `json:"filters,omitempty" jsonschema:"Conditions, all of which must match (AND). At most 50."`
	Order   string         `json:"order,omitempty" jsonschema:"desc (newest first, the default) or asc."`
	Limit   int            `json:"limit,omitempty" jsonschema:"How many records to return. Default 50."`
	Columns []string       `json:"columns,omitempty" jsonschema:"Extra keys to return per record in 'fields', e.g. attributes.http.route or resource.k8s.pod.name (at most 50)."`
	Cursor  string         `json:"cursor,omitempty" jsonschema:"next_cursor of a previous call with the same arguments, to read the next (older) page."`
}

// Status is a pointer so that omitting it keeps the documented default (true) instead of the zero value.
type listSLOsArgs struct {
	Status *bool `json:"status,omitempty" jsonschema:"Include the error budget of each SLO over its rolling window (default true). Set false for the definitions only, which runs no telemetry query."`
}

type sloStatusArgs struct {
	ID   string `json:"id" jsonschema:"SLO id, as returned by list_slos."`
	Step string `json:"step,omitempty" jsonschema:"Bucket width of the burndown series as a Go duration of at least 60s, e.g. 10m. Default: about 60 points over the window."`
}

// ---- handlers ----

func (s *Service) runOQL(ctx context.Context, cr Creds, in runOQLArgs) (json.RawMessage, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if (in.From == "") != (in.To == "") {
		return nil, fmt.Errorf("from and to must be given together (or neither, to use the query's SINCE/UNTIL)")
	}
	body := map[string]any{"query": in.Query}
	if in.From != "" {
		body["from"], body["to"] = in.From, in.To
	}
	if len(in.Variables) > 0 {
		body["variables"] = in.Variables
	}
	return s.client.Post(ctx, cr, "/api/v1/query", body)
}

func (s *Service) oqlSchema(ctx context.Context, cr Creds, in oqlSchemaArgs) (json.RawMessage, error) {
	q := url.Values{}
	if in.EventType != "" {
		q.Set("event_type", in.EventType)
	}
	return s.client.Get(ctx, cr, "/api/v1/query/schema", q)
}

func (s *Service) listServices(ctx context.Context, cr Creds, in listServicesArgs) (json.RawMessage, error) {
	q := url.Values{}
	setIf(q, "from", in.From)
	setIf(q, "to", in.To)
	setIf(q, "q", in.Query)
	// namespace and environment are exact matches when present, so an empty value is meaningful
	// (services without one) and must be distinguished from "not given" (apm.md §1).
	setIf(q, "namespace", in.Namespace)
	setIf(q, "environment", in.Environment)
	return s.client.Get(ctx, cr, "/api/v1/apm/services", q)
}

// serviceSummary joins the service's RED totals with its slowest transactions, the two reads a model
// needs to answer "how is this service doing?" without a second round trip.
func (s *Service) serviceSummary(ctx context.Context, cr Creds, in serviceSummaryArgs) (json.RawMessage, error) {
	service := strings.TrimSpace(in.Service)
	if service == "" {
		return nil, fmt.Errorf("service is required")
	}
	base := "/api/v1/apm/services/" + url.PathEscape(service)
	q := url.Values{}
	setIf(q, "from", in.From)
	setIf(q, "to", in.To)
	setIf(q, "namespace", in.Namespace)
	setIf(q, "environment", in.Environment)

	overviewQuery := cloneValues(q)
	setIf(overviewQuery, "transaction", in.Transaction)
	overview, err := s.client.Get(ctx, cr, base+"/overview", overviewQuery)
	if err != nil {
		return nil, err
	}
	txQuery := cloneValues(q)
	txQuery.Set("sort", "time")
	txQuery.Set("limit", strconv.Itoa(s.limit(in.Limit, 10)))
	transactions, err := s.client.Get(ctx, cr, base+"/transactions", txQuery)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"service_name": service,
		"overview":     overview,
		"transactions": transactions,
	})
}

func (s *Service) listIncidents(ctx context.Context, cr Creds, in listIncidentsArgs) (json.RawMessage, error) {
	q := url.Values{}
	if len(in.State) > 0 {
		q.Set("state", strings.Join(in.State, ","))
	}
	setIf(q, "rule_id", in.RuleID)
	setIf(q, "severity", in.Severity)
	setIf(q, "cursor", in.Cursor)
	q.Set("limit", strconv.Itoa(s.limit(in.Limit, 50)))
	return s.client.Get(ctx, cr, "/api/v1/alerts/incidents", q)
}

func (s *Service) getIncident(ctx context.Context, cr Creds, in getIncidentArgs) (json.RawMessage, error) {
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	return s.client.Get(ctx, cr, "/api/v1/alerts/incidents/"+url.PathEscape(id), nil)
}

func (s *Service) searchLogs(ctx context.Context, cr Creds, in searchLogsArgs) (json.RawMessage, error) {
	switch in.Order {
	case "", "desc", "asc":
	default:
		return nil, fmt.Errorf("order must be asc or desc")
	}
	body := map[string]any{"limit": s.limit(in.Limit, 50)}
	setBodyIf(body, "from", in.From)
	setBodyIf(body, "to", in.To)
	setBodyIf(body, "q", in.Query)
	setBodyIf(body, "order", in.Order)
	setBodyIf(body, "cursor", in.Cursor)
	if len(in.Columns) > 0 {
		body["columns"] = in.Columns
	}
	if len(in.Filters) > 0 {
		filters, err := encodeFilters(in.Filters)
		if err != nil {
			return nil, err
		}
		body["filters"] = filters
	}
	return s.client.Post(ctx, cr, "/api/v1/logs/query", body)
}

func (s *Service) listSLOs(ctx context.Context, cr Creds, in listSLOsArgs) (json.RawMessage, error) {
	q := url.Values{}
	status := in.Status == nil || *in.Status
	q.Set("status", strconv.FormatBool(status))
	return s.client.Get(ctx, cr, "/api/v1/slos", q)
}

func (s *Service) sloStatus(ctx context.Context, cr Creds, in sloStatusArgs) (json.RawMessage, error) {
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	q := url.Values{}
	setIf(q, "step", in.Step)
	return s.client.Get(ctx, cr, "/api/v1/slos/"+url.PathEscape(id)+"/results", q)
}

// ---- helpers ----

// limit clamps a requested row count to OPENLOG_MCP_MAX_ROWS; 0 takes the tool's default.
func (s *Service) limit(n, def int) int {
	if n <= 0 {
		n = def
	}
	return min(n, s.cfg.MaxRows)
}

// setIf adds a parameter only when it has a value. An omitted namespace or environment means "every
// one, aggregated" on the APM endpoints (api.md "APM"), which is what a model asking about a service
// by name wants; the exact-empty variant is not reachable through these tools.
func setIf(q url.Values, name, value string) {
	if value != "" {
		q.Set(name, value)
	}
}

func setBodyIf(body map[string]any, name, value string) {
	if value != "" {
		body[name] = value
	}
}

func cloneValues(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// encodeFilters turns the tool's string conditions into the API's filter shape. Values that parse as a
// number or as a boolean are sent as that JSON type, so numeric and boolean fields compare correctly.
func encodeFilters(filters []logFilterArg) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(filters))
	for i, f := range filters {
		if strings.TrimSpace(f.Key) == "" || strings.TrimSpace(f.Op) == "" {
			return nil, fmt.Errorf("filters[%d]: key and op are required", i)
		}
		c := map[string]any{"key": f.Key, "op": f.Op}
		if f.Value != "" {
			c["value"] = jsonScalar(f.Value)
		}
		if len(f.Values) > 0 {
			vals := make([]json.RawMessage, 0, len(f.Values))
			for _, v := range f.Values {
				vals = append(vals, jsonScalar(v))
			}
			c["values"] = vals
		}
		out = append(out, c)
	}
	return out, nil
}

// jsonScalar encodes s as a JSON number or boolean when it reads as one, else as a JSON string.
func jsonScalar(s string) json.RawMessage {
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return json.RawMessage(s)
	}
	if s == "true" || s == "false" {
		return json.RawMessage(s)
	}
	b, err := json.Marshal(s)
	if err != nil { // a Go string always marshals
		return json.RawMessage(`""`)
	}
	return json.RawMessage(b)
}
