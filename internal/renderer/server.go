package renderer

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Request limits of the render API.
const (
	DefaultWidth  = 720
	DefaultScale  = 2.0
	MinWidth      = 320
	MaxWidth      = 1600
	MaxScale      = 3.0
	maxRequestLen = 8 << 10
	// MaxImages bounds the elements captured by one render.
	MaxImages = 60
	// MaxElementHeight bounds the CSS height of one captured element.
	MaxElementHeight = 4000
)

var (
	// pathRe: only the web app's print routes can be rendered (no scheme, host, query, fragment or dot segments).
	pathRe = regexp.MustCompile(`^/print/[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$`)
	idRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// Request asks for PNG images of the elements marked data-render-id on a print page of the web app.
type Request struct {
	// Path is the UI path, e.g. "/print/dashboard"; the renderer prefixes its configured UI origin.
	Path string `json:"path"`
	// Token is the short-lived render token; it is passed in the URL fragment (never sent in request lines or logs).
	Token string `json:"token"`
	// Width is the CSS viewport width (default 720), Scale the device pixel ratio (default 2).
	Width int     `json:"width,omitempty"`
	Scale float64 `json:"scale,omitempty"`
}

// Image is one captured element (width and height in device pixels).
type Image struct {
	ID     string `json:"id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	PNG    []byte `json:"png"` // base64 in JSON
}

// ElementError is an element that was not captured.
type ElementError struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// Response is the result of a render.
type Response struct {
	Images []Image        `json:"images"`
	Errors []ElementError `json:"errors"`
}

// Browser renders a page (Chrome in internal/renderer/chrome; fakes in tests).
type Browser interface {
	Render(ctx context.Context, pageURL string, req Request) (*Response, error)
}

// Normalize applies defaults and validates a request.
func (r *Request) Normalize() error {
	if r.Width == 0 {
		r.Width = DefaultWidth
	}
	if r.Scale == 0 {
		r.Scale = DefaultScale
	}
	switch {
	case !pathRe.MatchString(r.Path):
		return errors.New("path must be a print route of the web app such as /print/dashboard")
	case !strings.HasPrefix(r.Token, TokenPrefix) || len(r.Token) > maxTokenLen || !tokenRe.MatchString(r.Token):
		return errors.New("token must be a render token")
	case r.Width < MinWidth || r.Width > MaxWidth:
		return fmt.Errorf("width must be %d-%d", MinWidth, MaxWidth)
	case r.Scale < 1 || r.Scale > MaxScale:
		return fmt.Errorf("scale must be 1-%g", MaxScale)
	}
	return nil
}

// PageURL returns the URL the browser opens: origin + path, token in the fragment.
func (r Request) PageURL(origin string) string {
	return strings.TrimRight(origin, "/") + r.Path + "#token=" + url.QueryEscape(r.Token)
}

// ValidElementID reports whether id can be an element id of a render response.
func ValidElementID(id string) bool { return idRe.MatchString(id) }

// ServerOptions configure the render API.
type ServerOptions struct {
	// Token is the shared secret callers send as "Authorization: Bearer <token>" (OPENLOG_RENDERER_TOKEN).
	Token string
	// Origin is the web UI origin pages are loaded from (OPENLOG_RENDERER_UI_ORIGIN).
	Origin string
	// MaxConcurrency bounds simultaneous renders (each runs its own Chromium); QueueTimeout is how long a request
	// waits for a slot before 429.
	MaxConcurrency int
	QueueTimeout   time.Duration
	// RenderTimeout bounds one render (browser start, page load, captures).
	RenderTimeout time.Duration
	// MaxImageBytes bounds one PNG; larger captures are reported as element errors.
	MaxImageBytes int
	Browser       Browser
	Log           *slog.Logger
	Registerer    prometheus.Registerer
}

// Server is the internal render API of openlog-renderer.
type Server struct {
	o        ServerOptions
	token    [32]byte
	slots    chan struct{}
	renders  *prometheus.CounterVec
	duration prometheus.Histogram
}

// NewServer validates the options and returns the API.
func NewServer(o ServerOptions) (*Server, error) {
	if len(o.Token) < 32 {
		return nil, errors.New("OPENLOG_RENDERER_TOKEN must be at least 32 characters")
	}
	if err := CheckOrigin(o.Origin); err != nil {
		return nil, fmt.Errorf("OPENLOG_RENDERER_UI_ORIGIN: %w", err)
	}
	if o.Browser == nil {
		return nil, errors.New("no browser")
	}
	if o.MaxConcurrency <= 0 {
		o.MaxConcurrency = 2
	}
	if o.QueueTimeout <= 0 {
		o.QueueTimeout = 30 * time.Second
	}
	if o.RenderTimeout <= 0 {
		o.RenderTimeout = 60 * time.Second
	}
	if o.MaxImageBytes <= 0 {
		o.MaxImageBytes = 2 << 20
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	s := &Server{o: o, token: sha256.Sum256([]byte(o.Token)), slots: make(chan struct{}, o.MaxConcurrency),
		renders: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_renderer_renders_total",
			Help: "Render requests by result (ok, invalid, unauthenticated, busy, failed)."}, []string{"result"}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "openlog_renderer_render_duration_seconds",
			Help: "Duration of successful and failed renders.", Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120}}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(s.renders, s.duration)
	}
	return s, nil
}

// CheckOrigin accepts an http(s) origin without path, query, fragment or credentials.
func CheckOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(origin, " \t\r\n#?") {
		return fmt.Errorf("must be an http(s) origin such as http://openlog-api:8080, got %q", origin)
	}
	return nil
}

// Handler returns the HTTP handler: POST /v1/render.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/render", s.render)
	return mux
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, status int, result, code, msg string) {
	s.renders.WithLabelValues(result).Inc()
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg}})
}

func (s *Server) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(got)))
	return subtle.ConstantTimeCompare(sum[:], s.token[:]) == 1
}

func (s *Server) render(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		s.fail(w, http.StatusUnauthorized, "unauthenticated", "unauthenticated", "missing or invalid renderer token")
		return
	}
	var req Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestLen))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.fail(w, http.StatusBadRequest, "invalid", "invalid_argument", "the request body must be a render request JSON object")
		return
	}
	if err := req.Normalize(); err != nil {
		s.fail(w, http.StatusBadRequest, "invalid", "invalid_argument", err.Error())
		return
	}

	wait := time.NewTimer(s.o.QueueTimeout)
	defer wait.Stop()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-wait.C:
		s.fail(w, http.StatusTooManyRequests, "busy", "resource_exhausted", "all render slots are busy; retry later")
		return
	case <-r.Context().Done():
		s.renders.WithLabelValues("busy").Inc()
		return
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), s.o.RenderTimeout)
	defer cancel()
	res, err := s.o.Browser.Render(ctx, req.PageURL(s.o.Origin), req)
	s.duration.Observe(time.Since(start).Seconds())
	if err != nil {
		// The page URL carries the token: log the path only.
		s.o.Log.Warn("render failed", "path", req.Path, "duration", time.Since(start).Round(time.Millisecond), "err", err)
		msg := "the page could not be rendered"
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "the render timed out"
		}
		s.fail(w, http.StatusBadGateway, "failed", "unavailable", msg)
		return
	}
	out := Response{Images: []Image{}, Errors: []ElementError{}}
	for _, img := range res.Images {
		switch {
		case !ValidElementID(img.ID) || len(out.Images)+len(out.Errors) >= MaxImages:
			continue
		case len(img.PNG) > s.o.MaxImageBytes:
			out.Errors = append(out.Errors, ElementError{ID: img.ID, Error: "image too large"})
		default:
			out.Images = append(out.Images, img)
		}
	}
	for _, e := range res.Errors {
		if ValidElementID(e.ID) && len(out.Images)+len(out.Errors) < MaxImages {
			out.Errors = append(out.Errors, e)
		}
	}
	s.renders.WithLabelValues("ok").Inc()
	s.o.Log.Info("rendered", "path", req.Path, "images", len(out.Images), "errors", len(out.Errors), "duration", time.Since(start).Round(time.Millisecond))
	writeJSON(w, http.StatusOK, out)
}
