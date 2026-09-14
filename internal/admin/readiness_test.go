package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func readyz(t *testing.T, s *Server) (int, map[string]string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("readyz body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

// A service migrating before its dependency checks exist must not report ready (a compose --wait, the updater's
// health step or a Kubernetes probe would send traffic to a port that is not listening yet).
func TestGateBlocksReadinessUntilReleased(t *testing.T) {
	s := New("127.0.0.1:0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if code, _ := readyz(t, s); code != http.StatusOK {
		t.Fatalf("no checks: %d", code)
	}
	release := s.Gate("migrations", "applying migrations")
	if code, body := readyz(t, s); code != http.StatusServiceUnavailable || body["migrations"] != "applying migrations" || body["version"] == "" {
		t.Fatalf("gated: %d %v", code, body)
	}
	release()
	release()
	if code, body := readyz(t, s); code != http.StatusOK || body["migrations"] != "ok" {
		t.Fatalf("released: %d %v", code, body)
	}
}

func TestListenerCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	for _, addr := range []string{":" + port, "0.0.0.0:" + port, "127.0.0.1:" + port} {
		if err := ListenerCheck(addr)(context.Background()); err != nil {
			t.Errorf("%s while listening: %v", addr, err)
		}
	}
	ln.Close()
	if err := ListenerCheck(":" + port)(context.Background()); err == nil || !strings.Contains(err.Error(), "not listening") {
		t.Errorf("after close: %v", err)
	}
	if err := ListenerCheck("no-port")(context.Background()); err == nil {
		t.Error("invalid address accepted")
	}
}
