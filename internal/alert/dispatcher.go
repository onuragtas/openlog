package alert

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/alert/notify"
	"github.com/onuragtas/openlog/internal/alert/secrets"
)

// Sender delivers one notification (notify.Sender).
type Sender interface {
	Send(ctx context.Context, t notify.Target, ev notify.Event, threadKey string) notify.Result
}

// DispatcherOptions configure a Dispatcher.
type DispatcherOptions struct {
	Instance        string
	Workers         int
	DeliveryTimeout time.Duration
	MaxAttempts     int
	MaxAge          time.Duration
	Poll            time.Duration
	Retention       time.Duration
	Log             *slog.Logger
	Registerer      prometheus.Registerer
	Now             func() time.Time
}

// Dispatcher delivers outbox rows (docs/contracts/alerting.md §5.1).
type Dispatcher struct {
	store  OutboxStore
	sender Sender
	keys   *secrets.Keyring
	o      DispatcherOptions

	muMutes sync.Mutex
	mutes   map[string]cachedMutes

	cNotifications *prometheus.CounterVec
	hDuration      *prometheus.HistogramVec
	gPending       prometheus.Gauge
}

type cachedMutes struct {
	at    time.Time
	mutes []Mute
}

// Backoff schedule (§5.1); the last entry repeats.
var backoffSchedule = []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute,
	10 * time.Minute, 20 * time.Minute, 30 * time.Minute}

// Backoff returns the delay before retry number attempt (1-based) with ±20 % jitter.
func Backoff(attempt int) time.Duration {
	d := backoffSchedule[min(max(attempt, 1), len(backoffSchedule))-1]
	jitter := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(d) * jitter)
}

// NewDispatcher creates a dispatcher.
func NewDispatcher(store OutboxStore, sender Sender, keys *secrets.Keyring, o DispatcherOptions) *Dispatcher {
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.DeliveryTimeout <= 0 {
		o.DeliveryTimeout = 10 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 10
	}
	if o.MaxAge <= 0 {
		o.MaxAge = 24 * time.Hour
	}
	if o.Poll <= 0 {
		o.Poll = time.Second
	}
	if o.Retention <= 0 {
		o.Retention = 30 * 24 * time.Hour
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	d := &Dispatcher{store: store, sender: sender, keys: keys, o: o, mutes: map[string]cachedMutes{},
		cNotifications: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_alert_notifications_total",
			Help: "Notification delivery outcomes by channel type."}, []string{"channel_type", "result"}),
		hDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "openlog_alert_delivery_duration_seconds",
			Help: "Notification delivery attempt duration by channel type.", Buckets: prometheus.ExponentialBuckets(0.01, 2, 12)}, []string{"channel_type"}),
		gPending: prometheus.NewGauge(prometheus.GaugeOpts{Name: "openlog_alert_outbox_pending", Help: "Pending and sending notifications in the outbox."}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(d.cNotifications, d.hDuration, d.gPending)
	}
	return d
}

// ClaimTTL is how long a claimed row stays reserved for one dispatcher.
func (d *Dispatcher) ClaimTTL() time.Duration { return d.o.DeliveryTimeout + 30*time.Second }

// Run delivers notifications until ctx is done. In-flight deliveries finish before it returns.
func (d *Dispatcher) Run(ctx context.Context) {
	lastPrune, lastGauge := time.Time{}, time.Time{}
	for ctx.Err() == nil {
		n, err := d.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			d.o.Log.Warn("alert dispatcher round failed", "err", err)
		}
		now := d.o.Now()
		if now.Sub(lastGauge) > 15*time.Second {
			lastGauge = now
			if c, err := d.store.PendingCount(ctx); err == nil {
				d.gPending.Set(float64(c))
			}
		}
		if now.Sub(lastPrune) > 10*time.Minute {
			lastPrune = now
			if c, err := d.store.Prune(ctx, now.Add(-d.o.Retention)); err != nil {
				d.o.Log.Debug("alert outbox prune failed", "err", err)
			} else if c > 0 {
				d.o.Log.Info("pruned finished alert notifications", "rows", c)
			}
		}
		if n > 0 {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(d.o.Poll):
		}
	}
}

