// Package selfmon holds the agent's self-telemetry counters (openlog.agent.*).
package selfmon

import (
	"sort"
	"sync"
	"time"
)

// ExportKey identifies an export counter series.
type ExportKey struct {
	Signal  string // metrics, logs
	Outcome string // sent, buffered, dropped
}

// Stats is a concurrency-safe set of self-telemetry counters.
type Stats struct {
	mu               sync.Mutex
	start            time.Time
	exportItems      map[ExportKey]uint64
	permissionDenied map[string]uint64
	durations        map[string]time.Duration
	bufferBytes      int64
	interval         time.Duration
	updateState      string
	updateAttempts   map[string]uint64

	integrationCollections map[string]uint64
	integrationErrors      map[string]uint64
	integrationDurations   map[string]time.Duration

	php *PHPSnapshot // nil until the PHP forwarder has started once
}

// PHPSnapshot holds the PHP forwarder counters (openlog.agent.php.*).
type PHPSnapshot struct {
	Messages           map[string]uint64 // result: accepted, malformed, unsupported_version, dropped
	Spans              uint64
	ReassemblyTimeouts uint64
	PendingTraces      int64
}

// PHPMessageResults lists the result values of openlog.agent.php.messages.
var PHPMessageResults = []string{"accepted", "malformed", "unsupported_version", "dropped"}

func (s *Stats) phpLocked() *PHPSnapshot {
	if s.php == nil {
		s.php = &PHPSnapshot{Messages: map[string]uint64{}}
	}
	return s.php
}

// PHPStarted makes the openlog.agent.php.* metrics visible (with zero values).
func (s *Stats) PHPStarted() {
	s.mu.Lock()
	s.phpLocked()
	s.mu.Unlock()
}

// AddPHPMessages increments openlog.agent.php.messages{result}.
func (s *Stats) AddPHPMessages(result string, n int) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	s.phpLocked().Messages[result] += uint64(n)
	s.mu.Unlock()
}

// AddPHPSpans increments openlog.agent.php.spans (spans handed to the export pipeline).
func (s *Stats) AddPHPSpans(n int) {
	s.mu.Lock()
	s.phpLocked().Spans += uint64(max(n, 0))
	s.mu.Unlock()
}

// AddPHPReassemblyTimeouts increments openlog.agent.php.reassembly_timeouts.
func (s *Stats) AddPHPReassemblyTimeouts(n int) {
	s.mu.Lock()
	s.phpLocked().ReassemblyTimeouts += uint64(max(n, 0))
	s.mu.Unlock()
}

// SetPHPPendingTraces records openlog.agent.php.pending_traces.
func (s *Stats) SetPHPPendingTraces(n int) {
	s.mu.Lock()
	s.phpLocked().PendingTraces = int64(n)
	s.mu.Unlock()
}

// AddIntegrationCollection records one collection of an integration for
// openlog.agent.integration.{collections,errors,duration}{integration}.
func (s *Stats) AddIntegrationCollection(integration string, d time.Duration, failed bool) {
	s.mu.Lock()
	if s.integrationCollections == nil {
		s.integrationCollections, s.integrationErrors, s.integrationDurations = map[string]uint64{}, map[string]uint64{}, map[string]time.Duration{}
	}
	s.integrationCollections[integration]++
	if failed {
		s.integrationErrors[integration]++
	} else if _, ok := s.integrationErrors[integration]; !ok {
		s.integrationErrors[integration] = 0
	}
	s.integrationDurations[integration] = d
	s.mu.Unlock()
}

// SetUpdateState records openlog.agent.update.state{state}.
func (s *Stats) SetUpdateState(state string) {
	s.mu.Lock()
	s.updateState = state
	s.mu.Unlock()
}

// AddUpdateAttempt increments openlog.agent.update.attempts{result}.
func (s *Stats) AddUpdateAttempt(result string) {
	s.mu.Lock()
	s.updateAttempts[result]++
	s.mu.Unlock()
}

