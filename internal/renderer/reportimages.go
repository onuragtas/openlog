package renderer

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/dashboard/report"
)

// PrintPath is the web app's print view of a report's dashboard (web/src/routes/print-dashboard.tsx).
const PrintPath = "/print/dashboard"

// Renderer is the render API (Client, or a fake in tests).
type Renderer interface {
	Render(ctx context.Context, req Request) (*Response, error)
}

// ReportImages implements report.Imager: it signs a render token for the report period and asks the renderer for the
// widget images of the print view.
type ReportImages struct {
	Renderer Renderer
	// Key signs render tokens (KeyFromSecret); every api pod verifies them with the same key.
	Key []byte
	// TokenTTL is the render token lifetime (default 5 minutes, at most MaxTokenTTL).
	TokenTTL time.Duration
	Now      func() time.Time
}

var _ report.Imager = (*ReportImages)(nil)

// Images renders the widgets of a report's dashboard for [from, to).
func (ri *ReportImages) Images(ctx context.Context, sr *dashboard.ScheduledReport, from, to time.Time) (map[string]report.Image, error) {
	if ri.Renderer == nil {
		return nil, errors.New("no renderer")
	}
	now := time.Now
	if ri.Now != nil {
		now = ri.Now
	}
	ttl := ri.TokenTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	token, err := NewReportToken(ri.Key, Claims{OrgID: sr.OrgID, TenantID: sr.TenantID, DashboardID: sr.DashboardID, ReportID: sr.ID,
		From: from.UnixMilli(), To: to.UnixMilli()}, now(), ttl)
	if err != nil {
		return nil, err
	}
	res, err := ri.Renderer.Render(ctx, Request{Path: PrintPath, Token: token, Width: DefaultWidth, Scale: DefaultScale})
	if err != nil {
		return nil, err
	}
	out := make(map[string]report.Image, len(res.Images))
	for _, img := range res.Images {
		if !ValidElementID(img.ID) || len(img.PNG) == 0 {
			continue
		}
		// Trust nothing about the payload but valid PNG dimensions.
		cfg, err := png.DecodeConfig(bytes.NewReader(img.PNG))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			continue
		}
		w := min(cfg.Width, int(float64(cfg.Width)/DefaultScale+0.5))
		out[img.ID] = report.Image{PNG: img.PNG, Width: max(w, 1), Height: max(int(float64(cfg.Height)/DefaultScale+0.5), 1)}
	}
	return out, nil
}
