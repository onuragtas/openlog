package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/auth"
)

// Error inbox (apm.md §3.4, api.md "APM"): error groups with their workflow state (PostgreSQL), filters, bulk
// status/assignee changes, comments and activity. Occurrence data comes from ClickHouse as before.

// SetAPMErrorStates enables the error workflow. Without a store (static auth mode) every group is unresolved and
// unassigned and the mutating endpoints are not registered.
func (s *Server) SetAPMErrorStates(store apm.ErrorStateStore) {
	if s.apm == nil {
		s.apm = &apmState{defaultT: apm.DefaultApdexT}
	}
	s.apm.errors = store
	s.srv.Handler = s.Handler()
}

func (s *Server) errorStore() apm.ErrorStateStore { return s.apmConf().errors }

// apmGARoutes registers the APM GA endpoints (error workflow, deployments, map path).
func (s *Server) apmGARoutes(mux *http.ServeMux) {
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/apm/services/{service}/errors", s.apmErrorInbox)
	route("GET /api/v1/apm/services/{service}/errors/{group_id}", s.apmErrorGroupDetail)
	route("GET /api/v1/apm/errors", s.apmErrorInbox)
	route("GET /api/v1/apm/errors/groups/{group_id}/comments", s.apmErrorComments)
	route("GET /api/v1/apm/services/{service}/deployments", s.apmDeployments)
	route("GET /api/v1/apm/services/{service}/deployments/compare", s.apmDeploymentCompare)
	route("GET /api/v1/apm/map/path", s.apmMapPath)
	if s.accounts != nil {
		s.apmWriteRoute(mux, "PATCH /api/v1/apm/errors/groups", s.apmPatchErrorGroups)
		s.apmWriteRoute(mux, "POST /api/v1/apm/errors/groups/{group_id}/comments", s.apmAddErrorComment)
		s.apmWriteRoute(mux, "DELETE /api/v1/apm/errors/groups/{group_id}/comments/{comment_id}", s.apmDeleteErrorComment)
	}
}

// ---- scope and filters ----

// errorScope is one service (path or ?service=) or every service, optionally one namespace/environment.
type errorScope struct {
	f       *svcFilter
	ns, env *string
}

func parseErrorScope(r *http.Request) (errorScope, error) {
	qp := r.URL.Query()
	if r.PathValue("service") != "" {
		f, err := parseSvcFilter(r)
		if err != nil {
			return errorScope{}, err
		}
		return errorScope{f: &f}, nil
	}
	if name := qp.Get("service"); name != "" {
		if len(name) > maxServiceNameBytes {
			return errorScope{}, badRequest("service name must be 1-%d bytes", maxServiceNameBytes)
		}
		return errorScope{f: &svcFilter{name: name, ns: optionalParam(qp, "namespace"), env: optionalParam(qp, "environment")}}, nil
	}
	return errorScope{ns: optionalParam(qp, "namespace"), env: optionalParam(qp, "environment")}, nil
}

func (e errorScope) apply(q *query.Select) *query.Select {
	if e.f != nil {
		return e.f.apply(q)
	}
	applyScope(q, e.ns, e.env)
	return q
}

func (e errorScope) stateFilter() apm.ErrorStateFilter {
	var f apm.ErrorStateFilter
	if e.f != nil {
		f.Service, f.Namespace, f.Environment = e.f.name, e.f.ns, e.f.env
	} else {
		f.Namespace, f.Environment = e.ns, e.env
	}
	return f
}

type inboxFilter struct {
	statuses map[apm.ErrorStatus]bool // empty: all
	assignee *string                  // nil: any, "": unassigned, else a user id
	q        string
	sort     string
}

