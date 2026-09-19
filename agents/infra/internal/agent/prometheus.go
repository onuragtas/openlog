package agent

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/k8s"
	"github.com/onuragtas/openlog/agents/infra/internal/promscrape"
	"github.com/onuragtas/openlog/agents/infra/internal/resource"
)

// setupPrometheus creates the scrape manager (semantic-conventions §6.9). It must run after the container
// source and the Kubernetes node support exist.
func (a *Agent) setupPrometheus() *promscrape.Manager {
	pc := &a.cfg.Prometheus
	if !pc.Enabled {
		return nil
	}
	m := promscrape.NewManager(promscrape.Options{
		Config: pc, Resource: a.res, AgentName: resource.AgentName, AgentVersion: a.version,
		Log: a.log.With("component", "prometheus"), Stats: a.stats,
	})
	if (pc.Containers && a.ctr != nil) || (pc.KubernetesPods && a.k8s != nil) {
		var lastProblems []string
		m.SetDiscover(func(ctx context.Context) []promscrape.Target {
			targets, problems := a.scrapeTargets(ctx)
			if !slices.Equal(problems, lastProblems) && len(problems) > 0 {
				a.log.Warn("prometheus targets skipped", "component", "prometheus", "problems", strings.Join(problems, "; "))
			}
			lastProblems = problems
			return targets
		})
	}
	return m
}

// scrapeTargets lists the pods and containers that opted in to scraping.
func (a *Agent) scrapeTargets(ctx context.Context) ([]promscrape.Target, []string) {
	pc := &a.cfg.Prometheus
	var out []promscrape.Target
	var problems []string
	pods := pc.KubernetesPods && a.k8s != nil
	if pods {
		var eps []promscrape.PodEndpoint
		for _, pi := range a.k8s.cache.Pods() {
			if pi.Scrape == nil || pi.Phase != "Running" {
				continue
			}
			eps = append(eps, promscrape.PodEndpoint{UID: pi.UID, Name: pi.Name, Namespace: pi.Namespace, IP: pi.IP,
				Annotations: pi.Scrape, ContainerPorts: pi.TCPPorts, Attrs: k8s.PodAttributes(pi)})
		}
		t, p := promscrape.PodTargets(eps)
		out, problems = append(out, t...), append(problems, p...)
	}
	if pc.Containers && a.ctr != nil {
		cs, err := a.ctr.List(ctx, a.interval/2)
		switch {
		case errors.Is(err, containers.ErrNoRuntime):
		case err != nil:
			problems = append(problems, "containers: "+err.Error())
		default:
			t, p := promscrape.ContainerTargets(cs, pods)
			out, problems = append(out, t...), append(problems, p...)
		}
	}
	return out, problems
}
