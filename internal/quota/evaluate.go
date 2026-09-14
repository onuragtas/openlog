package quota

import "math"

// Level is the state of a metric or a whole organization.
type Level string

// Levels, ordered.
const (
	LevelOK       Level = "ok"
	LevelWarning  Level = "warning"
	LevelExceeded Level = "exceeded"
)

var levelRank = map[Level]int{LevelOK: 0, LevelWarning: 1, LevelExceeded: 2}

// Metrics evaluated against plan limits.
const (
	MetricIngestBytes = "ingest_bytes"
	MetricHosts       = "hosts"
	MetricUsers       = "users"
)

// Usage is what the evaluator compares with the limits.
type Usage struct {
	// IngestBytes of the current billing period.
	IngestBytes int64
	// ActiveHosts reporting (the larger of today and yesterday).
	ActiveHosts int64
	// Users (members of the organization).
	Users int64
}

// MetricStatus is one metric against its limit. Limit 0 = unlimited (Percent 0, level ok).
type MetricStatus struct {
	Metric  string  `json:"metric"`
	Used    float64 `json:"used"`
	Limit   float64 `json:"limit"`
	Percent float64 `json:"percent"`
	Level   Level   `json:"level"`
}

// Status is the evaluation of one organization.
type Status struct {
	PlanID  string         `json:"plan_id"`
	Level   Level          `json:"level"`
	Metrics []MetricStatus `json:"metrics"`
	// IngestBlocked is true only in SaaS mode for plans with a hard ingest limit used up (limit + grace).
	IngestBlocked      bool  `json:"ingest_blocked"`
	IngestLimitBytes   int64 `json:"ingest_limit_bytes"`
	RateBytesPerSecond int64 `json:"rate_bytes_per_second"`
	BurstBytes         int64 `json:"burst_bytes"`
}

// DefaultWarnPercent is the warning threshold when none is configured.
const DefaultWarnPercent = 80

// WarnPercent returns the lowest configured notification threshold below 100 (the warning level), or
// DefaultWarnPercent.
func WarnPercent(thresholds []int) float64 {
	w := 0
	for _, t := range thresholds {
		if t < 100 && (w == 0 || t < w) {
			w = t
		}
	}
	if w == 0 {
		return DefaultWarnPercent
	}
	return float64(w)
}

func metric(name string, used, limit, warn float64) MetricStatus {
	m := MetricStatus{Metric: name, Used: used, Limit: limit, Level: LevelOK}
	if limit <= 0 {
		return m
	}
	m.Percent = math.Round(used/limit*10000) / 100
	switch {
	case used >= limit:
		m.Level = LevelExceeded
	case m.Percent >= warn:
		m.Level = LevelWarning
	}
	return m
}

// Evaluate compares u with the limits of p (overrides already applied). saas enables the hard ingest block;
// warnPercent is the warning threshold.
func Evaluate(p Plan, u Usage, saas bool, warnPercent float64) Status {
	l := p.Limits
	ingestLimit := int64(math.Round(l.IngestGBMonth * GiB))
	st := Status{
		PlanID: p.ID, Level: LevelOK, IngestLimitBytes: ingestLimit,
		RateBytesPerSecond: l.IngestBytesPerSecond, BurstBytes: l.IngestBurstBytes,
		Metrics: []MetricStatus{
			metric(MetricIngestBytes, float64(u.IngestBytes), float64(ingestLimit), warnPercent),
			metric(MetricHosts, float64(u.ActiveHosts), float64(l.Hosts), warnPercent),
			metric(MetricUsers, float64(u.Users), float64(l.Users), warnPercent),
		},
	}
	if st.RateBytesPerSecond > 0 && st.BurstBytes <= 0 {
		st.BurstBytes = st.RateBytesPerSecond // one second of burst by default
	}
	for _, m := range st.Metrics {
		if levelRank[m.Level] > levelRank[st.Level] {
			st.Level = m.Level
		}
	}
	if saas && p.Enforcement.HardIngestLimit && ingestLimit > 0 {
		hard := ingestLimit + int64(math.Round(float64(ingestLimit)*p.Enforcement.GracePercent/100))
		st.IngestBlocked = u.IngestBytes >= hard
	}
	return st
}

// CrossedThresholds returns the thresholds (percent) that m has reached, ascending.
func CrossedThresholds(m MetricStatus, thresholds []int) []int {
	var out []int
	if m.Limit <= 0 {
		return out
	}
	for _, t := range thresholds {
		if m.Used >= m.Limit*float64(t)/100 {
			out = append(out, t)
		}
	}
	return out
}