func parseInboxFilter(r *http.Request, p *auth.Principal) (inboxFilter, error) {
	qp := r.URL.Query()
	f := inboxFilter{statuses: map[apm.ErrorStatus]bool{}, q: strings.ToLower(strings.TrimSpace(qp.Get("q"))), sort: qp.Get("sort")}
	if v := qp.Get("status"); v != "" && v != "all" {
		for _, part := range strings.Split(v, ",") {
			st := apm.ErrorStatus(strings.TrimSpace(part))
			if !st.Valid() {
				return f, badRequest("status must be all or a comma-separated list of unresolved, resolved, ignored")
			}
			f.statuses[st] = true
		}
	}
	switch v := qp.Get("assignee"); v {
	case "", "any":
	case "none":
		f.assignee = new(string)
	case "me":
		if p == nil || p.UserID == "" {
			return f, badRequest("assignee=me needs a signed-in user")
		}
		id := p.UserID
		f.assignee = &id
	default:
		if len(v) != 36 {
			return f, badRequest("assignee must be any, none, me or a user id")
		}
		f.assignee = &v
	}
	switch f.sort {
	case "":
		f.sort = "count"
	case "count", "last_seen", "first_seen":
	default:
		return f, badRequest("sort must be count, last_seen or first_seen")
	}
	if len(f.q) > 256 {
		return f, badRequest("q is at most 256 bytes")
	}
	return f, nil
}

// needsStateRows: groups without occurrences in the range can match (resolved/ignored/assigned views).
func (f inboxFilter) needsStateRows() bool {
	return (f.assignee != nil && *f.assignee != "") || f.statuses[apm.StatusResolved] || f.statuses[apm.StatusIgnored]
}

func (f inboxFilter) statusList() []apm.ErrorStatus {
	var out []apm.ErrorStatus
	for _, st := range []apm.ErrorStatus{apm.StatusUnresolved, apm.StatusResolved, apm.StatusIgnored} {
		if f.statuses[st] {
			out = append(out, st)
		}
	}
	return out
}

func (f inboxFilter) matchAssignee(st apm.ErrorGroupState) bool {
	return f.assignee == nil || *f.assignee == st.AssigneeUserID
}

// ---- JSON ----

type errorAssigneeJSON struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}

type errorWorkflowJSON struct {
	Status            string             `json:"status"`
	Assignee          *errorAssigneeJSON `json:"assignee"`
	ResolvedAt        *string            `json:"resolved_at"`
	ResolvedInVersion string             `json:"resolved_in_version"`
	ResolvedByEmail   string             `json:"resolved_by_email"`
	RegressedAt       *string            `json:"regressed_at"`
	RegressionCount   int                `json:"regression_count"`
	CommentCount      int                `json:"comment_count"`
	UpdatedAt         *string            `json:"updated_at"`
	UpdatedByEmail    string             `json:"updated_by_email"`
}

func workflowJSON(st apm.ErrorGroupState) errorWorkflowJSON {
	out := errorWorkflowJSON{Status: string(st.EffectiveStatus()), ResolvedAt: optTime(st.ResolvedAt), ResolvedInVersion: st.ResolvedInVersion,
		ResolvedByEmail: st.ResolvedByEmail, RegressedAt: optTime(st.RegressedAt), RegressionCount: st.RegressionCount,
		CommentCount: st.CommentCount, UpdatedByEmail: st.UpdatedByEmail}
	if st.AssigneeUserID != "" {
		out.Assignee = &errorAssigneeJSON{UserID: st.AssigneeUserID, Email: st.AssigneeEmail, Name: st.AssigneeName}
	}
	if !st.UpdatedAt.IsZero() {
		out.UpdatedAt = optTime(&st.UpdatedAt)
	}
	return out
}

func (wj errorWorkflowJSON) addTo(m map[string]any) {
	m["status"], m["assignee"], m["resolved_at"], m["resolved_in_version"] = wj.Status, wj.Assignee, wj.ResolvedAt, wj.ResolvedInVersion
	m["resolved_by_email"], m["regressed_at"], m["regression_count"] = wj.ResolvedByEmail, wj.RegressedAt, wj.RegressionCount
	m["comment_count"], m["updated_at"], m["updated_by_email"] = wj.CommentCount, wj.UpdatedAt, wj.UpdatedByEmail
}

