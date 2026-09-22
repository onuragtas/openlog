package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/exporter"
)

// Poll interval bounds (releases-updates.md §3).
const (
	MinPollInterval     = 60 * time.Second
	MaxPollInterval     = 3600 * time.Second
	DefaultPollInterval = 300 * time.Second
	// MaxInitialDelay spreads the first sync of many agents starting together.
	MaxInitialDelay = 10 * time.Second
	syncTimeout     = 30 * time.Second
	maxResponse     = 64 << 20
)

// SyncPath is the sync endpoint below the ingest endpoint.
const SyncPath = "/v1/openlog/agent/sync"

// ErrSyncUnsupported means the backend answered 404 or 501: sync stays off until restart.
var ErrSyncUnsupported = errors.New("agent sync not supported by the backend")

// ClampPoll converts a server-provided poll interval to a duration within [60s, 3600s].
// Zero or negative means "not provided" and yields the default.
func ClampPoll(seconds int) time.Duration {
	if seconds <= 0 {
		return DefaultPollInterval
	}
	d := time.Duration(seconds) * time.Second
	return min(max(d, MinPollInterval), MaxPollInterval)
}

// Jitter spreads d by ±10%; r is uniform in [0, 1).
func Jitter(d time.Duration, r float64) time.Duration {
	return time.Duration(float64(d) * (0.9 + 0.2*r))
}

// Syncer periodically posts the agent state and hands update instructions to Handle.
type Syncer struct {
	Endpoint   string
	LicenseKey string
	UserAgent  string
	Client     *http.Client
	Log        *slog.Logger
	// Request builds the next request body.
	Request func() SyncRequest
	// Handle processes an instruction (may block for the duration of an update).
	Handle func(context.Context, *Instruction)
	// Integrations receives the remote integration config of a successful sync
	// when the backend sent one (optional; must not block for long).
	Integrations func(*config.RemoteIntegrations)
	// PHPAgent receives the php_agent section of a successful sync when the backend sent one (optional; must not
	// block for long).
	PHPAgent func(json.RawMessage)
	// JavaAgent receives the java_agent section of a successful sync when the backend sent one (optional; must not
	// block for long).
	JavaAgent func(json.RawMessage)
	// EBPFProfiler receives the ebpf_profiler section of a successful sync when the backend sent one (optional;
	// must not block).
	EBPFProfiler func(json.RawMessage)
	// Kick triggers an immediate sync (state changes).
	Kick <-chan struct{}
	// InitialDelay before the first sync; negative means a random delay up to MaxInitialDelay.
	InitialDelay time.Duration
	// Rand returns a uniform float in [0, 1); defaults to math/rand.
	Rand func() float64
}

// Once performs one sync.
func (s *Syncer) Once(ctx context.Context) (*SyncResponse, error) {
	body, err := json.Marshal(s.Request())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.Endpoint, "/")+SyncPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(exporter.LicenseHeader, s.LicenseKey)
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sync: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrSyncUnsupported, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("sync: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out SyncResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&out); err != nil {
		return nil, fmt.Errorf("sync: decode response: %w", err)
	}
	return &out, nil
}

// Run syncs until ctx is done or the backend does not support sync.
func (s *Syncer) Run(ctx context.Context) {
	rnd := s.Rand
	if rnd == nil {
		rnd = rand.Float64
	}
	log := s.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	wait := s.InitialDelay
	if wait < 0 {
		wait = time.Duration(rnd() * float64(MaxInitialDelay))
	}
	interval := DefaultPollInterval
	lastErr := ""
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.Kick:
			t.Stop()
		case <-t.C:
		}

		resp, err := s.Once(ctx)
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, ErrSyncUnsupported):
			log.Info("backend does not support agent sync; sync and updates disabled until restart", "error", err)
			return
		case err != nil:
			if msg := err.Error(); msg != lastErr {
				log.Warn("agent sync failed; will retry", "error", err, "retry_in", interval)
				lastErr = msg
			}
		default:
			if lastErr != "" {
				log.Info("agent sync recovered")
				lastErr = ""
			}
			interval = ClampPoll(resp.PollIntervalSeconds)
			if resp.IntegrationsConfig != nil && s.Integrations != nil {
				s.Integrations(resp.IntegrationsConfig)
			}
			if s.PHPAgent != nil && len(resp.PHPAgent) > 0 && string(resp.PHPAgent) != "null" {
				s.PHPAgent(resp.PHPAgent)
			}
			if s.JavaAgent != nil && len(resp.JavaAgent) > 0 && string(resp.JavaAgent) != "null" {
				s.JavaAgent(resp.JavaAgent)
			}
			if s.EBPFProfiler != nil && len(resp.EBPFProfiler) > 0 && string(resp.EBPFProfiler) != "null" {
				s.EBPFProfiler(resp.EBPFProfiler)
			}
		}
		wait = Jitter(interval, rnd())
		if err == nil && resp.Update != nil {
			s.Handle(ctx, resp.Update)
			if ctx.Err() != nil {
				return
			}
			// Come back when a scheduled update becomes due, if that is before the next poll.
			if nb, _ := resp.Update.times(); !nb.IsZero() {
				if until := time.Until(nb); until > 0 && until < wait {
					wait = until + time.Duration(rnd()*float64(time.Second))
				}
			}
		}
	}
}
