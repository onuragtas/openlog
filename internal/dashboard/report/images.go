package report

import (
	"context"
	"html/template"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/dashboard"
)

// PNG widget images in report e-mails (D-097, docs/operations/reports.md): when a renderer is configured
// (OPENLOG_RENDERER_URL), the job asks it for images of the report's widgets and embeds them as inline Content-ID
// parts. Widgets without an image (renderer failure, widget error, size caps) keep their HTML table; the plain text
// part always carries the tables.

// Image caps.
const (
	// MaxImageBytes bounds one embedded PNG.
	MaxImageBytes = 1 << 20
	// MaxImagesBytes bounds all embedded PNGs of one e-mail.
	MaxImagesBytes = 10 << 20
	// MaxImageHeight bounds the displayed height (CSS pixels) of one image.
	MaxImageHeight = 2000
)

// Image is a PNG rendering of a widget; Width and Height are the display size in CSS pixels.
type Image struct {
	PNG           []byte
	Width, Height int
}

// Imager renders the widgets of a report's dashboard for [from, to) (renderer.ReportImages).
type Imager interface {
	Images(ctx context.Context, sr *dashboard.ScheduledReport, from, to time.Time) (map[string]Image, error)
}

// TableImage is the inline image shown instead of a widget table.
type TableImage struct {
	Src           template.URL // cid:…
	Width, Height int
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// imageContentID is the Content-ID of the image of widget i (position in Content.Widgets).
func imageContentID(i int) string { return "widget-" + strconv.Itoa(i+1) + "@openlog" }

// attachImages fills c.Images from the imager; on failure the e-mail keeps the tables.
func (j *Job) attachImages(ctx context.Context, sr *dashboard.ScheduledReport, c *Content) {
	if j.o.Images == nil || len(c.Widgets) == 0 {
		return
	}
	ictx, cancel := context.WithTimeout(ctx, j.o.ImageTimeout)
	defer cancel()
	start := time.Now()
	images, err := j.o.Images.Images(ictx, sr, c.From, c.To)
	if err != nil {
		j.o.Log.Warn("dashboard report images unavailable, sending tables", "report_id", sr.ID, "err", err)
		return
	}
	c.Images = map[string]Image{}
	total, dropped := 0, 0
	for _, w := range c.Widgets {
		img, ok := images[w.Widget.ID]
		if !ok || w.Err != "" {
			continue
		}
		if len(img.PNG) == 0 || len(img.PNG) > MaxImageBytes || total+len(img.PNG) > MaxImagesBytes || img.Width <= 0 ||
			img.Height <= 0 || img.Height > MaxImageHeight || len(img.PNG) < len(pngMagic) || string(img.PNG[:len(pngMagic)]) != string(pngMagic) {
			dropped++
			continue
		}
		total += len(img.PNG)
		c.Images[w.Widget.ID] = img
	}
	j.o.Log.Info("dashboard report images", "report_id", sr.ID, "images", len(c.Images), "dropped", dropped, "bytes", total,
		"duration", time.Since(start).Round(time.Millisecond))
}

// tableImage returns the image of widget i for the HTML body, or nil.
func tableImage(c Content, i int) *TableImage {
	w := c.Widgets[i]
	img, ok := c.Images[w.Widget.ID]
	if !ok || w.Err != "" {
		return nil
	}
	width, height := img.Width, img.Height
	if width > 720 {
		height = height * 720 / width
		width = 720
	}
	return &TableImage{Src: template.URL("cid:" + imageContentID(i)), Width: width, Height: max(height, 1)}
}

// Inline returns the inline image parts referenced by the HTML body of Render.
func Inline(c Content) []auth.InlineImage {
	var out []auth.InlineImage
	for i, w := range c.Widgets {
		if tableImage(c, i) == nil {
			continue
		}
		out = append(out, auth.InlineImage{ContentID: imageContentID(i), ContentType: "image/png",
			Filename: "widget-" + strconv.Itoa(i+1) + ".png", Data: c.Images[w.Widget.ID].PNG})
	}
	return out
}
