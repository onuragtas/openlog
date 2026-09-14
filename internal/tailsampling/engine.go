package tailsampling

import (
	"container/list"
	"math"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/otlputil"
)

// Eviction / early decision reasons.
const (
	ReasonTimeout   = "timeout"
	ReasonMaxTraces = "max_traces"
	ReasonMaxSpans  = "max_spans"
	ReasonMaxBytes  = "max_bytes"
	ReasonRevoked   = "revoked"
	ReasonShutdown  = "shutdown"
)

// Options bound the engine's memory and timing.
type Options struct {
	DecisionWait      time.Duration
	MaxTraces         int
	MaxSpansPerTrace  int
	MaxBufferedBytes  int64
	DecisionCacheTTL  time.Duration
	DecisionCacheSize int
}

func (o *Options) defaults() {
	if o.DecisionWait <= 0 {
		o.DecisionWait = 30 * time.Second
	}
	if o.MaxTraces <= 0 {
		o.MaxTraces = 100_000
	}
	if o.MaxSpansPerTrace <= 0 {
		o.MaxSpansPerTrace = 2000
	}
	if o.MaxBufferedBytes <= 0 {
		o.MaxBufferedBytes = 512 << 20
	}
	if o.DecisionCacheTTL <= 0 {
		o.DecisionCacheTTL = 10 * time.Minute
	}
	if o.DecisionCacheSize <= 0 {
		o.DecisionCacheSize = 500_000
	}
}

// Policies returns the policy of a tenant.
type Policies interface {
	Policy(tenantID string) Policy
}

// StaticPolicies uses one policy for every tenant (plus per-tenant overrides).
type StaticPolicies struct {
	Default  Policy
	ByTenant map[string]Policy
}

// Policy implements Policies.
func (s StaticPolicies) Policy(tenantID string) Policy {
	if p, ok := s.ByTenant[tenantID]; ok {
		return p
	}
	return s.Default
}

// Source describes the Kafka record a request came from.
type Source struct {
	TenantID   string
	RequestID  string
	ReceivedAt time.Time
	Topic      string
	Partition  int32
	Offset     int64
	Epoch      int32
}

// Output is one decided (kept) trace or late-span batch to produce.
type Output struct {
	TenantID   string
	RequestID  string
	ReceivedAt time.Time
	TraceID    []byte
	Rule       string
	Request    *coltrace.ExportTraceServiceRequest
	refs       []*offEntry
}

type traceKey struct {
	tenant string
	id     string
}

type bufferedSpan struct {
	res   *tracepb.ResourceSpans
	scope *tracepb.ScopeSpans
	span  *tracepb.Span
}

type trace struct {
	key        traceKey
	first      time.Time
	elem       *list.Element
	spans      []bufferedSpan
	bytes      int64
	refs       []*offEntry
	receivedAt time.Time
	requestID  string
}

type offEntry struct {
	offset int64
	epoch  int32
	refs   int
}

type partState struct {
	entries   []*offEntry // increasing offsets not yet committable
	lastSeen  int64
	lastEpoch int32
	committed int64 // last returned commit offset (next offset to read)
}

type partKey struct {
	topic     string
	partition int32
}

type decision struct {
	keep    bool
	ratio   float64 // tail keep probability (after rate limiting)
	rule    string
	expires time.Time
}

// Engine buffers spans per trace and decides. It is not safe for concurrent use.
type Engine struct {
	opts     Options
	policies Policies
	m        *Metrics
	now      func() time.Time

	traces map[traceKey]*trace
	fifo   *list.List // *trace by first-seen time
	spans  int
	bytes  int64

	cache      map[traceKey]decision
	cacheOrder []cacheItem
	cacheHead  int

	rates map[string]*rateEstimator
	parts map[partKey]*partState
	out   []Output
}

type cacheItem struct {
	key     traceKey
	expires time.Time
}

// NewEngine creates an engine. m may be nil (metrics are then not registered anywhere).
func NewEngine(opts Options, policies Policies, m *Metrics) *Engine {
	opts.defaults()
	if m == nil {
		m = NewMetrics(nil)
	}
	return &Engine{
		opts: opts, policies: policies, m: m, now: time.Now,
		traces: map[traceKey]*trace{}, fifo: list.New(), cache: map[traceKey]decision{},
		rates: map[string]*rateEstimator{}, parts: map[partKey]*partState{},
	}
}

// SetClock replaces the time source (tests).
func (e *Engine) SetClock(now func() time.Time) { e.now = now }

