package openlog

import (
	"io"
	"log/slog"
	"sync"
	"time"
)

// diagLogger returns the agent's own diagnostics logger (text, stderr by default).
func diagLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})).With("component", "openlog-go-agent")
}

// errorHandler receives SDK/exporter errors (otel.SetErrorHandler). Export failures are
// never fatal to the application: they are logged, at most once per interval per message,
// with a count of suppressed repeats.
type errorHandler struct {
	log      *slog.Logger
	interval time.Duration
	now      func() time.Time

	mu   sync.Mutex
	seen map[string]*errState
}

type errState struct {
	last       time.Time
	suppressed int
}

func newErrorHandler(log *slog.Logger) *errorHandler {
	return &errorHandler{log: log, interval: time.Minute, now: time.Now, seen: map[string]*errState{}}
}

func (h *errorHandler) Handle(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if len(msg) > 512 {
		msg = msg[:512]
	}
	h.mu.Lock()
	st, ok := h.seen[msg]
	now := h.now()
	if ok && now.Sub(st.last) < h.interval {
		st.suppressed++
		h.mu.Unlock()
		return
	}
	suppressed := 0
	if ok {
		suppressed = st.suppressed
	}
	if len(h.seen) > 256 {
		clear(h.seen)
	}
	h.seen[msg] = &errState{last: now}
	h.mu.Unlock()
	h.log.Warn("telemetry export error", "error", msg, "suppressed_repeats", suppressed)
}
