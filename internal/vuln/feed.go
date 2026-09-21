package vuln

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The feed sync: OSV publishes one zip per ecosystem, and openlog downloads the ones its hosts actually
// run. The base URL is configurable so an installation without internet access can point at a mirror it
// fills itself — the catalog is public data, so mirroring it is a copy, not an integration.

// DefaultBaseURL is the OSV export bucket.
const DefaultBaseURL = "https://osv-vulnerabilities.storage.googleapis.com"

// SourceOSV names the feed in the catalog.
const SourceOSV = "osv"

// MaxArchiveBytes bounds one ecosystem export. Debian's is about 40 MB; the cap is what keeps a wrong URL
// (or a mirror serving something else) from becoming the installation's memory profile.
const MaxArchiveBytes = 512 << 20

// FetcherOptions configure a Fetcher.
type FetcherOptions struct {
	// BaseURL is where the exports are downloaded from (OPENLOG_VULN_FEED_URL).
	BaseURL string
	// HTTPClient is used for the downloads; nil means a client with Timeout.
	HTTPClient *http.Client
	// Timeout bounds one ecosystem download.
	Timeout time.Duration
	// UserAgent identifies openlog to the mirror.
	UserAgent string
	Log       *slog.Logger
}

// Fetcher downloads and parses ecosystem exports.
type Fetcher struct {
	o      FetcherOptions
	client *http.Client
}

// NewFetcher creates a fetcher.
func NewFetcher(o FetcherOptions) *Fetcher {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Minute
	}
	if o.UserAgent == "" {
		o.UserAgent = "openlog"
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: o.Timeout}
	}
	return &Fetcher{o: o, client: client}
}

// ArchiveURL is where one ecosystem's export lives. The ecosystem is part of the path, so it is escaped:
// "Rocky Linux:9" would otherwise become two path segments and a request for something else entirely.
func (f *Fetcher) ArchiveURL(ecosystem string) string {
	return strings.TrimSuffix(f.o.BaseURL, "/") + "/" + url.PathEscape(ecosystem) + "/all.zip"
}

// Fetch downloads and parses one ecosystem's advisories.
func (f *Fetcher) Fetch(ctx context.Context, ecosystem string) ([]Vulnerability, error) {
	target := f.ArchiveURL(ecosystem)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.o.UserAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", ecosystem, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// An ecosystem the feed does not publish (a release openlog derived that OSV does not have) is not
		// an error: it means there is nothing to match against, and saying so is the honest outcome.
		return nil, fmt.Errorf("%w: %s", ErrNoSuchEcosystem, ecosystem)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %s", ecosystem, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", ecosystem, err)
	}
	if int64(len(body)) > MaxArchiveBytes {
		return nil, fmt.Errorf("download %s: the archive is larger than %d bytes", ecosystem, int64(MaxArchiveBytes))
	}
	vulns, skipped, err := ParseOSVZip(body, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", ecosystem, err)
	}
	if skipped > 0 {
		f.o.Log.Warn("some advisories could not be parsed", "ecosystem", ecosystem, "skipped", skipped, "parsed", len(vulns))
	}
	return vulns, nil
}

// ErrNoSuchEcosystem is returned for an ecosystem the feed does not publish.
var ErrNoSuchEcosystem = errors.New("the feed has no such ecosystem")

// Sync downloads the ecosystems and stores them, recording the outcome of each one. A failing ecosystem
// does not stop the others: an installation with Debian and Alpine hosts must not lose both because one
// export was unavailable.
func Sync(ctx context.Context, f *Fetcher, catalog Catalog, ecosystems []string, log *slog.Logger) (int, error) {
	stored := 0
	var firstErr error
	for _, ecosystem := range ecosystems {
		if ctx.Err() != nil {
			return stored, ctx.Err()
		}
		start := time.Now()
		vulns, err := f.Fetch(ctx, ecosystem)
		if err != nil {
			if errors.Is(err, ErrNoSuchEcosystem) {
				log.Info("the vulnerability feed has no such ecosystem; nothing to match against",
					"ecosystem", ecosystem)
				_ = catalog.MarkSync(ctx, SourceOSV, ecosystem, time.Now().UTC(), 0, err)
				continue
			}
			log.Warn("cannot sync vulnerability feed", "ecosystem", ecosystem, "err", err)
			_ = catalog.MarkSync(ctx, SourceOSV, ecosystem, time.Now().UTC(), 0, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// The advisories are stored in batches: one transaction holding 60 000 of them would hold the
		// connection (and its locks) for minutes.
		const batchSize = 500
		count := 0
		for i := 0; i < len(vulns); i += batchSize {
			end := min(i+batchSize, len(vulns))
			if err := catalog.Upsert(ctx, SourceOSV, vulns[i:end]); err != nil {
				log.Warn("cannot store vulnerability advisories", "ecosystem", ecosystem, "err", err)
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			count += end - i
		}
		stored += count
		_ = catalog.MarkSync(ctx, SourceOSV, ecosystem, time.Now().UTC(), count, nil)
		log.Info("vulnerability feed synced", "ecosystem", ecosystem, "advisories", count,
			"duration", time.Since(start).Round(time.Second))
	}
	return stored, firstErr
}
