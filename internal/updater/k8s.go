package updater

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// Kube is the subset of the Kubernetes REST API used by the Kubernetes engine.
type Kube interface {
	Namespace() string
	Do(ctx context.Context, method, path, contentType string, body, out any) error
}

// KubeClient is a minimal in-cluster Kubernetes REST client (service account token + CA).
type KubeClient struct {
	base      string
	namespace string
	tokenFile string
	http      *http.Client
}

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// InClusterKube creates a client from the pod's service account.
func InClusterKube() (*KubeClient, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not running in a Kubernetes pod (KUBERNETES_SERVICE_HOST is not set)")
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("service account ca.crt has no certificates")
	}
	ns, err := os.ReadFile(saDir + "/namespace")
	if err != nil {
		return nil, err
	}
	return &KubeClient{
		base:      "https://" + net.JoinHostPort(host, port),
		namespace: strings.TrimSpace(string(ns)),
		tokenFile: saDir + "/token",
		http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}},
	}, nil
}

// NewKubeClient creates a client for tests or out-of-cluster use.
func NewKubeClient(base, namespace, tokenFile string, client *http.Client) *KubeClient {
	return &KubeClient{base: strings.TrimRight(base, "/"), namespace: namespace, tokenFile: tokenFile, http: client}
}

// Namespace implements Kube.
func (k *KubeClient) Namespace() string { return k.namespace }

// KubeError is a non-2xx response.
type KubeError struct {
	Status  int
	Message string
}

func (e *KubeError) Error() string { return fmt.Sprintf("kubernetes: %d %s", e.Status, e.Message) }

// Do implements Kube. The token is re-read on every call (projected tokens rotate).
func (k *KubeClient) Do(ctx context.Context, method, path, contentType string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if k.tokenFile != "" {
		if tok, err := os.ReadFile(k.tokenFile); err == nil {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
		}
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var st struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &st) != nil || st.Message == "" {
			st.Message = strings.TrimSpace(string(b))
		}
		return &KubeError{Status: resp.StatusCode, Message: st.Message}
	}
	if out == nil {
		return nil
	}
	if s, ok := out.(*string); ok {
		*s = string(b)
		return nil
	}
	return json.Unmarshal(b, out)
}

// K8sEngine updates the chart's Deployments: migrate Job → image patch (rolling update) →
// rollout watch → health; on failure the previous images are patched back.
type K8sEngine struct {
	Cfg    Config
	Kube   Kube
	Log    *slog.Logger
	Health HealthFunc
	Poll   time.Duration
}

// Name implements Engine.
func (e *K8sEngine) Name() string { return "kubernetes" }

func (e *K8sEngine) poll() time.Duration {
	if e.Poll > 0 {
		return e.Poll
	}
	return 5 * time.Second
}

// Recover implements Engine. Deployments are declarative, nothing to recover.
func (e *K8sEngine) Recover(context.Context) error { return nil }

// CurrentVersion implements Engine.
func (e *K8sEngine) CurrentVersion(ctx context.Context) (string, error) {
	if e.Health == nil {
		e.Health = HTTPHealth(&http.Client{Timeout: 5 * time.Second})
	}
	v, _, err := e.Health(ctx, e.Cfg.VersionURL)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", fmt.Errorf("%s does not report a version", e.Cfg.VersionURL)
	}
	return v, nil
}

