package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TailSampling configures tail-based sampling (D-075, docs/contracts/config.md "Tail sampling").
// Enabled must be the same on ingest (trace-id keyed records), processor (reads the sampled topic),
// openlog-sampler and allinone (runs the sampler in-process).
type TailSampling struct {
	Enabled           bool          // OPENLOG_TAILSAMPLING_ENABLED
	Group             string        // OPENLOG_TAILSAMPLING_GROUP
	DecisionWait      time.Duration // OPENLOG_TAILSAMPLING_DECISION_WAIT
	MaxTraces         int           // OPENLOG_TAILSAMPLING_MAX_TRACES
	MaxSpansPerTrace  int           // OPENLOG_TAILSAMPLING_MAX_SPANS_PER_TRACE
	MaxBufferedBytes  int64         // OPENLOG_TAILSAMPLING_MAX_BUFFERED_BYTES
	DecisionCacheTTL  time.Duration // OPENLOG_TAILSAMPLING_DECISION_CACHE_TTL
	DecisionCacheSize int           // OPENLOG_TAILSAMPLING_DECISION_CACHE_SIZE
	PolicyRefresh     time.Duration // OPENLOG_TAILSAMPLING_POLICY_REFRESH
	ProduceTimeout    time.Duration // OPENLOG_TAILSAMPLING_PRODUCE_TIMEOUT
	// DefaultPolicy is the JSON policy of tenants without a stored one (empty: keep everything).
	DefaultPolicy string // OPENLOG_TAILSAMPLING_DEFAULT_POLICY
}

func loadTailSampling(p *parser) TailSampling {
	return TailSampling{
		Enabled:           p.bool("OPENLOG_TAILSAMPLING_ENABLED", false),
		Group:             p.str("OPENLOG_TAILSAMPLING_GROUP", "openlog-sampler"),
		DecisionWait:      p.duration("OPENLOG_TAILSAMPLING_DECISION_WAIT", 30*time.Second),
		MaxTraces:         int(p.int64("OPENLOG_TAILSAMPLING_MAX_TRACES", 100000)),
		MaxSpansPerTrace:  int(p.int64("OPENLOG_TAILSAMPLING_MAX_SPANS_PER_TRACE", 2000)),
		MaxBufferedBytes:  p.int64("OPENLOG_TAILSAMPLING_MAX_BUFFERED_BYTES", 512<<20),
		DecisionCacheTTL:  p.duration("OPENLOG_TAILSAMPLING_DECISION_CACHE_TTL", 10*time.Minute),
		DecisionCacheSize: int(p.int64("OPENLOG_TAILSAMPLING_DECISION_CACHE_SIZE", 500000)),
		PolicyRefresh:     p.duration("OPENLOG_TAILSAMPLING_POLICY_REFRESH", 30*time.Second),
		ProduceTimeout:    p.duration("OPENLOG_TAILSAMPLING_PRODUCE_TIMEOUT", 10*time.Second),
		DefaultPolicy:     p.str("OPENLOG_TAILSAMPLING_DEFAULT_POLICY", ""),
	}
}

func (t TailSampling) validate() []error {
	var errs []error
	if t.Group == "" {
		errs = append(errs, errors.New("OPENLOG_TAILSAMPLING_GROUP must not be empty"))
	}
	if t.DecisionWait < time.Second || t.DecisionWait > 10*time.Minute {
		errs = append(errs, fmt.Errorf("OPENLOG_TAILSAMPLING_DECISION_WAIT: must be between 1s and 10m, got %s", t.DecisionWait))
	}
	if t.MaxTraces < 1 || t.MaxTraces > 100_000_000 {
		errs = append(errs, fmt.Errorf("OPENLOG_TAILSAMPLING_MAX_TRACES: must be between 1 and 100000000, got %d", t.MaxTraces))
	}
	if t.MaxSpansPerTrace < 1 || t.MaxSpansPerTrace > 1_000_000 {
		errs = append(errs, fmt.Errorf("OPENLOG_TAILSAMPLING_MAX_SPANS_PER_TRACE: must be between 1 and 1000000, got %d", t.MaxSpansPerTrace))
	}
	if t.MaxBufferedBytes < 1<<20 {
		errs = append(errs, fmt.Errorf("OPENLOG_TAILSAMPLING_MAX_BUFFERED_BYTES: must be at least 1048576, got %d", t.MaxBufferedBytes))
	}
	if t.DecisionCacheTTL < t.DecisionWait || t.DecisionCacheTTL > 24*time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_TAILSAMPLING_DECISION_CACHE_TTL: must be between OPENLOG_TAILSAMPLING_DECISION_WAIT and 24h, got %s", t.DecisionCacheTTL))
	}
	if t.DecisionCacheSize < 1 {
		errs = append(errs, fmt.Errorf("OPENLOG_TAILSAMPLING_DECISION_CACHE_SIZE: must be > 0, got %d", t.DecisionCacheSize))
	}
	if t.PolicyRefresh < time.Second || t.PolicyRefresh > time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_TAILSAMPLING_POLICY_REFRESH: must be between 1s and 1h, got %s", t.PolicyRefresh))
	}
	if t.ProduceTimeout <= 0 {
		errs = append(errs, errors.New("OPENLOG_TAILSAMPLING_PRODUCE_TIMEOUT must be > 0"))
	}
	if t.DefaultPolicy != "" && !json.Valid([]byte(t.DefaultPolicy)) {
		errs = append(errs, errors.New("OPENLOG_TAILSAMPLING_DEFAULT_POLICY: not valid JSON"))
	}
	return errs
}
