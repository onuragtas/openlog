package api

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/auth"
)

// APM endpoints (docs/contracts/apm.md §9, api.md "APM"). Every read goes through the
// tenant-scoped query layer and re-aggregates the APM tables with GROUP BY (sharding
// correctness, apm.md §8).

type apmState struct {
	settings apm.SettingsStore   // nil: default Apdex T only (static auth mode)
	errors   apm.ErrorStateStore // nil: no error workflow (static auth mode)
	defaultT time.Duration
}

// SetAPM configures Apdex settings (nil store: defaults only). Must be called before Run.
func (s *Server) SetAPM(settings apm.SettingsStore, defaultApdexT time.Duration) {
	if defaultApdexT <= 0 {
		defaultApdexT = apm.DefaultApdexT
	}
	s.apm = &apmState{settings: settings, defaultT: defaultApdexT}
	s.srv.Handler = s.Handler()
}

func (s *Server) apmConf() apmState {
	if s.apm == nil {
		return apmState{defaultT: apm.DefaultApdexT}
	}
	return *s.apm
}

func (s *Server) apmRoutes(mux *http.ServeMux) {
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/apm/services", s.apmServices)
	route("GET /api/v1/apm/services/{service}", s.apmService)
	route("GET /api/v1/apm/services/{service}/overview", s.apmOverview)
	route("GET /api/v1/apm/services/{service}/transactions", s.apmTransactions)
	route("GET /api/v1/apm/services/{service}/transaction", s.apmTransaction)
	route("GET /api/v1/apm/services/{service}/databases", s.apmDatabases)
	route("GET /api/v1/apm/services/{service}/hosts", s.apmServiceHosts)
	route("GET /api/v1/apm/services/{service}/settings", s.apmGetSettings)
	route("GET /api/v1/apm/hosts/{host_id}/services", s.apmHostServices)
	route("GET /api/v1/apm/map", s.apmMap)
	route("GET /api/v1/apm/traces", s.apmTraces)
	s.apmGARoutes(mux) // apm_error_inbox.go: error workflow, deployments, map path
	if s.accounts != nil {
		pattern := "PUT /api/v1/apm/services/{service}/settings"
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if err := s.apmPutSettings(rec, r, p); err != nil {
				s.writeAccountError(rec, pattern, err)
			}
		}))
	}
}

// ---- parameters ----

const maxServiceNameBytes = 512

// svcFilter selects one service name and, when given, one namespace and environment.
type svcFilter struct {
	name    string
	ns, env *string
}

func parseSvcFilter(r *http.Request) (svcFilter, error) {
	f := svcFilter{name: r.PathValue("service")}
	if f.name == "" || len(f.name) > maxServiceNameBytes {
		return f, badRequest("service name must be 1-%d bytes", maxServiceNameBytes)
	}
	f.ns, f.env = optionalParam(r.URL.Query(), "namespace"), optionalParam(r.URL.Query(), "environment")
	return f, nil
}

func optionalParam(q url.Values, name string) *string {
	if !q.Has(name) {
		return nil
	}
	v := q.Get(name)
	return &v
}

// apply adds the service conditions on the columns service_name, service_namespace, deployment_environment.
func (f svcFilter) apply(q *query.Select) *query.Select {
	q.Where("service_name = {svc_name:String}").Param("svc_name", f.name)
	applyScope(q, f.ns, f.env)
	return q
}

func applyScope(q *query.Select, ns, env *string) {
	if ns != nil {
		q.Where("service_namespace = {svc_ns:String}").Param("svc_ns", *ns)
	}
	if env != nil {
		q.Where("deployment_environment = {svc_env:String}").Param("svc_env", *env)
	}
}

func (f svcFilter) key() apm.ServiceKey {
	k := apm.ServiceKey{Name: f.name}
	if f.ns != nil {
		k.Namespace = *f.ns
	}
	if f.env != nil {
		k.Environment = *f.env
	}
	return k
}

// minuteRange restricts a 1-minute APM table to buckets starting in [from truncated to the minute, to).
func minuteRange(q *query.Select, from, to time.Time) *query.Select {
	return q.Where("timestamp >= toDateTime({t_from:Int64}, 'UTC') AND timestamp < toDateTime({t_to:Int64}, 'UTC')").
		Param("t_from", from.Truncate(time.Minute).Unix()).Param("t_to", to.Unix())
}

func spanRange(q *query.Select, from, to time.Time) *query.Select {
	return q.Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano())
}

func rangeMinutes(from, to time.Time) float64 {
	return math.Max(to.Sub(from.Truncate(time.Minute)).Minutes(), 1.0/60)
}

const (
	apmTargetPoints = 60
	apmMaxPoints    = 1500
)

// apmStep parses step (>= 60s) or picks ~60 points; always whole minutes.
func apmStep(r *http.Request, from, to time.Time) (time.Duration, error) {
	var step time.Duration
	if v := r.URL.Query().Get("step"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0, badRequest("step: invalid duration %q", v)
		}
		if d < time.Minute {
			return 0, badRequest("step must be at least 60s")
		}
		step = d
	} else {
		step = to.Sub(from) / apmTargetPoints
	}
	step = max(step, to.Sub(from)/apmMaxPoints, time.Minute)
	if rem := step % time.Minute; rem != 0 {
		step += time.Minute - rem
	}
	return step, nil
}

func (s *Server) apmLimit(r *http.Request, def, maxN int) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return min(def, s.cfg.MaxRows), nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, badRequest("limit must be a positive integer")
	}
	return min(n, maxN, s.cfg.MaxRows), nil
}

// ---- Apdex settings ----

func (s *Server) apmSettings(r *http.Request) []apm.Setting {
	conf := s.apmConf()
	p, ok := auth.PrincipalFrom(r.Context())
	if conf.settings == nil || !ok || p.OrgID == "" {
		return nil
	}
	list, err := conf.settings.List(r.Context(), p.OrgID)
	if err != nil {
		// Settings are an overlay: fall back to the default instead of failing the query.
		s.log.Warn("apm settings unavailable, using the default apdex T", "err", err)
		return nil
	}
	return list
}

