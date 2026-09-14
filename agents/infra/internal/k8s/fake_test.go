package k8s

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// fakeAPI is a minimal fake API server / kubelet: fixed JSON responses per path, watch streams, and a lease store.
type fakeAPI struct {
	t       *testing.T
	mu      sync.Mutex
	routes  map[string]string // "GET /path" → JSON body
	watches map[string][]string
	status  map[string]int
	lease   *Lease
	leaseRV int
	queries []string
	auth    []string
}

func newFakeAPI(t *testing.T) (*fakeAPI, *httptest.Server) {
	f := &fakeAPI{t: t, routes: map[string]string{}, watches: map[string][]string{}, status: map[string]int{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, r.Method+" "+r.URL.RequestURI())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	key := r.Method + " " + r.URL.Path
	if strings.Contains(r.URL.Path, "/leases") {
		f.serveLease(w, r)
		return
	}
	if code := f.status[key]; code != 0 {
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"kind":"Status","code":%d,"reason":"Forbidden","message":"denied"}`, code)
		return
	}
	if r.URL.Query().Get("watch") == "1" {
		evs := f.watches[r.URL.Path]
		f.watches[r.URL.Path] = nil
		for _, ev := range evs {
			fmt.Fprintln(w, ev)
		}
		return
	}
	body, ok := f.routes[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"kind":"Status","code":404,"reason":"NotFound"}`)
		return
	}
	fmt.Fprint(w, body)
}

func (f *fakeAPI) serveLease(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if f.lease == nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"kind":"Status","code":404}`)
			return
		}
		_ = json.NewEncoder(w).Encode(f.lease)
	case http.MethodPost:
		if f.lease != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		var l Lease
		_ = json.NewDecoder(r.Body).Decode(&l)
		f.leaseRV++
		l.Metadata.ResourceVersion = fmt.Sprint(f.leaseRV)
		f.lease = &l
		_ = json.NewEncoder(w).Encode(l)
	case http.MethodPut:
		var l Lease
		_ = json.NewDecoder(r.Body).Decode(&l)
		if f.lease == nil || l.Metadata.ResourceVersion != f.lease.Metadata.ResourceVersion {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.leaseRV++
		l.Metadata.ResourceVersion = fmt.Sprint(f.leaseRV)
		f.lease = &l
		_ = json.NewEncoder(w).Encode(l)
	}
}

func (f *fakeAPI) set(method, path, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = body
}

// testClient returns a client for srv with a token file.
func testClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("tok-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Client{Base: srv.URL, HTTP: srv.Client(), TokenPath: filepath.Join(dir, "token")}
}

// ---- metric helpers ----

func attrMap(kvs []*commonpb.KeyValue) map[string]string {
	m := map[string]string{}
	for _, kv := range kvs {
		switch v := kv.Value.Value.(type) {
		case *commonpb.AnyValue_StringValue:
			m[kv.Key] = v.StringValue
		case *commonpb.AnyValue_IntValue:
			m[kv.Key] = fmt.Sprint(v.IntValue)
		case *commonpb.AnyValue_ArrayValue:
			var parts []string
			for _, e := range v.ArrayValue.Values {
				parts = append(parts, e.GetStringValue())
			}
			m[kv.Key] = strings.Join(parts, ",")
		}
	}
	return m
}

type point struct {
	value float64
	attrs map[string]string
}

func findMetric(ms []*metricspb.Metric, name string) []point {
	var out []point
	for _, m := range ms {
		if m.Name != name {
			continue
		}
		var dps []*metricspb.NumberDataPoint
		if g := m.GetGauge(); g != nil {
			dps = g.DataPoints
		} else if s := m.GetSum(); s != nil {
			dps = s.DataPoints
		}
		for _, dp := range dps {
			v := dp.GetAsDouble()
			if _, ok := dp.Value.(*metricspb.NumberDataPoint_AsInt); ok {
				v = float64(dp.GetAsInt())
			}
			out = append(out, point{v, attrMap(dp.Attributes)})
		}
	}
	return out
}

func onePoint(t *testing.T, ms []*metricspb.Metric, name string, match map[string]string) point {
	t.Helper()
	var found []point
	for _, p := range findMetric(ms, name) {
		ok := true
		for k, v := range match {
			if p.attrs[k] != v {
				ok = false
			}
		}
		if ok {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s %v: %d points, want 1 (all: %v)", name, match, len(found), findMetric(ms, name))
	}
	return found[0]
}
