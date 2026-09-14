package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Event collection limits (§7.5).
const (
	eventMaxAgeAtStart = 5 * time.Minute
	eventSeenTTL       = 2 * time.Hour
	eventBatchMax      = 500
	eventFlushEvery    = 2 * time.Second
	eventMessageMax    = 16 << 10
)

// Events watches core/v1 events of all namespaces and emits every new or updated event once as a log record.
type Events struct {
	Client *Client
	Log    *slog.Logger
	// Emit receives batches of log records (called from Run's goroutine).
	Emit func(recs []*logspb.LogRecord)
	Now  func() time.Time

	seen map[string]seenEvent
}

type seenEvent struct {
	count int
	at    time.Time
}

func (e *Events) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// eventTime returns the most recent observation time of an event.
func eventTime(ev *Event) time.Time {
	switch {
	case ev.Series != nil && !ev.Series.LastObservedTime.IsZero():
		return ev.Series.LastObservedTime.Time
	case ev.LastTimestamp != nil && !ev.LastTimestamp.IsZero():
		return *ev.LastTimestamp
	case !ev.EventTime.IsZero():
		return ev.EventTime.Time
	case ev.FirstTimestamp != nil && !ev.FirstTimestamp.IsZero():
		return *ev.FirstTimestamp
	case ev.Metadata.CreationTimestamp != nil:
		return *ev.Metadata.CreationTimestamp
	}
	return time.Time{}
}

func eventCount(ev *Event) int {
	if ev.Series != nil && ev.Series.Count > 0 {
		return ev.Series.Count
	}
	return max(ev.Count, 1)
}

// record converts an event; ok is false when it was already sent with the same count.
func (e *Events) record(ev *Event, minTime time.Time) (*logspb.LogRecord, bool) {
	if e.seen == nil {
		e.seen = map[string]seenEvent{}
	}
	ts := eventTime(ev)
	count := eventCount(ev)
	uid := ev.Metadata.UID
	if s, ok := e.seen[uid]; ok && s.count >= count {
		return nil, false
	}
	if !minTime.IsZero() && ts.Before(minTime) {
		e.seen[uid] = seenEvent{count: count, at: e.now()}
		return nil, false
	}
	e.seen[uid] = seenEvent{count: count, at: e.now()}
	return EventRecord(ev, e.now()), true
}

// EventRecord builds the log record of an event (§7.5).
func EventRecord(ev *Event, observed time.Time) *logspb.LogRecord {
	ts := eventTime(ev)
	if ts.IsZero() {
		ts = observed
	}
	sevNum, sevText := logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"
	if ev.Type == "Warning" {
		sevNum, sevText = logspb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN"
	}
	ns := ev.InvolvedObject.Namespace
	if ns == "" {
		ns = ev.Metadata.Namespace
	}
	source := ev.Source.Component
	if source == "" {
		source = ev.ReportingController
	}
	attrs := []*commonpb.KeyValue{
		otlputil.Str("k8s.event.name", ev.Metadata.Name),
		otlputil.Str("k8s.event.uid", ev.Metadata.UID),
		otlputil.Str("k8s.event.reason", ev.Reason),
		otlputil.Str("k8s.event.type", ev.Type),
		otlputil.Int("k8s.event.count", int64(eventCount(ev))),
	}
	opt := func(k, v string) {
		if v != "" {
			attrs = append(attrs, otlputil.Str(k, v))
		}
	}
	opt("k8s.event.action", ev.Action)
	opt("k8s.event.source", source)
	opt(AttrNamespace, ns)
	opt("k8s.object.kind", ev.InvolvedObject.Kind)
	opt("k8s.object.name", ev.InvolvedObject.Name)
	opt("k8s.object.uid", ev.InvolvedObject.UID)
	opt("k8s.object.fieldpath", ev.InvolvedObject.FieldPath)
	switch ev.InvolvedObject.Kind {
	case "Pod":
		opt(AttrPodName, ev.InvolvedObject.Name)
		opt(AttrPodUID, ev.InvolvedObject.UID)
		opt(AttrNodeName, ev.Source.Host)
	case "Node":
		opt(AttrNodeName, ev.InvolvedObject.Name)
	}
	return &logspb.LogRecord{
		TimeUnixNano:         uint64(ts.UnixNano()),
		ObservedTimeUnixNano: uint64(observed.UnixNano()),
		SeverityNumber:       sevNum,
		SeverityText:         sevText,
		EventName:            EventName,
		Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: truncate(ev.Message, eventMessageMax)}},
		Attributes:           attrs,
	}
}

func (e *Events) prune() {
	cut := e.now().Add(-eventSeenTTL)
	for uid, s := range e.seen {
		if s.at.Before(cut) {
			delete(e.seen, uid)
		}
	}
}

// Run lists events (skipping ones older than 5 minutes), then watches until ctx ends, relisting on errors.
func (e *Events) Run(ctx context.Context) {
	var pending []*logspb.LogRecord
	flush := func() {
		if len(pending) > 0 {
			e.Emit(pending)
			pending = nil
		}
	}
	backoff := time.Second
	for ctx.Err() == nil {
		var list List[Event]
		lctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := e.Client.Get(lctx, "/api/v1/events", url.Values{"resourceVersion": {"0"}}, &list)
		cancel()
		if err != nil {
			if e.Log != nil && ctx.Err() == nil {
				e.Log.Warn("kubernetes event list failed", "error", err)
			}
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		minTime := e.now().Add(-eventMaxAgeAtStart)
		for i := range list.Items {
			if rec, ok := e.record(&list.Items[i], minTime); ok {
				pending = append(pending, rec)
				if len(pending) >= eventBatchMax {
					flush()
				}
			}
		}
		flush()
		e.prune()
		rv := list.Metadata.ResourceVersion
		lastFlush := time.Now()
		for ctx.Err() == nil {
			rv, err = e.Client.Watch(ctx, "/api/v1/events", nil, rv, func(w WatchEvent) error {
				if w.Type == "DELETED" {
					return nil
				}
				var ev Event
				if err := json.Unmarshal(w.Object, &ev); err != nil {
					return nil
				}
				if rec, ok := e.record(&ev, minTime); ok {
					pending = append(pending, rec)
				}
				if len(pending) >= eventBatchMax || time.Since(lastFlush) >= eventFlushEvery {
					flush()
					lastFlush = time.Now()
				}
				return nil
			})
			flush()
			if err != nil {
				if !errors.Is(err, ErrGone) && ctx.Err() == nil && e.Log != nil {
					e.Log.Warn("kubernetes event watch failed", "error", err)
					sleepCtx(ctx, time.Second)
				}
				break
			}
			e.prune()
		}
	}
	flush()
}
