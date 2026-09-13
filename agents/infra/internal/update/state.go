package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// State is <state_dir>/update-state.json. The first five fields are the contract's
// {previous, candidate, attempts, staged_at, confirmed}; the rest carry what is reported in sync
// requests and what is needed to avoid retrying a failed instruction.
type State struct {
	Previous  string    `json:"previous"`
	Candidate string    `json:"candidate"`
	Attempts  int       `json:"attempts"`
	StagedAt  time.Time `json:"staged_at"`
	Confirmed bool      `json:"confirmed"`

	Status      string    `json:"state"`
	FromVersion string    `json:"from_version,omitempty"`
	ToVersion   string    `json:"to_version,omitempty"`
	Error       string    `json:"error,omitempty"`
	ChangedAt   time.Time `json:"changed_at"`
	Action      string    `json:"action,omitempty"`
	RolloutID   string    `json:"rollout_id,omitempty"`
	// Counted is true once a terminal result was added to openlog.agent.update.attempts. A
	// result produced right before an exit (rollback at startup) is counted by the next process.
	Counted bool `json:"counted,omitempty"`
}

// terminal reports whether the status is a final result of an attempt.
func terminal(status string) bool {
	return status == StateSucceeded || status == StateFailed || status == StateRolledBack
}

// LoadState reads the state file. A missing file yields an idle state.
func LoadState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{Status: StateIdle}, nil
	}
	if err != nil {
		return State{Status: StateIdle}, fmt.Errorf("update state: %w", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{Status: StateIdle}, fmt.Errorf("update state %s: %w", path, err)
	}
	if st.Status == "" {
		st.Status = StateIdle
	}
	return st, nil
}

// SaveState writes the state file durably (temp file, fsync, rename, fsync dir).
func SaveState(path string, st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	return writeFileAtomic(path, append(b, '\n'), 0o640)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