// RunOnce requeues expired claims, claims one batch and delivers it. It returns the number of claimed rows.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	if _, err := d.store.RequeueExpired(ctx); err != nil {
		return 0, err
	}
	batch, err := d.store.ClaimDeliveries(ctx, d.o.Instance, d.o.Workers*2, d.ClaimTTL())
	if err != nil {
		return 0, err
	}
	sem := make(chan struct{}, d.o.Workers)
	var wg sync.WaitGroup
	for _, del := range batch {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			// A delivery that started finishes and is recorded even during shutdown.
			dctx := context.WithoutCancel(ctx)
			out := d.Process(dctx, del)
			fctx, cancel := context.WithTimeout(dctx, 10*time.Second)
			if err := d.store.FinishDelivery(fctx, d.o.Instance, del, out); err != nil {
				d.o.Log.Warn("cannot record notification outcome", "notification_id", del.ID, "err", err)
			}
			cancel()
		}()
	}
	wg.Wait()
	return len(batch), nil
}

func (d *Dispatcher) activeMutes(ctx context.Context, orgID string, now time.Time) []Mute {
	d.muMutes.Lock()
	c, ok := d.mutes[orgID]
	d.muMutes.Unlock()
	if ok && now.Sub(c.at) < 5*time.Second {
		return c.mutes
	}
	ms, err := d.store.ActiveMutes(ctx, orgID, now)
	if err != nil {
		d.o.Log.Warn("cannot load mutes; delivering unmuted", "org_id", orgID, "err", err)
		return nil
	}
	d.muMutes.Lock()
	d.mutes[orgID] = cachedMutes{at: now, mutes: ms}
	d.muMutes.Unlock()
	return ms
}

func (d *Dispatcher) result(del *Delivery, result string) {
	d.cNotifications.WithLabelValues(del.ChannelType, result).Inc()
}

func (d *Dispatcher) timeline(del *Delivery, kind, msg string, details map[string]any) *IncidentEvent {
	if del.IncidentID == "" {
		return nil
	}
	if details == nil {
		details = map[string]any{}
	}
	details["notification_id"] = del.ID
	details["notification_kind"] = del.Kind
	details["channel_type"] = del.ChannelType
	if del.Channel != nil {
		details["channel_id"] = del.Channel.ID
		details["channel_name"] = del.Channel.Name
	}
	return &IncidentEvent{IncidentID: del.IncidentID, OrgID: del.OrgID, At: d.o.Now(), Kind: kind, Message: msg, Details: details}
}

func (d *Dispatcher) terminal(del *Delivery, status, msg, eventKind string) DeliveryOutcome {
	d.result(del, map[string]string{StatusFailed: "failed", StatusSuppressed: "suppressed"}[status])
	return DeliveryOutcome{Status: status, Error: msg, Event: d.timeline(del, eventKind, msg, nil), MutedLogged: del.MutedLogged}
}