type k8sDeployment struct {
	Metadata struct {
		Name       string `json:"name"`
		Generation int64  `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int32 `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration  int64 `json:"observedGeneration"`
		Replicas            int32 `json:"replicas"`
		UpdatedReplicas     int32 `json:"updatedReplicas"`
		AvailableReplicas   int32 `json:"availableReplicas"`
		UnavailableReplicas int32 `json:"unavailableReplicas"`
		Conditions          []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

func (e *K8sEngine) deployPath(name string) string {
	return "/apis/apps/v1/namespaces/" + url.PathEscape(e.Kube.Namespace()) + "/deployments/" + url.PathEscape(name)
}

func (e *K8sEngine) getDeployment(ctx context.Context, name string) (*k8sDeployment, map[string]any, error) {
	var raw map[string]any
	if err := e.Kube.Do(ctx, http.MethodGet, e.deployPath(name), "", nil, &raw); err != nil {
		return nil, nil, err
	}
	b, _ := json.Marshal(raw)
	var d k8sDeployment
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, nil, err
	}
	return &d, raw, nil
}

// repository strips the tag or digest of an image reference.
func repository(ref string) string {
	r, _ := splitRef(ref)
	return r
}

// Apply implements Engine.
func (e *K8sEngine) Apply(ctx context.Context, t *Target, st *Status, save func()) error {
	step := func(name string, fn func() (string, error)) error {
		st.beginStep(name, time.Now().UTC())
		save()
		detail, err := fn()
		st.endStep(err, detail, time.Now().UTC())
		save()
		return errStep(name, err)
	}
	if len(e.Cfg.Deployments) == 0 {
		st.State = StateFailed
		return errors.New("OPENLOG_UPDATER_K8S_DEPLOYMENTS is empty")
	}
	tmplName := e.Cfg.MigrateTemplate
	if tmplName == "" {
		tmplName = e.Cfg.Deployments[0]
	}
	tmpl, tmplRaw, err := e.getDeployment(ctx, tmplName)
	if err != nil || len(tmpl.Spec.Template.Spec.Containers) == 0 {
		st.State = StateFailed
		return fmt.Errorf("read deployment %s: %w", tmplName, err)
	}
	openlogRepo := repository(tmpl.Spec.Template.Spec.Containers[0].Image)

	if err := step("migrate", func() (string, error) { return e.runMigrateJob(ctx, tmplRaw, openlogRepo, t) }); err != nil {
		st.State = StateFailed
		return err
	}

	previous := map[string]map[string]string{} // deployment -> container -> image
	applyErr := step("rollout", func() (string, error) {
		for _, name := range e.Cfg.Deployments {
			d, _, err := e.getDeployment(ctx, name)
			if err != nil {
				return "", err
			}
			prev := map[string]string{}
			var containers []map[string]string
			for _, c := range d.Spec.Template.Spec.Containers {
				if repository(c.Image) == openlogRepo || repository(c.Image) == repository(t.Image) {
					prev[c.Name] = c.Image
					containers = append(containers, map[string]string{"name": c.Name, "image": t.Image})
				}
			}
			if len(containers) == 0 {
				continue
			}
			previous[name] = prev
			if err := e.patchImages(ctx, name, containers); err != nil {
				return "", fmt.Errorf("patch %s: %w", name, err)
			}
		}
		for _, name := range e.Cfg.Deployments {
			if _, ok := previous[name]; !ok {
				continue
			}
			if err := e.waitRollout(ctx, name, e.Cfg.RolloutTimeout); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("%d deployment(s)", len(previous)), nil
	})
	if applyErr == nil {
		applyErr = step("health", func() (string, error) { return "", e.waitVersion(ctx, t.Version.String()) })
	}
	if applyErr == nil {
		return nil
	}
	rbErr := step("rollback", func() (string, error) {
		// Roll back in the configured deployment order (same as the rollout) and in container-name
		// order, so the sequence of patches is deterministic.
		var errs []error
		for _, name := range e.Cfg.Deployments {
			prev, ok := previous[name]
			if !ok {
				continue
			}
			var containers []map[string]string
			for _, c := range slices.Sorted(maps.Keys(prev)) {
				containers = append(containers, map[string]string{"name": c, "image": prev[c]})
			}
			if err := e.patchImages(ctx, name, containers); err != nil {
				errs = append(errs, fmt.Errorf("patch %s: %w", name, err))
			}
		}
		for _, name := range e.Cfg.Deployments {
			if _, ok := previous[name]; !ok {
				continue
			}
			if err := e.waitRollout(ctx, name, e.Cfg.RolloutTimeout); err != nil {
				errs = append(errs, err)
			}
		}
		return "", errors.Join(errs...)
	})
	st.State = StateRolledBack
	if rbErr != nil {
		st.State = StateRollbackFailed
		return fmt.Errorf("%w; %w", applyErr, rbErr)
	}
	return applyErr
}

func (e *K8sEngine) patchImages(ctx context.Context, name string, containers []map[string]string) error {
	patch := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": containers}}}}
	return e.Kube.Do(ctx, http.MethodPatch, e.deployPath(name), "application/strategic-merge-patch+json", patch, nil)
}

// waitRollout waits until the deployment has rolled out its current generation.
func (e *K8sEngine) waitRollout(ctx context.Context, name string, timeout time.Duration) error {
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	last := "not observed yet"
	for {
		d, _, err := e.getDeployment(wctx, name)
		if err == nil {
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			for _, c := range d.Status.Conditions {
				if c.Type == "Progressing" && c.Status == "False" && c.Reason == "ProgressDeadlineExceeded" {
					return fmt.Errorf("deployment %s: %s", name, c.Message)
				}
			}
			if d.Status.ObservedGeneration >= d.Metadata.Generation && d.Status.UpdatedReplicas == want &&
				d.Status.Replicas == want && d.Status.AvailableReplicas == want && d.Status.UnavailableReplicas == 0 {
				return nil
			}
			last = fmt.Sprintf("%d/%d updated, %d available", d.Status.UpdatedReplicas, want, d.Status.AvailableReplicas)
		} else {
			last = err.Error()
		}
		select {
		case <-wctx.Done():
			return fmt.Errorf("deployment %s not rolled out within %s: %s", name, timeout, last)
		case <-time.After(e.poll()):
		}
	}
}

func (e *K8sEngine) waitVersion(ctx context.Context, want string) error {
	wctx, cancel := context.WithTimeout(ctx, e.Cfg.HealthTimeout)
	defer cancel()
	var last error
	for {
		v, ready, err := e.Health(wctx, e.Cfg.VersionURL)
		switch {
		case err != nil:
			last = err
		case !ready:
			last = fmt.Errorf("%s not ready", e.Cfg.VersionURL)
		case !sameVersion(v, want):
			last = fmt.Errorf("%s reports %q, want %s", e.Cfg.VersionURL, v, want)
		default:
			return nil
		}
		select {
		case <-wctx.Done():
			return fmt.Errorf("not healthy within %s: %w", e.Cfg.HealthTimeout, last)
		case <-time.After(e.poll()):
		}
	}
}

// migrateJob builds the Job running openlog-migrate with the pod settings (service account,
// security context, env, volumes) of the template Deployment.
func migrateJob(tmplRaw map[string]any, openlogRepo string, t *Target) (map[string]any, error) {
	spec, _ := dig(tmplRaw, "spec", "template", "spec").(map[string]any)
	if spec == nil {
		return nil, errors.New("template deployment has no pod spec")
	}
	pod := deepCopy(spec)
	var src map[string]any
	containers, _ := pod["containers"].([]any)
	for _, c := range containers {
		cm, _ := c.(map[string]any)
		if img, _ := cm["image"].(string); cm != nil && repository(img) == openlogRepo {
			src = cm
			break
		}
	}
	if src == nil {
		return nil, fmt.Errorf("template deployment has no %s container", openlogRepo)
	}
	c := map[string]any{"name": "migrate", "image": t.Image, "command": []string{"/usr/local/bin/openlog-migrate"}}
	for _, k := range []string{"envFrom", "securityContext", "volumeMounts", "resources", "imagePullPolicy"} {
		if v, ok := src[k]; ok {
			c[k] = v
		}
	}
	// Topics exist on an installed release; operator-managed topics must not be touched.
	env := []any{}
	envList, _ := src["env"].([]any)
	for _, ev := range envList {
		if m, _ := ev.(map[string]any); m != nil && m["name"] == "OPENLOG_MIGRATE_SKIP_KAFKA" {
			continue
		}
		env = append(env, ev)
	}
	c["env"] = append(env, map[string]any{"name": "OPENLOG_MIGRATE_SKIP_KAFKA", "value": "true"})
	pod["containers"] = []any{c}
	pod["restartPolicy"] = "Never"
	for _, k := range []string{"initContainers", "affinity", "topologySpreadConstraints", "terminationGracePeriodSeconds"} {
		delete(pod, k)
	}
	labels := map[string]any{"app.kubernetes.io/part-of": "openlog", "app.kubernetes.io/component": "updater-migrate",
		"openlog.io/target-version": safeName(t.Version.String())}
	prefix, _ := dig(tmplRaw, "metadata", "name").(string)
	return map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata":   map[string]any{"generateName": truncate(prefix, 40) + "-updater-migrate-", "labels": labels},
		"spec": map[string]any{
			"backoffLimit":            2,
			"ttlSecondsAfterFinished": 86400,
			"template":                map[string]any{"metadata": map[string]any{"labels": labels}, "spec": pod},
		},
	}, nil
}

func (e *K8sEngine) runMigrateJob(ctx context.Context, tmplRaw map[string]any, openlogRepo string, t *Target) (string, error) {
	job, err := migrateJob(tmplRaw, openlogRepo, t)
	if err != nil {
		return "", err
	}
	jobs := "/apis/batch/v1/namespaces/" + url.PathEscape(e.Kube.Namespace()) + "/jobs"
	var created struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := e.Kube.Do(ctx, http.MethodPost, jobs, "", job, &created); err != nil {
		return "", fmt.Errorf("create migrate job: %w", err)
	}
	name := created.Metadata.Name
	wctx, cancel := context.WithTimeout(ctx, e.Cfg.MigrateTimeout)
	defer cancel()
	for {
		var j struct {
			Spec struct {
				BackoffLimit int32 `json:"backoffLimit"`
			} `json:"spec"`
			Status struct {
				Succeeded  int32 `json:"succeeded"`
				Failed     int32 `json:"failed"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		}
		if err := e.Kube.Do(wctx, http.MethodGet, jobs+"/"+url.PathEscape(name), "", nil, &j); err == nil {
			if j.Status.Succeeded > 0 {
				return "job " + name, nil
			}
			for _, c := range j.Status.Conditions {
				if c.Type == "Failed" && c.Status == "True" {
					return "", fmt.Errorf("migrate job %s failed: %s", name, e.jobLogs(ctx, name))
				}
			}
		}
		select {
		case <-wctx.Done():
			return "", fmt.Errorf("migrate job %s did not finish within %s", name, e.Cfg.MigrateTimeout)
		case <-time.After(e.poll()):
		}
	}
}

func (e *K8sEngine) jobLogs(ctx context.Context, job string) string {
	ns := url.PathEscape(e.Kube.Namespace())
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	q := url.Values{"labelSelector": {"job-name=" + job}}
	if err := e.Kube.Do(ctx, http.MethodGet, "/api/v1/namespaces/"+ns+"/pods?"+q.Encode(), "", nil, &pods); err != nil || len(pods.Items) == 0 {
		return "no pod logs"
	}
	var logs string
	p := pods.Items[len(pods.Items)-1].Metadata.Name
	if err := e.Kube.Do(ctx, http.MethodGet, "/api/v1/namespaces/"+ns+"/pods/"+url.PathEscape(p)+"/log?tailLines=10", "", nil, &logs); err != nil {
		return "no pod logs: " + err.Error()
	}
	return lastLines(logs, 10)
}

func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func truncate(s string, n int) string {
	if len(s) > n {
		return strings.TrimRight(s[:n], "-")
	}
	return s
}