func (s *Server) apdexTMs(settings []apm.Setting, key apm.ServiceKey) (float64, *apm.Setting) {
	if st := apm.ResolveApdexT(settings, key); st != nil {
		return float64(st.ApdexTMs), st
	}
	return float64(s.apmConf().defaultT) / float64(time.Millisecond), nil
}

type apmSettingsJSON struct {
	ServiceName      string  `json:"service_name"`
	ServiceNamespace string  `json:"service_namespace"`
	Environment      string  `json:"environment"`
	ApdexTMs         float64 `json:"apdex_t_ms"`
	IsDefault        bool    `json:"is_default"`
	UpdatedAt        *string `json:"updated_at"`
	UpdatedByEmail   string  `json:"updated_by_email"`
}

func (s *Server) settingsResponse(key apm.ServiceKey, tMs float64, st *apm.Setting) apmSettingsJSON {
	out := apmSettingsJSON{ServiceName: key.Name, ServiceNamespace: key.Namespace, Environment: key.Environment, ApdexTMs: tMs, IsDefault: st == nil}
	if st != nil {
		out.UpdatedAt, out.UpdatedByEmail = optTime(&st.UpdatedAt), st.UpdatedByEmail
	}
	return out
}

func (s *Server) apmGetSettings(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	key := f.key()
	tMs, st := s.apdexTMs(s.apmSettings(r), key)
	writeJSON(w, http.StatusOK, s.settingsResponse(key, tMs, st))
	return nil
}