func (e *Engine) part(src Source) *partState {
	k := partKey{src.Topic, src.Partition}
	ps := e.parts[k]
	if ps == nil {
		ps = &partState{lastSeen: -1, committed: -1}
		e.parts[k] = ps
	}
	return ps
}

// Add buffers the spans of one record. Spans of already decided traces follow the cached decision.
// It may decide traces early when a memory bound is reached.
func (e *Engine) Add(src Source, req *coltrace.ExportTraceServiceRequest) {
	ps := e.part(src)
	if src.Offset <= ps.lastSeen {
		return // re-delivered within this assignment
	}
	if ps.lastSeen < 0 {
		ps.committed = src.Offset // the group already starts here: committing it again is a no-op
	}
	ent := &offEntry{offset: src.Offset, epoch: src.Epoch}
	ps.entries = append(ps.entries, ent)
	ps.lastSeen, ps.lastEpoch = src.Offset, src.Epoch
	now := e.now()
	late := map[traceKey]*Output{}
	touched := map[*trace]bool{}
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				if len(sp.GetTraceId()) != 16 || len(sp.GetSpanId()) != 8 {
					e.m.RecordsRejected.WithLabelValues("invalid_span_id").Inc()
					continue
				}
				key := traceKey{src.TenantID, string(sp.GetTraceId())}
				if t := e.traces[key]; t != nil {
					e.appendSpan(t, ent, touched, rs, ss, sp)
					if len(t.spans) >= e.opts.MaxSpansPerTrace {
						e.decide(t, ReasonMaxSpans)
					}
					continue
				}
				if d, ok := e.cached(key, now); ok {
					e.m.LateSpans.WithLabelValues(outcome(d.keep)).Inc()
					e.m.Spans.WithLabelValues(outcome(d.keep)).Inc()
					if !d.keep {
						continue
					}
					o := late[key]
					if o == nil {
						o = &Output{TenantID: src.TenantID, RequestID: src.RequestID, ReceivedAt: src.ReceivedAt, TraceID: sp.GetTraceId(),
							Rule: d.rule, Request: &coltrace.ExportTraceServiceRequest{}}
						ent.refs++
						o.refs = []*offEntry{ent}
						late[key] = o
					}
					appendToRequest(o.Request, bufferedSpan{rs, ss, reweighted(sp, rs, d.ratio)})
					continue
				}
				if len(e.traces) >= e.opts.MaxTraces {
					e.decide(e.fifo.Front().Value.(*trace), ReasonMaxTraces)
				}
				t := &trace{key: key, first: now, receivedAt: src.ReceivedAt, requestID: src.RequestID}
				t.elem = e.fifo.PushBack(t)
				e.traces[key] = t
				e.appendSpan(t, ent, touched, rs, ss, sp)
				if len(t.spans) >= e.opts.MaxSpansPerTrace {
					e.decide(t, ReasonMaxSpans)
				}
			}
		}
	}
	for _, o := range late {
		e.out = append(e.out, *o)
	}
	for e.bytes > e.opts.MaxBufferedBytes && e.fifo.Len() > 0 {
		e.decide(e.fifo.Front().Value.(*trace), ReasonMaxBytes)
	}
	e.gauges()
}

func (e *Engine) appendSpan(t *trace, ent *offEntry, touched map[*trace]bool, rs *tracepb.ResourceSpans, ss *tracepb.ScopeSpans, sp *tracepb.Span) {
	t.spans = append(t.spans, bufferedSpan{rs, ss, sp})
	n := int64(proto.Size(sp)) + 16
	t.bytes += n
	e.bytes += n
	e.spans++
	if !touched[t] {
		touched[t] = true
		if len(t.refs) == 0 || t.refs[len(t.refs)-1] != ent {
			ent.refs++
			t.refs = append(t.refs, ent)
		}
	}
}

// Tick decides every trace whose decision wait has passed.
func (e *Engine) Tick() {
	now := e.now()
	for f := e.fifo.Front(); f != nil; f = e.fifo.Front() {
		t := f.Value.(*trace)
		if now.Sub(t.first) < e.opts.DecisionWait {
			break
		}
		e.decide(t, ReasonTimeout)
	}
	e.purgeCache(now)
	e.gauges()
}

// NextDeadline returns when the oldest buffered trace is due (zero time when nothing is buffered).
func (e *Engine) NextDeadline() time.Time {
	if f := e.fifo.Front(); f != nil {
		return f.Value.(*trace).first.Add(e.opts.DecisionWait)
	}
	return time.Time{}
}

// DecideAll decides every buffered trace now (shutdown).
func (e *Engine) DecideAll(reason string) {
	for f := e.fifo.Front(); f != nil; f = e.fifo.Front() {
		e.decide(f.Value.(*trace), reason)
	}
	e.gauges()
}

