// Package docker implements the Docker Engine integration. The OpenTelemetry
// Collector docker_stats receiver only defines container.* metrics, which the
// agent already produces from cgroups (semantic-conventions §2); there are no
// OTel engine-level docker.* metric names, so this integration emits no
// metrics of its own. It reports whether the Docker Engine API is usable
// (container names/images for container metrics and inventory).
package docker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
)

// Integration is the Docker Engine integration.
type Integration struct {
	// Source is the agent's Docker API source; nil when containers.enabled is false.
	Source *containers.Source
	Socket string
}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationDocker }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{NoEndpoint: true}
}

// Hint implements integrations.Integration.
func (i Integration) Hint(*integrations.Instance) string {
	return `# The agent user needs access to the Docker socket (` + i.Socket + `), e.g.
#   sudo usermod -aG docker openlog-agent   # docker group access is root-equivalent
containers:
  enabled: true
  docker_socket: ` + i.Socket
}

// New implements integrations.Integration.
func (i Integration) New(*integrations.Instance, integrations.Endpoint) (integrations.Collector, error) {
	if i.Source == nil {
		return nil, integrations.NotAvailable("container collection is disabled (containers.enabled: false)")
	}
	return collector{src: i.Source}, nil
}

// source is the part of *containers.Source the collector uses.
type source interface {
	List(ctx context.Context, maxAge time.Duration) ([]containers.Container, error)
	DockerErr() error
}

type collector struct{ src source }

func (collector) Close() {}

// Collect refreshes the (cached) container listing and reports the Docker
// Engine API state of it. The listing merges CRI runtimes (containerd, CRI-O)
// and succeeds when only a CRI runtime answers, so the state comes from
// DockerErr, not from the listing error.
func (c collector) Collect(ctx context.Context, _ *integrations.Batch) error {
	_, listErr := c.src.List(ctx, 15*time.Second)
	err := c.src.DockerErr()
	if err == nil && listErr != nil && !errors.Is(listErr, containers.ErrNoRuntime) {
		err = listErr // defensive: no Docker result recorded
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, containers.ErrNoRuntime):
		return fmt.Errorf("docker socket not found")
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return integrations.NeedsConfiguration("permission denied on the docker socket", false)
	}
	return err
}
