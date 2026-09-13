// Package updatereq is the request channel between the UI and openlog-updater (D-041,
// docs/contracts/releases-updates.md §5.1): "Check now" and "Update now" insert a row into the
// PostgreSQL table update_requests; the updater claims pending rows every few seconds, so an admin
// does not wait for OPENLOG_UPDATER_INTERVAL. Both the Compose updater and the Kubernetes CronJob
// already have the PostgreSQL DSN for their status document, so no new network path or credential
// is needed.
package updatereq

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Actions.
const (
	ActionCheck = "check"
	ActionApply = "apply"
)

// Request states.
const (
	StatePending = "pending"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
	StateExpired = "expired"
)

const (
	// MinGap is the minimum time between two requests of the same action (installation-wide).
	MinGap = 30 * time.Second
	// PendingTTL expires requests no updater picked up (no updater, or one without request support).
	PendingTTL = 15 * time.Minute
	// RunningTTL bounds how long a running apply blocks new apply requests (an updater that died
	// mid-request marks it failed on its next start; this covers one that never comes back).
	RunningTTL = 6 * time.Hour
	// HeartbeatKey is the system_state document the polling updater refreshes.
	HeartbeatKey = "updater_poll"
	// HeartbeatEvery is how often a polling updater refreshes HeartbeatKey.
	HeartbeatEvery = 30 * time.Second
)

// Request is one row of update_requests.
type Request struct {
	ID                      string     `json:"id"`
	Action                  string     `json:"action"`
	TargetVersion           string     `json:"target_version"`
	IgnoreMaintenanceWindow bool       `json:"ignore_maintenance_window"`
	State                   string     `json:"state"`
	Message                 string     `json:"message"`
	OrgID                   string     `json:"-"`
	RequestedBy             string     `json:"-"`
	RequestedByEmail        string     `json:"requested_by_email,omitempty"`
	RequestedAt             time.Time  `json:"requested_at"`
	PickedAt                *time.Time `json:"picked_at"`
	FinishedAt              *time.Time `json:"finished_at"`
}

// Open reports whether the request still waits for or is being handled by the updater.
func (r *Request) Open() bool {
	return r != nil && (r.State == StatePending || r.State == StateRunning)
}

// Heartbeat is the HeartbeatKey document: a polling updater is connected.
type Heartbeat struct {
	Engine      string    `json:"engine"`
	Mode        string    `json:"mode"`
	PollSeconds int       `json:"poll_seconds"`
	PolledAt    time.Time `json:"polled_at"`
}

// Listening reports whether the heartbeat is recent enough to expect a pickup within seconds.
func (h *Heartbeat) Listening(now time.Time) bool {
	if h == nil || h.PolledAt.IsZero() {
		return false
	}
	return now.Sub(h.PolledAt) <= max(3*HeartbeatEvery, 3*time.Duration(h.PollSeconds)*time.Second)
}

// TooSoonError means a request of the same action was made less than MinGap ago (429).
type TooSoonError struct{ RetryAfter time.Duration }

func (e *TooSoonError) Error() string {
	return fmt.Sprintf("a request was made less than %s ago; retry in %s", MinGap, e.RetryAfter.Round(time.Second))
}

// ErrBusy means an apply request is already pending or running (409).
var ErrBusy = errors.New("an update request is already pending or running")

// Queue is the API side: enqueue and read requests.
type Queue interface {
	// Enqueue validates the rate limit (and, for apply, that no apply is open) and stores r with
	// state pending, filling ID, State and RequestedAt.
	Enqueue(ctx context.Context, r *Request, minGap time.Duration) error
	// Latest returns the newest request (nil when there is none).
	Latest(ctx context.Context) (*Request, error)
	// Heartbeat returns the polling updater's heartbeat (nil when none was written).
	Heartbeat(ctx context.Context) (*Heartbeat, error)
}

