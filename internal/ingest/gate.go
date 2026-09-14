package ingest

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/onuragtas/openlog/internal/otlputil"
	"github.com/onuragtas/openlog/internal/queue"
)

// Gate enforces SaaS organization state in ingest (D-105; internal/operator.IngestGate): suspended organizations are
// rejected with 403 / PERMISSION_DENIED (ErrorInfo reason org_suspended), data of hosts beyond the plan's host limit
// with 429 / RESOURCE_EXHAUSTED (ErrorInfo reason quota_exceeded) or, in requests that also carry admitted hosts, as
// OTLP partial success. It also observes client addresses for abuse detection.
type Gate interface {
	Suspended(tenantID string) (reason string, suspended bool)
	RejectHosts(tenantID string, hosts []string) (rejected map[string]bool, limit int64)
	ObserveSource(tenantID, addr string)
}

// Error reasons of the ErrorInfo detail.
const (
	ReasonOrgSuspended  = "org_suspended"
	ReasonQuotaExceeded = "quota_exceeded"
	errorDomain         = "openlog"
)

// SetGate enables SaaS enforcement. Must be called before Run.
func (s *Service) SetGate(g Gate) { s.gate = g }

const suspendedMessage = "organization suspended: ingest is disabled for this organization; contact openlog support"

// hostRetryAfter is the Retry-After of requests rejected by the host limit (the limit changes with the plan).
const hostRetryAfter = 5 * time.Minute

// gateDecision is the outcome of gateHosts.
type gateDecision struct {
	rejectedItems int64
	message       string
	all           bool
}

// gateHosts applies the host limit to msg, removing resources of rejected hosts.
func (s *Service) gateHosts(tenantID string, sig queue.Signal, msg proto.Message) gateDecision {
	if s.gate == nil {
		return gateDecision{}
	}
	resources := resourcesOf(msg)
	seen := map[string]bool{}
	var hosts []string
	for _, r := range resources {
		if h := otlputil.AttrString(r.GetAttributes(), otlputil.AttrHostID); h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	rejected, limit := s.gate.RejectHosts(tenantID, hosts)
	if len(rejected) == 0 {
		return gateDecision{}
	}
	items, remaining := filterHosts(msg, rejected)
	d := gateDecision{rejectedItems: items, all: remaining == 0}
	d.message = fmt.Sprintf("quota_exceeded: host limit of %d hosts of the organization's plan reached; data of %d new host(s) rejected "+
		"(hosts already reporting are not affected); upgrade the plan or remove hosts", limit, len(rejected))
	return d
}

func resourcesOf(msg proto.Message) []*resourcepb.Resource {
	var out []*resourcepb.Resource
	switch req := msg.(type) {
	case *colmetrics.ExportMetricsServiceRequest:
		for _, r := range req.GetResourceMetrics() {
			out = append(out, r.GetResource())
		}
	case *collogs.ExportLogsServiceRequest:
		for _, r := range req.GetResourceLogs() {
			out = append(out, r.GetResource())
		}
	case *coltrace.ExportTraceServiceRequest:
		for _, r := range req.GetResourceSpans() {
			out = append(out, r.GetResource())
		}
	}
	return out
}

func rejectedHost(r *resourcepb.Resource, rejected map[string]bool) bool {
	h := otlputil.AttrString(r.GetAttributes(), otlputil.AttrHostID)
	return h != "" && rejected[h]
}

// filterHosts removes the resources of rejected hosts and returns the removed items and the remaining resources.
func filterHosts(msg proto.Message, rejected map[string]bool) (items int64, remaining int) {
	switch req := msg.(type) {
	case *colmetrics.ExportMetricsServiceRequest:
		kept := req.ResourceMetrics[:0]
		for _, rm := range req.ResourceMetrics {
			if !rejectedHost(rm.GetResource(), rejected) {
				kept = append(kept, rm)
				continue
			}
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					items += int64(dataPointCount(m))
				}
			}
		}
		req.ResourceMetrics = kept
		return items, len(kept)
	case *collogs.ExportLogsServiceRequest:
		kept := req.ResourceLogs[:0]
		for _, rl := range req.ResourceLogs {
			if !rejectedHost(rl.GetResource(), rejected) {
				kept = append(kept, rl)
				continue
			}
			for _, sl := range rl.GetScopeLogs() {
				items += int64(len(sl.GetLogRecords()))
			}
		}
		req.ResourceLogs = kept
		return items, len(kept)
	case *coltrace.ExportTraceServiceRequest:
		kept := req.ResourceSpans[:0]
		for _, rs := range req.ResourceSpans {
			if !rejectedHost(rs.GetResource(), rejected) {
				kept = append(kept, rs)
				continue
			}
			for _, ss := range rs.GetScopeSpans() {
				items += int64(len(ss.GetSpans()))
			}
		}
		req.ResourceSpans = kept
		return items, len(kept)
	}
	return 0, 1
}

// applyGateToPrepared merges the host limit outcome into the OTLP partial success.
func applyGateToPrepared(p *prepared, d gateDecision) {
	if d.rejectedItems == 0 && d.message == "" {
		return
	}
	p.rejected += d.rejectedItems
	p.changed = true
	if p.errMsg == "" {
		p.errMsg = d.message
	} else {
		p.errMsg += "; " + d.message
	}
}

func errorInfo(reason string, meta map[string]string) *errdetails.ErrorInfo {
	return &errdetails.ErrorInfo{Reason: reason, Domain: errorDomain, Metadata: meta}
}

// httpErrorInfo writes a google.rpc.Status with an ErrorInfo (and RetryInfo when retry > 0) detail.
func (s *Service) httpErrorInfo(w http.ResponseWriter, sig queue.Signal, isJSON bool, httpCode int, code codes.Code, msg, reason string, retry time.Duration) {
	st := &status.Status{Code: int32(code), Message: msg}
	if a, err := anypb.New(errorInfo(reason, nil)); err == nil {
		st.Details = append(st.Details, a)
	}
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retry)))
		if a, err := anypb.New(&errdetails.RetryInfo{RetryDelay: durationpb.New(retry)}); err == nil {
			st.Details = append(st.Details, a)
		}
	}
	s.m.requests.WithLabelValues(string(sig), "http", strconv.Itoa(httpCode)).Inc()
	s.writeProto(w, isJSON, httpCode, st)
}

// grpcErrorInfo is the gRPC status with an ErrorInfo (and RetryInfo when retry > 0) detail.
func grpcErrorInfo(code codes.Code, msg, reason string, retry time.Duration) error {
	st := grpcstatus.New(code, msg)
	var with *grpcstatus.Status
	var err error
	if retry > 0 {
		with, err = st.WithDetails(errorInfo(reason, nil), &errdetails.RetryInfo{RetryDelay: durationpb.New(retry)})
	} else {
		with, err = st.WithDetails(errorInfo(reason, nil))
	}
	if err == nil {
		st = with
	}
	return st.Err()
}
