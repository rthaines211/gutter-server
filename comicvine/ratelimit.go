package comicvine

import (
	"math"
	"sync"
	"time"
)

// tokenBucket is a simple wall-clock token bucket. wait() blocks until a
// token is available; refill is continuous (perSec tokens per second).
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	max    float64
	perSec float64
	last   time.Time
}

func newTokenBucket(max, perSec float64) *tokenBucket {
	return &tokenBucket{tokens: max, max: max, perSec: perSec, last: time.Now()}
}

func (b *tokenBucket) refillLocked() {
	now := time.Now()
	b.tokens = math.Min(b.max, b.tokens+now.Sub(b.last).Seconds()*b.perSec)
	b.last = now
}

func (b *tokenBucket) wait() {
	for {
		b.mu.Lock()
		b.refillLocked()
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return
		}
		need := time.Duration((1 - b.tokens) / b.perSec * float64(time.Second))
		b.mu.Unlock()
		time.Sleep(need)
	}
}

func (b *tokenBucket) available() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	return int(b.tokens)
}
