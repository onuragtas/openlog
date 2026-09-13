package alert

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/alert/notify"
)

// Incident is an alert incident (alert_incidents).
type Incident struct {
	ID                  string
	OrgID               string
	RuleID              string // "" when the rule was deleted
	RuleName            string
	RuleType            string
	Severity            string
	SeriesKey           string
	Labels              map[string]string
	Summary             string
	State               string
	Value               *float64
	LastValue           *float64
	Threshold           *float64
	ChannelIDs          []string
	Flapping            bool
	OpenedAt            time.Time
	AcknowledgedAt      *time.Time
	AcknowledgedByEmail string
	ResolvedAt          *time.Time
	ResolvedByEmail     string
	ResolveReason       string
	Muted               bool // computed by the reader
}

// IncidentEvent is one timeline entry (alert_incident_events).
type IncidentEvent struct {
	ID          int64
	IncidentID  string
	OrgID       string
	At          time.Time
	Kind        string
	ActorUserID string
	ActorEmail  string
	Message     string
	Details     map[string]any
}

// Timeline event kinds.
const (
	EventOpened                 = "opened"
	EventFlapping               = "flapping"
	EventAcknowledged           = "acknowledged"
	EventNote                   = "note"
	EventRenotified             = "renotified"
	EventResolved               = "resolved"
	EventNotificationDelivered  = "notification_delivered"
	EventNotificationFailed     = "notification_failed"
	EventNotificationSuppressed = "notification_suppressed"
	EventNotificationMuted      = "notification_muted"
)

// Notification is an outbox row (alert_notifications).
type Notification struct {
	ID             string
	OrgID          string
	IncidentID     string
	RuleID         string
	ChannelID      string
	ChannelType    string
	Kind           string
	IdempotencyKey string
	Payload        json.RawMessage
	Status         string
	Attempts       int
	NextAttemptAt  time.Time
	LastError      string
	CreatedAt      time.Time
	FinishedAt     *time.Time
}

// ChannelRef is a channel as seen by the evaluator.
type ChannelRef struct {
	ID      string
	Type    string
	Enabled bool
}

// IncidentResolve resolves an incident.
type IncidentResolve struct {
	ID        string
	Reason    string
	At        time.Time
	LastValue *float64
}

// IncidentUpdate changes an open incident.
type IncidentUpdate struct {
	ID        string
	LastValue *float64
	Flapping  bool
}

// Plan is everything one evaluation commits atomically (with lease fencing).
type Plan struct {
	RuleID        string
	RuleVersion   int
	OrgID         string
	EvalEnd       time.Time
	NextEvalAt    time.Time
	Result        string
	Error         string
	Duration      time.Duration
	SeriesUpserts []SeriesState
	SeriesDeletes []string
	Opens         []Incident
	Resolves      []IncidentResolve
	Updates       []IncidentUpdate
	Events        []IncidentEvent
	Notifications []Notification
	Transitions   map[string]int
}

// Empty reports whether the plan changes nothing but the schedule.
func (p *Plan) Empty() bool {
	return len(p.SeriesUpserts)+len(p.SeriesDeletes)+len(p.Opens)+len(p.Resolves)+len(p.Updates)+len(p.Events)+len(p.Notifications) == 0
}

// PlanInput is the input of BuildPlan.
type PlanInput struct {
	Rule        *Rule
	Channels    []ChannelRef // the rule's channels
	States      map[string]SeriesState
	PrevEvalEnd time.Time
	End         time.Time
	Result      *EvalResult
	Delay       time.Duration
	PublicURL   string
	NewID       func() string
}

// StepConfigFor builds the state machine settings of a rule.
func StepConfigFor(r *Definition, delay time.Duration) StepConfig {
	w := r.Condition.Window()
	return StepConfig{
		Judge: r.Condition.Judge(), For: time.Duration(r.ForSeconds) * time.Second,
		RecoveryFor: time.Duration(r.RecoveryForSeconds) * time.Second, Interval: r.Interval(), Delay: delay,
		Flapping: r.Flapping, Missing: r.Condition.Missing(), ExpireAfter: max(time.Hour, 10*w),
	}
}

