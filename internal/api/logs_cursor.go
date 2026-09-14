package api

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
)

// Cursor pagination of GET /api/v1/logs (api.md "Logs"). Rows are ordered newest first by (timestamp, row key),
// where the row key is a hash of the row's content columns (logRowKey). The opaque cursor holds the position of
// the last returned row: its timestamp (ns), row key and how many rows with exactly that (timestamp, key) were
// already returned, so rows that are identical in every hashed column are neither skipped nor repeated.

// logRowKey is the tiebreaker of rows with the same timestamp.
const logRowKey = "cityHash64(observed_timestamp, host_id, service_name, severity_number, trace_id, span_id, body, attributes, resource_attributes) AS l_key"

const logCursorVersion = 1

type logCursor struct {
	V   int    `json:"v"`
	TS  int64  `json:"t"`
	Key string `json:"k"` // uint64 as a decimal string (JSON numbers lose precision above 2^53)
	N   int    `json:"n"`
}

// logPos is the sort position of one row.
type logPos struct {
	ts  int64
	key uint64
}

func encodeLogCursor(p logPos, n int) string {
	b, _ := json.Marshal(logCursor{V: logCursorVersion, TS: p.ts, Key: strconv.FormatUint(p.key, 10), N: n})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeLogCursor(s string) (logPos, int, error) {
	bad := badRequest("cursor: invalid or expired cursor (use next_cursor of a previous response)")
	if len(s) > 512 {
		return logPos{}, 0, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return logPos{}, 0, bad
	}
	var c logCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.V != logCursorVersion || c.N < 0 || c.N > 1_000_000 {
		return logPos{}, 0, bad
	}
	key, err := strconv.ParseUint(c.Key, 10, 64)
	if err != nil {
		return logPos{}, 0, bad
	}
	return logPos{ts: c.TS, key: key}, c.N, nil
}

// logPage selects the rows of one page from rows fetched with "(timestamp, l_key) <= cursor" (or no cursor),
// newest first, and returns the indexes to emit plus the next cursor ("" when there are no more rows).
// skip is the cursor's n: that many leading rows at exactly the cursor position were returned before.
func logPage(positions []logPos, cursor *logPos, skip, limit int) (first, end int, next string) {
	i := 0
	if cursor != nil {
		for i < len(positions) && skip > 0 && positions[i] == *cursor {
			i++
			skip--
		}
	}
	end = min(len(positions), i+limit)
	if end >= len(positions) || end == i {
		return i, end, ""
	}
	last := positions[end-1]
	n := 0
	for j := end - 1; j >= i && positions[j] == last; j-- {
		n++
	}
	if cursor != nil && last == *cursor {
		// Every emitted row is at the cursor position: add the rows returned on earlier pages.
		n += i
	}
	return i, end, encodeLogCursor(last, n)
}
