// Command capture is a minimal OTLP/HTTP (protobuf) capture server for the Kubernetes live test
// (agents/infra/test/k8s/run.sh). It accepts /v1/metrics and /v1/logs, and serves a summary on GET /summary:
// metric names with their data point attribute keys and sample values, resource attributes per agent mode, and
// log records with k8s attributes, so the test can assert what arrived without a backend.
package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

type summary struct {
	mu        sync.Mutex
	Requests  map[string]int                          `json:"requests"`
	Metrics   map[string]map[string]string            `json:"metrics"`   // name → attr key → sample value
	Resources map[string]map[string]string            `json:"resources"` // openlog.agent.mode → resource attrs (last)
	Logs      map[string][]map[string]string          `json:"logs"`      // "event" / "container" → samples (attrs + body)
	Counts    map[string]int                          `json:"counts"`
	Values    map[string]map[string]float64           `json:"values"` // metric → selected attr combination → value
	Points    map[string]map[string]map[string]string `json:"points"`
	seen      map[string]bool
	perKey    map[string]int
}

func attrs(kvs []*commonpb.KeyValue) map[string]string {
	m := map[string]string{}
	for _, kv := range kvs {
		switch v := kv.Value.GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			m[kv.Key] = v.StringValue
		case *commonpb.AnyValue_IntValue:
			b, _ := json.Marshal(v.IntValue)
			m[kv.Key] = string(b)
		case *commonpb.AnyValue_BoolValue:
			b, _ := json.Marshal(v.BoolValue)
			m[kv.Key] = string(b)
		case *commonpb.AnyValue_ArrayValue:
			b, _ := json.Marshal(v.ArrayValue.String())
			m[kv.Key] = string(b)
		}
	}
	return m
}

func (s *summary) metrics(md *metricspb.MetricsData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rm := range md.ResourceMetrics {
		ra := attrs(rm.Resource.GetAttributes())
		mode := ra["openlog.agent.mode"]
		if mode == "" {
			mode = "none"
		}
		s.Resources[mode] = ra
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				var dps []*metricspb.NumberDataPoint
				if g := m.GetGauge(); g != nil {
					dps = g.DataPoints
				} else if su := m.GetSum(); su != nil {
					dps = su.DataPoints
				}
				keys := s.Metrics[m.Name]
				if keys == nil {
					keys = map[string]string{}
					s.Metrics[m.Name] = keys
				}
				s.Counts[m.Name] += len(dps)
				for _, dp := range dps {
					a := attrs(dp.Attributes)
					for k, v := range a {
						if len(v) > 200 {
							v = v[:200]
						}
						keys[k] = v
					}
					key := a["k8s.namespace.name"] + "/" + a["k8s.pod.name"] + a["openlog.k8s.workload.name"] + a["k8s.node.name"] + "/" + a["k8s.container.name"] + a["condition"]
					if s.Values[m.Name] == nil {
						s.Values[m.Name] = map[string]float64{}
					}
					v := dp.GetAsDouble()
					if _, ok := dp.Value.(*metricspb.NumberDataPoint_AsInt); ok {
						v = float64(dp.GetAsInt())
					}
					s.Values[m.Name][key] = v
					if s.Points[m.Name] == nil {
						s.Points[m.Name] = map[string]map[string]string{}
					}
					if len(s.Points[m.Name]) < 1000 {
						pa := map[string]string{"_value": strconv.FormatFloat(v, 'g', -1, 64)}
						for k, val := range a {
							pa[k] = val
						}
						s.Points[m.Name][key] = pa
					}
				}
			}
		}
	}
}

func (s *summary) logs(ld *logspb.LogsData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rl := range ld.ResourceLogs {
		ra := attrs(rl.Resource.GetAttributes())
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				kind := "other"
				switch {
				case lr.EventName == "k8s.event":
					kind = "event"
				case ra["container.id"] != "":
					kind = "container"
				case lr.EventName != "":
					kind = lr.EventName
				}
				s.Counts["logs."+kind]++
				if kind != "event" && kind != "container" {
					continue
				}
				sample := attrs(lr.Attributes)
				for k, v := range ra {
					if k == "k8s.pod.name" || k == "k8s.namespace.name" || k == "k8s.pod.uid" || k == "k8s.container.name" ||
						k == "k8s.deployment.name" || k == "openlog.k8s.workload.kind" || k == "openlog.k8s.workload.name" ||
						k == "k8s.cluster.name" || k == "k8s.node.name" || k == "container.id" || k == "k8s.pod.label.app" {
						sample["resource."+k] = v
					}
				}
				sample["body"] = lr.Body.GetStringValue()
				if len(sample["body"]) > 200 {
					sample["body"] = sample["body"][:200]
				}
				key := kind + sample["body"] + sample["resource.k8s.pod.name"]
				if s.seen[key] {
					continue
				}
				s.seen[key] = true
				// At most 20 samples per pod (or event reason), so chatty pods do not crowd out the others.
				capKey := kind + "|" + sample["resource.k8s.pod.name"] + sample["k8s.event.reason"]
				if s.perKey[capKey] < 20 && len(s.Logs[kind]) < 5000 {
					s.perKey[capKey]++
					s.Logs[kind] = append(s.Logs[kind], sample)
				}
			}
		}
	}
}

func main() {
	s := &summary{Requests: map[string]int{}, Metrics: map[string]map[string]string{}, Resources: map[string]map[string]string{},
		Logs: map[string][]map[string]string{}, Counts: map[string]int{}, Values: map[string]map[string]float64{}, Points: map[string]map[string]map[string]string{}, seen: map[string]bool{}, perKey: map[string]int{}}
	read := func(r *http.Request) ([]byte, error) {
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				return nil, err
			}
			defer zr.Close()
			body = zr
		}
		return io.ReadAll(io.LimitReader(body, 64<<20))
	}
	handle := func(signal string, msg func() proto.Message, apply func(proto.Message)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, err := read(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			m := msg()
			if err := proto.Unmarshal(b, m); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.Requests[signal+" "+r.Header.Get("Authorization")]++
			s.mu.Unlock()
			apply(m)
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.WriteHeader(http.StatusOK)
		}
	}
	http.HandleFunc("POST /v1/metrics", handle("metrics", func() proto.Message { return &metricspb.MetricsData{} },
		func(m proto.Message) { s.metrics(m.(*metricspb.MetricsData)) }))
	http.HandleFunc("POST /v1/logs", handle("logs", func() proto.Message { return &logspb.LogsData{} },
		func(m proto.Message) { s.logs(m.(*logspb.LogsData)) }))
	http.HandleFunc("POST /v1/traces", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	// Agent sync / update endpoints: not served (the agent tolerates failures).
	http.HandleFunc("GET /summary", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		names := make([]string, 0, len(s.Metrics))
		for n := range s.Metrics {
			names = append(names, n)
		}
		sort.Strings(names)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"metric_names": names, "summary": s})
	})
	log.Println("capture listening on :4318")
	log.Fatal(http.ListenAndServe(":4318", nil))
}