func (s *Server) apmPutSettings(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if !p.HasOrg() {
		return &apiError{http.StatusForbidden, "permission_denied", "you are not a member of any organization"}
	}
	if p.Kind != auth.KindSession {
		return &apiError{http.StatusForbidden, "permission_denied", "this operation requires a signed-in user; API keys are read-only"}
	}
	if !p.Role.Can(auth.ActUpdateOrg) {
		return &apiError{http.StatusForbidden, "permission_denied", "your role (" + string(p.Role) + ") does not allow this operation"}
	}
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	var body struct {
		ApdexTMs *float64 `json:"apdex_t_ms"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	if body.ApdexTMs == nil || *body.ApdexTMs != math.Trunc(*body.ApdexTMs) || *body.ApdexTMs < apm.MinApdexTMs || *body.ApdexTMs > apm.MaxApdexTMs {
		return badRequest("apdex_t_ms must be an integer between %d and %d", apm.MinApdexTMs, apm.MaxApdexTMs)
	}
	conf := s.apmConf()
	if conf.settings == nil {
		return notFound("apm settings are not available")
	}
	key := f.key()
	st, err := conf.settings.Put(r.Context(), p.OrgID, key, int(*body.ApdexTMs), apm.Actor{UserID: p.UserID, Email: p.Email, IP: s.accounts.Meta(r).IP})
	if errors.Is(err, apm.ErrInvalidSetting) {
		return badRequest("%v", err)
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.settingsResponse(key, float64(st.ApdexTMs), &st))
	return nil
}

// ---- RED aggregation ----

// redAgg is one aggregated row of an APM table.
type redAgg struct {
	requests, errors, durSum, maxMs float64
	hist, ok                        apm.Hist
}

func (a *redAgg) add(o redAgg) {
	a.requests += o.requests
	a.errors += o.errors
	a.durSum += o.durSum
	a.maxMs = math.Max(a.maxMs, o.maxMs)
	a.hist = a.hist.Add(o.hist)
	a.ok = a.ok.Add(o.ok)
}

// redJSON holds the metrics of apm.md §4. Pointers are null without requests.
type redJSON struct {
	Requests   float64  `json:"requests"`
	Throughput float64  `json:"throughput"`
	Errors     float64  `json:"errors"`
	ErrorRate  float64  `json:"error_rate"`
	AvgMs      *float64 `json:"avg_ms"`
	P50Ms      *float64 `json:"p50_ms"`
	P95Ms      *float64 `json:"p95_ms"`
	P99Ms      *float64 `json:"p99_ms"`
	Apdex      *float64 `json:"apdex"`
}

func num(v float64) *float64 {
	if !finite(v) {
		return nil
	}
	return &v
}

// red computes metrics over minutes; tMs <= 0 omits Apdex.
func (a redAgg) red(minutes, tMs float64) redJSON {
	out := redJSON{Requests: a.requests, Errors: a.errors}
	if minutes > 0 {
		out.Throughput = a.requests / minutes
	}
	if a.requests <= 0 {
		return out
	}
	out.ErrorRate = math.Min(1, a.errors/a.requests)
	out.AvgMs = num(a.durSum / a.requests)
	out.P50Ms, out.P95Ms, out.P99Ms = num(apm.HistQuantile(a.hist, 0.5)), num(apm.HistQuantile(a.hist, 0.95)), num(apm.HistQuantile(a.hist, 0.99))
	if tMs > 0 {
		if v, _, _, ok := apm.HistApdex(a.requests, a.ok, tMs); ok {
			out.Apdex = num(v)
		}
	}
	return out
}

// txAggColumns aggregate apm_transactions_1m.
var txAggColumns = []string{
	"sum(requests) AS a_req", "sum(errors) AS a_err", "sum(duration_sum_ms) AS a_dsum", "max(duration_max_ms) AS a_dmax",
	"tupleElement(sumMap(duration_hist), 1) AS a_hk", "tupleElement(sumMap(duration_hist), 2) AS a_hv",
	"tupleElement(sumMap(ok_hist), 1) AS a_ok_k", "tupleElement(sumMap(ok_hist), 2) AS a_ok_v",
}

// txAggDest returns scan destinations for txAggColumns and a function building the redAgg.
func txAggDest() ([]any, func() redAgg) {
	var a redAgg
	var hk, okk []int16
	var hv, okv []float64
	return []any{&a.requests, &a.errors, &a.durSum, &a.maxMs, &hk, &hv, &okk, &okv}, func() redAgg {
		a.hist, a.ok = apm.NewHist(hk, hv), apm.NewHist(okk, okv)
		return a
	}
}

// callAggColumns aggregate edge/link/db tables (no ok histogram).
var callAggColumns = []string{
	"sum(calls) AS a_req", "sum(errors) AS a_err", "sum(duration_sum_ms) AS a_dsum",
	"tupleElement(sumMap(duration_hist), 1) AS a_hk", "tupleElement(sumMap(duration_hist), 2) AS a_hv",
}

func callAggDest() ([]any, func() redAgg) {
	var a redAgg
	var hk []int16
	var hv []float64
	return []any{&a.requests, &a.errors, &a.durSum, &hk, &hv}, func() redAgg {
		a.hist = apm.NewHist(hk, hv)
		return a
	}
}

func cols(prefix []string, rest ...[]string) []string {
	out := append([]string{}, prefix...)
	for _, r := range rest {
		out = append(out, r...)
	}
	return out
}

type serviceIdentity struct {
	ServiceName      string `json:"service_name"`
	ServiceNamespace string `json:"service_namespace"`
	Environment      string `json:"environment"`
}

func (id serviceIdentity) key() apm.ServiceKey {
	return apm.ServiceKey{Name: id.ServiceName, Namespace: id.ServiceNamespace, Environment: id.Environment}
}

// ---- GET /apm/services ----

type apmServiceJSON struct {
	serviceIdentity
	Language string `json:"language"`
	Version  string `json:"version"`
	LastSeen string `json:"last_seen"`
	ApdexTMs float64
	redJSON
	Sparkline [][2]float64 `json:"sparkline"`
}

func (s *Server) apmServices(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	step, err := apmStep(r, from, to)
	if err != nil {
		return err
	}
	qp := r.URL.Query()
	ns, env := optionalParam(qp, "namespace"), optionalParam(qp, "environment")
	idCols := []string{"service_name", "service_namespace", "deployment_environment"}

	meta := sc.From(query.ApmServices).Columns(cols(idCols, []string{"max(last_seen) AS m_last", "argMaxMerge(service_version) AS m_ver", "argMaxMerge(sdk_language) AS m_lang"})...).
		Where("last_seen >= fromUnixTimestamp64Nano({t_seen:Int64})").Param("t_seen", from.UnixNano()).
		GroupBy(idCols...).OrderBy(idCols...).Limit(s.cfg.MaxRows)
	applyScope(meta, ns, env)
	if q := qp.Get("q"); q != "" {
		meta.Where("positionCaseInsensitiveUTF8(service_name, {q:String}) > 0").Param("q", q)
	}
	rows, err := sc.Query(r.Context(), meta)
	if err != nil {
		return err
	}
	services := []*apmServiceJSON{}
	byKey := map[apm.ServiceKey]*apmServiceJSON{}
	for rows.Next() {
		sv := &apmServiceJSON{Sparkline: [][2]float64{}}
		var last time.Time
		if err := rows.Scan(&sv.ServiceName, &sv.ServiceNamespace, &sv.Environment, &last, &sv.Version, &sv.Language); err != nil {
			rows.Close()
			return err
		}
		sv.LastSeen = formatTime(last)
		services = append(services, sv)
		byKey[sv.key()] = sv
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	settings := s.apmSettings(r)
	minutes := rangeMinutes(from, to)
	if len(services) > 0 {
		totals := minuteRange(sc.From(query.ApmTransactions1m).Columns(cols(idCols, txAggColumns)...), from, to).GroupBy(idCols...)
		applyScope(totals, ns, env)
		trows, err := sc.Query(r.Context(), totals)
		if err != nil {
			return err
		}
		aggs := map[apm.ServiceKey]redAgg{}
		for trows.Next() {
			var id serviceIdentity
			dest, build := txAggDest()
			if err := trows.Scan(append([]any{&id.ServiceName, &id.ServiceNamespace, &id.Environment}, dest...)...); err != nil {
				trows.Close()
				return err
			}
			aggs[id.key()] = build()
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			return err
		}
		spark := minuteRange(sc.From(query.ApmTransactions1m).Columns(cols(idCols,
			[]string{"toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t", "sum(requests) AS a_req"})...), from, to).
			Param("step", uint32(step/time.Second)).GroupBy(append(idCols, "t")...).OrderBy("t")
		applyScope(spark, ns, env)
		srows, err := sc.Query(r.Context(), spark)
		if err != nil {
			return err
		}
		for srows.Next() {
			var id serviceIdentity
			var t time.Time
			var req float64
			if err := srows.Scan(&id.ServiceName, &id.ServiceNamespace, &id.Environment, &t, &req); err != nil {
				srows.Close()
				return err
			}
			if sv := byKey[id.key()]; sv != nil {
				sv.Sparkline = append(sv.Sparkline, [2]float64{float64(t.UnixMilli()), req / step.Minutes()})
			}
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return err
		}
		for _, sv := range services {
			tMs, _ := s.apdexTMs(settings, sv.key())
			sv.ApdexTMs = tMs
			sv.redJSON = aggs[sv.key()].red(minutes, tMs)
		}
	}
	out := make([]map[string]any, 0, len(services))
	for _, sv := range services {
		out = append(out, serviceMap(sv))
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": out, "step": formatStep(step)})
	return nil
}

// serviceMap flattens apmServiceJSON (embedded structs) and adds apdex_t_ms.
func serviceMap(sv *apmServiceJSON) map[string]any {
	return map[string]any{
		"service_name": sv.ServiceName, "service_namespace": sv.ServiceNamespace, "environment": sv.Environment,
		"language": sv.Language, "version": sv.Version, "last_seen": sv.LastSeen, "apdex_t_ms": sv.ApdexTMs,
		"requests": sv.Requests, "throughput": sv.Throughput, "errors": sv.Errors, "error_rate": sv.ErrorRate,
		"avg_ms": sv.AvgMs, "p50_ms": sv.P50Ms, "p95_ms": sv.P95Ms, "p99_ms": sv.P99Ms, "apdex": sv.Apdex,
		"sparkline": sv.Sparkline,
	}
}

// ---- GET /apm/services/{service} ----

func (s *Server) apmService(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	q := f.apply(sc.From(query.ApmServices).Columns("service_namespace", "deployment_environment", "min(first_seen) AS m_first",
		"max(last_seen) AS m_last", "argMaxMerge(service_version) AS m_ver", "argMaxMerge(sdk_language) AS m_lang",
		"argMaxMerge(sdk_name) AS m_sdk", "argMaxMerge(resource_attributes) AS m_res")).
		GroupBy("service_namespace", "deployment_environment").OrderBy("m_last DESC").Limit(100)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	type instanceJSON struct {
		ServiceNamespace   string            `json:"service_namespace"`
		Environment        string            `json:"environment"`
		FirstSeen          string            `json:"first_seen"`
		LastSeen           string            `json:"last_seen"`
		Version            string            `json:"version"`
		Language           string            `json:"language"`
		SDKName            string            `json:"sdk_name"`
		ResourceAttributes map[string]string `json:"resource_attributes"`
	}
	instances := []instanceJSON{}
	for rows.Next() {
		var in instanceJSON
		var first, last time.Time
		if err := rows.Scan(&in.ServiceNamespace, &in.Environment, &first, &last, &in.Version, &in.Language, &in.SDKName, &in.ResourceAttributes); err != nil {
			rows.Close()
			return err
		}
		in.FirstSeen, in.LastSeen, in.ResourceAttributes = formatTime(first), formatTime(last), nonNilMap(in.ResourceAttributes)
		instances = append(instances, in)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(instances) == 0 {
		return notFound("service not found")
	}
	hosts, err := s.serviceHosts(r, sc, f)
	if err != nil {
		return err
	}
	key := f.key()
	tMs, st := s.apdexTMs(s.apmSettings(r), key)
	writeJSON(w, http.StatusOK, map[string]any{"service_name": f.name, "instances": instances, "hosts": hosts,
		"apdex_t_ms": tMs, "apdex_t_default": st == nil})
	return nil
}

type serviceHostJSON struct {
	HostID    string `json:"host_id"`
	HostName  string `json:"host_name"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	// Known: the host has a host record (hosts table), e.g. from the infra agent or any resource with host.id.
	Known bool `json:"known"`
}

func (s *Server) serviceHosts(r *http.Request, sc *query.Scope, f svcFilter) ([]serviceHostJSON, error) {
	q := f.apply(sc.From(query.ApmServiceHosts).Columns("host_id", "min(first_seen) AS m_first", "max(last_seen) AS m_last", "argMaxMerge(host_name) AS m_name")).
		GroupBy("host_id").OrderBy("m_last DESC").Limit(1000)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	hosts := []serviceHostJSON{}
	var ids []string
	for rows.Next() {
		var h serviceHostJSON
		var first, last time.Time
		if err := rows.Scan(&h.HostID, &first, &last, &h.HostName); err != nil {
			rows.Close()
			return nil, err
		}
		h.FirstSeen, h.LastSeen = formatTime(first), formatTime(last)
		hosts = append(hosts, h)
		ids = append(ids, h.HostID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return hosts, nil
	}
	hq := sc.From(query.Hosts).Columns("host_id", "argMax(host_name, last_seen)").
		Where("has({host_ids:Array(String)}, host_id)").Param("host_ids", ids).GroupBy("host_id")
	hrows, err := sc.Query(r.Context(), hq)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for hrows.Next() {
		var id, name string
		if err := hrows.Scan(&id, &name); err != nil {
			hrows.Close()
			return nil, err
		}
		names[id] = name
	}
	hrows.Close()
	for i := range hosts {
		if name, ok := names[hosts[i].HostID]; ok {
			hosts[i].Known = true
			if name != "" {
				hosts[i].HostName = name
			}
		}
	}
	return hosts, hrows.Err()
}

func (s *Server) apmServiceHosts(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	hosts, err := s.serviceHosts(r, sc, f)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts})
	return nil
}

