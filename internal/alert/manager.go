package alert

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/alert/notify"
	"github.com/onuragtas/openlog/internal/alert/secrets"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/slo"
)

// ErrForbidden reports a write a member may not perform (someone else's rule or mute).
var ErrForbidden = errors.New("forbidden")

// ManagerStore is the persistence used by the API (implemented by PGStore).
type ManagerStore interface {
	CountRules(ctx context.Context, orgID string) (int, error)
	ListRules(ctx context.Context, orgID string, f RuleFilter) ([]RuleView, error)
	GetRule(ctx context.Context, orgID, id string) (*RuleView, []SeriesState, error)
	CreateRule(ctx context.Context, orgID string, d *Definition, actor Actor) (*RuleView, error)
	UpdateRule(ctx context.Context, orgID, id string, d *Definition, expectedVersion int, actor Actor, publicURL string) (*RuleView, error)
	SetRuleEnabled(ctx context.Context, orgID, id string, enabled bool, actor Actor, publicURL string) (*RuleView, error)
	DeleteRule(ctx context.Context, orgID, id string, actor Actor, publicURL string) error

	ListIncidents(ctx context.Context, orgID string, f IncidentFilter) ([]Incident, string, IncidentCounts, error)
	GetIncident(ctx context.Context, orgID, id string) (*Incident, []IncidentEvent, []DeliveryView, error)
	AcknowledgeIncident(ctx context.Context, orgID, id string, actor Actor) (*Incident, error)
	ResolveIncident(ctx context.Context, orgID, id, note string, actor Actor, publicURL string) (*Incident, error)
	AddIncidentNote(ctx context.Context, orgID, id, text string, actor Actor) (*IncidentEvent, error)

	ListChannels(ctx context.Context, orgID string) ([]Channel, error)
	GetChannel(ctx context.Context, orgID, id string) (*Channel, error)
	CreateChannel(ctx context.Context, orgID, id string, p *PreparedChannel, stored, keyID string, actor Actor) (*Channel, error)
	UpdateChannel(ctx context.Context, orgID, id string, p *PreparedChannel, stored, keyID string, actor Actor) (*Channel, error)
	DeleteChannel(ctx context.Context, orgID, id string, actor Actor) error
	RecordTest(ctx context.Context, n Notification, att Attempt, actor Actor) error
	OrgName(ctx context.Context, orgID string) (string, error)

	ListMutes(ctx context.Context, orgID string, includeExpired bool) ([]Mute, error)
	GetMute(ctx context.Context, orgID, id string) (*Mute, error)
	CreateMute(ctx context.Context, orgID string, m *ValidMute, actor Actor) (*Mute, error)
	UpdateMute(ctx context.Context, orgID, id string, m *ValidMute, actor Actor) (*Mute, error)
	DeleteMute(ctx context.Context, orgID, id string, actor Actor) error

	ListHolidayCalendars(ctx context.Context, orgID string) ([]HolidayCalendar, error)
	GetHolidayCalendar(ctx context.Context, orgID, id string) (*HolidayCalendar, error)
	CreateHolidayCalendar(ctx context.Context, orgID string, v *ValidHolidayCalendar, actor Actor) (*HolidayCalendar, error)
	UpdateHolidayCalendar(ctx context.Context, orgID, id string, v *ValidHolidayCalendar, actor Actor) (*HolidayCalendar, error)
	DeleteHolidayCalendar(ctx context.Context, orgID, id string, actor Actor) error
	HolidayCalendarDates(ctx context.Context, orgID string, ids []string) (map[string][]string, error)

	ListDeliveries(ctx context.Context, orgID string, f DeliveryFilter) ([]DeliveryView, error)
}

var _ ManagerStore = (*PGStore)(nil)

// ApdexSettingsFunc lists an organization's APM settings (apm.PGSettings.List).
type ApdexSettingsFunc func(ctx context.Context, orgID string) ([]apm.Setting, error)

