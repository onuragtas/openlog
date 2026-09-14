package k8s

import (
	"context"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Kubelet /stats/summary (k8s.io/kubelet/pkg/apis/stats/v1alpha1), only the fields used.

type cpuStats struct {
	UsageNanoCores       *uint64 `json:"usageNanoCores"`
	UsageCoreNanoSeconds *uint64 `json:"usageCoreNanoSeconds"`
}

type memoryStats struct {
	AvailableBytes  *uint64 `json:"availableBytes"`
	UsageBytes      *uint64 `json:"usageBytes"`
	WorkingSetBytes *uint64 `json:"workingSetBytes"`
	RSSBytes        *uint64 `json:"rssBytes"`
}

type interfaceStats struct {
	Name     string  `json:"name"`
	RxBytes  *uint64 `json:"rxBytes"`
	RxErrors *uint64 `json:"rxErrors"`
	TxBytes  *uint64 `json:"txBytes"`
	TxErrors *uint64 `json:"txErrors"`
}

type networkStats struct {
	interfaceStats
	Interfaces []interfaceStats `json:"interfaces"`
}

type fsStats struct {
	AvailableBytes *uint64 `json:"availableBytes"`
	CapacityBytes  *uint64 `json:"capacityBytes"`
	UsedBytes      *uint64 `json:"usedBytes"`
}

// Summary is the kubelet stats summary.
type Summary struct {
	Node struct {
		NodeName string        `json:"nodeName"`
		CPU      *cpuStats     `json:"cpu"`
		Memory   *memoryStats  `json:"memory"`
		Network  *networkStats `json:"network"`
		Fs       *fsStats      `json:"fs"`
	} `json:"node"`
	Pods []struct {
		PodRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			UID       string `json:"uid"`
		} `json:"podRef"`
		Containers []struct {
			Name   string       `json:"name"`
			CPU    *cpuStats    `json:"cpu"`
			Memory *memoryStats `json:"memory"`
			Rootfs *fsStats     `json:"rootfs"`
			Logs   *fsStats     `json:"logs"`
		} `json:"containers"`
		CPU              *cpuStats     `json:"cpu"`
		Memory           *memoryStats  `json:"memory"`
		Network          *networkStats `json:"network"`
		EphemeralStorage *fsStats      `json:"ephemeral-storage"`
	} `json:"pods"`
}

// Kubelet collects kubelet stats summary metrics (§7.3). It implements the metrics collector interface.
type Kubelet struct {
	Client   *Client
	NodeName string
	Pods     *PodCache // owner, label and container id attributes; may be nil
	Timeout  time.Duration
}

// Name implements metrics.Collector.
func (k *Kubelet) Name() string { return "kubelet" }

// Collect implements metrics.Collector.
func (k *Kubelet) Collect(now time.Time) ([]*metricspb.Metric, error) {
	timeout := k.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var s Summary
	if err := k.Client.Get(ctx, "/stats/summary", nil, &s); err != nil {
		return nil, err
	}
	return k.Metrics(&s, now), nil
}

type builder struct {
	gauges map[string]*gaugeAcc
	order  []string
	now    time.Time
}

type gaugeAcc struct {
	unit   string
	sum    bool // cumulative monotonic sum
	points []otlputil.Point
}

func newBuilder(now time.Time) *builder { return &builder{gauges: map[string]*gaugeAcc{}, now: now} }

func (b *builder) add(name, unit string, sum bool, p otlputil.Point) {
	g := b.gauges[name]
	if g == nil {
		g = &gaugeAcc{unit: unit, sum: sum}
		b.gauges[name] = g
		b.order = append(b.order, name)
	}
	g.points = append(g.points, p)
}

func (b *builder) gauge(name, unit string, v float64, attrs []*commonpb.KeyValue) {
	b.add(name, unit, false, otlputil.DoublePoint(v, attrs...))
}

func (b *builder) gaugeInt(name, unit string, v int64, attrs []*commonpb.KeyValue) {
	b.add(name, unit, false, otlputil.IntPoint(v, attrs...))
}

func (b *builder) counter(name, unit string, v float64, attrs []*commonpb.KeyValue) {
	b.add(name, unit, true, otlputil.DoublePoint(v, attrs...))
}

func (b *builder) metrics() []*metricspb.Metric {
	out := make([]*metricspb.Metric, 0, len(b.order))
	for _, name := range b.order {
		g := b.gauges[name]
		if g.sum {
			out = append(out, otlputil.Sum(name, g.unit, true, time.Time{}, b.now, g.points...))
		} else {
			out = append(out, otlputil.Gauge(name, g.unit, b.now, g.points...))
		}
	}
	return out
}

func withKV(base []*commonpb.KeyValue, kv ...*commonpb.KeyValue) []*commonpb.KeyValue {
	out := make([]*commonpb.KeyValue, 0, len(base)+len(kv))
	return append(append(out, base...), kv...)
}

func (b *builder) cpu(prefix string, c *cpuStats, attrs []*commonpb.KeyValue) {
	if c == nil {
		return
	}
	if c.UsageNanoCores != nil {
		b.gauge(prefix+".cpu.usage", "{cpu}", float64(*c.UsageNanoCores)/1e9, attrs)
	}
	if c.UsageCoreNanoSeconds != nil {
		b.counter(prefix+".cpu.time", "s", float64(*c.UsageCoreNanoSeconds)/1e9, attrs)
	}
}

