package ingest

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/tenant"
)

const (
	ctProtobuf = "application/x-protobuf"
	ctJSON     = "application/json"
)

// HTTPHandler returns the OTLP/HTTP handler (/v1/metrics, /v1/logs, /v1/traces).
func (s *Service) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/metrics", s.httpExport(queue.SignalMetrics))
	mux.Handle("POST /v1/logs", s.httpExport(queue.SignalLogs))
	mux.Handle("POST /v1/traces", s.httpExport(queue.SignalTraces))
	if s.extraRoutes != nil {
		s.extraRoutes(mux)
	}
	return withCORS(s.cfg.CORSAllowedOrigins, mux)
}

var errTooLarge = errors.New("request body too large")

func (s *Service) httpExport(sig queue.Signal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isJSON := false
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch ct {
		case ctProtobuf:
		case ctJSON:
			isJSON = true
		default:
			s.httpError(w, sig, false, http.StatusUnsupportedMediaType, codes.InvalidArgument, "unsupported content type; use application/x-protobuf or application/json")
			return
		}

		key := tenant.KeyFromHTTP(r.Header)
		tenantID, err := s.resolver.Resolve(r.Context(), key)
		if errors.Is(err, tenant.ErrUnavailable) {
			// Key store down and key not cached: retryable, like a Kafka outage (D-014).
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter/time.Second)))
			s.httpError(w, sig, isJSON, http.StatusServiceUnavailable, codes.Unavailable, "authentication temporarily unavailable, retry later")
			return
		}
		if err != nil {
			s.httpError(w, sig, isJSON, http.StatusUnauthorized, codes.Unauthenticated, "invalid or missing license key")
			return
		}
		if s.gate != nil { // SaaS organization state (gate.go, D-105)
			s.gate.ObserveSource(tenantID, r.RemoteAddr)
			if _, suspended := s.gate.Suspended(tenantID); suspended {
				s.httpErrorInfo(w, sig, isJSON, http.StatusForbidden, codes.PermissionDenied, suspendedMessage, ReasonOrgSuspended, 0)
				return
			}
		}

		body, err := s.readBody(r)
		if err != nil {
			if errors.Is(err, errTooLarge) {
				s.httpError(w, sig, isJSON, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, "request body exceeds "+strconv.FormatInt(s.cfg.MaxBodyBytes, 10)+" bytes")
				return
			}
			s.httpError(w, sig, isJSON, http.StatusBadRequest, codes.InvalidArgument, "cannot read body: "+err.Error())
			return
		}

		msg := newRequest(sig)
		if isJSON {
			err = unmarshalOTLPJSON(body, msg)
		} else {
			err = proto.Unmarshal(body, msg)
		}
		if err != nil {
			s.httpError(w, sig, isJSON, http.StatusBadRequest, codes.InvalidArgument, "cannot decode request: "+err.Error())
			return
		}
		size := len(body)
		if isJSON {
			size = proto.Size(msg)
		}
		if d, ok := s.checkLimit(tenantID, sig, size); !ok { // tenant quota (limit.go, D-014, D-080)
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(d.RetryAfter)))
			s.httpError(w, sig, isJSON, http.StatusTooManyRequests, codes.ResourceExhausted, d.Message)
			return
		}

		gd := s.gateHosts(tenantID, sig, msg) // plan host limit (gate.go, D-105)
		if gd.all {
			s.httpErrorInfo(w, sig, isJSON, http.StatusTooManyRequests, codes.ResourceExhausted, gd.message, ReasonQuotaExceeded, hostRetryAfter)
			return
		}
		p := prepare(sig, tenantID, msg)
		applyGateToPrepared(&p, gd)
		raw := body
		if isJSON {
			raw = nil
		}
		if err := s.export(r.Context(), sig, tenantID, p, raw); err != nil {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter/time.Second)))
			s.httpError(w, sig, isJSON, http.StatusServiceUnavailable, codes.Unavailable, "backend temporarily unavailable, retry later")
			return
		}
		s.writeProto(w, isJSON, http.StatusOK, partialSuccess(sig, p.rejected, p.errMsg))
		s.m.requests.WithLabelValues(string(sig), "http", "200").Inc()
	})
}

// readBody reads the (optionally gzip-compressed) body enforcing the
// decompressed size limit.
func (s *Service) readBody(r *http.Request) ([]byte, error) {
	limit := s.cfg.MaxBodyBytes
	// Bound the wire size too; compressed payloads are never meaningfully larger than the limit.
	var rd io.Reader = http.MaxBytesReader(nil, r.Body, limit+limit/10+1024)
	switch enc := r.Header.Get("Content-Encoding"); enc {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(rd)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		rd = gz
	default:
		return nil, errors.New("unsupported content encoding " + strconv.Quote(enc))
	}
	var buf bytes.Buffer
	if r.ContentLength > 0 && r.ContentLength <= limit {
		buf.Grow(int(r.ContentLength))
	}
	n, err := buf.ReadFrom(io.LimitReader(rd, limit+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, errTooLarge
		}
		return nil, err
	}
	if n > limit {
		return nil, errTooLarge
	}
	return buf.Bytes(), nil
}

func (s *Service) httpError(w http.ResponseWriter, sig queue.Signal, isJSON bool, httpCode int, code codes.Code, msg string) {
	s.m.requests.WithLabelValues(string(sig), "http", strconv.Itoa(httpCode)).Inc()
	s.writeProto(w, isJSON, httpCode, &status.Status{Code: int32(code), Message: msg})
}

func (s *Service) writeProto(w http.ResponseWriter, isJSON bool, httpCode int, m proto.Message) {
	var (
		b   []byte
		err error
	)
	if isJSON {
		w.Header().Set("Content-Type", ctJSON)
		b, err = protojson.Marshal(m)
	} else {
		w.Header().Set("Content-Type", ctProtobuf)
		b, err = proto.Marshal(m)
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(httpCode)
	_, _ = w.Write(b)
}