// DecidePartitions decides every trace with spans from the given partitions (revocation).
func (e *Engine) DecidePartitions(parts map[string][]int32) {
	gone := map[*offEntry]bool{}
	for topic, ps := range parts {
		for _, p := range ps {
			if st := e.parts[partKey{topic, p}]; st != nil {
				for _, ent := range st.entries {
					gone[ent] = true
				}
			}
		}
	}
	if len(gone) == 0 {
		return
	}
	var due []*trace
	for f := e.fifo.Front(); f != nil; f = f.Next() {
		t := f.Value.(*trace)
		for _, r := range t.refs {
			if gone[r] {
				due = append(due, t)
				break
			}
		}
	}
	for _, t := range due {
		e.decide(t, ReasonRevoked)
	}
	e.gauges()
}

// DropPartitions forgets the offset state of partitions this member no longer owns.
func (e *Engine) DropPartitions(parts map[string][]int32) {
	for topic, ps := range parts {
		for _, p := range ps {
			delete(e.parts, partKey{topic, p})
		}
	}
}

// TakeOutputs returns and clears the outputs waiting to be produced.
func (e *Engine) TakeOutputs() []Output {
	out := e.out
	e.out = nil
	return out
}

// Release marks outputs as produced so their records become committable.
func (e *Engine) Release(outs []Output) {
	for i := range outs {
		for _, r := range outs[i].refs {
			r.refs--
		}
		outs[i].refs = nil
	}
}

// CommitOffsets returns, per partition that advanced, the offset to commit: the first record still
// referenced by a buffered trace or an unproduced output, else the offset after the last record seen.
// only restricts the result to some partitions (nil: all).
func (e *Engine) CommitOffsets(only map[string][]int32) map[string]map[int32]kgo.EpochOffset {
	out := map[string]map[int32]kgo.EpochOffset{}
	for k, ps := range e.parts {
		if only != nil && !containsPart(only, k) {
			continue
		}
		i := 0
		for i < len(ps.entries) && ps.entries[i].refs <= 0 {
			i++
		}
		var next kgo.EpochOffset
		if i < len(ps.entries) {
			next = kgo.EpochOffset{Epoch: ps.entries[i].epoch, Offset: ps.entries[i].offset}
		} else {
			next = kgo.EpochOffset{Epoch: ps.lastEpoch, Offset: ps.lastSeen + 1}
		}
		ps.entries = ps.entries[i:]
		if next.Offset <= ps.committed || ps.lastSeen < 0 {
			continue
		}
		ps.committed = next.Offset
		if out[k.topic] == nil {
			out[k.topic] = map[int32]kgo.EpochOffset{}
		}
		out[k.topic][k.partition] = next
	}
	return out
}

func containsPart(m map[string][]int32, k partKey) bool {
	for _, p := range m[k.topic] {
		if p == k.partition {
			return true
		}
	}
	return false
}

// Buffered returns the number of buffered traces and spans.
func (e *Engine) Buffered() (traces, spans int) { return len(e.traces), e.spans }

func (e *Engine) gauges() {
	e.m.TracesBuffered.Set(float64(len(e.traces)))
	e.m.SpansBuffered.Set(float64(e.spans))
	e.m.BytesBuffered.Set(float64(e.bytes))
	e.m.CacheEntries.Set(float64(len(e.cache)))
}

func outcome(keep bool) string {
	if keep {
		return "kept"
	}
	return "dropped"
}

// decide applies the tenant policy to t, removes it from the buffer and emits an output when kept.
func (e *Engine) decide(t *trace, reason string) {
	now := e.now()
	e.fifo.Remove(t.elem)
	delete(e.traces, t.key)
	e.spans -= len(t.spans)
	e.bytes -= t.bytes
	if reason != ReasonTimeout {
		e.m.Evictions.WithLabelValues(reason).Inc()
	}
	e.m.DecisionDelay.Observe(now.Sub(t.first).Seconds())

	policy := e.policies.Policy(t.key.tenant)
	keep, ratio, rule := e.evaluate(policy, t, now)
	e.remember(t.key, decision{keep: keep, ratio: ratio, rule: rule, expires: now.Add(e.opts.DecisionCacheTTL)})
	e.m.Decisions.WithLabelValues(rule, outcome(keep)).Inc()
	e.m.Spans.WithLabelValues(outcome(keep)).Add(float64(len(t.spans)))
	if !keep {
		for _, r := range t.refs {
			r.refs--
		}
		return
	}
	o := Output{TenantID: t.key.tenant, RequestID: t.requestID, ReceivedAt: t.receivedAt, TraceID: []byte(t.key.id), Rule: rule,
		Request: &coltrace.ExportTraceServiceRequest{}, refs: t.refs}
	for _, s := range t.spans {
		s.span = reweighted(s.span, s.res, ratio)
		appendToRequest(o.Request, s)
	}
	e.out = append(e.out, o)
}

