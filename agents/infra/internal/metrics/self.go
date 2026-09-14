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
	signals := []string{"metrics", "logs"}
	if snap.PHP != nil {
		signals = append(signals, "traces") // only agents that forward spans have trace payloads
	}
	for _, sig := range signals {
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
	if len(snap.IntegrationCollections) > 0 {
		var cols, errs, durs []otlputil.Point
		for _, k := range selfmon.SortedKeys(snap.IntegrationCollections) {
			attr := otlputil.Str("integration", k)
			cols = append(cols, otlputil.IntPoint(int64(snap.IntegrationCollections[k]), attr))
			errs = append(errs, otlputil.IntPoint(int64(snap.IntegrationErrors[k]), attr))
			durs = append(durs, otlputil.DoublePoint(snap.IntegrationDurations[k].Seconds(), attr))
		}
		out = append(out,
			otlputil.Sum("openlog.agent.integration.collections", "{collection}", true, start, now, cols...),
			otlputil.Sum("openlog.agent.integration.errors", "{error}", true, start, now, errs...),
			otlputil.Gauge("openlog.agent.integration.duration", "s", now, durs...),
		)
	}
	if p := snap.PHP; p != nil {
		var pts []otlputil.Point
		for _, result := range selfmon.PHPMessageResults {
			pts = append(pts, otlputil.IntPoint(int64(p.Messages[result]), otlputil.Str("result", result)))
		}
		out = append(out,
			otlputil.Sum("openlog.agent.php.messages", "{message}", true, start, now, pts...),
			otlputil.Sum("openlog.agent.php.spans", "{span}", true, start, now, otlputil.IntPoint(int64(p.Spans))),
			otlputil.Sum("openlog.agent.php.reassembly_timeouts", "{trace}", true, start, now, otlputil.IntPoint(int64(p.ReassemblyTimeouts))),
			otlputil.Gauge("openlog.agent.php.pending_traces", "{trace}", now, otlputil.IntPoint(p.PendingTraces)),
		)
	}
	if len(snap.PHPAgentOps) > 0 {
		keys := make([]selfmon.PHPAgentOp, 0, len(snap.PHPAgentOps))
		for k := range snap.PHPAgentOps {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return keys[i].Operation+"\x00"+keys[i].Result < keys[j].Operation+"\x00"+keys[j].Result
		})
		var pts []otlputil.Point
		for _, k := range keys {
			pts = append(pts, otlputil.IntPoint(int64(snap.PHPAgentOps[k]), otlputil.Str("operation", k.Operation), otlputil.Str("result", k.Result)))
		}
		out = append(out, otlputil.Sum("openlog.agent.php_agent.operations", "{operation}", true, start, now, pts...))
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
