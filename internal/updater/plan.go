package updater

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/internal/updatemsg"
)

// ImageComponent is the key of the backend image in manifest.images.
const ImageComponent = "openlog"

// maxCandidates bounds how many manifests one planning pass fetches.
const maxCandidates = 10

// Target is the release an update moves to.
type Target struct {
	Version     lib.Version
	Manifest    *lib.Manifest
	ManifestURL string
	// Image is the image reference to run (manifest digest, repository possibly replaced).
	Image string
}

// ManifestFetcher fetches and verifies one manifest.
type ManifestFetcher func(ctx context.Context, url string) (*lib.Manifest, error)

// SelectTarget picks the newest release of channel that is newer than current, has not failed
// before on this installation, has an openlog image and allows upgrading from current
// (compatibility.min_upgrade_from). When the newest release requires an intermediate version,
// the newest release that current can upgrade to is chosen, so repeated runs walk the chain.
// A nil target comes with a human-readable reason.
func SelectTarget(ctx context.Context, idx *lib.Index, channel string, current lib.Version, failed []string,
	imageRepo string, fetch ManifestFetcher) (*Target, string, error) {
	var entries []lib.IndexEntry
	switch channel {
	case lib.ChannelStable:
		entries = idx.Channels[lib.ChannelStable]
	case lib.ChannelBeta:
		entries = append(append(entries, idx.Channels[lib.ChannelBeta]...), idx.Channels[lib.ChannelStable]...)
	default:
		return nil, "", fmt.Errorf("unknown channel %q", channel)
	}
	type cand struct {
		v lib.Version
		e lib.IndexEntry
	}
	var cands []cand
	seen := map[string]bool{}
	for _, e := range entries {
		v, err := lib.ParseVersion(e.Version)
		if err != nil || !current.Less(v) || seen[v.String()] {
			continue
		}
		seen[v.String()] = true
		cands = append(cands, cand{v, e})
	}
	if len(cands) == 0 {
		return nil, updatemsg.Format(updatemsg.UpToDateNewest, updatemsg.Params{"version": current.String(), "channel": channel}), nil
	}
	sort.Slice(cands, func(i, j int) bool { return lib.Compare(cands[i].v, cands[j].v) > 0 })
	var skipped []string
	for i, c := range cands {
		if i >= maxCandidates {
			break
		}
		if slices.Contains(failed, c.v.String()) {
			skipped = append(skipped, c.v.String()+": failed before on this installation")
			continue
		}
		m, err := fetch(ctx, c.e.ManifestURL)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", c.v, err))
			continue
		}
		if lib.Compare(m.ParsedVersion(), c.v) != 0 {
			skipped = append(skipped, fmt.Sprintf("%s: manifest is version %s", c.v, m.Version))
			continue
		}
		if channel == lib.ChannelStable && m.Channel != lib.ChannelStable {
			skipped = append(skipped, fmt.Sprintf("%s: manifest channel %s", c.v, m.Channel))
			continue
		}
		if min := m.Compatibility.MinUpgradeFrom; min != "" {
			if mv, err := lib.ParseVersion(min); err != nil || current.Less(mv) {
				skipped = append(skipped, fmt.Sprintf("%s: requires upgrading from >= %s", c.v, min))
				continue
			}
		}
		image := m.Images[ImageComponent]
		if image == "" {
			skipped = append(skipped, fmt.Sprintf("%s: manifest has no %q image", c.v, ImageComponent))
			continue
		}
		return &Target{Version: c.v, Manifest: m, ManifestURL: c.e.ManifestURL, Image: rewriteRepository(image, imageRepo)}, "", nil
	}
	return nil, updatemsg.Format(updatemsg.UpToDateNoEligible, updatemsg.Params{"version": current.String(), "details": strings.Join(skipped, "; ")}), nil
}

// rewriteRepository replaces the repository of ref ("repo@sha256:…" or "repo:tag") by repo.
func rewriteRepository(ref, repo string) string {
	if repo == "" {
		return ref
	}
	if i := strings.Index(ref, "@"); i >= 0 {
		return repo + ref[i:]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return repo + ref[i:]
	}
	return repo
}

// sameVersion compares product versions ignoring build metadata.
func sameVersion(a, b string) bool {
	va, err1 := lib.ParseVersion(a)
	vb, err2 := lib.ParseVersion(b)
	if err1 != nil || err2 != nil {
		return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
	}
	return lib.Compare(va, vb) == 0
}