// Poller is the updater side.
type Poller interface {
	// Claim expires stale pending requests and marks the oldest pending one running (nil: none).
	Claim(ctx context.Context) (*Request, error)
	// Finish stores the result of a claimed request.
	Finish(ctx context.Context, id, state, message string) error
	// Abandon fails requests left running by a previous updater process.
	Abandon(ctx context.Context, message string) error
	// PutHeartbeat refreshes the heartbeat document.
	PutHeartbeat(ctx context.Context, hb Heartbeat) error
}

// MemQueue implements Queue and Poller in memory with the semantics of PGQueue (tests, static mode).
type MemQueue struct {
	Now func() time.Time

	mu   sync.Mutex
	reqs []*Request
	hb   *Heartbeat
	seq  int
}

func (m *MemQueue) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

// Enqueue implements Queue.
func (m *MemQueue) Enqueue(_ context.Context, r *Request, minGap time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for i := len(m.reqs) - 1; i >= 0; i-- {
		o := m.reqs[i]
		if o.Action == r.Action {
			if d := now.Sub(o.RequestedAt); d < minGap {
				return &TooSoonError{RetryAfter: minGap - d}
			}
			break
		}
	}
	if r.Action == ActionApply {
		for _, o := range m.reqs {
			if o.Action == ActionApply && openAt(o, now) {
				return ErrBusy
			}
		}
	}
	m.seq++
	c := *r
	c.ID, c.State, c.Message, c.RequestedAt, c.PickedAt, c.FinishedAt = fmt.Sprintf("00000000-0000-4000-8000-%012d", m.seq), StatePending, "", now, nil, nil
	m.reqs = append(m.reqs, &c)
	*r = c
	return nil
}

func openAt(o *Request, now time.Time) bool {
	switch o.State {
	case StatePending:
		return now.Sub(o.RequestedAt) < PendingTTL
	case StateRunning:
		return o.PickedAt == nil || now.Sub(*o.PickedAt) < RunningTTL
	}
	return false
}

// Latest implements Queue.
func (m *MemQueue) Latest(context.Context) (*Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.reqs) == 0 {
		return nil, nil
	}
	c := *m.reqs[len(m.reqs)-1]
	return &c, nil
}

// Heartbeat implements Queue.
func (m *MemQueue) Heartbeat(context.Context) (*Heartbeat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hb == nil {
		return nil, nil
	}
	c := *m.hb
	return &c, nil
}

// Claim implements Poller.
func (m *MemQueue) Claim(context.Context) (*Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var pending []*Request
	for _, o := range m.reqs {
		if o.State != StatePending {
			continue
		}
		if now.Sub(o.RequestedAt) >= PendingTTL {
			o.State, o.Message, o.FinishedAt = StateExpired, expiredMessage, &now
			continue
		}
		pending = append(pending, o)
	}
	if len(pending) == 0 {
		return nil, nil
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].RequestedAt.Before(pending[j].RequestedAt) })
	o := pending[0]
	o.State, o.PickedAt = StateRunning, &now
	c := *o
	return &c, nil
}

// Finish implements Poller.
func (m *MemQueue) Finish(_ context.Context, id, state, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for _, o := range m.reqs {
		if o.ID == id {
			o.State, o.Message, o.FinishedAt = state, clip(message), &now
			return nil
		}
	}
	return nil
}

// Abandon implements Poller.
func (m *MemQueue) Abandon(_ context.Context, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for _, o := range m.reqs {
		if o.State == StateRunning {
			o.State, o.Message, o.FinishedAt = StateFailed, clip(message), &now
		}
	}
	return nil
}

// PutHeartbeat implements Poller.
func (m *MemQueue) PutHeartbeat(_ context.Context, hb Heartbeat) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hb = &hb
	return nil
}

// All returns copies of every request, oldest first (tests).
func (m *MemQueue) All() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Request, 0, len(m.reqs))
	for _, o := range m.reqs {
		out = append(out, *o)
	}
	return out
}

const expiredMessage = "not picked up by openlog-updater within 15m (is an updater with request support running?)"

// clip bounds a message to the column limit (2000 bytes, valid UTF-8).
func clip(s string) string {
	const n = 2000
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && (s[i]&0xC0) == 0x80 {
		i--
	}
	return s[:i]
}
