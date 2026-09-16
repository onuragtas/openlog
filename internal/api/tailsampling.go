package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/tailsampling"
)

// Tail sampling policy endpoints (D-075, apm.md §4.2): GET/PUT /api/v1/apm/sampling and
// POST /api/v1/apm/sampling/preview.

type tailSamplingState struct {
	store   tailsampling.Store // nil in static auth mode
	enabled bool               // OPENLOG_TAILSAMPLING_ENABLED
}

// SetTailSampling configures the policy endpoints. store may be nil (static mode: read-only default).
// Must be called before Run.
func (s *Server) SetTailSampling(store tailsampling.Store, enabled bool) {
	s.tailSampling = &tailSamplingState{store: store, enabled: enabled}
	s.srv.Handler = s.Handler()
}

func (s *Server) tailSamplingConf() tailSamplingState {
	if s.tailSampling == nil {
		return tailSamplingState{}
	}
	return *s.tailSampling
}

func (s *Server) tailSamplingRoutes(mux *http.ServeMux) {
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/apm/sampling", s.getTailSampling)
	route("POST /api/v1/apm/sampling/preview", s.previewTailSampling)
	if s.accounts != nil {
		pattern := "PUT /api/v1/apm/sampling"
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if err := s.putTailSampling(rec, r, p); err != nil {
				s.writeAccountError(rec, pattern, err)
			}
		}))
	}
}

type tailSamplingJSON struct {
	Enabled        bool                `json:"enabled"`
	Policy         tailsampling.Policy `json:"policy"`
	IsDefault      bool                `json:"is_default"`
	Version        int                 `json:"version"`
	UpdatedAt      *string             `json:"updated_at"`
	UpdatedByEmail string              `json:"updated_by_email"`
}

