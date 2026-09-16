package alert

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/slo"
)

// ScopeProvider creates tenant-bound query scopes (*query.DB).
type ScopeProvider interface {
	Scope(tenantID string) (*query.Scope, error)
}

// EvaluatorOptions configure an Evaluator.
type EvaluatorOptions struct {
	Instance            string
	Delay               time.Duration
	MaxConcurrent       int
	TenantMaxConcurrent int
	TenantPerMinute     int
	QueryTimeout        time.Duration
	Limits              Limits
	PublicURL           string
	Log                 *slog.Logger
	Registerer          prometheus.Registerer
	Now                 func() time.Time
	// ApdexSettings resolves per-service Apdex thresholds for APM rules (nil: DefaultApdexT for every service).
	ApdexSettings ApdexSettingsFunc
	DefaultApdexT time.Duration
	// ErrorStates is the APM error workflow for apm_error rules (nil: regressed conditions fail).
	ErrorStates apm.ErrorStateStore
	// SLOs holds the SLO definitions of slo_burn rules (nil: such rules report an evaluation error).
	SLOs slo.Store
	// Summaries receives the rows of every committed evaluation (alert_evaluations, §3.6); nil = not recorded.
	Summaries EvaluationSink
}

// Evaluator evaluates the rules whose leases this process holds (docs/contracts/alerting.md §3).
type Evaluator struct {
	store  EvalStore
	leases *LeaseManager
	db     ScopeProvider
	o      EvaluatorOptions
	budget *tenantBudget

	sem     chan struct{}
	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup

	cEvals       *prometheus.CounterVec
	hDuration    *prometheus.HistogramVec
	hLag         prometheus.Histogram
	cTransitions *prometheus.CounterVec
}

// NewEvaluator creates an evaluator.
func NewEvaluator(store EvalStore, leases *LeaseManager, db ScopeProvider, o EvaluatorOptions) *Evaluator {
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = 16
	}
	if o.TenantMaxConcurrent <= 0 {
		o.TenantMaxConcurrent = 4
	}
	if o.TenantPerMinute <= 0 {
		o.TenantPerMinute = 600
	}
	if o.QueryTimeout <= 0 {
		o.QueryTimeout = 20 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DefaultApdexT <= 0 {
		o.DefaultApdexT = apm.DefaultApdexT
	}
	e := &Evaluator{
		store: store, leases: leases, db: db, o: o,
		budget:  newTenantBudget(o.TenantMaxConcurrent, o.TenantPerMinute, o.Now),
		sem:     make(chan struct{}, o.MaxConcurrent),
		running: map[string]bool{},
		cEvals: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_alert_evaluations_total",
			Help: "Alert rule evaluations by result."}, []string{"result"}),
		hDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "openlog_alert_evaluation_duration_seconds",
			Help: "Alert rule evaluation duration by rule type.", Buckets: prometheus.ExponentialBuckets(0.005, 2, 14)}, []string{"type"}),
		hLag: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "openlog_alert_evaluation_lag_seconds",
			Help: "Delay between a rule's scheduled evaluation time and the start of its evaluation.", Buckets: prometheus.ExponentialBuckets(0.1, 2, 12)}),
		cTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_alert_transitions_total",
			Help: "Series state transitions by target state."}, []string{"to"}),
	}
	for _, r := range []string{"ok", "error", "throttled", "lease_lost", "stale", "skipped"} {
		e.cEvals.WithLabelValues(r)
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(e.cEvals, e.hDuration, e.hLag, e.cTransitions)
	}
	return e
}

// Phase is the stable offset of a rule inside its interval (jitter, §3.1).
func Phase(ruleID string, interval time.Duration) time.Duration {
	secs := int64(interval / time.Second)
	if secs <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(ruleID))
	return time.Duration(int64(h.Sum32())%secs) * time.Second
}

// AlignedEnd is the end of the window evaluated at now (§3.1).
func AlignedEnd(now time.Time, interval, delay, phase time.Duration) time.Time {
	secs := int64(interval / time.Second)
	if secs <= 0 {
		secs = 1
	}
	base := now.Add(-delay - phase).Unix()
	aligned := base - mod(base, secs)
	return time.Unix(aligned, 0).UTC().Add(phase)
}

