package updater

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeKube simulates Deployments (rolled out unless the image is broken) and Jobs.
type fakeKube struct {
	mu         sync.Mutex
	deploys    map[string]map[string]any
	jobs       map[string]map[string]any
	patches    []string
	broken     map[string]bool
	jobFails   bool
	createdJob map[string]any
}

func (k *fakeKube) Namespace() string { return "obs" }

func deploymentJSON(name, image string) map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(fmt.Sprintf(`{"metadata":{"name":%q,"generation":1},
	"spec":{"replicas":2,"selector":{"matchLabels":{"app.kubernetes.io/component":%q}},
	"template":{"metadata":{"labels":{"app.kubernetes.io/component":%q}},
	"spec":{"serviceAccountName":"openlog","securityContext":{"runAsNonRoot":true},
	"affinity":{"podAntiAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":[]}},
	"containers":[{"name":%q,"image":%q,"ports":[{"containerPort":8080}],
	"env":[{"name":"OPENLOG_CLICKHOUSE_ADDR","value":"ch:9000"},{"name":"OPENLOG_MIGRATE_SKIP_KAFKA","value":"false"}],
	"securityContext":{"readOnlyRootFilesystem":true},"volumeMounts":[{"name":"tmp","mountPath":"/tmp"}]},
	{"name":"sidecar","image":"envoyproxy/envoy:v1"}],
	"volumes":[{"name":"tmp","emptyDir":{}}]}}}}`, name, name, name, strings.TrimPrefix(name, "rel-"), image)), &m)
	return m
}

