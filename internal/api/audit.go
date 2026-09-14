package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
)

// auditFilter parses GET /api/v1/audit-log parameters: limit (1-500, default 100), actor (e-mail substring),
// action (prefix), from/to (RFC3339 or unix ms) and cursor (next_cursor of the previous page).
func auditFilter(r *http.Request) (auth.AuditFilter, error) {
	q := r.URL.Query()
	f := auth.AuditFilter{Limit: 100, Actor: strings.TrimSpace(q.Get("actor")), Action: strings.TrimSpace(q.Get("action"))}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return f, badRequest("limit must be a positive integer")
		}
		f.Limit = min(n, 500)
	}
	var err error
	if v := q.Get("from"); v != "" {
		if f.From, err = parseTime(v); err != nil {
			return f, badRequest("from: %v", err)
		}
	}
	if v := q.Get("to"); v != "" {
		if f.To, err = parseTime(v); err != nil {
			return f, badRequest("to: %v", err)
		}
	}
	if v := q.Get("cursor"); v != "" {
		c, ok := decodeAuditCursor(v)
		if !ok {
			return f, badRequest("cursor is invalid")
		}
		f.Before = &c
	}
	return f, nil
}

// encodeAuditCursor encodes the position of e (opaque to clients).
func encodeAuditCursor(e auth.AuditEvent) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(e.CreatedAt.UnixMicro(), 10) + ":" + strconv.FormatInt(e.ID, 10)))
}

func decodeAuditCursor(v string) (auth.AuditCursor, bool) {
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return auth.AuditCursor{}, false
	}
	ts, id, ok := strings.Cut(string(b), ":")
	if !ok {
		return auth.AuditCursor{}, false
	}
	us, err1 := strconv.ParseInt(ts, 10, 64)
	n, err2 := strconv.ParseInt(id, 10, 64)
	if err1 != nil || err2 != nil || n <= 0 {
		return auth.AuditCursor{}, false
	}
	return auth.AuditCursor{CreatedAt: time.UnixMicro(us).UTC(), ID: n}, true
}