func mod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

// Run schedules due rules every second until ctx is done and waits for running evaluations.
func (e *Evaluator) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.wg.Wait()
			return
		case <-t.C:
			e.dispatchDue(ctx, false)
		}
	}
}

// RunOnce evaluates every due rule synchronously (tests).
func (e *Evaluator) RunOnce(ctx context.Context) {
	e.dispatchDue(ctx, true)
	e.wg.Wait()
}

func (e *Evaluator) dispatchDue(ctx context.Context, sync bool) {
	now := e.o.Now()
	for _, l := range e.leases.Owned(now) {
		if l.NextEvalAt.After(now) {
			break // ordered by next evaluation
		}
		e.mu.Lock()
		busy := e.running[l.RuleID]
		e.mu.Unlock()
		if busy {
			continue
		}
		select {
		case e.sem <- struct{}{}:
		default:
			return // pod-wide concurrency exhausted; retried next tick
		}
		if !e.budget.acquire(l.OrgID) {
			<-e.sem
			e.cEvals.WithLabelValues("throttled").Inc()
			e.leases.Scheduled(l.RuleID, now.Add(time.Second), time.Time{})
			continue
		}
		e.mu.Lock()
		e.running[l.RuleID] = true
		e.mu.Unlock()
		e.wg.Add(1)
		run := func(l Lease) {
			defer func() {
				e.budget.release(l.OrgID)
				<-e.sem
				e.mu.Lock()
				delete(e.running, l.RuleID)
				e.mu.Unlock()
				e.wg.Done()
			}()
			// Evaluations finish even when shutdown starts (bounded by the query timeout).
			e.Evaluate(context.WithoutCancel(ctx), l)
		}
		if sync {
			run(l)
		} else {
			go run(l)
		}
	}
}

// Evaluate runs one evaluation of a leased rule and commits it. It returns the result label.
func (e *Evaluator) Evaluate(ctx context.Context, l Lease) string {
	start := e.o.Now()
	e.hLag.Observe(max(0, start.Sub(l.NextEvalAt).Seconds()))
	rule, chans, err := e.store.LoadRule(ctx, l.RuleID)
	if errors.Is(err, ErrNotFound) {
		e.leases.Drop(l.RuleID)
		return e.count("skipped")
	}
	if err != nil {
		e.o.Log.Warn("cannot load alert rule", "rule_id", l.RuleID, "err", err)
		e.leases.Scheduled(l.RuleID, start.Add(10*time.Second), time.Time{})
		return e.count("error")
	}
	interval := rule.Interval()
	end := AlignedEnd(start, interval, e.o.Delay, Phase(rule.ID, interval))
	next := end.Add(interval + e.o.Delay)
	if !rule.Enabled {
		e.leases.Drop(l.RuleID)
		return e.count("skipped")
	}
	if !l.LastEvalEnd.IsZero() && !end.After(l.LastEvalEnd) {
		e.leases.Scheduled(l.RuleID, next, time.Time{})
		return e.count("skipped")
	}

	plan, evalErr := e.plan(ctx, rule, chans, l, end)
	plan.NextEvalAt = next
	plan.Duration = e.o.Now().Sub(start)
	if evalErr != nil {
		plan.Result, plan.Error = "error", truncateErr(evalErr)
	}
	switch err := e.store.Commit(ctx, e.o.Instance, plan); {
	case errors.Is(err, ErrLeaseLost):
		e.leases.Drop(l.RuleID)
		return e.count("lease_lost")
	case errors.Is(err, ErrStale):
		e.leases.Scheduled(l.RuleID, next, end)
		return e.count("stale")
	case errors.Is(err, ErrRuleChanged):
		e.leases.Scheduled(l.RuleID, e.o.Now().Add(time.Second), time.Time{})
		return e.count("skipped")
	case err != nil:
		e.o.Log.Warn("cannot commit alert evaluation", "rule_id", l.RuleID, "err", err)
		e.leases.Scheduled(l.RuleID, e.o.Now().Add(5*time.Second), time.Time{})
		return e.count("error")
	}
	e.leases.Scheduled(l.RuleID, next, end)
	if e.o.Summaries != nil {
		// Only committed (fenced) evaluations are recorded, so a window has at most one set of rows.
		e.o.Summaries.Add(EvaluationRows(rule, plan))
	}
	e.hDuration.WithLabelValues(rule.Type).Observe(plan.Duration.Seconds())
	for to, n := range plan.Transitions {
		e.cTransitions.WithLabelValues(to).Add(float64(n))
	}
	if evalErr != nil {
		e.o.Log.Debug("alert evaluation failed", "rule_id", rule.ID, "err", evalErr)
		return e.count("error")
	}
	return e.count("ok")
}

