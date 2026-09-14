package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// PodInfo is the cached metadata of one pod on the node.
type PodInfo struct {
	UID, Name, Namespace, Node string
	Owner                      Owner
	Labels                     []*commonpb.KeyValue // allowlisted k8s.pod.label.*
	// ContainerIDs maps container name → runtime container id (prefix removed); empty before the container started.
	ContainerIDs map[string]string
	StartTime    time.Time
}

// PodCache keeps the pods of one node from a list + watch of the API server (node mode, §7.2).
type PodCache struct {
	Client    *Client
	Node      string
	Resync    time.Duration
	Allowlist []string
	Log       *slog.Logger

	mu      sync.RWMutex
	pods    map[string]*PodInfo // uid → pod
	byCID   map[string]string   // container id → pod uid
	synced  bool
	lastErr string
}

// NewPodCache returns a cache for the pods of node.
func NewPodCache(c *Client, node string, resync time.Duration, allow []string, log *slog.Logger) *PodCache {
	if resync <= 0 {
		resync = 5 * time.Minute
	}
	return &PodCache{Client: c, Node: node, Resync: resync, Allowlist: allow, Log: log,
		pods: map[string]*PodInfo{}, byCID: map[string]string{}}
}

// Synced reports whether a first list succeeded.
func (p *PodCache) Synced() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.synced
}

// Pod returns the cached pod with uid (nil when unknown).
func (p *PodCache) Pod(uid string) *PodInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pods[uid]
}

// PodByContainerID returns the pod and container name of a runtime container id.
func (p *PodCache) PodByContainerID(id string) (*PodInfo, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	uid, ok := p.byCID[id]
	if !ok {
		return nil, ""
	}
	pod := p.pods[uid]
	if pod == nil {
		return nil, ""
	}
	for name, cid := range pod.ContainerIDs {
		if cid == id {
			return pod, name
		}
	}
	return pod, ""
}

func (p *PodCache) info(pod *Pod) *PodInfo {
	pi := &PodInfo{UID: pod.Metadata.UID, Name: pod.Metadata.Name, Namespace: pod.Metadata.Namespace, Node: pod.Spec.NodeName,
		Owner: (*Resolver)(nil).ResolveOwner(pod), Labels: LabelAttributes(pod.Metadata.Labels, p.Allowlist),
		ContainerIDs: map[string]string{}}
	if pod.Status.StartTime != nil {
		pi.StartTime = *pod.Status.StartTime
	}
	for _, cs := range pod.Status.ContainerStatuses {
		pi.ContainerIDs[cs.Name] = trimRuntimePrefix(cs.ContainerID)
	}
	return pi
}

// replace swaps the whole cache content (after a list).
func (p *PodCache) replace(pods []Pod) {
	m := make(map[string]*PodInfo, len(pods))
	for i := range pods {
		m[pods[i].Metadata.UID] = p.info(&pods[i])
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pods, p.byCID, p.synced = m, map[string]string{}, true
	for uid, pi := range m {
		for _, cid := range pi.ContainerIDs {
			if cid != "" {
				p.byCID[cid] = uid
			}
		}
	}
}

// apply handles one watch event.
func (p *PodCache) apply(ev WatchEvent) error {
	var pod Pod
	if err := json.Unmarshal(ev.Object, &pod); err != nil {
		return err
	}
	uid := pod.Metadata.UID
	p.mu.Lock()
	defer p.mu.Unlock()
	if old := p.pods[uid]; old != nil {
		for _, cid := range old.ContainerIDs {
			delete(p.byCID, cid)
		}
	}
	if ev.Type == "DELETED" {
		delete(p.pods, uid)
		return nil
	}
	pi := p.info(&pod)
	p.pods[uid] = pi
	for _, cid := range pi.ContainerIDs {
		if cid != "" {
			p.byCID[cid] = uid
		}
	}
	return nil
}

func (p *PodCache) query() url.Values {
	return url.Values{"fieldSelector": {"spec.nodeName=" + p.Node}}
}

// List lists the node's pods once and replaces the cache; it returns the list resource version.
func (p *PodCache) List(ctx context.Context) (string, error) {
	var list PodList
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := p.Client.Get(lctx, "/api/v1/pods", p.query(), &list); err != nil {
		return "", err
	}
	p.replace(list.Items)
	return list.Metadata.ResourceVersion, nil
}

// Run lists and watches until ctx ends; every Resync (±10%) and after errors it lists again.
func (p *PodCache) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		rv, err := p.List(ctx)
		if err != nil {
			p.logErr("pod list failed", err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		p.logErr("", nil)
		deadline := time.Now().Add(jitter(p.Resync))
		for ctx.Err() == nil && time.Now().Before(deadline) {
			wctx, cancel := context.WithDeadline(ctx, deadline)
			rv, err = p.Client.Watch(wctx, "/api/v1/pods", p.query(), rv, p.apply)
			cancel()
			if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				if !errors.Is(err, ErrGone) {
					p.logErr("pod watch failed", err)
					sleepCtx(ctx, time.Second)
				}
				break // relist
			}
		}
	}
}

