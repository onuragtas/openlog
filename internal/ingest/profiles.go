package ingest

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/collector/pdata/pprofile"
	"go.opentelemetry.io/collector/pdata/pprofile/pprofileotlp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/tenant"
)

// OTLP profiles (continuous profiling). POST /v1/profiles and the gRPC ProfilesService.
//
// This is a path of its own rather than another arm of httpExport, and the reason is a type, not a
// preference: the OTLP profiles request is `pprofileotlp.ExportRequest`, a pdata wrapper, while newRequest,
// prepare and partialSuccess are built on proto.Message, which it does not implement. Forcing it in would
// mean an interface shim around an unstable proto in the middle of the path every other signal takes.
//
// What this path does not do differently: license key resolution, the SaaS gate, the body limit, the tenant
// quota and the produce are the same calls in the same order, so profiles cannot become a way around them.
// The payload is validated and then produced **as it arrived** — export() re-marshals only when the message
// changed — so the unstable wire encoding is never re-encoded by openlog.

// profileRoutes registers POST /v1/profiles.
func (s *Service) profileRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/profiles", s.httpExportProfiles())
}

func (s *Service) httpExportProfiles() http.Handler {
	const sig = queue.SignalProfiles
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isJSON := false
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch ct {
		case ctProtobuf:
		case ctJSON:
			isJSON = true
		default:
			s.httpError(w, sig, false, http.StatusUnsupportedMediaType, codes.InvalidArgument,
				"unsupported content type; use application/x-protobuf or application/json")
			return
		}

		tenantID, ok := s.resolveForProfiles(w, r, isJSON)
		if !ok {
			return
		}

		body, err := s.readBody(r)
		if err != nil {
			if errors.Is(err, errTooLarge) {
				s.httpError(w, sig, isJSON, http.StatusRequestEntityTooLarge, codes.ResourceExhausted,
					"request body exceeds "+strconv.FormatInt(s.cfg.MaxBodyBytes, 10)+" bytes")
				return
			}
			s.httpError(w, sig, isJSON, http.StatusBadRequest, codes.InvalidArgument, "cannot read body: "+err.Error())
			return
		}

		req := pprofileotlp.NewExportRequest()
		if isJSON {
			err = req.UnmarshalJSON(body)
		} else {
			err = req.UnmarshalProto(body)
		}
		if err != nil {
			s.httpError(w, sig, isJSON, http.StatusBadRequest, codes.InvalidArgument, "cannot decode request: "+err.Error())
			return
		}
		profiles := req.Profiles()

		size := len(body)
		if isJSON {
			// JSON is larger on the wire than the protobuf the quota is defined in, so the payload is
			// measured the way it will be stored and forwarded.
			if b, err := req.MarshalProto(); err == nil {
				size = len(b)
			}
		}
		if d, ok := s.checkLimit(tenantID, sig, size); !ok { // tenant quota (limit.go, D-014, D-080)
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(d.RetryAfter)))
			s.httpError(w, sig, isJSON, http.StatusTooManyRequests, codes.ResourceExhausted, d.Message)
			return
		}

		p := prepareProfiles(tenantID, profiles)
		raw := body
		if isJSON {
			// The stored bytes must be protobuf whatever the caller sent, so a JSON payload is re-encoded
			// once here rather than left for the processor to guess at.
			if b, err := req.MarshalProto(); err == nil {
				raw = b
			} else {
				s.httpError(w, sig, isJSON, http.StatusBadRequest, codes.InvalidArgument, "cannot encode request: "+err.Error())
				return
			}
		}
		if err := s.export(r.Context(), sig, tenantID, p, raw); err != nil {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter/time.Second)))
			s.httpError(w, sig, isJSON, http.StatusServiceUnavailable, codes.Unavailable, "backend temporarily unavailable, retry later")
			return
		}
		s.writeProfileResponse(w, isJSON)
		s.m.requests.WithLabelValues(string(sig), "http", "200").Inc()
	})
}

// resolveForProfiles runs the same authentication and organization-state checks every other HTTP signal
// goes through, and writes the response itself when one of them refuses.
func (s *Service) resolveForProfiles(w http.ResponseWriter, r *http.Request, isJSON bool) (string, bool) {
	const sig = queue.SignalProfiles
	key := tenant.KeyFromHTTP(r.Header)
	tenantID, err := s.resolver.Resolve(r.Context(), key)
	switch {
	case errors.Is(err, tenant.ErrUnavailable):
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter/time.Second)))
		s.httpError(w, sig, isJSON, http.StatusServiceUnavailable, codes.Unavailable, "authentication temporarily unavailable, retry later")
		return "", false
	case errors.Is(err, tenant.ErrOrgDeleted): // D-115
		s.httpErrorInfo(w, sig, isJSON, http.StatusForbidden, codes.PermissionDenied, orgDeletedMessage, ReasonOrgDeleted, 0)
		return "", false
	case err != nil:
		s.httpError(w, sig, isJSON, http.StatusUnauthorized, codes.Unauthenticated, "invalid or missing license key")
		return "", false
	}
	if s.gate != nil { // SaaS organization state (gate.go, D-105)
		s.gate.ObserveSource(tenantID, r.RemoteAddr)
		if _, suspended := s.gate.Suspended(tenantID); suspended {
			s.httpErrorInfo(w, sig, isJSON, http.StatusForbidden, codes.PermissionDenied, suspendedMessage, ReasonOrgSuspended, 0)
			return "", false
		}
	}
	return tenantID, true
}