// Process decides and performs the delivery of one claimed row.
func (d *Dispatcher) Process(ctx context.Context, del *Delivery) DeliveryOutcome {
	now := d.o.Now()
	if del.Channel == nil {
		return d.terminal(del, StatusFailed, "channel deleted", EventNotificationFailed)
	}
	if !del.Channel.Enabled && del.Kind != KindTest {
		return d.terminal(del, StatusFailed, "channel disabled", EventNotificationFailed)
	}
	if del.Kind != KindTest {
		inc := del.Incident
		if inc == nil {
			return d.terminal(del, StatusFailed, "incident deleted", EventNotificationFailed)
		}
		if del.Kind != KindOpened && del.OpenedStatus != StatusDelivered {
			return d.terminal(del, StatusSuppressed, "opening notification was not delivered", EventNotificationSuppressed)
		}
		if del.Kind == KindRenotify && inc.State != IncidentOpen {
			return d.terminal(del, StatusSuppressed, "incident acknowledged or resolved", EventNotificationSuppressed)
		}
		if m := MatchingMute(d.activeMutes(ctx, del.OrgID, now), inc.RuleID, inc.Labels, now); m != nil {
			if del.Kind == KindRenotify {
				return d.terminal(del, StatusSuppressed, "muted by "+m.Name, EventNotificationSuppressed)
			}
			d.result(del, "muted")
			out := DeliveryOutcome{Status: StatusPending, NextAttemptAt: m.EndsAt, Error: "muted until " + fmtTime(m.EndsAt), MutedLogged: true}
			if !del.MutedLogged {
				out.Event = d.timeline(del, EventNotificationMuted, "muted by "+m.Name+" until "+fmtTime(m.EndsAt), map[string]any{"mute_id": m.ID})
			}
			return out
		}
		if del.Kind == KindOpened && del.MutedLogged && inc.State == IncidentResolved {
			return d.terminal(del, StatusSuppressed, "incident resolved during a mute", EventNotificationSuppressed)
		}
	}

	sec, err := DecryptSecrets(d.keys, del.OrgID, del.Channel.ID, del.Channel.Secrets)
	if err != nil {
		msg := "cannot decrypt channel secrets"
		if errors.Is(err, secrets.ErrNoKey) {
			msg += " (OPENLOG_SECRETS_KEY is not configured)"
		} else if errors.Is(err, secrets.ErrUnknownKey) {
			msg += " (encrypted with a key that is not configured)"
		}
		return d.retryOrFail(del, now, msg, 0, nil)
	}
	var ev notify.Event
	if err := json.Unmarshal(del.Payload, &ev); err != nil {
		return d.terminal(del, StatusFailed, "invalid payload", EventNotificationFailed)
	}
	ev.NotificationID = del.ID
	ev.SentAt = fmtTime(now)
	thread := ""
	if del.Kind == KindResolved || del.Kind == KindRenotify {
		thread = IdempotencyKey(del.IncidentID, KindOpened, "", del.Channel.ID)
	}
	res := d.sender.Send(ctx, TargetFor(del.Channel, sec), ev, thread)
	d.hDuration.WithLabelValues(del.ChannelType).Observe(res.Duration.Seconds())
	att := &Attempt{Attempt: del.Attempts, Instance: d.o.Instance, StartedAt: now, Duration: res.Duration, Success: res.OK(), StatusCode: res.StatusCode}
	if res.OK() {
		d.result(del, "delivered")
		return DeliveryOutcome{Status: StatusDelivered, Attempt: att, MutedLogged: del.MutedLogged,
			Event: d.timeline(del, EventNotificationDelivered, "delivered to "+del.Channel.Name, map[string]any{"attempt": del.Attempts, "status_code": res.StatusCode})}
	}
	att.Error = truncateErr(res.Err)
	if !res.Retryable {
		d.result(del, "failed")
		return DeliveryOutcome{Status: StatusFailed, Error: att.Error, Attempt: att, MutedLogged: del.MutedLogged,
			Event: d.timeline(del, EventNotificationFailed, "delivery to "+del.Channel.Name+" failed: "+att.Error, map[string]any{"attempt": del.Attempts, "status_code": res.StatusCode})}
	}
	return d.retryOrFail(del, now, att.Error, res.RetryAfter, att)
}

func (d *Dispatcher) retryOrFail(del *Delivery, now time.Time, msg string, retryAfter time.Duration, att *Attempt) DeliveryOutcome {
	if del.Attempts >= d.o.MaxAttempts || now.Sub(del.CreatedAt) >= d.o.MaxAge {
		d.result(del, "failed")
		return DeliveryOutcome{Status: StatusFailed, Error: msg, Attempt: att, MutedLogged: del.MutedLogged,
			Event: d.timeline(del, EventNotificationFailed, "gave up after "+itoa(del.Attempts)+" attempts: "+msg, map[string]any{"attempts": del.Attempts})}
	}
	d.result(del, "retry")
	wait := max(Backoff(del.Attempts), retryAfter)
	return DeliveryOutcome{Status: StatusPending, NextAttemptAt: now.Add(wait), Error: msg, Attempt: att, MutedLogged: del.MutedLogged}
}
