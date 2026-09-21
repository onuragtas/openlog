package jolokia

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// server answers /version and the bulk POST; other paths are 404, so the probing can be observed.
func server(t *testing.T, path string, answer string) (*httptest.Server, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == path+"/version":
			_, _ = w.Write([]byte(`{"status":200,"value":{"agent":"2.1.0","protocol":"7.2"}}`))
		case r.URL.Path == path && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(answer))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func clientFor(t *testing.T, url string) *Client {
	t.Helper()
	inst := testutil.Instance()
	inst.Settings.Endpoint = ""
	c, err := New(inst, integrations.TCP(strings.TrimPrefix(url, "http://"), 0))
	if err != nil {
		t.Fatal(err)
	}
	// The instance has no configured endpoint, so the base is the URL under test.
	c.base = url
	return c
}

func TestReadAllIsOneRequest(t *testing.T) {
	answer := `[
	  {"status":200,"value":{"used":1024,"max":4096},"request":{"mbean":"java.lang:type=Memory","attribute":"HeapMemoryUsage"}},
	  {"status":404,"error":"javax.management.InstanceNotFoundException","request":{"mbean":"kafka.server:type=app-info","attribute":"version"}}
	]`
	srv, asked := server(t, "/jolokia", answer)
	c := clientFor(t, srv.URL)
	defer c.Close()

	res, err := c.ReadAll(context.Background(), []Read{
		{MBean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage"},
		{MBean: "kafka.server:type=app-info", Attribute: "version"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("responses = %d", len(res))
	}
	if !res[0].OK() {
		t.Errorf("first read = %+v", res[0])
	}
	if v, ok := Field(res[0].Value, "used"); !ok || v != 1024 {
		t.Errorf("used = %v, %v", v, ok)
	}
	// An MBean this JVM does not have is one failed read, not a failed request: a Kafka MBean missing on a
	// plain JVM must not cost the memory reading next to it.
	if res[1].OK() {
		t.Errorf("second read = %+v, want a failure", res[1])
	}

	// Two attributes, one POST (plus the version probe).
	posts := 0
	for _, a := range *asked {
		if strings.HasPrefix(a, "POST ") {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("sent %d POSTs for two reads: %v", posts, *asked)
	}
}

// The endpoint is probed once and remembered, so a collection every 30 seconds does not re-probe forever.
func TestEndpointProbingIsRememberedAndBounded(t *testing.T) {
	srv, asked := server(t, "/actuator/jolokia", `[]`)
	c := clientFor(t, srv.URL)
	defer c.Close()

	if _, err := c.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := len(*asked)
	if !strings.Contains(strings.Join(*asked, " "), "/actuator/jolokia/version") {
		t.Fatalf("the probe did not reach the second path: %v", *asked)
	}
	if _, err := c.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The second call asks exactly once: the remembered endpoint, no re-probing.
	if got := len(*asked) - first; got != 1 {
		t.Errorf("second call asked %d times, want 1", got)
	}
}

func TestEndpointThatIsNotJolokia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()
	c := clientFor(t, srv.URL)
	defer c.Close()
	_, err := c.Version(context.Background())
	if err == nil || !errors.Is(err, integrations.ErrTryNext) {
		t.Fatalf("err = %v, want try-next", err)
	}
}

func TestUnauthorizedIsNeedsConfiguration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/version") {
			_, _ = w.Write([]byte(`{"status":200,"value":{"agent":"2.1.0"}}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := clientFor(t, srv.URL)
	defer c.Close()
	_, err := c.ReadAll(context.Background(), []Read{{MBean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage"}})
	var se *integrations.StatusError
	if !errors.As(err, &se) || se.Status != "needs_configuration" {
		t.Fatalf("err = %v", err)
	}
}

func TestBulkLimit(t *testing.T) {
	srv, _ := server(t, "/jolokia", `[]`)
	c := clientFor(t, srv.URL)
	defer c.Close()
	reads := make([]Read, MaxReads+1)
	for i := range reads {
		reads[i] = Read{MBean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage"}
	}
	if _, err := c.ReadAll(context.Background(), reads); err == nil {
		t.Fatal("a request past the bulk limit must be refused")
	}
}

func TestNumberAndField(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`{"used":1024,"max":-1,"name":"G1 Eden Space"}`), &doc); err != nil {
		t.Fatal(err)
	}
	if v, ok := Field(doc, "used"); !ok || v != 1024 {
		t.Errorf("used = %v, %v", v, ok)
	}
	// A string attribute is not a number, and saying so is what keeps it from becoming 0.
	if _, ok := Field(doc, "name"); ok {
		t.Error("a string must not read as a number")
	}
	if _, ok := Number("12"); ok {
		t.Error("a string must not read as a number")
	}
	if _, ok := Field(doc, "missing"); ok {
		t.Error("a missing field must not read as a number")
	}
}