func (b *builder) memory(prefix string, m *memoryStats, attrs []*commonpb.KeyValue, available bool) {
	if m == nil {
		return
	}
	for _, f := range []struct {
		name string
		v    *uint64
	}{{".memory.usage", m.UsageBytes}, {".memory.working_set", m.WorkingSetBytes}, {".memory.rss", m.RSSBytes}} {
		if f.v != nil {
			b.gaugeInt(prefix+f.name, "By", int64(*f.v), attrs)
		}
	}
	if available && m.AvailableBytes != nil {
		b.gaugeInt(prefix+".memory.available", "By", int64(*m.AvailableBytes), attrs)
	}
}

func (b *builder) fs(name string, f *fsStats, attrs []*commonpb.KeyValue) {
	if f == nil {
		return
	}
	if f.UsedBytes != nil {
		b.gaugeInt(name+".usage", "By", int64(*f.UsedBytes), attrs)
	}
	if f.CapacityBytes != nil {
		b.gaugeInt(name+".capacity", "By", int64(*f.CapacityBytes), attrs)
	}
	if f.AvailableBytes != nil {
		b.gaugeInt(name+".available", "By", int64(*f.AvailableBytes), attrs)
	}
}

func (b *builder) network(prefix string, n *networkStats, attrs []*commonpb.KeyValue, errs bool) {
	if n == nil {
		return
	}
	ifaces := n.Interfaces
	if len(ifaces) == 0 && (n.RxBytes != nil || n.TxBytes != nil) {
		ifaces = []interfaceStats{n.interfaceStats}
	}
	if len(ifaces) == 0 {
		return
	}
	var rx, tx, rxe, txe uint64
	for _, i := range ifaces {
		rx += deref(i.RxBytes)
		tx += deref(i.TxBytes)
		rxe += deref(i.RxErrors)
		txe += deref(i.TxErrors)
	}
	b.counter(prefix+".network.io", "By", float64(rx), withKV(attrs, otlputil.Str("network.io.direction", "receive")))
	b.counter(prefix+".network.io", "By", float64(tx), withKV(attrs, otlputil.Str("network.io.direction", "transmit")))
	if errs {
		b.counter(prefix+".network.errors", "{error}", float64(rxe), withKV(attrs, otlputil.Str("network.io.direction", "receive")))
		b.counter(prefix+".network.errors", "{error}", float64(txe), withKV(attrs, otlputil.Str("network.io.direction", "transmit")))
	}
}

func deref(v *uint64) uint64 {
	if v == nil {
		return 0
	}
	return *v
}

// Metrics converts a summary into metrics.
func (k *Kubelet) Metrics(s *Summary, now time.Time) []*metricspb.Metric {
	b := newBuilder(now)
	node := k.NodeName
	if node == "" {
		node = s.Node.NodeName
	}
	nodeAttrs := []*commonpb.KeyValue{otlputil.Str(AttrNodeName, node)}
	b.cpu("k8s.node", s.Node.CPU, nodeAttrs)
	b.memory("k8s.node", s.Node.Memory, nodeAttrs, true)
	b.network("k8s.node", s.Node.Network, nodeAttrs, false)
	b.fs("k8s.node.filesystem", s.Node.Fs, nodeAttrs)
	for _, p := range s.Pods {
		var pi *PodInfo
		if k.Pods != nil {
			pi = k.Pods.Pod(p.PodRef.UID)
		}
		var attrs []*commonpb.KeyValue
		if pi != nil {
			attrs = PodAttributes(pi)
		} else {
			attrs = []*commonpb.KeyValue{otlputil.Str(AttrNamespace, p.PodRef.Namespace), otlputil.Str(AttrPodName, p.PodRef.Name),
				otlputil.Str(AttrPodUID, p.PodRef.UID), otlputil.Str(AttrNodeName, node)}
		}
		b.cpu("k8s.pod", p.CPU, attrs)
		b.memory("k8s.pod", p.Memory, attrs, false)
		b.network("k8s.pod", p.Network, attrs, true)
		b.fs("k8s.pod.filesystem", p.EphemeralStorage, attrs)
		for _, c := range p.Containers {
			cattrs := withKV(attrs, otlputil.Str(AttrContainerName, c.Name))
			if pi != nil && pi.ContainerIDs[c.Name] != "" {
				cattrs = append(cattrs, otlputil.Str(AttrContainerID, pi.ContainerIDs[c.Name]))
			}
			b.cpu("k8s.container", c.CPU, cattrs)
			b.memory("k8s.container", c.Memory, cattrs, false)
			if c.Rootfs != nil || c.Logs != nil {
				var used uint64
				if c.Rootfs != nil {
					used += deref(c.Rootfs.UsedBytes)
				}
				if c.Logs != nil {
					used += deref(c.Logs.UsedBytes)
				}
				b.gaugeInt("k8s.container.filesystem.usage", "By", int64(used), cattrs)
			}
		}
	}
	return b.metrics()
}