// ManagerOptions configure a Manager.
type ManagerOptions struct {
	Keys           *secrets.Keyring
	Sender         Sender
	PublicURL      string
	MaxRulesPerOrg int
	Limits         Limits
	Delay          time.Duration
	QueryTimeout   time.Duration
	DefaultApdexT  time.Duration
	ApdexSettings  ApdexSettingsFunc
	// ErrorStates is the APM error workflow for apm_error previews (nil: regressed previews fail).
	ErrorStates apm.ErrorStateStore
	// SLOs holds the SLO definitions of slo_burn previews (nil: such previews fail).
	SLOs slo.Store
	Now  func() time.Time
}

// Manager implements the alerting API operations (docs/contracts/alerting.md §7). Role checks that depend only
// on the role are done by the HTTP layer; ownership checks (members change their own rules and mutes) are here.
type Manager struct {
	store ManagerStore
	o     ManagerOptions
}

// NewManager creates a manager.
func NewManager(store ManagerStore, o ManagerOptions) *Manager {
	if o.MaxRulesPerOrg <= 0 {
		o.MaxRulesPerOrg = 1000
	}
	if o.QueryTimeout <= 0 {
		o.QueryTimeout = 20 * time.Second
	}
	if o.DefaultApdexT <= 0 {
		o.DefaultApdexT = apm.DefaultApdexT
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Manager{store: store, o: o}
}

// SecretsConfigured reports whether channel secrets can be encrypted.
func (m *Manager) SecretsConfigured() bool { return m.o.Keys.Configured() }

// ---- rules ----

func (m *Manager) ListRules(ctx context.Context, orgID string, f RuleFilter) ([]RuleView, error) {
	return m.store.ListRules(ctx, orgID, f)
}

func (m *Manager) GetRule(ctx context.Context, orgID, id string) (*RuleView, []SeriesState, error) {
	return m.store.GetRule(ctx, orgID, id)
}

func (m *Manager) CreateRule(ctx context.Context, orgID string, in RuleInput, actor Actor) (*RuleView, error) {
	d, err := in.Validate()
	if err != nil {
		return nil, err
	}
	n, err := m.store.CountRules(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if n >= m.o.MaxRulesPerOrg {
		return nil, &PreconditionError{Msg: "the organization has reached the maximum number of alert rules"}
	}
	return m.store.CreateRule(ctx, orgID, d, actor)
}

// owned loads a rule and checks that actor may change it.
func (m *Manager) owned(ctx context.Context, orgID, id string, actor Actor, manageAny bool) (*RuleView, error) {
	v, _, err := m.store.GetRule(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if !manageAny && (v.CreatedBy == "" || v.CreatedBy != actor.UserID) {
		return nil, ErrForbidden
	}
	return v, nil
}

func (m *Manager) UpdateRule(ctx context.Context, orgID, id string, in RuleInput, actor Actor, manageAny bool) (*RuleView, error) {
	d, err := in.Validate()
	if err != nil {
		return nil, err
	}
	if _, err := m.owned(ctx, orgID, id, actor, manageAny); err != nil {
		return nil, err
	}
	return m.store.UpdateRule(ctx, orgID, id, d, in.Version, actor, m.o.PublicURL)
}

func (m *Manager) SetRuleEnabled(ctx context.Context, orgID, id string, enabled bool, actor Actor, manageAny bool) (*RuleView, error) {
	if _, err := m.owned(ctx, orgID, id, actor, manageAny); err != nil {
		return nil, err
	}
	return m.store.SetRuleEnabled(ctx, orgID, id, enabled, actor, m.o.PublicURL)
}

func (m *Manager) DeleteRule(ctx context.Context, orgID, id string, actor Actor, manageAny bool) error {
	if _, err := m.owned(ctx, orgID, id, actor, manageAny); err != nil {
		return err
	}
	return m.store.DeleteRule(ctx, orgID, id, actor, m.o.PublicURL)
}

// WithApdex attaches the organization's Apdex thresholds to ctx for APM conditions.
func (m *Manager) WithApdex(ctx context.Context, orgID string) context.Context {
	return WithApdexT(ctx, m.apdexLookup(ctx, orgID))
}

func (m *Manager) apdexLookup(ctx context.Context, orgID string) ApdexTFunc {
	var settings []apm.Setting
	if m.o.ApdexSettings != nil {
		settings, _ = m.o.ApdexSettings(ctx, orgID)
	}
	return ApdexLookup(settings, m.o.DefaultApdexT)
}

// Preview evaluates a rule definition over the last hours. sc must be the caller's tenant scope.
func (m *Manager) Preview(ctx context.Context, sc *query.Scope, orgID string, in RuleInput, hours int) (*PreviewResult, error) {
	d, err := in.Validate()
	if err != nil {
		return nil, err
	}
	if hours == 0 {
		hours = 6
	}
	qctx, cancel := context.WithTimeout(m.WithApdex(ctx, orgID), m.o.QueryTimeout)
	defer cancel()
	if m.o.ErrorStates != nil {
		qctx = WithErrorWorkflow(qctx, ErrorWorkflow{OrgID: orgID, Store: m.o.ErrorStates})
	}
	if m.o.SLOs != nil {
		qctx = WithSLOs(qctx, SLOLookup{OrgID: orgID, Store: m.o.SLOs})
	}
	lim := m.o.Limits
	return Preview(qctx, sc, d, hours, m.o.Now(), m.o.Delay, lim)
}

// EvaluationHistory returns the evaluation summaries of a rule of the organization (404 for another org's rule).
// sc must be the caller's tenant scope.
func (m *Manager) EvaluationHistory(ctx context.Context, sc *query.Scope, orgID, ruleID string, from, to time.Time) (*History, error) {
	if _, _, err := m.store.GetRule(ctx, orgID, ruleID); err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, m.o.QueryTimeout)
	defer cancel()
	return EvaluationHistory(qctx, sc, ruleID, from, to)
}

// ---- incidents ----

func (m *Manager) ListIncidents(ctx context.Context, orgID string, f IncidentFilter) ([]Incident, string, IncidentCounts, error) {
	return m.store.ListIncidents(ctx, orgID, f)
}

func (m *Manager) GetIncident(ctx context.Context, orgID, id string) (*Incident, []IncidentEvent, []DeliveryView, error) {
	return m.store.GetIncident(ctx, orgID, id)
}

func (m *Manager) AcknowledgeIncident(ctx context.Context, orgID, id string, actor Actor) (*Incident, error) {
	return m.store.AcknowledgeIncident(ctx, orgID, id, actor)
}

func (m *Manager) ResolveIncident(ctx context.Context, orgID, id, note string, actor Actor) (*Incident, error) {
	if len([]rune(note)) > 4000 {
		return nil, invalid("note", "at most 4000 characters")
	}
	return m.store.ResolveIncident(ctx, orgID, id, note, actor, m.o.PublicURL)
}

func (m *Manager) AddIncidentNote(ctx context.Context, orgID, id, text string, actor Actor) (*IncidentEvent, error) {
	if n := len([]rune(text)); n < 1 || n > 4000 {
		return nil, invalid("text", "must be 1-4000 characters")
	}
	return m.store.AddIncidentNote(ctx, orgID, id, text, actor)
}

// ---- channels ----

func (m *Manager) ListChannels(ctx context.Context, orgID string) ([]Channel, error) {
	return m.store.ListChannels(ctx, orgID)
}

func (m *Manager) GetChannel(ctx context.Context, orgID, id string) (*Channel, error) {
	return m.store.GetChannel(ctx, orgID, id)
}

// CreateChannel validates, encrypts and stores a channel. The generated secrets are returned once.
func (m *Manager) CreateChannel(ctx context.Context, orgID string, in ChannelInput, actor Actor) (*Channel, map[string]string, error) {
	p, err := PrepareChannel(in, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	if !m.o.Keys.Configured() {
		return nil, nil, ErrNoSecretsKey
	}
	id := uuid.NewString()
	stored, keyID, err := EncryptSecrets(m.o.Keys, orgID, id, p.Secrets)
	if err != nil {
		return nil, nil, err
	}
	ch, err := m.store.CreateChannel(ctx, orgID, id, p, stored, keyID, actor)
	return ch, p.Generated, err
}

func (m *Manager) UpdateChannel(ctx context.Context, orgID, id string, in ChannelInput, actor Actor) (*Channel, error) {
	old, err := m.store.GetChannel(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if !m.o.Keys.Configured() {
		return nil, ErrNoSecretsKey
	}
	existing, err := DecryptSecrets(m.o.Keys, orgID, id, old.Secrets)
	if err != nil {
		return nil, &PreconditionError{Msg: "the stored channel secrets cannot be decrypted with the configured keys; enter all secrets again"}
	}
	p, err := PrepareChannel(in, old, existing)
	if err != nil {
		return nil, err
	}
	stored, keyID, err := EncryptSecrets(m.o.Keys, orgID, id, p.Secrets)
	if err != nil {
		return nil, err
	}
	return m.store.UpdateChannel(ctx, orgID, id, p, stored, keyID, actor)
}

func (m *Manager) DeleteChannel(ctx context.Context, orgID, id string, actor Actor) error {
	return m.store.DeleteChannel(ctx, orgID, id, actor)
}

// TestResult is the outcome of a test send.
type TestResult struct {
	Success        bool
	StatusCode     int
	Error          string
	Duration       time.Duration
	NotificationID string
}

// TestChannel sends a test notification synchronously and records it in the delivery log.
func (m *Manager) TestChannel(ctx context.Context, orgID, id string, actor Actor) (*TestResult, error) {
	ch, err := m.store.GetChannel(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if !m.o.Keys.Configured() {
		return nil, ErrNoSecretsKey
	}
	sec, err := DecryptSecrets(m.o.Keys, orgID, id, ch.Secrets)
	if err != nil {
		return nil, &PreconditionError{Msg: "the stored channel secrets cannot be decrypted with the configured keys"}
	}
	orgName, err := m.store.OrgName(ctx, orgID)
	if err != nil {
		return nil, err
	}
	now := m.o.Now().UTC()
	nid := uuid.NewString()
	key := "test:" + nid
	ev := notify.Event{
		Version: "1", Event: notify.EventTest, IdempotencyKey: key, NotificationID: nid, SentAt: fmtTime(now),
		Organization: notify.Org{ID: orgID, Name: orgName},
		Rule:         notify.RuleInfo{Name: "Test notification", Severity: SeverityInfo},
		Incident: notify.IncidentInfo{State: "test", Summary: "Test notification from openlog (channel " + ch.Name + ", sent by " + actor.Email + ")",
			Labels: map[string]string{}, OpenedAt: fmtTime(now)},
	}
	res := m.o.Sender.Send(ctx, TargetFor(ch, sec), ev, "")
	out := &TestResult{Success: res.OK(), StatusCode: res.StatusCode, Duration: res.Duration, NotificationID: nid}
	status := StatusDelivered
	if !res.OK() {
		out.Error = truncateErr(res.Err)
		status = StatusFailed
	}
	payload, _ := json.Marshal(ev)
	n := Notification{ID: nid, OrgID: orgID, ChannelID: id, ChannelType: ch.Type, Kind: KindTest, IdempotencyKey: key, Payload: payload,
		Status: status, LastError: out.Error}
	att := Attempt{Attempt: 1, Instance: "api", StartedAt: now, Duration: res.Duration, Success: res.OK(), StatusCode: res.StatusCode, Error: out.Error}
	if err := m.store.RecordTest(context.WithoutCancel(ctx), n, att, actor); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- mutes ----

func (m *Manager) ListMutes(ctx context.Context, orgID string, includeExpired bool) ([]Mute, error) {
	return m.store.ListMutes(ctx, orgID, includeExpired)
}

func (m *Manager) CreateMute(ctx context.Context, orgID string, in MuteInput, parseTime func(string) (time.Time, error), actor Actor) (*Mute, error) {
	v, err := m.validMute(ctx, orgID, in, parseTime)
	if err != nil {
		return nil, err
	}
	return m.store.CreateMute(ctx, orgID, v, actor)
}

// validMute validates a mute write and resolves the holiday calendars of its schedule (they must exist in the
// organization); the first occurrence then skips holidays.
func (m *Manager) validMute(ctx context.Context, orgID string, in MuteInput, parseTime func(string) (time.Time, error)) (*ValidMute, error) {
	now := m.o.Now()
	v, err := in.Validate(parseTime, now)
	if err != nil {
		return nil, err
	}
	if v.Schedule == nil || len(v.Schedule.HolidayCalendarIDs) == 0 {
		return v, nil
	}
	cals, err := m.store.HolidayCalendarDates(ctx, orgID, v.Schedule.HolidayCalendarIDs)
	if err != nil {
		return nil, err
	}
	if err := v.ApplyHolidays(cals, now); err != nil {
		return nil, err
	}
	return v, nil
}

// PreviewMuteSchedule validates a schedule and returns its next n occurrences (holiday calendars resolved).
func (m *Manager) PreviewMuteSchedule(ctx context.Context, orgID string, in MuteScheduleInput, parseTime func(string) (time.Time, error), n int) ([][2]time.Time, error) {
	v, err := m.validMute(ctx, orgID, MuteInput{Name: "preview", Schedule: &in}, parseTime)
	if err != nil {
		return nil, err
	}
	return v.Schedule.Occurrences(m.o.Now(), n), nil
}

func (m *Manager) ownedMute(ctx context.Context, orgID, id string, actor Actor, manageAny bool) error {
	mu, err := m.store.GetMute(ctx, orgID, id)
	if err != nil {
		return err
	}
	if !manageAny && (mu.CreatedBy == "" || mu.CreatedBy != actor.UserID) {
		return ErrForbidden
	}
	return nil
}

func (m *Manager) UpdateMute(ctx context.Context, orgID, id string, in MuteInput, parseTime func(string) (time.Time, error), actor Actor, manageAny bool) (*Mute, error) {
	v, err := m.validMute(ctx, orgID, in, parseTime)
	if err != nil {
		return nil, err
	}
	if err := m.ownedMute(ctx, orgID, id, actor, manageAny); err != nil {
		return nil, err
	}
	return m.store.UpdateMute(ctx, orgID, id, v, actor)
}

func (m *Manager) DeleteMute(ctx context.Context, orgID, id string, actor Actor, manageAny bool) error {
	if err := m.ownedMute(ctx, orgID, id, actor, manageAny); err != nil {
		return err
	}
	return m.store.DeleteMute(ctx, orgID, id, actor)
}

// ---- holiday calendars ----

func (m *Manager) ListHolidayCalendars(ctx context.Context, orgID string) ([]HolidayCalendar, error) {
	return m.store.ListHolidayCalendars(ctx, orgID)
}

func (m *Manager) GetHolidayCalendar(ctx context.Context, orgID, id string) (*HolidayCalendar, error) {
	return m.store.GetHolidayCalendar(ctx, orgID, id)
}

func (m *Manager) CreateHolidayCalendar(ctx context.Context, orgID string, in HolidayCalendarInput, actor Actor) (*HolidayCalendar, error) {
	v, err := in.Validate()
	if err != nil {
		return nil, err
	}
	return m.store.CreateHolidayCalendar(ctx, orgID, v, actor)
}

func (m *Manager) UpdateHolidayCalendar(ctx context.Context, orgID, id string, in HolidayCalendarInput, actor Actor) (*HolidayCalendar, error) {
	v, err := in.Validate()
	if err != nil {
		return nil, err
	}
	return m.store.UpdateHolidayCalendar(ctx, orgID, id, v, actor)
}

func (m *Manager) DeleteHolidayCalendar(ctx context.Context, orgID, id string, actor Actor) error {
	return m.store.DeleteHolidayCalendar(ctx, orgID, id, actor)
}

// ---- deliveries ----

func (m *Manager) ListDeliveries(ctx context.Context, orgID string, f DeliveryFilter) ([]DeliveryView, error) {
	return m.store.ListDeliveries(ctx, orgID, f)
}

// OrgName returns the name of an organization.
func (s *PGStore) OrgName(ctx context.Context, orgID string) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, orgID).Scan(&name)
	return name, mapPGErr(err)
}
