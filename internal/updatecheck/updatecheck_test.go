package updatecheck

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/internal/release"
)

type releaseServer struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	priv  ed25519.PrivateKey
}

func newReleaseServer(t *testing.T) (*releaseServer, ed25519.PublicKey) {
	pub, seed, err := lib.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := lib.PrivateKeyFromSeed(seed)
	pk, _ := lib.ParsePublicKey(pub)
	rs := &releaseServer{files: map[string][]byte{}, priv: priv}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		b, ok := rs.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(rs.Close)
	return rs, pk
}

// publish writes a signed file (and its .sig) at path.
func (rs *releaseServer) publish(path, body string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.files[path] = []byte(body)
	rs.files[path+".sig"] = []byte(lib.SignatureLine([]byte(body), rs.priv) + "\n")
}

func (rs *releaseServer) release(version, channel string) {
	rs.publish("/v"+version+"/manifest.json", fmt.Sprintf(`{"schema":1,"product":"openlog","version":%q,"channel":%q,
		"released_at":"2026-09-01T00:00:00Z","notes_url":"https://notes.test/v%s","compatibility":{},"artifacts":[]}`, version, channel, version))
}

func (rs *releaseServer) index(stable ...string) {
	var entries []string
	for _, v := range stable {
		entries = append(entries, fmt.Sprintf(`{"version":%q,"manifest_url":"%s/v%s/manifest.json"}`, v, rs.URL, v))
	}
	rs.publish("/index.json", `{"schema":1,"product":"openlog","generated_at":"2026-09-01T00:00:00Z","channels":{"stable":[`+strings.Join(entries, ",")+`]}}`)
}

type memStore struct {
	st    State
	found bool
	err   error
}

func (m *memStore) LoadCheck(context.Context) (State, bool, error) { return m.st, m.found, m.err }
func (m *memStore) SaveCheck(_ context.Context, st State) error {
	m.st, m.found = st, true
	return m.err
}

func TestCheckAndReader(t *testing.T) {
	rs, key := newReleaseServer(t)
	rs.release("0.9.0", "stable")
	rs.release("0.9.1", "stable")
	rs.index("0.9.0", "0.9.1")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{Enabled: true, IndexURL: rs.URL + "/index.json", Channel: "stable"}
	store := &memStore{}
	c := NewChecker(cfg, store, FetchLatest(release.NewFetcher([]ed25519.PublicKey{key})), log)

	st := c.Check(context.Background(), State{})
	if st.Error != "" || st.Latest == nil || st.Latest.Version != "0.9.1" || st.Latest.NotesURL != "https://notes.test/v0.9.1" || st.LastSuccessAt == nil {
		t.Fatalf("check: %+v", st)
	}
	_ = store.SaveCheck(context.Background(), st)

	info := NewReader(cfg, "0.9.0", store, nil).Info(context.Background())
	if info.UpdateCheck != StatusEnabled || info.LatestAvailable == nil || info.LatestAvailable.Version != "0.9.1" {
		t.Errorf("0.9.0 pod: %+v", info)
	}
	if info := NewReader(cfg, "0.9.1+abc", store, nil).Info(context.Background()); info.LatestAvailable != nil {
		t.Errorf("0.9.1 pod must not see an update: %+v", info.LatestAvailable)
	}
	if info := NewReader(Config{Channel: "stable"}, "0.9.0", store, nil).Info(context.Background()); info.UpdateCheck != StatusDisabled || info.LatestAvailable != nil {
		t.Errorf("disabled: %+v", info)
	}

	// A tampered index fails verification; the last good release is kept.
	rs.mu.Lock()
	rs.files["/index.json"] = []byte(strings.Replace(string(rs.files["/index.json"]), "0.9.1", "0.9.9", -1))
	rs.mu.Unlock()
	st2 := c.Check(context.Background(), st)
	if !errors.Is(errors.New(st2.Error), lib.ErrBadSignature) && !strings.Contains(st2.Error, "signature") {
		t.Errorf("tampered index accepted: %+v", st2)
	}
	if st2.Latest == nil || st2.Latest.Version != "0.9.1" {
		t.Errorf("last good release lost: %+v", st2)
	}
	_ = store.SaveCheck(context.Background(), st2)
	if info := NewReader(cfg, "0.9.0", store, nil).Info(context.Background()); info.UpdateCheck != StatusFailed || info.LatestAvailable == nil {
		t.Errorf("failed check: %+v", info)
	}

	// Without trusted keys nothing is fetched.
	nokeys := NewChecker(cfg, store, FetchLatest(release.NewFetcher(nil)), log).Check(context.Background(), State{})
	if !strings.Contains(nokeys.Error, "no trusted release keys") {
		t.Errorf("no keys: %+v", nokeys)
	}
	// A manifest signed by an unknown key is rejected.
	other, _ := newReleaseServer(t)
	other.release("0.9.2", "stable")
	rs.publish("/v0.9.2/manifest.json", string(other.files["/v0.9.2/manifest.json"]))
	rs.mu.Lock()
	rs.files["/v0.9.2/manifest.json.sig"] = other.files["/v0.9.2/manifest.json.sig"]
	rs.mu.Unlock()
	rs.index("0.9.1", "0.9.2")
	if st := c.Check(context.Background(), State{}); !strings.Contains(st.Error, "no signature from a trusted key") {
		t.Errorf("foreign signature: %+v", st)
	}
}

func TestNextDue(t *testing.T) {
	c := NewChecker(Config{Enabled: true, Channel: "stable"}, &memStore{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Now()
	if d := c.nextDue(State{}, false); d > 0 {
		t.Error("never checked must be due")
	}
	if d := c.nextDue(State{Channel: "stable", CheckedAt: now.Add(-time.Hour)}, true); d < 22*time.Hour {
		t.Errorf("successful check due in %s", d)
	}
	if d := c.nextDue(State{Channel: "stable", CheckedAt: now.Add(-time.Hour), Error: "x"}, true); d > 5*time.Hour || d < 4*time.Hour {
		t.Errorf("failed check due in %s", d)
	}
	if d := c.nextDue(State{Channel: "beta", CheckedAt: now}, true); d > 0 {
		t.Error("channel change must be due")
	}
}

// OPENLOG_UPDATE_CHECK_INTERVAL: a release published to a mirror shows up within the interval,
// and a failed check is not retried later than a successful one.
func TestNextDueCustomInterval(t *testing.T) {
	c := NewChecker(Config{Enabled: true, Channel: "stable", Interval: time.Minute}, &memStore{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Now()
	if d := c.nextDue(State{Channel: "stable", CheckedAt: now.Add(-30 * time.Second)}, true); d > 31*time.Second || d < 29*time.Second {
		t.Errorf("successful check due in %s, want ~30s", d)
	}
	if d := c.nextDue(State{Channel: "stable", CheckedAt: now.Add(-2 * time.Minute)}, true); d > 0 {
		t.Errorf("check older than the interval must be due, got %s", d)
	}
	if d := c.nextDue(State{Channel: "stable", CheckedAt: now, Error: "x"}, true); d > time.Minute {
		t.Errorf("failed check due in %s, want <= interval", d)
	}
	c = NewChecker(Config{Enabled: true, Channel: "stable", Interval: 48 * time.Hour}, &memStore{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if d := c.nextDue(State{Channel: "stable", CheckedAt: now, Error: "x"}, true); d > RetryInterval {
		t.Errorf("failed check due in %s, want <= %s", d, RetryInterval)
	}
}