// evaluate returns the keep decision, the tail probability that was applied and the rule name.
func (e *Engine) evaluate(policy Policy, t *trace, now time.Time) (bool, float64, string) {
	if !policy.Enabled {
		return true, 1, DefaultRuleName
	}
	sum := summarize(t, policy.needsAttributes())
	d := policy.Evaluate(sum)
	ratio := d.Ratio
	if policy.MaxSpansPerSecond > 0 && ratio > 0 {
		r := e.rates[t.key.tenant]
		if r == nil {
			r = &rateEstimator{}
			e.rates[t.key.tenant] = r
		}
		q := r.factor(now, float64(len(t.spans))*ratio, policy.MaxSpansPerSecond)
		if q < 1 {
			ratio *= q
			e.m.RateLimited.Inc()
		}
	}
	if ratio >= 1 {
		return true, 1, d.Rule
	}
	if ratio <= 0 {
		return false, 0, d.Rule
	}
	// Head probability of the trace: the largest consistent (tracestate) probability of its spans.
	var headTS, minHead float64 = 0, 1
	var traceState string
	for _, s := range t.spans {
		p, fromTS := apm.HeadProbability(s.span.GetTraceState(), nil, nil)
		if !fromTS {
			p, _ = apm.HeadProbability("", ratioAttr(s.span.GetAttributes(), "sampling.ratio"), ratioAttr(s.res.GetResource().GetAttributes(), "sampling.ratio"))
		}
		if p > 0 && p < minHead {
			minHead = p
		}
		if fromTS && p > headTS {
			headTS, traceState = p, s.span.GetTraceState()
		}
	}
	// Keep every span's composite probability within what the processor honours (apm.md §4 clamp).
	ratio = math.Min(1, math.Max(ratio, apm.MinProbability/minHead))
	traceID := []byte(t.key.id)
	if headTS > 0 {
		// Consistent with the head decision: kept by head means R >= T(head), so
		// P(R >= T(head·ratio) | kept by head) = ratio.
		return traceRandomness(traceID, traceState) >= Threshold(headTS*ratio), ratio, d.Rule
	}
	return hashRandomness(traceID) < uint64(ratio*float64(maxThreshold)), ratio, d.Rule
}

func (p Policy) needsAttributes() bool {
	for _, r := range p.Rules {
		if r.Type == RuleAttribute {
			return true
		}
	}
	return false
}

func summarize(t *trace, attrs bool) *TraceSummary {
	sum := &TraceSummary{Spans: len(t.spans), Items: make([]SpanFacts, 0, len(t.spans))}
	var minStart, maxEnd uint64
	services := map[*tracepb.ResourceSpans]string{}
	resAttrs := map[*tracepb.ResourceSpans]map[string]string{}
	for _, s := range t.spans {
		svc, ok := services[s.res]
		if !ok {
			svc = otlputil.AttrString(s.res.GetResource().GetAttributes(), otlputil.AttrServiceName)
			services[s.res] = svc
			if attrs {
				resAttrs[s.res] = otlputil.AttrsToMap(s.res.GetResource().GetAttributes())
			}
		}
		st, en := s.span.GetStartTimeUnixNano(), s.span.GetEndTimeUnixNano()
		f := SpanFacts{
			Service: svc, Name: s.span.GetName(),
			Route: otlputil.AttrString(s.span.GetAttributes(), "http.route"),
			Error: s.span.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR,
		}
		if st > 0 && en > st {
			f.DurationMs = float64(en-st) / 1e6
			if minStart == 0 || st < minStart {
				minStart = st
			}
			if en > maxEnd {
				maxEnd = en
			}
		}
		if attrs {
			f.Attributes = otlputil.AttrsToMap(s.span.GetAttributes())
			f.Resource = resAttrs[s.res]
		}
		sum.Items = append(sum.Items, f)
	}
	if maxEnd > minStart {
		sum.DurationMs = float64(maxEnd-minStart) / 1e6
	}
	return sum
}

