// Package chrome renders print pages of the web app with headless Chromium over the DevTools protocol (chromedp).
// It is imported only by cmd/openlog-renderer, so the api binaries do not link the DevTools protocol packages.
//
// Every render starts its own Chromium process with a fresh temporary profile (no state shared between renders,
// crash and memory isolation) and kills it afterwards. Network access of the page is restricted twice: DNS resolves
// only the UI origin's host (--host-resolver-rules), and every HTTP(S) request is paused (Fetch domain) and failed
// unless its origin is the UI origin. Deployments add a NetworkPolicy / internal network (docs/operations/reports.md).
package chrome

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/onuragtas/openlog/internal/renderer"
)

// Options configure Chromium.
type Options struct {
	// ExecPath is the Chromium binary ("" = search PATH for chromium, chromium-browser, google-chrome).
	ExecPath string
	// NoSandbox disables Chromium's sandbox. Only for containers that cannot create the sandbox's namespaces and are
	// locked down otherwise (non-root, read-only root filesystem, no capabilities, egress restricted).
	NoSandbox bool
	// Origin is the only origin pages may load resources from (OPENLOG_RENDERER_UI_ORIGIN).
	Origin string
	// SettleDelay is waited after the page reports ready (chart layout; default 250 ms).
	SettleDelay time.Duration
	// JSHeapMB caps the V8 heap per renderer process (default 512).
	JSHeapMB int
	Log      *slog.Logger
}

// Chrome implements renderer.Browser.
type Chrome struct {
	o      Options
	origin *url.URL
}

var _ renderer.Browser = (*Chrome)(nil)

// New validates the options.
func New(o Options) (*Chrome, error) {
	if err := renderer.CheckOrigin(o.Origin); err != nil {
		return nil, err
	}
	u, _ := url.Parse(o.Origin)
	if o.SettleDelay <= 0 {
		o.SettleDelay = 250 * time.Millisecond
	}
	if o.JSHeapMB <= 0 {
		o.JSHeapMB = 512
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Chrome{o: o, origin: u}, nil
}

// sameOrigin reports whether raw has the UI origin's scheme, host and port.
func (c *Chrome) sameOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, c.origin.Scheme) && strings.EqualFold(u.Host, c.origin.Host)
}