// ---- GET /apm/hosts/{host_id}/services ----

func (s *Server) apmHostServices(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	now := s.now().UTC()
	from := now.Add(-24 * time.Hour)
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := parseTime(v)
		if err != nil {
			return badRequest("from: %v", err)
		}
		from = t
	}
	idCols := []string{"service_name", "service_namespace", "deployment_environment"}
	q := sc.From(query.ApmServiceHosts).Columns(cols(idCols, []string{"min(first_seen) AS m_first", "max(last_seen) AS m_last"})...).
		Where("host_id = {host_id:String}").Param("host_id", r.PathValue("host_id")).
		Where("last_seen >= fromUnixTimestamp64Nano({t_seen:Int64})").Param("t_seen", from.UnixNano()).
		GroupBy(idCols...).OrderBy(idCols...).Limit(1000)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type hostServiceJSON struct {
		serviceIdentity
		FirstSeen string `json:"first_seen"`
		LastSeen  string `json:"last_seen"`
	}
	out := []hostServiceJSON{}
	for rows.Next() {
		var hs hostServiceJSON
		var first, last time.Time
		if err := rows.Scan(&hs.ServiceName, &hs.ServiceNamespace, &hs.Environment, &first, &last); err != nil {
			return err
		}
		hs.FirstSeen, hs.LastSeen = formatTime(first), formatTime(last)
		out = append(out, hs)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": out})
	return nil
}

// ---- overview / transaction timeseries ----

type pointJSON struct {
	T int64 `json:"t"`
	redJSON
}

// txFilter narrows apm_transactions_1m or spans to one transaction.
type txFilter struct{ name, typ string }

func (t txFilter) apply(q *query.Select) {
	if t.name != "" {
		q.Where("transaction_name = {txn:String}").Param("txn", t.name)
	}
	if t.typ != "" {
		q.Where("transaction_type = {txn_type:String}").Param("txn_type", t.typ)
	}
}

