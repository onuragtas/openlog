package updater

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/updatecheck"
	"github.com/onuragtas/openlog/internal/updatemsg"
)

// States reported in Status.State.
const (
	StateOff            = "off"
	StateError          = "error"
	StateUpToDate       = "up_to_date"
	StateAvailable      = "available"
	StateWaiting        = "waiting_for_maintenance_window"
	StateUpdating       = "updating"
	StateSucceeded      = "succeeded"
	StateFailed         = "failed"
	StateRolledBack     = "rolled_back"
	StateRollbackFailed = "rollback_failed"
)

// Step status values.
const (
	StepRunning = "running"
	StepOK      = "ok"
	StepFailed  = "failed"
)

// StepRecord is one step of the last update attempt.
type StepRecord struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Detail     string     `json:"detail,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// HistoryEntry is a finished update attempt.
type HistoryEntry struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Result string    `json:"result"`
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
}

// Status is the updater state document (system_state "updater", shown as `updater` in
// GET /api/v1/version).
type Status struct {
	Engine  string `json:"engine"`
	Mode    string `json:"mode"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	// MessageCode and MessageParams identify Message for translation (internal/updatemsg); empty
	// when Message is free text or empty.
	MessageCode     string            `json:"message_code,omitempty"`
	MessageParams   map[string]string `json:"message_params,omitempty"`
	Error           string            `json:"error,omitempty"`
	CurrentVersion  string            `json:"current_version,omitempty"`
	TargetVersion   string            `json:"target_version,omitempty"`
	PreviousVersion string            `json:"previous_version,omitempty"`
	NotesURL        string            `json:"notes_url,omitempty"`
	CheckedAt       time.Time         `json:"checked_at"`
	StartedAt       *time.Time        `json:"started_at,omitempty"`
	FinishedAt      *time.Time        `json:"finished_at,omitempty"`
	BackupFile      string            `json:"backup_file,omitempty"`
	Steps           []StepRecord      `json:"steps,omitempty"`
	FailedVersions  []string          `json:"failed_versions,omitempty"`
	History         []HistoryEntry    `json:"history,omitempty"`
}

const maxHistory = 10

// setMessage sets Message to the English text of code (internal/updatemsg) and records the code
// and params for translation.
func (s *Status) setMessage(code string, params updatemsg.Params) {
	s.Message, s.MessageCode, s.MessageParams = updatemsg.Format(code, params), code, params
}

// setText sets Message to text, with its code when text is a known updater message.
func (s *Status) setText(text string) {
	s.Message, s.MessageCode, s.MessageParams = text, "", nil
	if code, params, ok := updatemsg.Parse(text); ok {
		s.MessageCode, s.MessageParams = code, params
	}
}

// clearMessage removes Message and its code.
func (s *Status) clearMessage() {
	s.Message, s.MessageCode, s.MessageParams = "", "", nil
}

func (s *Status) beginStep(name string, now time.Time) {
	s.Steps = append(s.Steps, StepRecord{Name: name, Status: StepRunning, StartedAt: now})
}

func (s *Status) endStep(err error, detail string, now time.Time) {
	if len(s.Steps) == 0 {
		return
	}
	st := &s.Steps[len(s.Steps)-1]
	st.FinishedAt = &now
	st.Status, st.Detail = StepOK, detail
	if err != nil {
		st.Status, st.Detail = StepFailed, err.Error()
	}
}

func (s *Status) addHistory(h HistoryEntry) {
	s.History = append([]HistoryEntry{h}, s.History...)
	if len(s.History) > maxHistory {
		s.History = s.History[:maxHistory]
	}
}

// StatusStore persists Status.
type StatusStore interface {
	Load(ctx context.Context) (Status, bool, error)
	Save(ctx context.Context, st Status) error
}

// FileStore keeps Status in a JSON file (the compose updater's backup directory), so the state
// survives while PostgreSQL is unreachable.
type FileStore struct{ Path string }

// Load implements StatusStore.
func (f FileStore) Load(context.Context) (Status, bool, error) {
	var st Status
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	return st, true, json.Unmarshal(b, &st)
}

// Save implements StatusStore (atomic rename).
func (f FileStore) Save(_ context.Context, st Status) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o750); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// PostgresStore keeps Status in system_state so every API pod can show it.
type PostgresStore struct{ Pool *pgxpool.Pool }

// Load implements StatusStore.
func (p PostgresStore) Load(ctx context.Context) (Status, bool, error) {
	var st Status
	_, found, err := postgres.GetSystemState(ctx, p.Pool, updatecheck.UpdaterStateKey, &st)
	return st, found, err
}

// Save implements StatusStore.
func (p PostgresStore) Save(ctx context.Context, st Status) error {
	return postgres.PutSystemState(ctx, p.Pool, updatecheck.UpdaterStateKey, st)
}

// MultiStore loads from the first store that has a document and saves to all of them; a failing
// store is logged, not fatal (PostgreSQL may be briefly unavailable during an update).
type MultiStore struct {
	Stores []StatusStore
	Log    *slog.Logger
}

// Load implements StatusStore.
func (m MultiStore) Load(ctx context.Context) (Status, bool, error) {
	var firstErr error
	for _, s := range m.Stores {
		st, found, err := s.Load(ctx)
		if err == nil && found {
			return st, true, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return Status{}, false, firstErr
}

// Save implements StatusStore.
func (m MultiStore) Save(ctx context.Context, st Status) error {
	ok := false
	var firstErr error
	for _, s := range m.Stores {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.Save(sctx, st)
		cancel()
		if err != nil {
			if m.Log != nil {
				m.Log.Warn("cannot store updater status", "store", storeName(s), "err", err)
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok = true
	}
	if ok {
		return nil
	}
	return firstErr
}

func storeName(s StatusStore) string {
	switch s.(type) {
	case FileStore:
		return "file"
	case PostgresStore:
		return "postgres"
	}
	return "other"
}

// Auditor records updater events in the audit log.
type Auditor interface {
	Audit(ctx context.Context, action string, details map[string]any)
}

// PostgresAuditor writes organization-less audit_log rows (actor "openlog-updater").
type PostgresAuditor struct {
	Store *postgres.Store
	Log   *slog.Logger
}

// Audit implements Auditor.
func (a PostgresAuditor) Audit(ctx context.Context, action string, details map[string]any) {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	target, _ := details["to"].(string)
	err := a.Store.AddAuditEvent(actx, &auth.AuditEvent{
		ActorEmail: "openlog-updater", Action: action, TargetType: "openlog_release", TargetID: target, Details: details,
	})
	if err != nil && a.Log != nil {
		a.Log.Warn("cannot write audit event", "action", action, "err", err)
	}
}

type noAudit struct{}

func (noAudit) Audit(context.Context, string, map[string]any) {}
