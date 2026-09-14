package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/agents/infra/internal/buffer"
	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/exporter"
	"github.com/onuragtas/openlog/agents/infra/internal/k8s"
	"github.com/onuragtas/openlog/agents/infra/internal/metrics"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/resource"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// ClusterAgent is the Kubernetes cluster-mode agent (semantic-conventions §7.4–7.5, D-071): one leader-elected
// replica lists the cluster state every kubernetes.cluster.interval and forwards Kubernetes events. It collects
// nothing from the host it runs on and has no update manager (container installs never update themselves).
type ClusterAgent struct {
	cfg     *config.Config
	version string
	log     *slog.Logger
	stats   *selfmon.Stats
	res     *resourcepb.Resource
	client  *k8s.Client
	coll    *k8s.ClusterCollector
	pipe    *pipeline
	buf     *buffer.Buffer

	identity, leaseNamespace string
}

// NewCluster builds the cluster agent. forExport false (-once) creates no buffer or exporter.
func NewCluster(cfg *config.Config, version string, log *slog.Logger, forExport bool) (*ClusterAgent, error) {
	kc := cfg.Kubernetes
	ua := resource.AgentName + "/" + version
	client, err := k8s.InCluster(os.Getenv, "", ua)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	uid, err := k8s.ClusterUID(ctx, client)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("k8s.cluster.uid (get namespaces/kube-system): %w", err)
	}
	attrs := []*commonpb.KeyValue{
		otlputil.Str(k8s.AttrClusterUID, uid),
		otlputil.Str("openlog.entity.type", k8s.EntityTypeCluster),
		otlputil.Str("openlog.agent.name", resource.AgentName),
		otlputil.Str("openlog.agent.version", version),
		otlputil.Str(k8s.AttrAgentMode, k8s.ModeCluster),
	}
	if kc.ClusterName != "" {
		attrs = append([]*commonpb.KeyValue{otlputil.Str(k8s.AttrClusterName, kc.ClusterName)}, attrs...)
	}
	for _, k := range selfmon.SortedKeys(cfg.Host.ExtraAttributes) {
		attrs = append(attrs, otlputil.Str(k, cfg.Host.ExtraAttributes[k]))
	}
	c := &ClusterAgent{cfg: cfg, version: version, log: log, stats: selfmon.New(time.Now()), client: client,
		res:  &resourcepb.Resource{Attributes: attrs},
		coll: &k8s.ClusterCollector{Client: client, MaxPods: kc.Cluster.MaxPods, Log: log.With("component", "kubernetes")},
	}
	c.identity = os.Getenv("OPENLOG_K8S_POD_NAME")
	if c.identity == "" {
		c.identity, _ = os.Hostname()
	}
	c.leaseNamespace = kc.Cluster.LeaseNamespace
	if c.leaseNamespace == "" {
		c.leaseNamespace = k8s.PodNamespace("")
	}
	if c.leaseNamespace == "" {
		c.leaseNamespace = "default"
	}
	c.stats.SetCollectionInterval(kc.Cluster.Interval.D())
	if forExport {
		c.buf, err = buffer.Open(cfg.Buffer.Dir, cfg.Buffer.MaxBytes)
		if err != nil {
			return nil, err
		}
		c.pipe = newPipeline(exporter.New(cfg.Endpoint, cfg.LicenseKey, version, cfg.Export.Timeout.D()), c.buf, c.stats, log)
	}
	return c, nil
}

func (c *ClusterAgent) scope() *commonpb.InstrumentationScope {
	return &commonpb.InstrumentationScope{Name: resource.AgentName, Version: c.version}
}

// CollectMetrics lists the cluster once.
func (c *ClusterAgent) CollectMetrics(ctx context.Context, now time.Time) (*metricspb.MetricsData, error) {
	start := time.Now()
	ms, err := c.coll.Collect(ctx, now)
	c.stats.SetCollectorDuration("kubernetes_cluster", time.Since(start))
	if err != nil {
		return nil, err
	}
	ms = append(ms, metrics.SelfTelemetry(c.stats, now)...)
	return &metricspb.MetricsData{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: c.res, ScopeMetrics: []*metricspb.ScopeMetrics{{Scope: c.scope(), Metrics: ms}},
	}}}, nil
}

// Once prints one cluster metrics collection as JSON (nothing is sent).
func (c *ClusterAgent) Once(ctx context.Context, w io.Writer) error {
	md, err := c.CollectMetrics(ctx, time.Now())
	if err != nil {
		return err
	}
	mj, err := protojson.Marshal(md)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Metrics json.RawMessage `json:"metrics"`
	}{mj})
}

func (c *ClusterAgent) enqueue(signal exporter.Signal, items int, msg proto.Message) {
	data, err := proto.Marshal(msg)
	if err != nil {
		c.log.Error("payload encoding failed", "signal", signal, "error", err)
		c.stats.AddExportItems(string(signal), "dropped", items)
		return
	}
	c.pipe.Enqueue(signal, items, data)
}

// lead collects while this replica holds the lease.
func (c *ClusterAgent) lead(ctx context.Context) {
	kc := c.cfg.Kubernetes.Cluster
	if kc.Events {
		ev := &k8s.Events{Client: c.client, Log: c.log.With("component", "kubernetes_events"),
			Emit: func(recs []*logspb.LogRecord) {
				ld := &logspb.LogsData{ResourceLogs: []*logspb.ResourceLogs{{Resource: c.res,
					ScopeLogs: []*logspb.ScopeLogs{{Scope: c.scope(), LogRecords: recs}}}}}
				for _, part := range exporter.SplitLogs(ld, c.cfg.Export.MaxRequestBytes) {
					c.enqueue(exporter.SignalLogs, countRecords(part), part)
				}
			}}
		go ev.Run(ctx)
	}
	ticker := time.NewTicker(kc.Interval.D())
	defer ticker.Stop()
	lastErr := ""
	for {
		if c.pipe.backlogged() {
			c.log.Warn("export backlog; skipping a cluster collection")
		} else {
			md, err := c.CollectMetrics(ctx, time.Now())
			switch {
			case err != nil && ctx.Err() == nil:
				if err.Error() != lastErr {
					c.log.Warn("kubernetes cluster collection failed", "error", err)
				}
				lastErr = err.Error()
			case err == nil:
				lastErr = ""
				c.enqueue(exporter.SignalMetrics, countPoints(md), md)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Run elects and collects until ctx ends, then flushes the export queue like the host agent.
func (c *ClusterAgent) Run(ctx context.Context) error {
	if c.pipe == nil {
		return errors.New("cluster agent built without export")
	}
	c.log.Info("kubernetes cluster agent started", "version", c.version, "cluster", c.cfg.Kubernetes.ClusterName,
		"lease", c.leaseNamespace+"/"+c.cfg.Kubernetes.Cluster.LeaseName, "identity", c.identity)
	exportCtx, cancelExport := context.WithCancel(context.Background())
	defer cancelExport()
	draining := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		c.pipe.run(exportCtx, draining)
	}()
	el := &k8s.Elector{Client: c.client, Namespace: c.leaseNamespace, Name: c.cfg.Kubernetes.Cluster.LeaseName, Identity: c.identity,
		Log: c.log.With("component", "leader_election")}
	el.Run(ctx, c.lead)

	close(draining)
	timer := time.NewTimer(shutdownTimeout)
	select {
	case <-exited:
	case <-timer.C:
		cancelExport()
		<-exited
	}
	timer.Stop()
	persisted := c.pipe.spillAll()
	c.log.Info("kubernetes cluster agent stopped", "persisted_payloads", persisted)
	return nil
}