func (s *Server) txTotalsAndSeries(r *http.Request, sc *query.Scope, f svcFilter, tx txFilter, from, to time.Time, step time.Duration, tMs float64) (redAgg, redJSON, []pointJSON, error) {
	totals := f.apply(minuteRange(sc.From(query.ApmTransactions1m).Columns(txAggColumns...), from, to))
	tx.apply(totals)
	rows, err := sc.Query(r.Context(), totals)
	if err != nil {
		return redAgg{}, redJSON{}, nil, err
	}
	var total redAgg
	if rows.Next() {
		dest, build := txAggDest()
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return redAgg{}, redJSON{}, nil, err
		}
		total = build()
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return redAgg{}, redJSON{}, nil, err
	}
	series := f.apply(minuteRange(sc.From(query.ApmTransactions1m).
		Columns(cols([]string{"toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t"}, txAggColumns)...), from, to)).
		Param("step", uint32(step/time.Second)).GroupBy("t").OrderBy("t")
	tx.apply(series)
	srows, err := sc.Query(r.Context(), series)
	if err != nil {
		return redAgg{}, redJSON{}, nil, err
	}
	defer srows.Close()
	points := []pointJSON{}
	for srows.Next() {
		var t time.Time
		dest, build := txAggDest()
		if err := srows.Scan(append([]any{&t}, dest...)...); err != nil {
			return redAgg{}, redJSON{}, nil, err
		}
		points = append(points, pointJSON{T: t.UnixMilli(), redJSON: build().red(step.Minutes(), tMs)})
	}
	return total, total.red(rangeMinutes(from, to), tMs), points, srows.Err()
}

func (s *Server) apmOverview(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	step, err := apmStep(r, from, to)
	if err != nil {
		return err
	}
	tMs, _ := s.apdexTMs(s.apmSettings(r), f.key())
	tx := txFilter{name: r.URL.Query().Get("transaction"), typ: r.URL.Query().Get("type")}
	_, totals, points, err := s.txTotalsAndSeries(r, sc, f, tx, from, to, step, tMs)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"step": formatStep(step), "apdex_t_ms": tMs, "totals": totals, "series": points})
	return nil
}

// ---- transactions ----

type transactionJSON struct {
	TransactionType string  `json:"transaction_type"`
	TransactionName string  `json:"transaction_name"`
	TimeConsumedMs  float64 `json:"time_consumed_ms"`
	TimeShare       float64 `json:"time_share"`
	MaxMs           float64 `json:"max_ms"`
	redJSON
}

func (s *Server) apmTransactions(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.apmLimit(r, 100, 1000)
	if err != nil {
		return err
	}
	sortBy := r.URL.Query().Get("sort")
	switch sortBy {
	case "":
		sortBy = "time"
	case "time", "throughput", "slowest", "errors":
	default:
		return badRequest("sort must be time, throughput, slowest or errors")
	}
	tMs, _ := s.apdexTMs(s.apmSettings(r), f.key())
	q := f.apply(minuteRange(sc.From(query.ApmTransactions1m).Columns(cols([]string{"transaction_type", "transaction_name"}, txAggColumns)...), from, to)).
		GroupBy("transaction_type", "transaction_name").OrderBy("a_dsum DESC").Limit(s.cfg.MaxRows)
	if typ := r.URL.Query().Get("type"); typ != "" {
		q.Where("transaction_type = {txn_type:String}").Param("txn_type", typ)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	minutes := rangeMinutes(from, to)
	out := []transactionJSON{}
	var totalTime float64
	for rows.Next() {
		var tx transactionJSON
		dest, build := txAggDest()
		if err := rows.Scan(append([]any{&tx.TransactionType, &tx.TransactionName}, dest...)...); err != nil {
			return err
		}
		a := build()
		tx.redJSON, tx.TimeConsumedMs, tx.MaxMs = a.red(minutes, tMs), a.durSum, a.maxMs
		totalTime += a.durSum
		out = append(out, tx)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range out {
		if totalTime > 0 {
			out[i].TimeShare = out[i].TimeConsumedMs / totalTime
		}
	}
	val := func(p *float64) float64 {
		if p == nil {
			return -1
		}
		return *p
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch sortBy {
		case "throughput":
			return a.Requests > b.Requests
		case "slowest":
			return val(a.P95Ms) > val(b.P95Ms)
		case "errors":
			return a.Errors > b.Errors
		}
		return a.TimeConsumedMs > b.TimeConsumedMs
	})
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"transactions": out, "apdex_t_ms": tMs})
	return nil
}

type histBinJSON struct {
	FromMs float64 `json:"from_ms"`
	ToMs   float64 `json:"to_ms"`
	Count  float64 `json:"count"`
}

func histBins(h apm.Hist) []histBinJSON {
	out := []histBinJSON{}
	for i, k := range h.Keys {
		if h.Vals[i] > 0 {
			out = append(out, histBinJSON{FromMs: apm.BucketLowerMs(k), ToMs: apm.BucketUpperMs(k), Count: h.Vals[i]})
		}
	}
	return out
}

type traceSampleJSON struct {
	TraceID         string  `json:"trace_id"`
	SpanID          string  `json:"span_id"`
	Timestamp       string  `json:"timestamp"`
	DurationMs      float64 `json:"duration_ms"`
	IsError         bool    `json:"is_error"`
	HTTPStatusCode  uint16  `json:"http_status_code"`
	ServiceName     string  `json:"service_name"`
	TransactionName string  `json:"transaction_name"`
}

func (s *Server) apmTransaction(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	tx := txFilter{name: r.URL.Query().Get("name"), typ: r.URL.Query().Get("type")}
	if tx.name == "" || len(tx.name) > 4096 {
		return badRequest("name is required")
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	step, err := apmStep(r, from, to)
	if err != nil {
		return err
	}
	tMs, _ := s.apdexTMs(s.apmSettings(r), f.key())
	total, totals, points, err := s.txTotalsAndSeries(r, sc, f, tx, from, to, step, tMs)
	if err != nil {
		return err
	}
	q := f.apply(spanRange(sc.From(query.Spans).Columns("trace_id", "span_id", "timestamp", "duration_ns", "is_error", "http_status_code"), from, to)).
		Where("is_entry").OrderBy("duration_ns DESC").Limit(10)
	tx.apply(q)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	slowest := []traceSampleJSON{}
	for rows.Next() {
		ts := traceSampleJSON{ServiceName: f.name, TransactionName: tx.name}
		var t time.Time
		var dur uint64
		if err := rows.Scan(&ts.TraceID, &ts.SpanID, &t, &dur, &ts.IsError, &ts.HTTPStatusCode); err != nil {
			return err
		}
		ts.Timestamp, ts.DurationMs = formatTime(t), float64(dur)/1e6
		slowest = append(slowest, ts)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"transaction_name": tx.name, "transaction_type": tx.typ, "step": formatStep(step),
		"apdex_t_ms": tMs, "totals": totals, "max_ms": total.maxMs, "series": points, "histogram": histBins(total.hist), "slowest": slowest})
	return nil
}

