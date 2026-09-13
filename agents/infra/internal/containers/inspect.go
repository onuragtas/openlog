package containers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Inspection limits: GET /containers/{id}/json runs for new containers, on every state
// change and at most once per inspectRefresh for running containers (restart count and
// start time change without a visible state change when a container restarts quickly).
const (
	inspectRefresh    = time.Minute
	maxInspectPerList = 64
)

// Details are the fields of GET /containers/{id}/json that the list endpoint lacks.
type Details struct {
	StartedAt    time.Time
	FinishedAt   time.Time
	RestartCount int
	ExitCode     int
	Health       string // healthy, unhealthy, starting; "" without a healthcheck
	LogPath      string // host path of the json-file log; "" for other drivers
	LogDriver    string // json-file, local, journald, none, …
	Tty          bool   // log stream is not multiplexed
}

type inspectJSON struct {
	RestartCount int    `json:"RestartCount"`
	LogPath      string `json:"LogPath"`
	State        struct {
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		ExitCode   int    `json:"ExitCode"`
		Health     *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	HostConfig struct {
		LogConfig struct {
			Type string `json:"Type"`
		} `json:"LogConfig"`
	} `json:"HostConfig"`
	Config struct {
		Tty bool `json:"Tty"`
	} `json:"Config"`
}

// ParseInspect parses a GET /containers/{id}/json response.
func ParseInspect(body []byte) (Details, error) {
	var raw inspectJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return Details{}, fmt.Errorf("containers: decode docker inspect: %w", err)
	}
	d := Details{
		RestartCount: raw.RestartCount, ExitCode: raw.State.ExitCode, LogPath: raw.LogPath,
		LogDriver: raw.HostConfig.LogConfig.Type, Tty: raw.Config.Tty,
		StartedAt: parseDockerTime(raw.State.StartedAt), FinishedAt: parseDockerTime(raw.State.FinishedAt),
	}
	if raw.State.Health != nil && raw.State.Health.Status != "none" {
		d.Health = raw.State.Health.Status
	}
	return d, nil
}

// parseDockerTime parses Docker's RFC3339Nano times; the zero value "0001-01-01T00:00:00Z" becomes the zero time.
func parseDockerTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() <= 1 {
		return time.Time{}
	}
	return t.UTC()
}

// HealthFromStatus reads the health state from the list endpoint's Status text
// ("Up 2 hours (healthy)", "Up 3 seconds (health: starting)").
func HealthFromStatus(status string) string {
	switch {
	case strings.Contains(status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(status, "(healthy)"):
		return "healthy"
	case strings.Contains(status, "(health: starting)"):
		return "starting"
	}
	return ""
}

type detailEntry struct {
	d     Details
	state string
	at    time.Time
}

// Apply copies inspected details into the container.
func (c *Container) Apply(d Details) {
	if !d.StartedAt.IsZero() {
		c.StartedAt = d.StartedAt.Format(time.RFC3339Nano)
	}
	if !d.FinishedAt.IsZero() {
		c.FinishedAt = d.FinishedAt.Format(time.RFC3339Nano)
	}
	c.RestartCount = d.RestartCount
	if c.State == "exited" || c.State == "dead" {
		c.ExitCode = d.ExitCode
	}
	if c.Health == "" {
		c.Health = d.Health
	}
	c.LogPath, c.LogDriver, c.Tty = d.LogPath, d.LogDriver, d.Tty
	c.inspected = true
}

// inspect fills Details of the listed containers from the cache, inspecting stale entries.
// Failed inspections keep the list data. Called with s.mu held.
func (s *Source) inspect(ctx context.Context, sock string, cs []Container) {
	now := time.Now()
	if s.details == nil {
		s.details = map[string]detailEntry{}
	}
	live := make(map[string]bool, len(cs))
	n := 0
	for i := range cs {
		c := &cs[i]
		live[c.ID] = true
		e, ok := s.details[c.ID]
		stale := !ok || e.state != c.State || (c.State == "running" && now.Sub(e.at) >= inspectRefresh)
		if stale && n < maxInspectPerList {
			n++
			if body, err := s.get(ctx, sock, "/containers/"+c.ID+"/json"); err == nil {
				if d, err := ParseInspect(body); err == nil {
					e, ok = detailEntry{d: d, state: c.State, at: now}, true
					s.details[c.ID] = e
				}
			}
		}
		if ok {
			c.Apply(e.d)
		}
	}
	for id := range s.details {
		if !live[id] {
			delete(s.details, id)
		}
	}
}