// New creates empty stats; start is the counters' start timestamp.
func New(start time.Time) *Stats {
	return &Stats{
		start:            start,
		exportItems:      map[ExportKey]uint64{},
		permissionDenied: map[string]uint64{},
		durations:        map[string]time.Duration{},
		updateAttempts:   map[string]uint64{},
	}
}

// Start returns the time from which cumulative counters are counted.
func (s *Stats) Start() time.Time { return s.start }

// PermissionDenied increments openlog.agent.permission_denied{collector}.
func (s *Stats) PermissionDenied(collector string) {
	s.mu.Lock()
	s.permissionDenied[collector]++
	s.mu.Unlock()
}

// AddExportItems increments openlog.agent.export.items{signal,outcome}.
func (s *Stats) AddExportItems(signal, outcome string, n int) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	s.exportItems[ExportKey{signal, outcome}] += uint64(n)
	s.mu.Unlock()
}

// SetCollectorDuration records openlog.agent.collector.duration{collector}.
func (s *Stats) SetCollectorDuration(collector string, d time.Duration) {
	s.mu.Lock()
	s.durations[collector] = d
	s.mu.Unlock()
}

// SetBufferBytes records openlog.agent.buffer.usage.
func (s *Stats) SetBufferBytes(n int64) {
	s.mu.Lock()
	s.bufferBytes = n
	s.mu.Unlock()
}

// SetCollectionInterval records openlog.agent.collection.interval.
func (s *Stats) SetCollectionInterval(d time.Duration) {
	s.mu.Lock()
	s.interval = d
	s.mu.Unlock()
}

// Snapshot is a point-in-time copy of all counters.
type Snapshot struct {
	ExportItems      map[ExportKey]uint64
	PermissionDenied map[string]uint64
	Durations        map[string]time.Duration
	BufferBytes      int64
	Interval         time.Duration
	UpdateState      string // empty until the update manager reports one
	UpdateAttempts   map[string]uint64

	IntegrationCollections map[string]uint64
	IntegrationErrors      map[string]uint64
	IntegrationDurations   map[string]time.Duration

	PHP *PHPSnapshot // nil until the PHP forwarder has started once
}

// Snapshot copies the current values.
func (s *Stats) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{
		IntegrationCollections: make(map[string]uint64, len(s.integrationCollections)),
		IntegrationErrors:      make(map[string]uint64, len(s.integrationErrors)),
		IntegrationDurations:   make(map[string]time.Duration, len(s.integrationDurations)),
		ExportItems:            make(map[ExportKey]uint64, len(s.exportItems)),
		PermissionDenied:       make(map[string]uint64, len(s.permissionDenied)),
		Durations:              make(map[string]time.Duration, len(s.durations)),
		BufferBytes:            s.bufferBytes,
		Interval:               s.interval,
		UpdateState:            s.updateState,
		UpdateAttempts:         make(map[string]uint64, len(s.updateAttempts)),
	}
	if s.php != nil {
		p := *s.php
		p.Messages = make(map[string]uint64, len(s.php.Messages))
		for k, v := range s.php.Messages {
			p.Messages[k] = v
		}
		out.PHP = &p
	}
	for k, v := range s.integrationCollections {
		out.IntegrationCollections[k] = v
	}
	for k, v := range s.integrationErrors {
		out.IntegrationErrors[k] = v
	}
	for k, v := range s.integrationDurations {
		out.IntegrationDurations[k] = v
	}
	for k, v := range s.updateAttempts {
		out.UpdateAttempts[k] = v
	}
	for k, v := range s.exportItems {
		out.ExportItems[k] = v
	}
	for k, v := range s.permissionDenied {
		out.PermissionDenied[k] = v
	}
	for k, v := range s.durations {
		out.Durations[k] = v
	}
	return out
}

// SortedKeys returns map keys in sorted order for deterministic output.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