// writeProfileResponse writes an empty ExportProfilesServiceResponse. Nothing is ever partially rejected
// here: a payload that cannot be read is refused whole, because a profile is one measurement of one process
// over one window and half of it is not a smaller truth.
func (s *Service) writeProfileResponse(w http.ResponseWriter, isJSON bool) {
	resp := pprofileotlp.NewExportResponse()
	var (
		body []byte
		err  error
	)
	if isJSON {
		body, err = resp.MarshalJSON()
		w.Header().Set("Content-Type", ctJSON)
	} else {
		body, err = resp.MarshalProto()
		w.Header().Set("Content-Type", ctProtobuf)
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// prepareProfiles builds the produce parameters. The partition key is the service, which is also the
// sharding key of profiles_local (0095_profiles): a flame graph is one service over one window, so keeping a
// service's samples on one partition and one shard keeps it a single-shard read.
func prepareProfiles(tenantID string, profiles pprofile.Profiles) prepared {
	return prepared{
		key:   tenantID + "/" + profileServiceName(profiles),
		empty: profilesEmpty(profiles),
	}
}

// profileServiceName returns the service.name of the first resource, or "" when none carries one.
func profileServiceName(profiles pprofile.Profiles) string {
	for i := 0; i < profiles.ResourceProfiles().Len(); i++ {
		if v, ok := profiles.ResourceProfiles().At(i).Resource().Attributes().Get("service.name"); ok {
			return v.AsString()
		}
	}
	return ""
}

// profilesEmpty reports whether the payload carries no profile at all, which export() drops rather than
// producing an empty record.
func profilesEmpty(profiles pprofile.Profiles) bool {
	for i := 0; i < profiles.ResourceProfiles().Len(); i++ {
		rp := profiles.ResourceProfiles().At(i)
		for j := 0; j < rp.ScopeProfiles().Len(); j++ {
			if rp.ScopeProfiles().At(j).Profiles().Len() > 0 {
				return false
			}
		}
	}
	return true
}

// profilesServer is the gRPC ProfilesService.
type profilesServer struct {
	pprofileotlp.UnimplementedGRPCServer
	s *Service
}

func (p profilesServer) Export(ctx context.Context, req pprofileotlp.ExportRequest) (pprofileotlp.ExportResponse, error) {
	const sig = queue.SignalProfiles
	md, _ := metadata.FromIncomingContext(ctx)
	key := tenant.KeyFromValues(func(name string) string {
		if v := md.Get(name); len(v) > 0 {
			return v[0]
		}
		return ""
	})
	tenantID, err := p.s.resolver.Resolve(ctx, key)
	switch {
	case errors.Is(err, tenant.ErrUnavailable):
		return pprofileotlp.NewExportResponse(), unavailableError()
	case errors.Is(err, tenant.ErrOrgDeleted): // D-115
		return pprofileotlp.NewExportResponse(), grpcErrorInfo(codes.PermissionDenied, orgDeletedMessage, ReasonOrgDeleted, 0)
	case err != nil:
		return pprofileotlp.NewExportResponse(), status.Error(codes.Unauthenticated, "invalid or missing license key")
	}
	if p.s.gate != nil { // SaaS organization state (gate.go, D-105)
		if pr, ok := peer.FromContext(ctx); ok && pr.Addr != nil {
			p.s.gate.ObserveSource(tenantID, pr.Addr.String())
		}
		if _, suspended := p.s.gate.Suspended(tenantID); suspended {
			return pprofileotlp.NewExportResponse(), grpcErrorInfo(codes.PermissionDenied, suspendedMessage, ReasonOrgSuspended, 0)
		}
	}
	body, err := req.MarshalProto()
	if err != nil {
		return pprofileotlp.NewExportResponse(), status.Error(codes.InvalidArgument, "cannot encode request: "+err.Error())
	}
	if d, ok := p.s.checkLimit(tenantID, sig, len(body)); !ok { // tenant quota (limit.go)
		return pprofileotlp.NewExportResponse(), resourceExhaustedError(d)
	}
	prep := prepareProfiles(tenantID, req.Profiles())
	if err := p.s.export(ctx, sig, tenantID, prep, body); err != nil {
		return pprofileotlp.NewExportResponse(), unavailableError()
	}
	return pprofileotlp.NewExportResponse(), nil
}