func (s *Server) getTailSampling(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
	conf := s.tailSamplingConf()
	out := tailSamplingJSON{Enabled: conf.enabled, Policy: tailsampling.KeepAll(), IsDefault: true}
	if p, ok := auth.PrincipalFrom(r.Context()); ok && conf.store != nil && p.OrgID != "" {
		st, err := conf.store.Get(r.Context(), p.OrgID)
		if err != nil {
			return err
		}
		if st != nil {
			out.Policy, out.IsDefault, out.Version = st.Policy, false, st.Version
			out.UpdatedAt, out.UpdatedByEmail = optTime(&st.UpdatedAt), st.UpdatedByEmail
		}
	}
	noStore(w)
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) putTailSampling(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if ae := authorize(p, auth.ActUpdateOrg); ae != nil {
		return ae
	}
	var body struct {
		Policy  json.RawMessage `json:"policy"`
		Version *int            `json:"version"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	if len(body.Policy) == 0 || body.Version == nil || *body.Version < 0 {
		return badRequest("policy and version (0 when no policy is stored) are required")
	}
	policy, err := tailsampling.ParsePolicy(body.Policy)
	if err != nil {
		return badRequest("%v", err)
	}
	conf := s.tailSamplingConf()
	if conf.store == nil {
		return notFound("tail sampling policies are not available")
	}
	st, err := conf.store.Put(r.Context(), p.OrgID, policy, *body.Version, s.apmActor(r, p))
	switch {
	case errors.Is(err, tailsampling.ErrVersionConflict):
		return &apiError{http.StatusConflict, "conflict", err.Error()}
	case errors.Is(err, tailsampling.ErrInvalidPolicy):
		return badRequest("%v", err)
	case err != nil:
		return err
	}
	writeJSON(w, http.StatusOK, tailSamplingJSON{Enabled: conf.enabled, Policy: st.Policy, Version: st.Version,
		UpdatedAt: optTime(&st.UpdatedAt), UpdatedByEmail: st.UpdatedByEmail})
	return nil
}

// ---- preview ----

const (
	previewMaxTraces     = 20000
	previewDefaultWindow = 60
	previewMaxWindow     = 24 * 60
	previewRouteValues   = 64
)

// previewTrace is one stored trace as the preview sees it.
type previewTrace struct {
	spans  float64
	weight float64 // adjusted count of the trace (max span sample_weight)
	durMs  float64
	match  []bool     // per rule, evaluated in ClickHouse (routes and trace latency are nil here)
	routes [][]string // per rule: route/span names of the rule's service, for route rules
}

type previewRuleJSON struct {
	Name             string  `json:"name"`
	MatchedTraceRate float64 `json:"matched_trace_ratio"`
	KeptTraceRate    float64 `json:"kept_trace_ratio"`
}

type previewJSON struct {
	WindowMinutes   int               `json:"window_minutes"`
	TracesExamined  int               `json:"traces_examined"`
	SampledFraction float64           `json:"sampled_fraction"`
	EstimatedTraces float64           `json:"estimated_traces"`
	KeptTraceRatio  float64           `json:"kept_trace_ratio"`
	KeptSpanRatio   float64           `json:"kept_span_ratio"`
	Rules           []previewRuleJSON `json:"rules"`
}

// estimateKept applies the policy's first-match rules to stored traces, weighting each by its adjusted count
// so the ratios estimate what the policy would keep of the original traffic (rate limits are not simulated).
func estimateKept(policy tailsampling.Policy, traces []previewTrace) previewJSON {
	out := previewJSON{Rules: make([]previewRuleJSON, 0, len(policy.Rules)+1), KeptTraceRatio: 1, KeptSpanRatio: 1}
	for _, r := range policy.Rules {
		out.Rules = append(out.Rules, previewRuleJSON{Name: r.Name})
	}
	out.Rules = append(out.Rules, previewRuleJSON{Name: tailsampling.DefaultRuleName})
	var total, kept, spans, keptSpans float64
	matched := make([]float64, len(out.Rules))
	keptBy := make([]float64, len(out.Rules))
	for _, t := range traces {
		w := t.weight
		if w <= 0 {
			continue
		}
		idx, ratio := len(policy.Rules), policy.BaselineRatio
		if !policy.Enabled {
			ratio = 1
		} else {
			for i, r := range policy.Rules {
				if previewMatches(r, i, t) {
					idx, ratio = i, r.KeepRatio()
					break
				}
			}
		}
		total += w
		kept += w * ratio
		spans += w * t.spans
		keptSpans += w * t.spans * ratio
		matched[idx] += w
		keptBy[idx] += w * ratio
	}
	out.TracesExamined = len(traces)
	if total > 0 {
		out.KeptTraceRatio, out.KeptSpanRatio = kept/total, keptSpans/spans
		for i := range out.Rules {
			out.Rules[i].MatchedTraceRate, out.Rules[i].KeptTraceRate = matched[i]/total, keptBy[i]/total
		}
	}
	return out
}

func previewMatches(r tailsampling.Rule, i int, t previewTrace) bool {
	switch {
	case r.Type == tailsampling.RuleLatency && r.Service == "":
		return t.durMs >= float64(r.ThresholdMs)
	case r.Type == tailsampling.RuleRoute:
		for _, v := range t.routes[i] {
			if tailsampling.MatchRoute(r.Route, v) {
				return true
			}
		}
		return false
	default:
		return t.match[i]
	}
}

func (s *Server) previewTailSampling(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	var body struct {
		Policy        json.RawMessage `json:"policy"`
		WindowMinutes int             `json:"window_minutes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	policy, err := tailsampling.ParsePolicy(body.Policy)
	if err != nil {
		return badRequest("%v", err)
	}
	window := body.WindowMinutes
	if window == 0 {
		window = previewDefaultWindow
	}
	if window < 1 || window > previewMaxWindow {
		return badRequest("window_minutes must be between 1 and %d", previewMaxWindow)
	}
	to := s.now()
	from := to.Add(-time.Duration(window) * time.Minute)

	count := spanRange(sc.From(query.Spans).Columns("uniq(trace_id)"), from, to)
	rows, err := sc.Query(r.Context(), count)
	if err != nil {
		return err
	}
	var distinct uint64
	if rows.Next() {
		if err := rows.Scan(&distinct); err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	mod := uint64(1)
	if distinct > previewMaxTraces {
		mod = (distinct + previewMaxTraces - 1) / previewMaxTraces
	}

	q := spanRange(sc.From(query.Spans).Columns(
		"toFloat64(count())", "max(sample_weight)",
		"toFloat64(max(toUnixTimestamp64Nano(timestamp) + toInt64(duration_ns)) - min(toUnixTimestamp64Nano(timestamp))) / 1e6",
	), from, to).GroupBy("trace_id").Limit(previewMaxTraces)
	if mod > 1 {
		q.Where("cityHash64(trace_id) % {pv_mod:UInt64} = 0").Param("pv_mod", mod)
	}
	for i, rule := range policy.Rules {
		n := strconv.Itoa(i)
		svc := "pv_svc_" + n
		svcCond := "({" + svc + ":String} = '' OR service_name = {" + svc + ":String})"
		q.Param(svc, rule.Service)
		switch rule.Type {
		case tailsampling.RuleError:
			q.Columns("toUInt8(max(status_code = 'error' AND " + svcCond + "))")
		case tailsampling.RuleLatency:
			q.Columns("toUInt8(max(duration_ns >= {pv_thr_"+n+":UInt64} AND "+svcCond+"))").Param("pv_thr_"+n, uint64(rule.ThresholdMs)*1_000_000)
		case tailsampling.RuleService:
			q.Columns("toUInt8(max(has({pv_svcs_"+n+":Array(String)}, service_name)))").Param("pv_svcs_"+n, rule.Services)
		case tailsampling.RuleRoute:
			q.Columns("groupUniqArrayIf(" + strconv.Itoa(previewRouteValues) + ")(if(attributes['http.route'] != '', attributes['http.route'], name), " + svcCond + ")")
		case tailsampling.RuleAttribute:
			k, v := "{pv_key_"+n+":String}", "{pv_val_"+n+":String}"
			cond := "((mapContains(attributes, " + k + ") AND ({pv_val_" + n + ":String} = '' OR attributes[" + k + "] = " + v + ")) OR " +
				"(mapContains(resource_attributes, " + k + ") AND ({pv_val_" + n + ":String} = '' OR resource_attributes[" + k + "] = " + v + ")))"
			q.Columns("toUInt8(max("+cond+" AND "+svcCond+"))").Param("pv_key_"+n, rule.Key).Param("pv_val_"+n, rule.Value)
		}
	}
	rows, err = sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	var traces []previewTrace
	for rows.Next() {
		t := previewTrace{match: make([]bool, len(policy.Rules)), routes: make([][]string, len(policy.Rules))}
		dest := []any{&t.spans, &t.weight, &t.durMs}
		flags := make([]uint8, len(policy.Rules))
		for i, rule := range policy.Rules {
			if rule.Type == tailsampling.RuleRoute {
				dest = append(dest, &t.routes[i])
			} else {
				dest = append(dest, &flags[i])
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		for i := range flags {
			t.match[i] = flags[i] == 1
		}
		traces = append(traces, t)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out := estimateKept(policy, traces)
	out.WindowMinutes = window
	out.SampledFraction = 1 / float64(mod)
	for _, t := range traces {
		out.EstimatedTraces += t.weight
	}
	out.EstimatedTraces *= float64(mod)
	writeJSON(w, http.StatusOK, out)
	return nil
}
