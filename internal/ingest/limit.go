package ingest

import (
	"math"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/onuragtas/openlog/internal/queue"
)

// Limiter enforces tenant quotas (SaaS mode, D-080; internal/quota.IngestLimiter). Rejections are the tenant limit
// case of D-014: HTTP 429 with Retry-After and a google.rpc.Status body, gRPC RESOURCE_EXHAUSTED with RetryInfo (OTLP
// exporters retry both after the delay).
type Limiter interface {
	Allow(tenantID string, sig queue.Signal, bytes int) LimitDecision
}

// LimitDecision is the outcome of Limiter.Allow.
type LimitDecision struct {
	Allowed    bool
	RetryAfter time.Duration
	Message    string
}

// SetLimiter enables quota enforcement. Must be called before Run.
func (s *Service) SetLimiter(l Limiter) { s.limiter = l }

// checkLimit returns the decision and whether the request may proceed.
func (s *Service) checkLimit(tenantID string, sig queue.Signal, bytes int) (LimitDecision, bool) {
	if s.limiter == nil {
		return LimitDecision{Allowed: true}, true
	}
	d := s.limiter.Allow(tenantID, sig, bytes)
	if d.Allowed {
		return d, true
	}
	if d.Message == "" {
		d.Message = "tenant ingest limit exceeded, retry later"
	}
	s.log.Debug("ingest request rejected by quota", "tenant_id", tenantID, "signal", sig, "bytes", bytes, "retry_after", d.RetryAfter)
	return d, false
}

// retryAfterSeconds rounds a delay up to whole seconds (at least 1) for the Retry-After header.
func retryAfterSeconds(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}

// resourceExhaustedError is RESOURCE_EXHAUSTED with a RetryInfo detail.
func resourceExhaustedError(d LimitDecision) error {
	st := status.New(codes.ResourceExhausted, d.Message)
	delay := time.Duration(retryAfterSeconds(d.RetryAfter)) * time.Second
	if withInfo, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(delay)}); err == nil {
		st = withInfo
	}
	return st.Err()
}