func (e *Evaluator) plan(ctx context.Context, rule *Rule, chans []ChannelRef, l Lease, end time.Time) (*Plan, error) {
	empty := &Plan{RuleID: rule.ID, RuleVersion: rule.Version, OrgID: rule.OrgID, EvalEnd: end, Result: "ok"}
	if rule.Condition == nil {
		return empty, errors.New("rule type is not available")
	}
	sc, err := e.db.Scope(rule.TenantID)
	if err != nil {
		return empty, err
	}
	qctx, cancel := context.WithTimeout(ctx, e.o.QueryTimeout)
	if rule.Type == TypeAPM {
		var settings []apm.Setting
		if e.o.ApdexSettings != nil {
			if settings, err = e.o.ApdexSettings(ctx, rule.OrgID); err != nil {
				e.o.Log.Debug("cannot load apdex settings; using the default", "org_id", rule.OrgID, "err", err)
			}
		}
		qctx = WithApdexT(qctx, ApdexLookup(settings, e.o.DefaultApdexT))
	}
	if rule.Type == TypeAPMError && e.o.ErrorStates != nil {
		qctx = WithErrorWorkflow(qctx, ErrorWorkflow{OrgID: rule.OrgID, Store: e.o.ErrorStates})
	}
	if rule.Type == TypeSLOBurn && e.o.SLOs != nil {
		qctx = WithSLOs(qctx, SLOLookup{OrgID: rule.OrgID, Store: e.o.SLOs})
	}
	res, err := rule.Condition.Evaluate(qctx, sc, end, e.o.Limits)
	cancel()
	if err != nil {
		return empty, err
	}
	states, err := e.store.LoadSeries(ctx, rule.ID)
	if err != nil {
		return empty, err
	}
	return BuildPlan(PlanInput{Rule: rule, Channels: chans, States: states, PrevEvalEnd: l.LastEvalEnd, End: end,
		Result: res, Delay: e.o.Delay, PublicURL: e.o.PublicURL}), nil
}

func (e *Evaluator) count(result string) string {
	e.cEvals.WithLabelValues(result).Inc()
	return result
}

func truncateErr(err error) string {
	s := err.Error()
	if len(s) > 1000 {
		s = s[:1000]
	}
	return s
}

// tenantBudget limits concurrent evaluations and evaluations per minute per organization (token bucket).
type tenantBudget struct {
	mu         sync.Mutex
	maxRunning int
	perMinute  float64
	now        func() time.Time
	tenants    map[string]*bucket
}

type bucket struct {
	running int
	tokens  float64
	last    time.Time
}

func newTenantBudget(maxRunning, perMinute int, now func() time.Time) *tenantBudget {
	return &tenantBudget{maxRunning: maxRunning, perMinute: float64(perMinute), now: now, tenants: map[string]*bucket{}}
}

func (b *tenantBudget) acquire(tenant string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	t, ok := b.tenants[tenant]
	if !ok {
		t = &bucket{tokens: b.perMinute, last: now}
		b.tenants[tenant] = t
	}
	t.tokens = min(b.perMinute, t.tokens+now.Sub(t.last).Minutes()*b.perMinute)
	t.last = now
	if t.running >= b.maxRunning || t.tokens < 1 {
		return false
	}
	t.running++
	t.tokens--
	return true
}

func (b *tenantBudget) release(tenant string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.tenants[tenant]; ok && t.running > 0 {
		t.running--
	}
}
