package admin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A failing check (e.g. kafka_topics while topics are missing, D-019) makes /readyz 503
// and names the failing dependency; once it passes, /readyz is 200.
func TestReadyzReflectsChecks(t *testing.T) {
	s := New("127.0.0.1:0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	topicsErr := errors.New("missing kafka topics: openlog.otlp.traces.v1")
	s.AddCheck("kafka", func(context.Context) error { return nil })
	s.AddCheck("kafka_topics", func(context.Context) error { return topicsErr })

	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec
	}
	rec := get()
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "openlog.otlp.traces.v1") {
		t.Errorf("readyz = %d %s", rec.Code, rec.Body.String())
	}
	topicsErr = nil
	if rec := get(); rec.Code != http.StatusOK {
		t.Errorf("readyz = %d %s", rec.Code, rec.Body.String())
	}
}