func (k *fakeKube) Do(_ context.Context, method, path, contentType string, body, out any) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	const dp, jp = "/apis/apps/v1/namespaces/obs/deployments/", "/apis/batch/v1/namespaces/obs/jobs"
	write := func(v any) error {
		if out == nil {
			return nil
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	switch {
	case strings.HasPrefix(path, dp) && method == http.MethodGet:
		d := k.deploys[strings.TrimPrefix(path, dp)]
		if d == nil {
			return &KubeError{Status: 404}
		}
		d = deepCopy(d)
		gen := dig(d, "metadata", "generation")
		img := dig(d, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["image"].(string)
		avail := 2
		if k.broken[img] {
			avail = 0
		}
		d["status"] = map[string]any{"observedGeneration": gen, "replicas": 2, "updatedReplicas": 2, "availableReplicas": avail, "unavailableReplicas": 2 - avail}
		return write(d)
	case strings.HasPrefix(path, dp) && method == http.MethodPatch:
		if contentType != "application/strategic-merge-patch+json" {
			return &KubeError{Status: 415}
		}
		name := strings.TrimPrefix(path, dp)
		d := k.deploys[name]
		var patch map[string]any
		roundTrip(body, &patch)
		for _, pc := range dig(patch, "spec", "template", "spec", "containers").([]any) {
			pm := pc.(map[string]any)
			for _, c := range dig(d, "spec", "template", "spec", "containers").([]any) {
				if cm := c.(map[string]any); cm["name"] == pm["name"] {
					cm["image"] = pm["image"]
					k.patches = append(k.patches, fmt.Sprintf("%s/%s=%s", name, pm["name"], pm["image"]))
				}
			}
		}
		md := d["metadata"].(map[string]any)
		md["generation"] = md["generation"].(float64) + 1
		return nil
	case path == jp && method == http.MethodPost:
		roundTrip(body, &k.createdJob)
		name := dig(k.createdJob, "metadata", "generateName").(string) + "x1"
		k.jobs[name] = k.createdJob
		return write(map[string]any{"metadata": map[string]any{"name": name}})
	case strings.HasPrefix(path, jp+"/") && method == http.MethodGet:
		if k.jobFails {
			return write(map[string]any{"status": map[string]any{"failed": 3, "conditions": []any{map[string]any{"type": "Failed", "status": "True"}}}})
		}
		return write(map[string]any{"status": map[string]any{"succeeded": 1}})
	case strings.HasPrefix(path, "/api/v1/namespaces/obs/pods"):
		if strings.HasSuffix(strings.SplitN(path, "?", 2)[0], "/log") {
			return write("migration 0042 failed: syntax error")
		}
		return write(map[string]any{"items": []any{map[string]any{"metadata": map[string]any{"name": "job-pod"}}}})
	}
	return &KubeError{Status: 404, Message: method + " " + path}
}

func newK8sFixture(t *testing.T) (*fakeKube, *Runner, *memStore, *fakeSource) {
	t.Helper()
	const old = "ghcr.io/onuragtas/openlog:0.9.0"
	fk := &fakeKube{
		deploys: map[string]map[string]any{
			"rel-ingest": deploymentJSON("rel-ingest", old), "rel-processor": deploymentJSON("rel-processor", old), "rel-api": deploymentJSON("rel-api", old),
		},
		jobs: map[string]map[string]any{}, broken: map[string]bool{},
	}
	versions := map[string]string{old: "0.9.0", img091: "0.9.1", img092: "0.9.2"}
	health := func(context.Context, string) (string, bool, error) {
		fk.mu.Lock()
		defer fk.mu.Unlock()
		img := dig(fk.deploys["rel-api"], "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["image"].(string)
		return versions[img], !fk.broken[img], nil
	}
	cfg, err := LoadConfig(func(k string) string {
		return map[string]string{
			"OPENLOG_UPDATER_MODE": "auto", "OPENLOG_UPDATER_K8S_DEPLOYMENTS": "rel-ingest,rel-processor,rel-api",
			"OPENLOG_UPDATER_K8S_MIGRATE_TEMPLATE": "rel-api", "OPENLOG_UPDATER_VERSION_URL": "http://rel-api:8080/api/v1/auth/config",
			"OPENLOG_UPDATER_ROLLOUT_TIMEOUT": "200ms", "OPENLOG_UPDATER_HEALTH_TIMEOUT": "200ms",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := &K8sEngine{Cfg: cfg, Kube: fk, Log: log, Health: health, Poll: 10 * time.Millisecond}
	src := &fakeSource{
		channels: map[string][]string{"stable": {"0.9.1"}},
		manifests: map[string]string{
			"0.9.1": manifestJSON("0.9.1", "stable", "", img091),
			"0.9.2": manifestJSON("0.9.2", "stable", "", img092),
		},
	}
	store := &memStore{}
	return fk, &Runner{Cfg: cfg, Engine: eng, Source: src, Store: store, Log: log}, store, src
}

func TestK8sUpdateAndRollback(t *testing.T) {
	fk, r, store, src := newK8sFixture(t)
	ctx := context.Background()
	if err := r.RunOnce(ctx); err != nil {
		t.Fatalf("%v %+v", err, store.st)
	}
	if store.st.State != StateSucceeded {
		t.Fatalf("status %+v", store.st)
	}
	if want := "rel-ingest/ingest=" + img091 + " rel-processor/processor=" + img091 + " rel-api/api=" + img091; strings.Join(fk.patches, " ") != want {
		t.Errorf("patches %v", fk.patches)
	}
	// The migrate Job reuses the api pod settings but not its labels, affinity or sidecars.
	job := fk.createdJob
	pod := dig(job, "spec", "template", "spec").(map[string]any)
	containers := pod["containers"].([]any)
	c := containers[0].(map[string]any)
	switch {
	case len(containers) != 1 || c["image"] != img091 || c["command"].([]any)[0] != "/usr/local/bin/openlog-migrate":
		t.Errorf("job container %v", containers)
	case pod["restartPolicy"] != "Never" || pod["affinity"] != nil || pod["serviceAccountName"] != "openlog":
		t.Errorf("job pod %v", pod)
	case dig(job, "spec", "template", "metadata", "labels").(map[string]any)["app.kubernetes.io/component"] != "updater-migrate":
		t.Errorf("job labels must not match the api Service selector: %v", dig(job, "spec", "template", "metadata"))
	case c["ports"] != nil || c["volumeMounts"] == nil:
		t.Errorf("job container fields %v", c)
	}
	env, _ := json.Marshal(c["env"])
	if !strings.Contains(string(env), `{"name":"OPENLOG_MIGRATE_SKIP_KAFKA","value":"true"}`) || strings.Contains(string(env), `"false"`) {
		t.Errorf("env %s", env)
	}

	src.channels["stable"] = append(src.channels["stable"], "0.9.2")
	fk.broken[img092] = true
	fk.patches = nil
	if err := r.RunOnce(ctx); err == nil {
		t.Fatal("expected failure")
	}
	if store.st.State != StateRolledBack || !strings.Contains(store.st.Error, "not rolled out") {
		t.Fatalf("status %+v", store.st)
	}
	if got := strings.Join(fk.patches, " "); !strings.HasSuffix(got, "rel-api/api="+img091) || !strings.Contains(got, "rel-ingest/ingest="+img092) {
		t.Errorf("patches %v", fk.patches)
	}
}

func TestK8sMigrateJobFailure(t *testing.T) {
	fk, r, store, _ := newK8sFixture(t)
	fk.jobFails = true
	if err := r.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("err %v", err)
	}
	if store.st.State != StateFailed || len(fk.patches) != 0 {
		t.Errorf("status %+v patches %v", store.st, fk.patches)
	}
}

func TestKubeClientAndDockerDemux(t *testing.T) {
	tok := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(tok, []byte("secret-token\n"), 0o600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodPatch {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"kind":"Status","message":"image is invalid"}`))
			return
		}
		_, _ = w.Write([]byte(`{"metadata":{"name":"x"}}`))
	}))
	defer srv.Close()
	kc := NewKubeClient(srv.URL, "obs", tok, srv.Client())
	var out map[string]any
	if err := kc.Do(context.Background(), http.MethodGet, "/x", "", nil, &out); err != nil || dig(out, "metadata", "name") != "x" {
		t.Fatalf("get: %v %v", err, out)
	}
	if err := kc.Do(context.Background(), http.MethodPatch, "/x", "application/merge-patch+json", map[string]any{}, nil); err == nil || !strings.Contains(err.Error(), "image is invalid") {
		t.Errorf("patch error %v", err)
	}

	var stream bytes.Buffer
	frame := func(kind byte, s string) {
		h := make([]byte, 8)
		h[0] = kind
		binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
		stream.Write(h)
		stream.WriteString(s)
	}
	frame(1, "out1 ")
	frame(2, "err1")
	frame(1, "out2")
	var o, e bytes.Buffer
	if err := demux(&stream, &o, &e); err != nil || o.String() != "out1 out2" || e.String() != "err1" {
		t.Errorf("demux %q %q %v", o.String(), e.String(), err)
	}
}
