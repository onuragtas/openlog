package ingest

import (
	"encoding/hex"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// SetSplitTraces makes ingest produce one traces record per trace id, keyed <tenant>/<trace id>, so every
// span of a trace lands on the same partition and therefore on the same openlog-sampler instance (tail
// sampling, D-075). Off (the default) keeps one record per export request. Must be called before Run.
func (s *Service) SetSplitTraces(on bool) { s.splitTraces = on }

type traceSplit struct {
	key string
	req *coltrace.ExportTraceServiceRequest
}

// splitByTrace groups the spans of req by trace id (first-seen order), keeping the resource and scope of
// every span. It returns nil when req holds at most one trace id (the original record is produced).
func splitByTrace(tenant string, req *coltrace.ExportTraceServiceRequest) []traceSplit {
	var first []byte
	multi := false
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				if first == nil {
					first = sp.GetTraceId()
				} else if string(sp.GetTraceId()) != string(first) {
					multi = true
				}
			}
		}
	}
	if !multi {
		return nil
	}
	index := map[string]int{}
	var out []traceSplit
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				id := string(sp.GetTraceId())
				i, ok := index[id]
				if !ok {
					i = len(out)
					index[id] = i
					out = append(out, traceSplit{key: tenant + "/" + hex.EncodeToString(sp.GetTraceId()), req: &coltrace.ExportTraceServiceRequest{}})
				}
				dst := out[i].req
				var drs *tracepb.ResourceSpans
				if n := len(dst.ResourceSpans); n > 0 && dst.ResourceSpans[n-1].Resource == rs.GetResource() {
					drs = dst.ResourceSpans[n-1]
				} else {
					drs = &tracepb.ResourceSpans{Resource: rs.GetResource(), SchemaUrl: rs.GetSchemaUrl()}
					dst.ResourceSpans = append(dst.ResourceSpans, drs)
				}
				var dss *tracepb.ScopeSpans
				if n := len(drs.ScopeSpans); n > 0 && drs.ScopeSpans[n-1].Scope == ss.GetScope() {
					dss = drs.ScopeSpans[n-1]
				} else {
					dss = &tracepb.ScopeSpans{Scope: ss.GetScope(), SchemaUrl: ss.GetSchemaUrl()}
					drs.ScopeSpans = append(drs.ScopeSpans, dss)
				}
				dss.Spans = append(dss.Spans, sp)
			}
		}
	}
	return out
}
