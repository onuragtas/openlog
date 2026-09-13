package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onuragtas/openlog/internal/version"
)

// /readyz reports the product version in its body and every admin response has X-Openlog-Version
// (docs/contracts/releases-updates.md §1, §5), also while not ready.
func TestReadyzVersion(t *testing.T) {
	s := New("127.0.0.1:0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetDraining()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable || body["version"] != version.String() || rec.Header().Get(version.Header) != version.String() {
		t.Errorf("readyz %d %v header %q", rec.Code, body, rec.Header().Get(version.Header))
	}
}
