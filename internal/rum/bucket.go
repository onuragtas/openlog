package rum

import (
	"math"
	"time"
)

// bucket is a token bucket, one per browser key per ingest pod (keys.go Allow).
//
// internal/quota has an identical primitive, but importing it here would pull the SaaS quota package — and
// through it internal/auth — into the RUM ingest path, which is both a heavier dependency than a 25-line
// counter deserves and a cycle waiting to happen the first time the auth service wants to validate a browser
// key. Duplicating this much arithmetic is the cheaper trade.
type bucket struct {
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(rate, burst float64, now time.Time) *bucket {
	if burst < rate {
		burst = rate
	}
	return &bucket{rate: rate, burst: burst, tokens: burst, last: now}
}

// setRate changes the rate and burst (the key's limit was edited), keeping the current tokens.
func (b *bucket) setRate(rate, burst float64, now time.Time) {
	b.refill(now)
	if burst < rate {
		burst = rate
	}
	b.rate, b.burst = rate, burst
	b.tokens = math.Min(b.tokens, burst)
}

func (b *bucket) refill(now time.Time) {
	if now.After(b.last) {
		b.tokens = math.Min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
		b.last = now
	}
}

// take admits n tokens. A partially funded request is admitted in full and puts the bucket in debt, so one
// oversized batch is never rejected outright while sustained traffic still converges on the rate.
func (b *bucket) take(n float64, now time.Time) (bool, time.Duration) {
	if b.rate <= 0 {
		return true, 0
	}
	b.refill(now)
	if b.tokens > 0 {
		b.tokens -= n
		return true, 0
	}
	wait := (-b.tokens + 1) / b.rate
	return false, time.Duration(math.Ceil(wait * float64(time.Second)))
}