func optFloat(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// BuildPlan runs the state machine for every series of one evaluation and derives incidents, timeline events
// and notifications (docs/contracts/alerting.md §3, §5.1). It is pure.
func BuildPlan(in PlanInput) *Plan {
	r := in.Rule
	newID := in.NewID
	if newID == nil {
		newID = func() string { return uuid.NewString() }
	}
	p := &Plan{RuleID: r.ID, RuleVersion: r.Version, OrgID: r.OrgID, EvalEnd: in.End, Result: "ok", Transitions: map[string]int{}}
	cfg := StepConfigFor(&r.Definition, in.Delay)
	unit := ""
	samples := map[string]Sample{}
	if in.Result != nil {
		unit = in.Result.Unit
		for _, s := range in.Result.Samples {
			samples[s.Key] = s
		}
	}
	keys := make([]string, 0, len(samples)+len(in.States))
	for k := range samples {
		keys = append(keys, k)
	}
	for k := range in.States {
		if _, ok := samples[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var channels []ChannelRef
	for _, c := range in.Channels {
		if c.Enabled {
			channels = append(channels, c)
		}
	}

	for _, key := range keys {
		prev, known := in.States[key]
		// The incident was resolved outside the evaluator (manual resolve): the series starts over.
		if known && prev.IncidentID != "" && (prev.Incident == nil || prev.Incident.State == IncidentResolved) {
			prev = resetToOK(prev)
		}
		sample, present := samples[key]
		if !known && !present {
			continue
		}
		if !known {
			prev = SeriesState{Key: key, Labels: sample.Labels, LastValue: math.NaN()}
		}
		if present {
			prev.Labels = sample.Labels
		}
		prev.Key = key
		out := Step(prev, StepInput{End: in.End, PrevEvalEnd: in.PrevEvalEnd, Present: present, Value: sample.Value}, cfg)
		next := out.Next
		if out.Transition != "" {
			p.Transitions[out.Transition]++
		}
		value := math.NaN()
		if present {
			value = sample.Value
		}

		switch {
		case out.Open:
			if !present {
				sample = Sample{Key: key, Labels: next.Labels, Value: math.NaN()}
			}
			inc := Incident{
				ID: newID(), OrgID: r.OrgID, RuleID: r.ID, RuleName: r.Name, RuleType: r.Type, Severity: r.Severity,
				SeriesKey: key, Labels: incidentLabels(r, next.Labels), Summary: r.Condition.Summary(sample, unit),
				State: IncidentOpen, Value: optFloat(value), LastValue: optFloat(value), Threshold: optFloat(cfg.Judge.Threshold),
				OpenedAt: in.End, ChannelIDs: []string{},
			}
			for _, c := range channels {
				inc.ChannelIDs = append(inc.ChannelIDs, c.ID)
			}
			p.Opens = append(p.Opens, inc)
			next.IncidentID = inc.ID
			next.Incident = &inc
			p.Events = append(p.Events, IncidentEvent{IncidentID: inc.ID, OrgID: r.OrgID, At: in.End, Kind: EventOpened,
				Message: inc.Summary, Details: map[string]any{"value": inc.Value, "threshold": inc.Threshold}})
			for _, c := range channels {
				p.Notifications = append(p.Notifications, newNotification(r, &inc, c, KindOpened, "", in.PublicURL, newID))
			}
		case out.Resolve != "" && prev.IncidentID != "":
			inc := prev.Incident
			res := IncidentResolve{ID: prev.IncidentID, Reason: out.Resolve, At: in.End, LastValue: optFloat(value)}
			p.Resolves = append(p.Resolves, res)
			msg := "recovered"
			if present {
				msg = "recovered: value " + formatValue(value)
			}
			if out.Resolve != ReasonRecovered {
				msg = out.Resolve
			}
			p.Events = append(p.Events, IncidentEvent{IncidentID: prev.IncidentID, OrgID: r.OrgID, At: in.End, Kind: EventResolved,
				Message: msg, Details: map[string]any{"reason": out.Resolve, "value": optFloat(value)}})
			if inc != nil {
				resolved := *inc
				resolved.State = IncidentResolved
				resolved.ResolvedAt = &res.At
				resolved.ResolveReason = res.Reason
				if res.LastValue != nil {
					resolved.LastValue = res.LastValue
				}
				p.Notifications = append(p.Notifications, resolveNotifications(r, &resolved, in.PublicURL, newID)...)
			}
		default:
			if next.State == StateFiring && next.Incident != nil {
				inc := next.Incident
				if out.Flapping && !inc.Flapping {
					p.Updates = append(p.Updates, IncidentUpdate{ID: inc.ID, LastValue: optFloat(value), Flapping: true})
					p.Events = append(p.Events, IncidentEvent{IncidentID: inc.ID, OrgID: r.OrgID, At: in.End, Kind: EventFlapping,
						Message: "flapping: recovery is held", Details: map[string]any{"hold_seconds": r.Flapping.HoldSeconds}})
					inc.Flapping = true
				}
				if r.RenotifyIntervalSeconds > 0 && inc.State == IncidentOpen &&
					in.End.Sub(next.LastNotifiedAt) >= time.Duration(r.RenotifyIntervalSeconds)*time.Second {
					next.RenotifyCount++
					next.LastNotifiedAt = in.End
					cur := *inc
					if v := optFloat(value); v != nil {
						cur.LastValue = v
					}
					p.Updates = append(p.Updates, IncidentUpdate{ID: inc.ID, LastValue: cur.LastValue, Flapping: inc.Flapping})
					p.Events = append(p.Events, IncidentEvent{IncidentID: inc.ID, OrgID: r.OrgID, At: in.End, Kind: EventRenotified,
						Message: "re-notification " + itoa(next.RenotifyCount)})
					for _, cid := range inc.ChannelIDs {
						ch := channelType(in.Channels, cid)
						if ch.ID == "" {
							continue
						}
						p.Notifications = append(p.Notifications, newNotification(r, &cur, ch, KindRenotify, itoa(next.RenotifyCount), in.PublicURL, newID))
					}
				}
			}
		}

		switch {
		case out.Persist:
			p.SeriesUpserts = append(p.SeriesUpserts, next)
		case out.Delete && known:
			p.SeriesDeletes = append(p.SeriesDeletes, key)
		}
	}
	return p
}

func itoa(n int) string { return strconv.Itoa(n) }

func channelType(chs []ChannelRef, id string) ChannelRef {
	for _, c := range chs {
		if c.ID == id {
			return c
		}
	}
	return ChannelRef{}
}

func incidentLabels(r *Rule, series map[string]string) map[string]string {
	out := make(map[string]string, len(series)+len(r.Labels)+2)
	for k, v := range r.Labels {
		out[k] = v
	}
	for k, v := range series {
		out[k] = v
	}
	out["alert.severity"] = r.Severity
	out["alert.rule_name"] = r.Name
	return out
}

// IdempotencyKey returns the outbox key of a notification (§5.1).
func IdempotencyKey(incidentID, kind, seq, channelID string) string {
	if kind == KindRenotify {
		return incidentID + ":renotify:" + seq + ":" + channelID
	}
	return incidentID + ":" + kind + ":" + channelID
}

var kindEvent = map[string]string{KindOpened: notify.EventOpened, KindResolved: notify.EventResolved, KindRenotify: notify.EventRenotify, KindTest: notify.EventTest}

// BuildEvent renders the stored payload of a notification.
func BuildEvent(r *Rule, inc *Incident, kind, key, publicURL string) notify.Event {
	ev := notify.Event{
		Version: "1", Event: kindEvent[kind], IdempotencyKey: key,
		Organization: notify.Org{ID: r.OrgID, Name: r.OrgName},
		Rule:         notify.RuleInfo{ID: r.ID, Name: r.Name, Type: r.Type, Severity: r.Severity, RunbookURL: r.RunbookURL},
	}
	base := strings.TrimRight(publicURL, "/")
	if base != "" && r.ID != "" {
		ev.Rule.URL = base + "/alerts/rules/" + r.ID
	}
	if inc != nil {
		ev.Incident = notify.IncidentInfo{
			ID: inc.ID, State: inc.State, Summary: inc.Summary, Value: inc.LastValue, Threshold: inc.Threshold,
			Labels: inc.Labels, OpenedAt: fmtTime(inc.OpenedAt),
		}
		if kind == KindOpened {
			ev.Incident.Value = inc.Value
		}
		if ev.Incident.Labels == nil {
			ev.Incident.Labels = map[string]string{}
		}
		if base != "" {
			ev.Incident.URL = base + "/alerts/incidents/" + inc.ID
		}
		if inc.AcknowledgedAt != nil {
			s := fmtTime(*inc.AcknowledgedAt)
			ev.Incident.AcknowledgedAt = &s
		}
		if inc.ResolvedAt != nil {
			s := fmtTime(*inc.ResolvedAt)
			ev.Incident.ResolvedAt = &s
		}
		if inc.ResolveReason != "" {
			reason := inc.ResolveReason
			ev.Incident.ResolveReason = &reason
		}
	}
	return ev
}

func fmtTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }

func newNotification(r *Rule, inc *Incident, ch ChannelRef, kind, seq, publicURL string, newID func() string) Notification {
	key := IdempotencyKey(inc.ID, kind, seq, ch.ID)
	payload, _ := json.Marshal(BuildEvent(r, inc, kind, key, publicURL))
	return Notification{ID: newID(), OrgID: r.OrgID, IncidentID: inc.ID, RuleID: r.ID, ChannelID: ch.ID, ChannelType: ch.Type,
		Kind: kind, IdempotencyKey: key, Payload: payload, Status: StatusPending}
}

// resolveNotifications enqueues resolve notifications to the channels the incident was opened with. Channel types
// are resolved by the store (ChannelType may be empty here; the store fills it from alert_channels).
func resolveNotifications(r *Rule, inc *Incident, publicURL string, newID func() string) []Notification {
	out := make([]Notification, 0, len(inc.ChannelIDs))
	for _, cid := range inc.ChannelIDs {
		out = append(out, newNotification(r, inc, ChannelRef{ID: cid}, KindResolved, "", publicURL, newID))
	}
	return out
}
