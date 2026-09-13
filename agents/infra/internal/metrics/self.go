package metrics

import (
	"sort"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

type commonKV = commonpb.KeyValue

// SelfTelemetry builds the openlog.agent.* metrics.
func SelfTelemetry(stats *selfmon.Stats, now time.Time) []*metricspb.Metric {
	snap := stats.Snapshot()
	start := stats.Start()

	var exportPts []otlputil.Point
	for _, sig := range []string{"metrics", "logs"} {
		for _, outcome := range []string{"sent", "buffered", "dropped"} {
			v := snap.ExportItems[selfmon.ExportKey{Signal: sig, Outcome: outcome}]
			exportPts = append(exportPts, otlputil.IntPoint(int64(v), otlputil.Str("signal", sig), otlputil.Str("outcome", outcome)))
		}
	}
	out := []*metricspb.Metric{
		otlputil.Sum("openlog.agent.export.items", "{item}", true, start, now, exportPts...),
		otlputil.Sum("openlog.agent.buffer.usage", "By", false, time.Time{}, now, otlputil.IntPoint(snap.BufferBytes)),
	}
	if snap.Interval > 0 {
		out = append(out, otlputil.Gauge("openlog.agent.collection.interval", "s", now, otlputil.DoublePoint(snap.Interval.Seconds())))
	}
	if len(snap.Durations) > 0 {
		var pts []otlputil.Point
		for _, k := range selfmon.SortedKeys(snap.Durations) {
			pts = append(pts, otlputil.DoublePoint(snap.Durations[k].Seconds(), otlputil.Str("collector", k)))
		}
		out = append(out, otlputil.Gauge("openlog.agent.collector.duration", "s", now, pts...))
	}
	if len(snap.PermissionDenied) > 0 {
		keys := selfmon.SortedKeys(snap.PermissionDenied)
		sort.Strings(keys)
		var pts []otlputil.Point
		for _, k := range keys {
			pts = append(pts, otlputil.IntPoint(int64(snap.PermissionDenied[k]), otlputil.Str("collector", k)))
		}
		out = append(out, otlputil.Sum("openlog.agent.permission_denied", "{error}", true, start, now, pts...))
	}
	if snap.UpdateState != "" {
		out = append(out, otlputil.Gauge("openlog.agent.update.state", "1", now,
			otlputil.IntPoint(1, otlputil.Str("state", snap.UpdateState))))
		var pts []otlputil.Point
		for _, result := range []string{"succeeded", "failed", "rolled_back"} {
			pts = append(pts, otlputil.IntPoint(int64(snap.UpdateAttempts[result]), otlputil.Str("result", result)))
		}
		out = append(out, otlputil.Sum("openlog.agent.update.attempts", "{attempt}", true, start, now, pts...))
	}
	return out
}