// reweighted returns sp with tracestate `ot=th` set to the composite head × tail probability. With
// ratio 1 the span is returned unchanged.
func reweighted(sp *tracepb.Span, rs *tracepb.ResourceSpans, ratio float64) *tracepb.Span {
	if ratio >= 1 {
		return sp
	}
	head, fromTS := apm.HeadProbability(sp.GetTraceState(), nil, nil)
	if !fromTS {
		head, _ = apm.HeadProbability("", ratioAttr(sp.GetAttributes(), "sampling.ratio"), ratioAttr(rs.GetResource().GetAttributes(), "sampling.ratio"))
	}
	if head <= 0 {
		return sp // not counted anyway (ot=p:63)
	}
	c := proto.Clone(sp).(*tracepb.Span)
	c.TraceState = WithThreshold(sp.GetTraceState(), EncodeThreshold(Threshold(head*ratio)))
	return c
}

// appendToRequest adds a span, reusing the last resource/scope group when it is the same source group.
func appendToRequest(req *coltrace.ExportTraceServiceRequest, s bufferedSpan) {
	var rs *tracepb.ResourceSpans
	if n := len(req.ResourceSpans); n > 0 && sameResource(req.ResourceSpans[n-1], s.res) {
		rs = req.ResourceSpans[n-1]
	} else {
		rs = &tracepb.ResourceSpans{Resource: s.res.GetResource(), SchemaUrl: s.res.GetSchemaUrl()}
		req.ResourceSpans = append(req.ResourceSpans, rs)
	}
	var ss *tracepb.ScopeSpans
	if n := len(rs.ScopeSpans); n > 0 && sameScope(rs.ScopeSpans[n-1], s.scope) {
		ss = rs.ScopeSpans[n-1]
	} else {
		ss = &tracepb.ScopeSpans{Scope: s.scope.GetScope(), SchemaUrl: s.scope.GetSchemaUrl()}
		rs.ScopeSpans = append(rs.ScopeSpans, ss)
	}
	ss.Spans = append(ss.Spans, s.span)
}

func sameResource(a, b *tracepb.ResourceSpans) bool {
	return a.GetResource() == b.GetResource() && a.GetSchemaUrl() == b.GetSchemaUrl()
}

func sameScope(a, b *tracepb.ScopeSpans) bool {
	return a.GetScope() == b.GetScope() && a.GetSchemaUrl() == b.GetSchemaUrl()
}

// ---- decision cache ----

func (e *Engine) cached(key traceKey, now time.Time) (decision, bool) {
	d, ok := e.cache[key]
	if !ok || !now.Before(d.expires) {
		return decision{}, false
	}
	return d, true
}

func (e *Engine) remember(key traceKey, d decision) {
	e.cache[key] = d
	e.cacheOrder = append(e.cacheOrder, cacheItem{key, d.expires})
	for len(e.cache) > e.opts.DecisionCacheSize && e.cacheHead < len(e.cacheOrder) {
		e.popCache()
	}
}

func (e *Engine) popCache() {
	it := e.cacheOrder[e.cacheHead]
	e.cacheOrder[e.cacheHead] = cacheItem{}
	e.cacheHead++
	if d, ok := e.cache[it.key]; ok && d.expires.Equal(it.expires) {
		delete(e.cache, it.key)
	}
	if e.cacheHead > 1024 && e.cacheHead*2 > len(e.cacheOrder) {
		e.cacheOrder = append([]cacheItem(nil), e.cacheOrder[e.cacheHead:]...)
		e.cacheHead = 0
	}
}

func (e *Engine) purgeCache(now time.Time) {
	for e.cacheHead < len(e.cacheOrder) && !now.Before(e.cacheOrder[e.cacheHead].expires) {
		e.popCache()
	}
}

// ---- rate limit ----

// rateEstimator estimates a tenant's expected kept spans per second (exponentially weighted over whole
// seconds, plus the current second as a lower bound).
type rateEstimator struct {
	sec  int64
	cur  float64
	rate float64
}

// factor records v expected kept spans and returns the probability that keeps the rate at limit.
func (r *rateEstimator) factor(now time.Time, v, limit float64) float64 {
	s := now.Unix()
	if s != r.sec {
		if r.sec != 0 && s > r.sec {
			r.rate = 0.7*r.rate + 0.3*r.cur
			if gap := s - r.sec - 1; gap > 0 {
				r.rate *= math.Pow(0.7, float64(min(gap, 60)))
			}
		}
		r.sec, r.cur = s, 0
	}
	r.cur += v
	est := math.Max(r.rate, r.cur)
	if est <= limit {
		return 1
	}
	return limit / est
}

// ratioAttr returns {"sampling.ratio": v} when the attribute is present (apm.HeadProbability input).
func ratioAttr(kvs []*commonpb.KeyValue, key string) map[string]string {
	if v := otlputil.AttrString(kvs, key); v != "" {
		return map[string]string{key: v}
	}
	return nil
}
