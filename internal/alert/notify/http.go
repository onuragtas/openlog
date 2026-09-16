package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Webhook headers (docs/contracts/alerting.md §5.3).
const (
	HeaderEvent          = "X-Openlog-Event"
	HeaderDelivery       = "X-Openlog-Delivery"
	HeaderIdempotencyKey = "X-Openlog-Idempotency-Key"
	HeaderTimestamp      = "X-Openlog-Timestamp"
	HeaderSignature      = "X-Openlog-Signature"
)

// Sign returns the X-Openlog-Signature value for body sent at unix time ts.
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a webhook signature as a receiver should: constant-time HMAC comparison and a timestamp within
// tolerance of now.
func Verify(secret, signature, timestamp string, body []byte, now time.Time, tolerance time.Duration) error {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("invalid timestamp")
	}
	if d := now.Sub(time.Unix(ts, 0)); math.Abs(float64(d)) > float64(tolerance) {
		return errors.New("timestamp outside tolerance")
	}
	want := Sign(secret, ts, body)
	if subtle.ConstantTimeCompare([]byte(want), []byte(signature)) != 1 {
		return errors.New("signature mismatch")
	}
	return nil
}

func (s *Sender) postJSON(ctx context.Context, rawURL string, payload any, headers map[string]string) Result {
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{Err: err}
	}
	return s.post(ctx, rawURL, body, headers)
}

func (s *Sender) post(ctx context.Context, rawURL string, body []byte, headers map[string]string) Result {
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return Result{Err: errors.New("invalid URL")}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return Result{Err: errors.New("invalid URL")}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.opts.UserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return networkResult(scrubURL(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return classifyHTTP(resp, s.now())
}

// scrubURL removes the request URL (which may embed webhook tokens) from transport errors.
func scrubURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", strings.ToLower(ue.Op), ue.Err)
	}
	return err
}

func (s *Sender) sendWebhook(ctx context.Context, t Target, ev Event) Result {
	body, err := json.Marshal(ev)
	if err != nil {
		return Result{Err: err}
	}
	ts := s.now().Unix()
	h := map[string]string{
		HeaderEvent: ev.Event, HeaderDelivery: ev.NotificationID, HeaderIdempotencyKey: ev.IdempotencyKey,
		HeaderTimestamp: strconv.FormatInt(ts, 10),
	}
	if t.HMACSecret != "" {
		h[HeaderSignature] = Sign(t.HMACSecret, ts, body)
	}
	return s.post(ctx, t.URL, body, h)
}

// ---- rendering ----

func title(ev Event) (prefix, emoji string) {
	switch ev.Event {
	case EventResolved:
		return "RESOLVED", "✅"
	case EventAcknowledged:
		return "ACKNOWLEDGED", "👀"
	case EventRenotify:
		return "STILL FIRING", "🔁"
	case EventTest:
		return "TEST", "🧪"
	default:
		return "FIRING", "🔴"
	}
}

func sortedLabels(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if strings.HasPrefix(k, "alert.") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}

func fmtFloat(v *float64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatFloat(*v, 'g', 6, 64)
}

func summaryOrTest(ev Event) string {
	if ev.Event == EventTest {
		return "Test notification from openlog. If you can read this, the channel works."
	}
	return ev.Incident.Summary
}

// SlackMessage renders an incoming-webhook message with Block Kit blocks.
func SlackMessage(ev Event) map[string]any {
	prefix, emoji := title(ev)
	head := fmt.Sprintf("%s %s: %s", emoji, prefix, ev.Rule.Name)
	if ev.Event == EventTest {
		head = emoji + " openlog test notification"
	}
	fields := []map[string]any{
		{"type": "mrkdwn", "text": "*Severity*\n" + orDash(ev.Rule.Severity)},
		{"type": "mrkdwn", "text": "*Rule*\n" + orDash(ev.Rule.Name)},
	}
	if l := sortedLabels(ev.Incident.Labels); l != "" {
		fields = append(fields, map[string]any{"type": "mrkdwn", "text": "*Labels*\n" + l})
	}
	if ev.Incident.OpenedAt != "" {
		fields = append(fields, map[string]any{"type": "mrkdwn", "text": "*Opened*\n" + ev.Incident.OpenedAt})
	}
	if ev.Incident.ResolvedAt != nil {
		fields = append(fields, map[string]any{"type": "mrkdwn", "text": "*Resolved*\n" + *ev.Incident.ResolvedAt})
	}
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": truncate(head, 150), "emoji": true}},
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": truncate(summaryOrTest(ev), 2900)}},
		{"type": "section", "fields": fields},
	}
	if ev.Event != EventTest {
		blocks = append(blocks, map[string]any{"type": "context", "elements": []map[string]any{{"type": "mrkdwn",
			"text": fmt.Sprintf("value %s · threshold %s · %s", fmtFloat(ev.Incident.Value), fmtFloat(ev.Incident.Threshold), ev.Organization.Name)}}})
	}
	if ev.Incident.URL != "" {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []map[string]any{{
			"type": "button", "text": map[string]any{"type": "plain_text", "text": "Open incident"}, "url": ev.Incident.URL}}})
	}
	return map[string]any{"text": head + " — " + summaryOrTest(ev), "blocks": blocks}
}

// TeamsMessage renders an Adaptive Card message for Teams Workflows / incoming webhooks.
func TeamsMessage(ev Event) map[string]any {
	prefix, _ := title(ev)
	color := "attention"
	if ev.Event == EventResolved {
		color = "good"
	} else if ev.Event == EventTest {
		color = "accent"
	}
	facts := []map[string]string{
		{"title": "Severity", "value": orDash(ev.Rule.Severity)},
		{"title": "Rule", "value": orDash(ev.Rule.Name)},
		{"title": "State", "value": orDash(ev.Incident.State)},
		{"title": "Value", "value": fmtFloat(ev.Incident.Value)},
		{"title": "Threshold", "value": fmtFloat(ev.Incident.Threshold)},
	}
	if l := sortedLabels(ev.Incident.Labels); l != "" {
		facts = append(facts, map[string]string{"title": "Labels", "value": l})
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard", "version": "1.4",
		"body": []map[string]any{
			{"type": "TextBlock", "size": "Large", "weight": "Bolder", "color": color, "wrap": true, "text": prefix + ": " + ev.Rule.Name},
			{"type": "TextBlock", "wrap": true, "text": summaryOrTest(ev)},
			{"type": "FactSet", "facts": facts},
		},
	}
	if ev.Incident.URL != "" {
		card["actions"] = []map[string]any{{"type": "Action.OpenUrl", "title": "Open incident", "url": ev.Incident.URL}}
	}
	return map[string]any{"type": "message", "attachments": []map[string]any{{
		"contentType": "application/vnd.microsoft.card.adaptive", "content": card}}}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