func (c *Chrome) allocatorOptions(profile string, req renderer.Request) []chromedp.ExecAllocatorOption {
	host := c.origin.Hostname()
	opts := []chromedp.ExecAllocatorOption{
		chromedp.UserDataDir(profile),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-breakpad", true),
		chromedp.Flag("disable-domain-reliability", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("no-pings", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("deny-permission-prompts", true),
		chromedp.Flag("disable-features", "Translate,MediaRouter,OptimizationHints,AutofillServerCommunication,CalculateNativeWinOcclusion"),
		// Containers have a small /dev/shm.
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("renderer-process-limit", "1"), // chromedp flags take bool or string values
		chromedp.Flag("js-flags", fmt.Sprintf("--max-old-space-size=%d", c.o.JSHeapMB)),
		// No proxy; DNS only for the UI host (IP literal origins need no resolution).
		chromedp.Flag("no-proxy-server", true),
		chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND , EXCLUDE "+host),
		chromedp.WindowSize(req.Width, 800),
	}
	if net.ParseIP(host) != nil {
		opts[len(opts)-2] = chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND")
	}
	if c.o.ExecPath != "" {
		opts = append(opts, chromedp.ExecPath(c.o.ExecPath))
	}
	if c.o.NoSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	return opts
}

type element struct {
	ID    string  `json:"id"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Error string  `json:"error"`
}

type layout struct {
	State    string    `json:"state"`
	Message  string    `json:"message"`
	Height   float64   `json:"height"`
	Elements []element `json:"elements"`
}

const layoutJS = `(() => {
  const root = document.documentElement;
  const elements = [];
  document.querySelectorAll("[data-render-id]").forEach((el) => {
    const r = el.getBoundingClientRect();
    elements.push({ id: String(el.dataset.renderId || ""), x: r.left + window.scrollX, y: r.top + window.scrollY,
      w: r.width, h: r.height, error: String(el.dataset.renderError || "") });
  });
  return { state: String(root.dataset.renderState || ""), message: String(root.dataset.renderMessage || "").slice(0, 200),
    height: Math.ceil(root.scrollHeight), elements };
})()`

// Render opens pageURL and captures every element marked data-render-id once the page sets
// document.documentElement.dataset.renderState to "ready" ("error" fails the render).
func (c *Chrome) Render(ctx context.Context, pageURL string, req renderer.Request) (*renderer.Response, error) {
	if !c.sameOrigin(pageURL) {
		return nil, errors.New("page URL is not on the UI origin")
	}
	profile, err := os.MkdirTemp("", "openlog-renderer-")
	if err != nil {
		return nil, fmt.Errorf("profile directory: %w", err)
	}
	defer os.RemoveAll(profile)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, c.allocatorOptions(profile, req)...)
	defer cancelAlloc()
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	defer cancelTab()

	var blocked atomic.Int64
	chromedp.ListenTarget(tabCtx, func(ev any) {
		e, ok := ev.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		go func() {
			if c.sameOrigin(e.Request.URL) {
				_ = chromedp.Run(tabCtx, fetch.ContinueRequest(e.RequestID))
				return
			}
			blocked.Add(1)
			_ = chromedp.Run(tabCtx, fetch.FailRequest(e.RequestID, network.ErrorReasonBlockedByClient))
		}()
	})

	scale := req.Scale
	if err := chromedp.Run(tabCtx,
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}}),
		emulation.SetDeviceMetricsOverride(int64(req.Width), 800, scale, false),
		emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "light"},
			{Name: "prefers-reduced-motion", Value: "reduce"}}),
		chromedp.Navigate(pageURL),
	); err != nil {
		return nil, fmt.Errorf("load page: %w", err)
	}
	var ready bool
	if err := chromedp.Run(tabCtx, chromedp.Poll(`["ready", "error"].includes(document.documentElement.dataset.renderState || "")`,
		&ready, chromedp.WithPollingInterval(100*time.Millisecond))); err != nil {
		return nil, fmt.Errorf("wait for the page: %w", err)
	}
	select {
	case <-time.After(c.o.SettleDelay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var l layout
	if err := chromedp.Run(tabCtx, chromedp.Evaluate(layoutJS, &l)); err != nil {
		return nil, fmt.Errorf("read layout: %w", err)
	}
	if l.State == "error" {
		return nil, fmt.Errorf("the page reported an error: %s", l.Message)
	}
	if n := blocked.Load(); n > 0 {
		c.o.Log.Info("blocked requests outside the UI origin", "count", n)
	}

	// Grow the viewport to the page so every element is laid out at its final size.
	height := int64(math.Min(math.Max(l.Height, 1), 16384))
	if err := chromedp.Run(tabCtx, emulation.SetDeviceMetricsOverride(int64(req.Width), height, scale, false)); err != nil {
		return nil, fmt.Errorf("resize viewport: %w", err)
	}
	if err := chromedp.Run(tabCtx, chromedp.Evaluate(layoutJS, &l)); err != nil {
		return nil, fmt.Errorf("read layout: %w", err)
	}

	out := &renderer.Response{Images: []renderer.Image{}, Errors: []renderer.ElementError{}}
	for _, el := range l.Elements {
		if len(out.Images)+len(out.Errors) >= renderer.MaxImages {
			break
		}
		if !renderer.ValidElementID(el.ID) {
			continue
		}
		switch {
		case el.Error != "":
			out.Errors = append(out.Errors, renderer.ElementError{ID: el.ID, Error: "the widget could not be loaded"})
			continue
		case el.W < 1 || el.H < 1 || el.H > renderer.MaxElementHeight:
			out.Errors = append(out.Errors, renderer.ElementError{ID: el.ID, Error: "the widget has no printable size"})
			continue
		}
		var buf []byte
		clip := &page.Viewport{X: math.Floor(el.X), Y: math.Floor(el.Y), Width: math.Ceil(el.W), Height: math.Ceil(el.H), Scale: 1}
		if err := chromedp.Run(tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			buf, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).WithClip(clip).WithCaptureBeyondViewport(true).Do(ctx)
			return err
		})); err != nil {
			return nil, fmt.Errorf("capture %s: %w", el.ID, err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(buf))
		if err != nil {
			out.Errors = append(out.Errors, renderer.ElementError{ID: el.ID, Error: "invalid capture"})
			continue
		}
		out.Images = append(out.Images, renderer.Image{ID: el.ID, Width: cfg.Width, Height: cfg.Height, PNG: buf})
	}
	return out, nil
}
