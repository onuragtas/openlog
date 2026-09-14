package apm

import (
	"sort"
	"time"
)

// Deployment detection (apm.md §12): a deployment is a service.version that starts reporting spans for a service
// after another version did. It is derived at query time from apm_service_versions_1m (spans per version per minute).

// DeploymentGap is how long a version must have been absent for its reappearance to count as a new deployment
// (shorter gaps are rolling updates, canaries or quiet minutes of the same rollout).
const DeploymentGap = 30 * time.Minute

// DeploymentLookback is how far before the requested range versions are read to know the previous version.
const DeploymentLookback = 24 * time.Hour

// VersionMinute is the number of spans of one version in one minute.
type VersionMinute struct {
	Minute  time.Time
	Version string
	Spans   uint64
}

// Deployment is one detected version change.
type Deployment struct {
	Time            time.Time
	Version         string
	PreviousVersion string
	// Initial: no other version was seen before (the first version of the service within retention).
	Initial bool
	// Rollback: the version had already reported before DeploymentGap ago (a return to an older version).
	Rollback bool
}

// DetectDeployments returns the deployments starting at or after from, oldest first. rows may start before from
// (up to DeploymentLookback) and need not be sorted. firstSeen holds the first time each version was seen within
// retention (min(first_seen)); versions missing from it are treated as first seen at their first row.
// Spans without service.version (Version "") never form a deployment.
func DetectDeployments(rows []VersionMinute, firstSeen map[string]time.Time, from time.Time, gap time.Duration) []Deployment {
	if gap <= 0 {
		gap = DeploymentGap
	}
	sorted := append([]VersionMinute(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].Minute.Equal(sorted[j].Minute) {
			return sorted[i].Minute.Before(sorted[j].Minute)
		}
		if sorted[i].Spans != sorted[j].Spans {
			return sorted[i].Spans > sorted[j].Spans
		}
		return sorted[i].Version < sorted[j].Version
	})
	lastSeen := map[string]time.Time{}
	out := []Deployment{}
	for i := 0; i < len(sorted); {
		m := sorted[i].Minute
		j := i
		for j < len(sorted) && sorted[j].Minute.Equal(m) {
			j++
		}
		minute := sorted[i:j]
		for _, r := range minute {
			v := r.Version
			if v == "" || r.Spans == 0 {
				continue
			}
			if last, ok := lastSeen[v]; ok && m.Sub(last) <= gap {
				continue // same rollout, or still running
			}
			prev, prevAt := latestOther(lastSeen, v)
			if last, ok := lastSeen[v]; ok && !last.Before(prevAt) {
				continue // the same version restarted after a pause: no other version in between
			}
			first, known := firstSeen[v]
			d := Deployment{Time: m, Version: v, PreviousVersion: prev}
			switch {
			case prev == "":
				if known && first.Before(m.Add(-time.Minute)) {
					continue // reported before the lookback: not a new version
				}
				d.Initial = true
			default:
				d.Rollback = known && first.Before(m.Add(-gap))
			}
			if !m.Before(from) {
				out = append(out, d)
			}
		}
		for _, r := range minute {
			if r.Version != "" && r.Spans > 0 {
				lastSeen[r.Version] = m
			}
		}
		i = j
	}
	return out
}

// latestOther returns the most recently seen version other than v.
func latestOther(lastSeen map[string]time.Time, v string) (string, time.Time) {
	var best string
	var at time.Time
	for ver, t := range lastSeen {
		if ver == v {
			continue
		}
		if best == "" || t.After(at) || (t.Equal(at) && ver < best) {
			best, at = ver, t
		}
	}
	return best, at
}