type errorGroupJSON struct {
	GroupID          string       `json:"group_id"`
	ServiceName      string       `json:"service_name"`
	ServiceNamespace string       `json:"service_namespace"`
	Environment      string       `json:"environment"`
	ErrorType        string       `json:"error_type"`
	Message          string       `json:"message"`
	Count            float64      `json:"count"`
	TotalCount       float64      `json:"total_count"`
	FirstSeen        *string      `json:"first_seen"`
	LastSeen         *string      `json:"last_seen"`
	LastTraceID      string       `json:"last_trace_id"`
	LastSpanName     string       `json:"last_span_name"`
	Sparkline        [][2]float64 `json:"sparkline"`
	errorWorkflowJSON
}

// ---- ClickHouse group facts ----

type groupInfo struct {
	key                                             apm.ServiceKey
	first, last                                     time.Time
	total                                           float64
	typ, msg, traceID, spanID, spanName, raw, stack string
}

// errorGroupInfos reads apm_error_groups for ids (decimal strings) within scope.
func errorGroupInfos(ctx context.Context, sc *query.Scope, scope func(*query.Select) *query.Select, ids []string, withSample bool) (map[uint64]groupInfo, error) {
	out := map[uint64]groupInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	columns := []string{"service_name", "service_namespace", "deployment_environment", "error_group_id", "min(first_seen) AS m_first",
		"max(last_seen) AS m_last", "sum(count) AS m_count", "anyLast(error_type) AS m_type", "anyLast(error_message) AS m_msg",
		"argMaxMerge(last_trace_id) AS m_trace", "argMaxMerge(last_span_id) AS m_span", "argMaxMerge(last_span_name) AS m_name"}
	if withSample {
		columns = append(columns, "argMaxMerge(last_message) AS m_raw", "argMaxMerge(last_stacktrace) AS m_stack")
	}
	q := scope(sc.From(query.ApmErrorGroups).Columns(columns...)).
		Where("has({group_ids:Array(String)}, toString(error_group_id))").Param("group_ids", ids).
		GroupBy("service_name", "service_namespace", "deployment_environment", "error_group_id")
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uint64
		var m groupInfo
		dest := []any{&m.key.Name, &m.key.Namespace, &m.key.Environment, &id, &m.first, &m.last, &m.total, &m.typ, &m.msg, &m.traceID, &m.spanID, &m.spanName}
		if withSample {
			dest = append(dest, &m.raw, &m.stack)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}

// ---- GET /apm/errors, /apm/services/{service}/errors ----

const maxInboxCandidates = 2000

func (s *Server) apmErrorInbox(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	scope, err := parseErrorScope(r)
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
	limit, err := s.apmLimit(r, 50, 500)
	if err != nil {
		return err
	}
	p, _ := auth.PrincipalFrom(r.Context())
	filter, err := parseInboxFilter(r, p)
	if err != nil {
		return err
	}
	ctx := r.Context()
	store := s.errorStore()
	orgID := ""
	if p != nil {
		orgID = p.OrgID
	}
	workflow := store != nil && orgID != ""

	idCols := []string{"service_name", "service_namespace", "deployment_environment"}
	cq := scope.apply(minuteRange(sc.From(query.ApmErrors1m).Columns(cols(idCols, []string{"error_group_id", "sum(count) AS m_count"})...), from, to)).
		GroupBy(cols(idCols, []string{"error_group_id"})...).OrderBy("m_count DESC", "error_group_id").Limit(maxInboxCandidates + 1)
	rows, err := sc.Query(ctx, cq)
	if err != nil {
		return err
	}
	counts := map[uint64]float64{}
	var order []uint64
	truncated := false
	for rows.Next() {
		var k apm.ServiceKey
		var id uint64
		var c float64
		if err := rows.Scan(&k.Name, &k.Namespace, &k.Environment, &id, &c); err != nil {
			rows.Close()
			return err
		}
		if len(order) == maxInboxCandidates {
			truncated = true
			continue
		}
		counts[id] = c
		order = append(order, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	states := map[uint64]apm.ErrorGroupState{}
	if workflow {
		if len(order) > 0 {
			list, err := store.States(ctx, orgID, apm.ErrorStateFilter{GroupIDs: order})
			if err != nil {
				return err
			}
			for _, st := range list {
				states[st.GroupID] = st
			}
		}
		if filter.needsStateRows() {
			sf := scope.stateFilter()
			sf.Statuses, sf.Assignee, sf.Limit = filter.statusList(), filter.assignee, 500
			list, err := store.States(ctx, orgID, sf)
			if err != nil {
				return err
			}
			for _, st := range list {
				if _, ok := counts[st.GroupID]; !ok {
					counts[st.GroupID] = 0
					order = append(order, st.GroupID)
				}
				states[st.GroupID] = st
			}
		}
		var resolved []apm.ErrorGroupState
		for _, st := range states {
			if st.EffectiveStatus() == apm.StatusResolved {
				resolved = append(resolved, st)
			}
		}
		if len(resolved) > 0 {
			updated, _, err := apm.CheckRegressions(ctx, sc, store, orgID, resolved)
			if err != nil {
				s.log.Warn("apm error regression check failed", "err", err)
			}
			for _, st := range updated {
				states[st.GroupID] = st
			}
		}
	}

	ids := make([]string, len(order))
	for i, id := range order {
		ids[i] = strconv.FormatUint(id, 10)
	}
	infos, err := errorGroupInfos(ctx, sc, scope.apply, ids, false)
	if err != nil {
		return err
	}
	statusCounts := map[string]int{string(apm.StatusUnresolved): 0, string(apm.StatusResolved): 0, string(apm.StatusIgnored): 0}
	groups := []*errorGroupJSON{}
	for _, id := range order {
		m, ok := infos[id]
		if !ok {
			continue // expired from apm_error_groups (retention) or outside the scope
		}
		st := states[id]
		st.GroupID, st.Key = id, m.key
		if !filter.matchAssignee(st) {
			continue
		}
		if filter.q != "" && !strings.Contains(strings.ToLower(m.typ+"\x00"+m.msg+"\x00"+m.key.Name+"\x00"+m.spanName), filter.q) {
			continue
		}
		status := st.EffectiveStatus()
		statusCounts[string(status)]++
		if len(filter.statuses) > 0 && !filter.statuses[status] {
			continue
		}
		groups = append(groups, &errorGroupJSON{GroupID: apm.GroupIDString(id), ServiceName: m.key.Name, ServiceNamespace: m.key.Namespace,
			Environment: m.key.Environment, ErrorType: m.typ, Message: m.msg, Count: counts[id], TotalCount: m.total,
			FirstSeen: optTime(&m.first), LastSeen: optTime(&m.last), LastTraceID: m.traceID, LastSpanName: m.spanName,
			Sparkline: [][2]float64{}, errorWorkflowJSON: workflowJSON(st)})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		switch filter.sort {
		case "last_seen":
			if *a.LastSeen != *b.LastSeen {
				return *a.LastSeen > *b.LastSeen
			}
		case "first_seen":
			if *a.FirstSeen != *b.FirstSeen {
				return *a.FirstSeen > *b.FirstSeen
			}
		default:
			if a.Count != b.Count {
				return a.Count > b.Count
			}
			if a.TotalCount != b.TotalCount {
				return a.TotalCount > b.TotalCount
			}
		}
		return a.GroupID < b.GroupID
	})
	if len(groups) > limit {
		groups, truncated = groups[:limit], true
	}
	byID := map[uint64]*errorGroupJSON{}
	var sparkIDs []string
	for _, g := range groups {
		id, _ := apm.ParseGroupID(g.GroupID)
		byID[id] = g
		if g.Count > 0 {
			sparkIDs = append(sparkIDs, strconv.FormatUint(id, 10))
		}
	}
	if len(sparkIDs) > 0 {
		spark := scope.apply(minuteRange(sc.From(query.ApmErrors1m).Columns("error_group_id",
			"toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t", "sum(count) AS m_count"), from, to)).
			Where("has({group_ids:Array(String)}, toString(error_group_id))").Param("group_ids", sparkIDs).
			Param("step", uint32(step/time.Second)).GroupBy("error_group_id", "t").OrderBy("t")
		srows, err := sc.Query(ctx, spark)
		if err != nil {
			return err
		}
		for srows.Next() {
			var id uint64
			var t time.Time
			var c float64
			if err := srows.Scan(&id, &t, &c); err != nil {
				srows.Close()
				return err
			}
			if g := byID[id]; g != nil {
				g.Sparkline = append(g.Sparkline, [2]float64{float64(t.UnixMilli()), c})
			}
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "step": formatStep(step), "counts": statusCounts,
		"truncated": truncated, "workflow": workflow})
	return nil
}

// ---- GET /apm/services/{service}/errors/{group_id} ----

type affectedJSON struct {
	Value     string  `json:"value"`
	Name      string  `json:"name"`
	Count     float64 `json:"count"`
	FirstSeen string  `json:"first_seen"`
	LastSeen  string  `json:"last_seen"`
}

type errorCommentJSON struct {
	ID           string `json:"id"`
	AuthorUserID string `json:"author_user_id"`
	AuthorEmail  string `json:"author_email"`
	AuthorName   string `json:"author_name"`
	Body         string `json:"body"`
	CreatedAt    string `json:"created_at"`
}

func commentJSON(c apm.ErrorComment) errorCommentJSON {
	return errorCommentJSON{ID: c.ID, AuthorUserID: c.AuthorUserID, AuthorEmail: c.AuthorEmail, AuthorName: c.AuthorName, Body: c.Body, CreatedAt: formatTime(c.CreatedAt)}
}

type errorActivityJSON struct {
	Action     string         `json:"action"`
	ActorEmail string         `json:"actor_email"`
	Details    map[string]any `json:"details"`
	CreatedAt  string         `json:"created_at"`
}

func (s *Server) apmErrorGroupDetail(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	id, ok := apm.ParseGroupID(strings.ToLower(r.PathValue("group_id")))
	if !ok {
		return badRequest("group_id must be 16 hex characters")
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	step, err := apmStep(r, from, to)
	if err != nil {
		return err
	}
	ctx := r.Context()
	infos, err := errorGroupInfos(ctx, sc, f.apply, []string{strconv.FormatUint(id, 10)}, true)
	if err != nil {
		return err
	}
	m, found := infos[id]
	if !found {
		return notFound("error group not found")
	}
	series := f.apply(minuteRange(sc.From(query.ApmErrors1m).Columns("toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t", "sum(count) AS m_count"), from, to)).
		Where("error_group_id = {group_id:UInt64}").Param("group_id", id).Param("step", uint32(step/time.Second)).GroupBy("t").OrderBy("t")
	srows, err := sc.Query(ctx, series)
	if err != nil {
		return err
	}
	points := [][2]float64{}
	var count float64
	for srows.Next() {
		var t time.Time
		var c float64
		if err := srows.Scan(&t, &c); err != nil {
			srows.Close()
			return err
		}
		points = append(points, [2]float64{float64(t.UnixMilli()), c})
		count += c
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return err
	}
	q := f.apply(spanRange(sc.From(query.Spans).Columns("trace_id", "span_id", "timestamp", "name", "transaction_name", "duration_ns",
		"if(arrayLastIndex(x -> x = 'exception', events_name) > 0, events_attributes[arrayLastIndex(x -> x = 'exception', events_name)]['exception.message'], status_message) AS m_msg",
		"resource_attributes['service.version'] AS m_version", "host_id"), from, to)).
		Where("error_group_id = {group_id:UInt64}").Param("group_id", id).OrderBy("timestamp DESC").Limit(20)
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return err
	}
	type sampleJSON struct {
		TraceID         string  `json:"trace_id"`
		SpanID          string  `json:"span_id"`
		Timestamp       string  `json:"timestamp"`
		SpanName        string  `json:"span_name"`
		TransactionName string  `json:"transaction_name"`
		DurationMs      float64 `json:"duration_ms"`
		Message         string  `json:"message"`
		Version         string  `json:"version"`
		HostID          string  `json:"host_id"`
	}
	samples := []sampleJSON{}
	for rows.Next() {
		var sm sampleJSON
		var t time.Time
		var dur uint64
		if err := rows.Scan(&sm.TraceID, &sm.SpanID, &t, &sm.SpanName, &sm.TransactionName, &dur, &sm.Message, &sm.Version, &sm.HostID); err != nil {
			rows.Close()
			return err
		}
		sm.Timestamp, sm.DurationMs = formatTime(t), float64(dur)/1e6
		samples = append(samples, sm)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	affected, err := s.errorAffected(ctx, sc, f, id)
	if err != nil {
		return err
	}

	state := apm.ErrorGroupState{ErrorGroupRef: apm.ErrorGroupRef{GroupID: id, Key: m.key}}
	comments, activity := []errorCommentJSON{}, []errorActivityJSON{}
	p, _ := auth.PrincipalFrom(ctx)
	store := s.errorStore()
	workflow := store != nil && p != nil && p.OrgID != ""
	if workflow {
		list, err := store.States(ctx, p.OrgID, apm.ErrorStateFilter{GroupIDs: []uint64{id}})
		if err != nil {
			return err
		}
		if len(list) > 0 {
			state = list[0]
		}
		if state.EffectiveStatus() == apm.StatusResolved {
			updated, _, err := apm.CheckRegressions(ctx, sc, store, p.OrgID, []apm.ErrorGroupState{state})
			if err != nil {
				s.log.Warn("apm error regression check failed", "err", err)
			} else {
				state = updated[0]
			}
		}
		cs, err := store.Comments(ctx, p.OrgID, id)
		if err != nil {
			return err
		}
		for _, c := range cs {
			comments = append(comments, commentJSON(c))
		}
		acts, err := store.Activity(ctx, p.OrgID, id, 50)
		if err != nil {
			return err
		}
		for _, a := range acts {
			activity = append(activity, errorActivityJSON{Action: a.Action, ActorEmail: a.ActorEmail, Details: a.Details, CreatedAt: formatTime(a.CreatedAt)})
		}
		state.CommentCount = len(comments)
	}
	out := map[string]any{
		"group_id": apm.GroupIDString(id), "service_name": m.key.Name, "service_namespace": m.key.Namespace, "environment": m.key.Environment,
		"error_type": m.typ, "message": m.msg, "count": count, "total_count": m.total,
		"first_seen": formatTime(m.first), "last_seen": formatTime(m.last), "last_message": m.raw, "stacktrace": m.stack,
		"last_trace_id": m.traceID, "last_span_id": m.spanID, "last_span_name": m.spanName,
		"step": formatStep(step), "series": points, "samples": samples, "affected": affected,
		"comments": comments, "activity": activity, "workflow": workflow,
	}
	workflowJSON(state).addTo(out)
	writeJSON(w, http.StatusOK, out)
	return nil
}

// errorAffected lists the top 20 versions, hosts, containers and transactions of a group over retention.
func (s *Server) errorAffected(ctx context.Context, sc *query.Scope, f svcFilter, id uint64) (map[string][]affectedJSON, error) {
	out := map[string][]affectedJSON{"versions": {}, "hosts": {}, "containers": {}, "transactions": {}}
	keyOf := map[string]string{"version": "versions", "host": "hosts", "container": "containers", "transaction": "transactions"}
	q := f.apply(sc.From(query.ApmErrorGroupDims).Columns("dim", "value", "sum(count) AS m_count", "min(first_seen) AS m_first", "max(last_seen) AS m_last")).
		Where("error_group_id = {group_id:UInt64}").Param("group_id", id).
		GroupBy("dim", "value").OrderBy("dim", "m_count DESC", "value").LimitBy(20, "dim")
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	var hostIDs, containerIDs []string
	for rows.Next() {
		var dim string
		var a affectedJSON
		var first, last time.Time
		if err := rows.Scan(&dim, &a.Value, &a.Count, &first, &last); err != nil {
			rows.Close()
			return nil, err
		}
		key, ok := keyOf[dim]
		if !ok {
			continue
		}
		a.FirstSeen, a.LastSeen = formatTime(first), formatTime(last)
		out[key] = append(out[key], a)
		switch dim {
		case "host":
			hostIDs = append(hostIDs, a.Value)
		case "container":
			containerIDs = append(containerIDs, a.Value)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := func(q *query.Select, key string) error {
		rows, err := sc.Query(ctx, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		byID := map[string]string{}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			byID[id] = name
		}
		for i := range out[key] {
			out[key][i].Name = byID[out[key][i].Value]
		}
		return rows.Err()
	}
	if len(hostIDs) > 0 {
		if err := names(sc.From(query.Hosts).Columns("host_id", "argMax(host_name, last_seen)").
			Where("has({host_ids:Array(String)}, host_id)").Param("host_ids", hostIDs).GroupBy("host_id"), "hosts"); err != nil {
			return nil, err
		}
	}
	if len(containerIDs) > 0 {
		if err := names(sc.From(query.ApmServiceContainers).Columns("container_id", "argMaxMerge(container_name)").
			Where("has({container_ids:Array(String)}, container_id)").Param("container_ids", containerIDs).GroupBy("container_id"), "containers"); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---- comments (read) ----

func (s *Server) apmErrorComments(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
	id, ok := apm.ParseGroupID(strings.ToLower(r.PathValue("group_id")))
	if !ok {
		return badRequest("group_id must be 16 hex characters")
	}
	out := []errorCommentJSON{}
	p, _ := auth.PrincipalFrom(r.Context())
	if store := s.errorStore(); store != nil && p != nil && p.OrgID != "" {
		cs, err := store.Comments(r.Context(), p.OrgID, id)
		if err != nil {
			return err
		}
		for _, c := range cs {
			out = append(out, commentJSON(c))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": out})
	return nil
}

// ---- mutations (members; an API key with that role too) ----

type apmWriteHandler func(w http.ResponseWriter, r *http.Request, p *auth.Principal, sc *query.Scope, store apm.ErrorStateStore) error

// apmWriteRoute registers a mutating endpoint: wrap authenticates (session CSRF included) and creates the tenant
// scope; the handler additionally needs a principal whose role may work on the error inbox.
func (s *Server) apmWriteRoute(mux *http.ServeMux, pattern string, h apmWriteHandler) {
	mux.Handle(pattern, s.wrap(pattern, func(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
		noStore(w)
		p, _ := auth.PrincipalFrom(r.Context())
		if ae := authorize(p, auth.ActWriteAPMErrors); ae != nil {
			return ae
		}
		store := s.errorStore()
		if store == nil {
			return notFound("the error workflow is not available")
		}
		err := h(w, r, p, sc, store)
		switch {
		case errors.Is(err, apm.ErrInvalidErrorPatch), errors.Is(err, apm.ErrNotMember):
			return badRequest("%v", err)
		case errors.Is(err, apm.ErrCommentNotFound):
			return notFound("comment not found")
		}
		return err
	}))
}

// errorGroupKeys resolves group ids to their services (apm_error_groups); unknown ids are returned separately.
func errorGroupKeys(ctx context.Context, sc *query.Scope, ids []uint64) ([]apm.ErrorGroupRef, []string, error) {
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = strconv.FormatUint(id, 10)
	}
	q := sc.From(query.ApmErrorGroups).Columns("error_group_id", "service_name", "service_namespace", "deployment_environment").
		Where("has({group_ids:Array(String)}, toString(error_group_id))").Param("group_ids", strs).
		GroupBy("error_group_id", "service_name", "service_namespace", "deployment_environment")
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	found := map[uint64]apm.ServiceKey{}
	for rows.Next() {
		var id uint64
		var k apm.ServiceKey
		if err := rows.Scan(&id, &k.Name, &k.Namespace, &k.Environment); err != nil {
			return nil, nil, err
		}
		found[id] = k
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var refs []apm.ErrorGroupRef
	var missing []string
	for _, id := range ids {
		if k, ok := found[id]; ok {
			refs = append(refs, apm.ErrorGroupRef{GroupID: id, Key: k})
		} else {
			missing = append(missing, apm.GroupIDString(id))
		}
	}
	return refs, missing, nil
}

func (s *Server) apmActor(r *http.Request, p *auth.Principal) apm.Actor {
	a := apm.Actor{UserID: p.UserID, Email: p.Email, APIKeyID: p.APIKeyID, APIKeyName: p.APIKeyName}
	if s.accounts != nil {
		a.IP = s.accounts.Meta(r).IP
	}
	return a
}

func (s *Server) apmPatchErrorGroups(w http.ResponseWriter, r *http.Request, p *auth.Principal, sc *query.Scope, store apm.ErrorStateStore) error {
	var body struct {
		GroupIDs          []string `json:"group_ids"`
		Status            *string  `json:"status"`
		AssigneeUserID    *string  `json:"assignee_user_id"`
		ResolvedInVersion *string  `json:"resolved_in_version"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	if len(body.GroupIDs) == 0 || len(body.GroupIDs) > apm.MaxBulkGroups {
		return badRequest("group_ids must list 1-%d error groups", apm.MaxBulkGroups)
	}
	seen := map[uint64]bool{}
	var ids []uint64
	for _, v := range body.GroupIDs {
		id, ok := apm.ParseGroupID(strings.ToLower(v))
		if !ok {
			return badRequest("group_ids: %q is not 16 hex characters", truncate(v, 32))
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	var patch apm.ErrorGroupPatch
	if body.Status != nil {
		st := apm.ErrorStatus(*body.Status)
		patch.Status = &st
	}
	patch.AssigneeUserID, patch.ResolvedInVersion = body.AssigneeUserID, body.ResolvedInVersion
	if patch.AssigneeUserID != nil && *patch.AssigneeUserID != "" && len(*patch.AssigneeUserID) != 36 {
		return badRequest("assignee_user_id must be a user id or \"\"")
	}
	if err := patch.Validate(); err != nil {
		return err
	}
	refs, missing, err := errorGroupKeys(r.Context(), sc, ids)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return notFound("unknown error groups: " + strings.Join(missing, ", "))
	}
	states, err := store.Update(r.Context(), p.OrgID, refs, patch, s.apmActor(r, p))
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for _, st := range states {
		m := map[string]any{"group_id": apm.GroupIDString(st.GroupID), "service_name": st.Key.Name,
			"service_namespace": st.Key.Namespace, "environment": st.Key.Environment}
		workflowJSON(st).addTo(m)
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
	return nil
}

func (s *Server) apmAddErrorComment(w http.ResponseWriter, r *http.Request, p *auth.Principal, sc *query.Scope, store apm.ErrorStateStore) error {
	id, ok := apm.ParseGroupID(strings.ToLower(r.PathValue("group_id")))
	if !ok {
		return badRequest("group_id must be 16 hex characters")
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	text, err := apm.ValidateComment(body.Body)
	if err != nil {
		return err
	}
	refs, _, err := errorGroupKeys(r.Context(), sc, []uint64{id})
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return notFound("error group not found")
	}
	c, err := store.AddComment(r.Context(), p.OrgID, refs[0], text, s.apmActor(r, p))
	if err != nil {
		return err
	}
	c.AuthorName = p.Name
	writeJSON(w, http.StatusCreated, commentJSON(c))
	return nil
}

func (s *Server) apmDeleteErrorComment(w http.ResponseWriter, r *http.Request, p *auth.Principal, _ *query.Scope, store apm.ErrorStateStore) error {
	id, ok := apm.ParseGroupID(strings.ToLower(r.PathValue("group_id")))
	if !ok {
		return badRequest("group_id must be 16 hex characters")
	}
	commentID := r.PathValue("comment_id")
	if len(commentID) != 36 {
		return notFound("comment not found")
	}
	if err := store.DeleteComment(r.Context(), p.OrgID, id, commentID, s.apmActor(r, p), allowed(p, auth.ActManageAPMErrors)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
