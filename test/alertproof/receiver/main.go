// Command alert-receiver records alert notifications for tests: a generic webhook (verifies X-Openlog-Signature),
// a fake Slack incoming webhook and a fake Teams webhook. GET /requests returns everything received.
//
//	HMAC_SECRET   webhook signature secret (optional)
//	TLS_CERT/KEY  also serve HTTPS on :8443
//	FAIL_FIRST    fail the first N requests per path with 503 (retry tests)
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type record struct {
	Path           string          `json:"path"`
	ReceivedAt     time.Time       `json:"received_at"`
	Event          string          `json:"event"`
	IdempotencyKey string          `json:"idempotency_key"`
	Delivery       string          `json:"delivery"`
	Timestamp      string          `json:"timestamp"`
	Signature      string          `json:"signature"`
	SignatureValid *bool           `json:"signature_valid"`
	Status         int             `json:"status"`
	Body           json.RawMessage `json:"body"`
}

var (
	mu       sync.Mutex
	records  []record
	attempts = map[string]int{}
)

func verify(secret, sig, ts string, body []byte) bool {
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(t, 0)).Abs() > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}

func main() {
	secret := os.Getenv("HMAC_SECRET")
	failFirst, _ := strconv.Atoi(os.Getenv("FAIL_FIRST"))
	mux := http.NewServeMux()
	hook := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		rec := record{Path: r.URL.Path, ReceivedAt: time.Now().UTC(), Event: r.Header.Get("X-Openlog-Event"),
			IdempotencyKey: r.Header.Get("X-Openlog-Idempotency-Key"), Delivery: r.Header.Get("X-Openlog-Delivery"),
			Timestamp: r.Header.Get("X-Openlog-Timestamp"), Signature: r.Header.Get("X-Openlog-Signature"), Status: http.StatusOK}
		if json.Valid(body) {
			rec.Body = body
		} else {
			rec.Body, _ = json.Marshal(string(body))
		}
		if strings.HasPrefix(r.URL.Path, "/webhook") && secret != "" {
			ok := verify(secret, rec.Signature, rec.Timestamp, body)
			rec.SignatureValid = &ok
			if !ok {
				rec.Status = http.StatusUnauthorized
			}
		}
		mu.Lock()
		attempts[r.URL.Path]++
		if attempts[r.URL.Path] <= failFirst {
			rec.Status = http.StatusServiceUnavailable
		}
		records = append(records, rec)
		mu.Unlock()
		log.Printf("%s event=%s key=%s status=%d", r.URL.Path, rec.Event, rec.IdempotencyKey, rec.Status)
		w.WriteHeader(rec.Status)
		if strings.HasPrefix(r.URL.Path, "/slack") && rec.Status == http.StatusOK {
			_, _ = w.Write([]byte("ok"))
		}
	}
	mux.HandleFunc("POST /webhook", hook)
	mux.HandleFunc("POST /slack", hook)
	mux.HandleFunc("POST /teams", hook)
	mux.HandleFunc("GET /requests", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(records)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if cert, key := os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY"); cert != "" && key != "" {
		go func() { log.Fatal(http.ListenAndServeTLS(":8443", cert, key, mux)) }()
	}
	log.Fatal(http.ListenAndServe(":8080", mux))
}