// ---- databases ----

func (s *Server) apmDatabases(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.apmLimit(r, 100, 1000)
	if err != nil {
		return err
	}
	sortBy := r.URL.Query().Get("sort")
	order := map[string]string{"": "a_dsum DESC", "time": "a_dsum DESC", "calls": "a_req DESC", "slowest": "a_avg DESC", "errors": "a_err DESC"}[sortBy]
	if order == "" {
		return badRequest("sort must be time, calls, slowest or errors")
	}
	q := f.apply(minuteRange(sc.From(query.ApmDBQueries1m).Columns(cols([]string{"db_system", "db_name", "db_statement_normalized", "anyLast(db_operation) AS a_op"},
		callAggColumns, []string{"max(duration_max_ms) AS a_max", "sum(duration_sum_ms) / greatest(sum(calls), 1e-9) AS a_avg"})...), from, to)).
		GroupBy("db_system", "db_name", "db_statement_normalized").OrderBy(order).Limit(limit)
	if sys := r.URL.Query().Get("db_system"); sys != "" {
		q.Where("db_system = {db_system:String}").Param("db_system", sys)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type dbQueryJSON struct {
		DBSystem       string   `json:"db_system"`
		DBName         string   `json:"db_name"`
		Operation      string   `json:"db_operation"`
		Statement      string   `json:"statement"`
		Calls          float64  `json:"calls"`
		Throughput     float64  `json:"throughput"`
		Errors         float64  `json:"errors"`
		ErrorRate      float64  `json:"error_rate"`
		AvgMs          *float64 `json:"avg_ms"`
		P95Ms          *float64 `json:"p95_ms"`
		MaxMs          float64  `json:"max_ms"`
		TimeConsumedMs float64  `json:"time_consumed_ms"`
		TimeShare      float64  `json:"time_share"`
	}
	minutes := rangeMinutes(from, to)
	out := []dbQueryJSON{}
	var totalTime float64
	for rows.Next() {
		var d dbQueryJSON
		var avg float64
		dest, build := callAggDest()
		if err := rows.Scan(append(append([]any{&d.DBSystem, &d.DBName, &d.Statement, &d.Operation}, dest...), &d.MaxMs, &avg)...); err != nil {
			return err
		}
		red := build().red(minutes, 0)
		d.Calls, d.Throughput, d.Errors, d.ErrorRate, d.AvgMs, d.P95Ms = red.Requests, red.Throughput, red.Errors, red.ErrorRate, red.AvgMs, red.P95Ms
		d.TimeConsumedMs = build().durSum
		totalTime += d.TimeConsumedMs
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range out {
		if totalTime > 0 {
			out[i].TimeShare = out[i].TimeConsumedMs / totalTime
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"queries": out})
	return nil
}

// ---- service map ----

// MapEdgeRow is an attribute edge (apm_service_edges_1m) aggregated over a range.
type MapEdgeRow struct {
	Source     apm.ServiceKey
	TargetType string
	TargetName string
	agg        redAgg
}

// MapLinkRow is a trace-linked edge (apm_service_links_1m).
type MapLinkRow struct {
	Source, Target apm.ServiceKey
	Via            string
	agg            redAgg
}

type mapNodeJSON struct {
	ID               string   `json:"id"`
	Type             string   `json:"type"`
	Name             string   `json:"name"`
	ServiceNamespace string   `json:"service_namespace"`
	Environment      string   `json:"environment"`
	Requests         float64  `json:"requests"`
	Throughput       float64  `json:"throughput"`
	ErrorRate        float64  `json:"error_rate"`
	AvgMs            *float64 `json:"avg_ms"`
	P95Ms            *float64 `json:"p95_ms"`
	Apdex            *float64 `json:"apdex"`
	// Services only: hosts and containers that reported the service since from (apm_service_hosts/_containers).
	HostCount      int `json:"host_count"`
	ContainerCount int `json:"container_count"`
}

type mapEdgeJSON struct {
	ID         string   `json:"id"`
	Source     string   `json:"source"`
	Target     string   `json:"target"`
	TargetType string   `json:"target_type"`
	Calls      float64  `json:"calls"`
	Throughput float64  `json:"throughput"`
	Errors     float64  `json:"errors"`
	ErrorRate  float64  `json:"error_rate"`
	AvgMs      *float64 `json:"avg_ms"`
	P95Ms      *float64 `json:"p95_ms"`
}

func serviceNodeID(k apm.ServiceKey) string {
	return "service:" + k.Name + "|" + k.Namespace + "|" + k.Environment
}

func depNodeID(typ, name string) string { return typ + ":" + name }

// serviceMapInput is everything mergeServiceMap needs.
type serviceMapInput struct {
	Services map[apm.ServiceKey]redAgg // entry-span totals of every known service
	Apdex    func(apm.ServiceKey) float64
	Edges    []MapEdgeRow
	Links    []MapLinkRow
	Minutes  float64
	// Focus restricts the map to edges touching this service (nil: whole map).
	Focus *svcFilter
}

// mergeServiceMap applies the merge rules of apm.md §5.
func mergeServiceMap(in serviceMapInput) ([]mapNodeJSON, []mapEdgeJSON) {
	type edgeKey struct{ src, tgt string }
	edges := map[edgeKey]*redAgg{}
	edgeType := map[edgeKey]string{}
	nodes := map[string]*mapNodeJSON{}
	serviceNode := func(k apm.ServiceKey) string {
		id := serviceNodeID(k)
		if nodes[id] == nil {
			nodes[id] = &mapNodeJSON{ID: id, Type: apm.PeerService, Name: k.Name, ServiceNamespace: k.Namespace, Environment: k.Environment}
		}
		return id
	}
	addEdge := func(src, tgt, typ string, a redAgg) {
		k := edgeKey{src, tgt}
		if edges[k] == nil {
			edges[k] = &redAgg{}
			edgeType[k] = typ
		}
		edges[k].add(a)
	}
	vias := map[string]map[string]bool{}
	linked := map[edgeKey]bool{}
	for _, l := range in.Links {
		src, tgt := serviceNode(l.Source), serviceNode(l.Target)
		addEdge(src, tgt, apm.PeerService, l.agg)
		linked[edgeKey{src, tgt}] = true
		if l.Via != "" {
			if vias[src] == nil {
				vias[src] = map[string]bool{}
			}
			vias[src][l.Via] = true
		}
	}
	byName := map[string][]apm.ServiceKey{}
	for k := range in.Services {
		byName[k.Name] = append(byName[k.Name], k)
	}
	for _, e := range in.Edges {
		src := serviceNode(e.Source)
		switch e.TargetType {
		case apm.PeerService:
			target := apm.ServiceKey{Name: e.TargetName}
			for _, k := range byName[e.TargetName] {
				target = k
				if k.Namespace == e.Source.Namespace && k.Environment == e.Source.Environment {
					break
				}
			}
			tgt := serviceNode(target)
			if linked[edgeKey{src, tgt}] {
				continue // the trace-linked edge counts the same client spans
			}
			addEdge(src, tgt, apm.PeerService, e.agg)
		case apm.PeerExternal:
			if vias[src][e.TargetName] {
				continue // the address is a known service (trace-linked edge)
			}
			fallthrough
		default:
			tgt := depNodeID(e.TargetType, e.TargetName)
			if nodes[tgt] == nil {
				nodes[tgt] = &mapNodeJSON{ID: tgt, Type: e.TargetType, Name: e.TargetName}
			}
			addEdge(src, tgt, e.TargetType, e.agg)
		}
	}
	for k := range in.Services {
		serviceNode(k)
	}
	focused := func(id string) bool {
		n := nodes[id]
		if in.Focus == nil {
			return true
		}
		return n.Type == apm.PeerService && n.Name == in.Focus.name &&
			(in.Focus.ns == nil || *in.Focus.ns == n.ServiceNamespace) && (in.Focus.env == nil || *in.Focus.env == n.Environment)
	}
	keep := map[string]bool{}
	outEdges := []mapEdgeJSON{}
	incoming := map[string]*redAgg{}
	for k, a := range edges {
		if !focused(k.src) && !focused(k.tgt) {
			continue
		}
		keep[k.src], keep[k.tgt] = true, true
		red := a.red(in.Minutes, 0)
		outEdges = append(outEdges, mapEdgeJSON{ID: k.src + "->" + k.tgt, Source: k.src, Target: k.tgt, TargetType: edgeType[k],
			Calls: red.Requests, Throughput: red.Throughput, Errors: red.Errors, ErrorRate: red.ErrorRate, AvgMs: red.AvgMs, P95Ms: red.P95Ms})
		if incoming[k.tgt] == nil {
			incoming[k.tgt] = &redAgg{}
		}
		incoming[k.tgt].add(*a)
	}
	outNodes := []mapNodeJSON{}
	for id, n := range nodes {
		if !keep[id] && !(focused(id) && n.Type == apm.PeerService) && in.Focus != nil {
			continue
		}
		if n.Type == apm.PeerService {
			k := apm.ServiceKey{Name: n.Name, Namespace: n.ServiceNamespace, Environment: n.Environment}
			if a, ok := in.Services[k]; ok {
				t := 0.0
				if in.Apdex != nil {
					t = in.Apdex(k)
				}
				red := a.red(in.Minutes, t)
				n.Requests, n.Throughput, n.ErrorRate, n.AvgMs, n.P95Ms, n.Apdex = red.Requests, red.Throughput, red.ErrorRate, red.AvgMs, red.P95Ms, red.Apdex
			}
		} else if a := incoming[id]; a != nil {
			red := a.red(in.Minutes, 0)
			n.Requests, n.Throughput, n.ErrorRate, n.AvgMs, n.P95Ms = red.Requests, red.Throughput, red.ErrorRate, red.AvgMs, red.P95Ms
		}
		outNodes = append(outNodes, *n)
	}
	sort.Slice(outNodes, func(i, j int) bool { return outNodes[i].ID < outNodes[j].ID })
	sort.Slice(outEdges, func(i, j int) bool { return outEdges[i].ID < outEdges[j].ID })
	return outNodes, outEdges
}

func (s *Server) apmMap(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	qp := r.URL.Query()
	var focus *svcFilter
	if name := qp.Get("service"); name != "" {
		focus = &svcFilter{name: name, ns: optionalParam(qp, "namespace"), env: optionalParam(qp, "environment")}
	}
	// Without a focus service, namespace/environment filter the whole map (sources and link targets).
	var ns, env *string
	if focus == nil {
		ns, env = optionalParam(qp, "namespace"), optionalParam(qp, "environment")
	}
	idCols := []string{"service_name", "service_namespace", "deployment_environment"}
	in := serviceMapInput{Services: map[apm.ServiceKey]redAgg{}, Minutes: rangeMinutes(from, to), Focus: focus}

	tq := minuteRange(sc.From(query.ApmTransactions1m).Columns(cols(idCols, txAggColumns)...), from, to).GroupBy(idCols...).Limit(s.cfg.MaxRows)
	applyScope(tq, ns, env)
	rows, err := sc.Query(r.Context(), tq)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k apm.ServiceKey
		dest, build := txAggDest()
		if err := rows.Scan(append([]any{&k.Name, &k.Namespace, &k.Environment}, dest...)...); err != nil {
			rows.Close()
			return err
		}
		in.Services[k] = build()
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Services with spans but no entry spans in the range (e.g. only outgoing calls).
	sq := sc.From(query.ApmServices).Columns(idCols...).Where("last_seen >= fromUnixTimestamp64Nano({t_seen:Int64})").
		Param("t_seen", from.UnixNano()).GroupBy(idCols...).Limit(s.cfg.MaxRows)
	applyScope(sq, ns, env)
	rows, err = sc.Query(r.Context(), sq)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k apm.ServiceKey
		if err := rows.Scan(&k.Name, &k.Namespace, &k.Environment); err != nil {
			rows.Close()
			return err
		}
		if _, ok := in.Services[k]; !ok {
			in.Services[k] = redAgg{}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	eq := minuteRange(sc.From(query.ApmServiceEdges1m).Columns(cols(idCols, []string{"target_type", "target_name"}, callAggColumns)...), from, to).
		GroupBy(append(idCols, "target_type", "target_name")...).Limit(s.cfg.MaxRows)
	applyScope(eq, ns, env)
	rows, err = sc.Query(r.Context(), eq)
	if err != nil {
		return err
	}
	for rows.Next() {
		var e MapEdgeRow
		dest, build := callAggDest()
		if err := rows.Scan(append([]any{&e.Source.Name, &e.Source.Namespace, &e.Source.Environment, &e.TargetType, &e.TargetName}, dest...)...); err != nil {
			rows.Close()
			return err
		}
		e.agg = build()
		in.Edges = append(in.Edges, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	lcols := append(append([]string{}, idCols...), "target_service", "target_namespace", "target_environment", "via")
	lq := minuteRange(sc.From(query.ApmServiceLinks1m).Final().Columns(cols(lcols, callAggColumns)...), from, to).GroupBy(lcols...).Limit(s.cfg.MaxRows)
	applyScope(lq, ns, env)
	if ns != nil {
		lq.Where("target_namespace = {svc_ns:String}")
	}
	if env != nil {
		lq.Where("target_environment = {svc_env:String}")
	}
	rows, err = sc.Query(r.Context(), lq)
	if err != nil {
		return err
	}
	for rows.Next() {
		var l MapLinkRow
		dest, build := callAggDest()
		if err := rows.Scan(append([]any{&l.Source.Name, &l.Source.Namespace, &l.Source.Environment, &l.Target.Name, &l.Target.Namespace, &l.Target.Environment, &l.Via}, dest...)...); err != nil {
			rows.Close()
			return err
		}
		l.agg = build()
		in.Links = append(in.Links, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	settings := s.apmSettings(r)
	in.Apdex = func(k apm.ServiceKey) float64 {
		t, _ := s.apdexTMs(settings, k)
		return t
	}
	nodes, edges := mergeServiceMap(in)
	if err := s.mapNodeCounts(r.Context(), sc, from, nodes); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "edges": edges})
	return nil
}

// ---- trace search ----

const (
	maxTraceAttrFilters  = 10
	maxTraceAttrKeyBytes = 256
)

func (s *Server) apmTraces(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.apmLimit(r, 50, 500)
	if err != nil {
		return err
	}
	qp := r.URL.Query()
	q := spanRange(sc.From(query.Spans).Columns("trace_id", "span_id", "timestamp", "duration_ns", "is_error", "http_status_code",
		"service_name", "service_namespace", "deployment_environment", "transaction_type", "transaction_name"), from, to).
		Where("is_entry").Limit(limit)
	service := qp.Get("service")
	if service != "" {
		(svcFilter{name: service, ns: optionalParam(qp, "namespace"), env: optionalParam(qp, "environment")}).apply(q)
	} else {
		applyScope(q, optionalParam(qp, "namespace"), optionalParam(qp, "environment"))
	}
	(txFilter{name: qp.Get("transaction"), typ: qp.Get("type")}).apply(q)
	for _, p := range []struct{ name, cond string }{{"min_duration_ms", "duration_ns >= {min_ns:UInt64}"}, {"max_duration_ms", "duration_ns <= {max_ns:UInt64}"}} {
		v := qp.Get(p.name)
		if v == "" {
			continue
		}
		ms, err := strconv.ParseFloat(v, 64)
		if err != nil || ms < 0 || !finite(ms) || ms > 1e12 {
			return badRequest("%s must be a non-negative number", p.name)
		}
		param := "min_ns"
		if strings.HasPrefix(p.name, "max") {
			param = "max_ns"
		}
		q.Where(p.cond).Param(param, uint64(ms*1e6))
	}
	switch qp.Get("error") {
	case "":
	case "true", "1":
		q.Where("is_error")
	case "false", "0":
		q.Where("NOT is_error")
	default:
		return badRequest("error must be true or false")
	}
	var keys []string
	for p := range qp {
		if k, ok := strings.CutPrefix(p, "attr."); ok {
			keys = append(keys, k)
		}
	}
	if len(keys) > maxTraceAttrFilters {
		return badRequest("at most %d attr.* filters", maxTraceAttrFilters)
	}
	sort.Strings(keys)
	for i, k := range keys {
		vals := qp["attr."+k]
		if k == "" || len(k) > maxTraceAttrKeyBytes || len(vals) != 1 || vals[0] == "" || len(vals[0]) > maxAttrFilterValueBytes {
			return badRequest("attr.%s: a key of 1-%d bytes and exactly one non-empty value of at most %d bytes are required", truncate(k, 64), maxTraceAttrKeyBytes, maxAttrFilterValueBytes)
		}
		n := strconv.Itoa(i)
		q.Where("attributes[{attr_key_"+n+":String}] = {attr_value_"+n+":String}").Param("attr_key_"+n, k).Param("attr_value_"+n, vals[0])
	}
	switch qp.Get("sort") {
	case "", "timestamp":
		q.OrderBy("timestamp DESC")
	case "duration":
		q.OrderBy("duration_ns DESC")
	default:
		return badRequest("sort must be timestamp or duration")
	}
	if service == "" {
		q.LimitBy(1, "trace_id")
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type traceJSON struct {
		traceSampleJSON
		ServiceNamespace string `json:"service_namespace"`
		Environment      string `json:"environment"`
		TransactionType  string `json:"transaction_type"`
	}
	out := []traceJSON{}
	for rows.Next() {
		var tr traceJSON
		var t time.Time
		var dur uint64
		if err := rows.Scan(&tr.TraceID, &tr.SpanID, &t, &dur, &tr.IsError, &tr.HTTPStatusCode, &tr.ServiceName, &tr.ServiceNamespace,
			&tr.Environment, &tr.TransactionType, &tr.TransactionName); err != nil {
			return err
		}
		tr.Timestamp, tr.DurationMs = formatTime(t), float64(dur)/1e6
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"traces": out})
	return nil
}
