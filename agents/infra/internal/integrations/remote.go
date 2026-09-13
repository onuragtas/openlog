package integrations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
)

// RemoteStateFile is the file in state_dir holding the last remote integration
// config (it contains plaintext passwords: mode 0600).
const RemoteStateFile = "integrations-remote.json"

// secretHash identifies a secret in instance signatures without revealing it.
func secretHash(s config.Secret) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// RemoteRevision is the integrations_config_revision reported in agent sync:
// the applied remote config revision, "" without one, or "disabled" when
// integrations.remote_config is false.
func (m *Manager) RemoteRevision() string {
	if !m.base.RemoteConfig {
		return config.RevisionDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.remote == nil {
		return ""
	}
	return m.remote.Revision
}

// SetRemote applies a remote integration config from agent sync: it is
// persisted, merged over config.yaml and applied to the running instances
// without a restart (instances whose settings changed are restarted). nil
// means "unchanged" and is ignored. It reports whether anything changed.
func (m *Manager) SetRemote(rc *config.RemoteIntegrations) bool {
	if rc == nil {
		return false
	}
	if !m.base.RemoteConfig {
		m.mu.Lock()
		first := m.remote == nil
		m.remote = &config.RemoteIntegrations{Revision: rc.Revision}
		m.mu.Unlock()
		if first {
			m.log.Info("remote integration config ignored (integrations.remote_config: false)", "revision", rc.Revision)
		}
		return false
	}
	if m.RemoteRevision() == rc.Revision {
		return false
	}
	if m.o.RemoteStatePath != "" {
		if err := saveRemote(m.o.RemoteStatePath, rc); err != nil {
			m.log.Warn("remote integration config not persisted; it is applied until the agent restarts", "error", err)
		}
	}
	m.applyRemote(rc)
	m.log.Info("remote integration config applied", "revision", rc.Revision, "items", len(rc.Items))
	m.mu.Lock()
	replay, services, ctrs := m.reconciled, m.lastServices, m.lastCtrs
	m.mu.Unlock()
	if replay {
		m.Reconcile(services, ctrs)
	}
	return true
}

func (m *Manager) applyRemote(rc *config.RemoteIntegrations) {
	eff, errs := m.base.ApplyRemote(rc.Items)
	for _, err := range errs {
		m.log.Warn("remote integration config item skipped", "error", err)
	}
	m.mu.Lock()
	m.cfg, m.remote = &eff, rc
	m.mu.Unlock()
}

// loadRemote applies the persisted remote config at startup so that it is
// effective before the backend answers.
func (m *Manager) loadRemote() {
	if m.o.RemoteStatePath == "" || !m.base.RemoteConfig {
		return
	}
	rc, err := loadRemote(m.o.RemoteStatePath)
	if err != nil {
		m.log.Warn("persisted remote integration config unreadable; waiting for the backend", "error", err)
		return
	}
	if rc != nil {
		m.applyRemote(rc)
		m.log.Info("persisted remote integration config loaded", "revision", rc.Revision, "items", len(rc.Items))
	}
}

func loadRemote(path string) (*config.RemoteIntegrations, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rc config.RemoteIntegrations
	if err := json.Unmarshal(b, &rc); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON", path) // the decoder error may quote a password
	}
	return &rc, nil
}

func saveRemote(path string, rc *config.RemoteIntegrations) error {
	b, err := json.Marshal(rc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	err = tmp.Chmod(0o600)
	if err == nil {
		_, err = tmp.Write(b)
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
	}
	return err
}
