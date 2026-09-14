package mail

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
)

func TestMessageInlineImages(t *testing.T) {
	img := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 200)...)
	raw := Message("openlog <noreply@example.com>", "a@example.com", "Report", "text", `<img src="cid:widget-1@openlog">`, time.Now(),
		auth.InlineImage{ContentID: "widget-1@openlog", ContentType: "image/png", Filename: "widget-1.png", Data: img},
		auth.InlineImage{ContentID: "bad>\r\nX-Injected: 1", ContentType: "image/png", Filename: "x.png", Data: img},
		auth.InlineImage{ContentID: "widget-3@openlog", ContentType: "text/html", Filename: "x.html", Data: img},
	)
	if bytes.Contains(raw, []byte("X-Injected")) || bytes.Contains(raw, []byte("x.html")) {
		t.Fatal("invalid inline part written")
	}
	msg, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	alt := multipart.NewReader(msg.Body, params["boundary"])
	first, _ := alt.NextPart()
	if !strings.HasPrefix(first.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("first part %s", first.Header.Get("Content-Type"))
	}
	second, err := alt.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	mt, rp, _ := mime.ParseMediaType(second.Header.Get("Content-Type"))
	if mt != "multipart/related" {
		t.Fatalf("second part %s", mt)
	}
	rel := multipart.NewReader(second, rp["boundary"])
	htmlPart, _ := rel.NextPart()
	if !strings.HasPrefix(htmlPart.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("related html %s", htmlPart.Header.Get("Content-Type"))
	}
	imgPart, err := rel.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if imgPart.Header.Get("Content-ID") != "<widget-1@openlog>" || imgPart.Header.Get("Content-Type") != "image/png" {
		t.Errorf("image headers %v", imgPart.Header)
	}
	// multipart.Part decodes quoted-printable only; decode base64 here.
	enc, _ := io.ReadAll(imgPart)
	dec, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\r", "", "\n", "").Replace(string(enc)))
	if err != nil || !bytes.Equal(dec, img) {
		t.Errorf("image data mismatch: %v", err)
	}
	if _, err := rel.NextPart(); err != io.EOF {
		t.Errorf("more related parts: %v", err)
	}

	// Without images the message stays multipart/alternative with text/html.
	plain := Message("openlog <noreply@example.com>", "a@example.com", "S", "t", "<p>h</p>", time.Now())
	if bytes.Contains(plain, []byte("multipart/related")) || !bytes.Contains(plain, []byte("text/html")) {
		t.Errorf("plain message changed:\n%s", plain)
	}
}
