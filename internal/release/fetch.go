package release

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/internal/version"
)

// ErrNoTrustedKeys is returned when the build has no compiled-in keys and no key file
// (docs/contracts/releases-updates.md §2: such binaries cannot advertise or apply updates).
var ErrNoTrustedKeys = errors.New("no trusted release keys")

// maxDocument bounds index, manifest and signature downloads.
const maxDocument = 4 << 20

// Fetcher downloads and verifies signed release documents. The signature of <url> is <url>.sig.
type Fetcher struct {
	Client *http.Client
	Keys   []ed25519.PublicKey
}

// NewFetcher returns a Fetcher with a 30 s HTTP timeout.
func NewFetcher(keys []ed25519.PublicKey) *Fetcher {
	return &Fetcher{Client: &http.Client{Timeout: 30 * time.Second}, Keys: keys}
}

// Index fetches and verifies index.json.
func (f *Fetcher) Index(ctx context.Context, url string) (*lib.Index, error) {
	data, sig, err := f.signed(ctx, url)
	if err != nil {
		return nil, err
	}
	idx, _, err := lib.VerifyIndex(data, sig, f.Keys)
	if err != nil {
		return nil, fmt.Errorf("index %s: %w", url, err)
	}
	return idx, nil
}

// Manifest fetches and verifies a manifest.json.
func (f *Fetcher) Manifest(ctx context.Context, url string) (*lib.Manifest, error) {
	data, sig, err := f.signed(ctx, url)
	if err != nil {
		return nil, err
	}
	m, _, err := lib.VerifyManifest(data, sig, f.Keys)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", url, err)
	}
	return m, nil
}

func (f *Fetcher) signed(ctx context.Context, url string) (data, sig []byte, err error) {
	if len(f.Keys) == 0 {
		return nil, nil, ErrNoTrustedKeys
	}
	if data, err = f.get(ctx, url); err != nil {
		return nil, nil, err
	}
	if sig, err = f.get(ctx, url+".sig"); err != nil {
		return nil, nil, err
	}
	return data, sig, nil
}

func (f *Fetcher) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "openlog/"+version.String())
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if len(b) > maxDocument {
		return nil, fmt.Errorf("GET %s: document larger than %d bytes", url, maxDocument)
	}
	return b, nil
}