func (p *PodCache) logErr(msg string, err error) {
	s := ""
	if err != nil {
		s = err.Error()
	}
	p.mu.Lock()
	changed := p.lastErr != s
	p.lastErr = s
	p.mu.Unlock()
	if changed && err != nil && p.Log != nil {
		p.Log.Warn(msg, "error", err)
	}
}

func jitter(d time.Duration) time.Duration {
	return d - d/10 + time.Duration(rand.Int64N(int64(d/5)+1))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// PodAttributes returns the pod identity, owner and label attributes of a pod.
func PodAttributes(pi *PodInfo) []*commonpb.KeyValue {
	out := []*commonpb.KeyValue{
		otlputil.Str(AttrNamespace, pi.Namespace), otlputil.Str(AttrPodName, pi.Name), otlputil.Str(AttrPodUID, pi.UID),
	}
	if pi.Node != "" {
		out = append(out, otlputil.Str(AttrNodeName, pi.Node))
	}
	out = append(out, pi.Owner.Attributes()...)
	return append(out, pi.Labels...)
}

// Enricher adds Kubernetes attributes to containers listed from the runtime (containers.Source.Enrich).
type Enricher struct {
	Cache       *PodCache
	NodeName    string
	ClusterName string
}

// Enrich sets Extra on every container of a Kubernetes pod: pod, owner and label attributes from the cache, the pod
// identity from CRI labels or the CRI log path otherwise.
func (e *Enricher) Enrich(cs []containers.Container) {
	for i := range cs {
		c := &cs[i]
		uid := c.Labels["io.kubernetes.pod.uid"]
		ns, name, cname := c.Labels["io.kubernetes.pod.namespace"], c.Labels["io.kubernetes.pod.name"], c.Labels["io.kubernetes.container.name"]
		if uid == "" || cname == "" {
			if lp, ok := ParsePodLogPath(c.LogPath); ok {
				uid, ns, name, cname = lp.PodUID, lp.Namespace, lp.Pod, lp.Container
			}
		}
		var pi *PodInfo
		if e.Cache != nil {
			if pod, n := e.Cache.PodByContainerID(c.ID); pod != nil {
				pi = pod
				if cname == "" {
					cname = n
				}
			} else if uid != "" {
				pi = e.Cache.Pod(uid)
			}
		}
		if pi == nil && uid == "" {
			continue // not a Kubernetes container
		}
		var extra []*commonpb.KeyValue
		if pi != nil {
			extra = PodAttributes(pi)
		} else {
			extra = []*commonpb.KeyValue{otlputil.Str(AttrNamespace, ns), otlputil.Str(AttrPodName, name), otlputil.Str(AttrPodUID, uid)}
			if e.NodeName != "" {
				extra = append(extra, otlputil.Str(AttrNodeName, e.NodeName))
			}
		}
		if cname != "" {
			extra = append(extra, otlputil.Str(AttrContainerName, cname))
		}
		if e.ClusterName != "" {
			extra = append(extra, otlputil.Str(AttrClusterName, e.ClusterName))
		}
		c.Extra = extra
	}
}
