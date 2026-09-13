package updater

import (
	"context"
	"fmt"
	"strings"
	"testing"

	lib "github.com/onuragtas/openlog/libs/release"
)

// manifestJSON renders a minimal valid manifest.
func manifestJSON(version, channel, minUpgradeFrom, image string) string {
	images := ""
	if image != "" {
		images = fmt.Sprintf(`,"images":{"openlog":%q}`, image)
	}
	return fmt.Sprintf(`{"schema":1,"product":"openlog","version":%q,"channel":%q,"released_at":"2026-09-01T00:00:00Z",
		"notes_url":"https://example.test/v%s","compatibility":{"min_upgrade_from":%q},"artifacts":[]%s}`,
		version, channel, version, minUpgradeFrom, images)
}

// fakeSource serves an index and manifests from memory.
type fakeSource struct {
	channels  map[string][]string // channel -> versions
	manifests map[string]string   // version -> manifest JSON
	indexErr  error
}

func manifestURL(v string) string { return "https://rel.test/v" + v + "/manifest.json" }

func (f *fakeSource) Index(context.Context, string) (*lib.Index, error) {
	if f.indexErr != nil {
		return nil, f.indexErr
	}
	idx := &lib.Index{Schema: 1, Product: "openlog", Channels: map[string][]lib.IndexEntry{}}
	for ch, vs := range f.channels {
		for _, v := range vs {
			idx.Channels[ch] = append(idx.Channels[ch], lib.IndexEntry{Version: v, ManifestURL: manifestURL(v)})
		}
	}
	return idx, nil
}

func (f *fakeSource) Manifest(_ context.Context, url string) (*lib.Manifest, error) {
	v := strings.TrimSuffix(strings.TrimPrefix(url, "https://rel.test/v"), "/manifest.json")
	s, ok := f.manifests[v]
	if !ok {
		return nil, fmt.Errorf("GET %s: 404", url)
	}
	return lib.ParseManifest([]byte(s))
}

type recordAudit struct{ actions []string }

func (r *recordAudit) Audit(_ context.Context, action string, _ map[string]any) {
	r.actions = append(r.actions, action)
}

type memStore struct {
	st    Status
	found bool
	saves int
}

func (m *memStore) Load(context.Context) (Status, bool, error) { return m.st, m.found, nil }
func (m *memStore) Save(_ context.Context, st Status) error {
	m.st, m.found = st, true
	m.saves++
	return nil
}

func mustVersion(t *testing.T, s string) lib.Version {
	t.Helper()
	v, err := lib.ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
