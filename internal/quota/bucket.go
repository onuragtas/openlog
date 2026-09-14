package quota

import (
	"math"
	"time"
)

// TokenBucket limits bytes per second with a burst. A request is admitted while the bucket holds any tokens and takes
// its full size, so the bucket can go into debt: a request larger than the burst is not starved, and the debt delays
// the following requests (the long-run rate stays bounded). Not safe for concurrent use.
type TokenBucket struct {
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
}

// NewTokenBucket returns a full bucket.
func NewTokenBucket(rate, burst float64, now time.Time) *TokenBucket {
	if burst < rate {
		burst = rate
	}
	return &TokenBucket{rate: rate, burst: burst, tokens: burst, last: now}
}

// SetRate changes rate and burst, keeping the current tokens (capped at the new burst).
func (b *TokenBucket) SetRate(rate, burst float64, now time.Time) {
	b.refill(now)
	if burst < rate {
		burst = rate
	}
	b.rate, b.burst = rate, burst
	b.tokens = math.Min(b.tokens, burst)
}

// Rate returns the tokens per second and burst.
func (b *TokenBucket) Rate() (rate, burst float64) { return b.rate, b.burst }

func (b *TokenBucket) refill(now time.Time) {
	if now.After(b.last) {
		b.tokens = math.Min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
		b.last = now
	}
}

// Take admits a request of n tokens. When the bucket is empty or in debt it returns false and how long until a request
// would be admitted.
func (b *TokenBucket) Take(n float64, now time.Time) (bool, time.Duration) {
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
