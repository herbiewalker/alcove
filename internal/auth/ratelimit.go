package auth

import (
	"sync"
	"time"
)

// Limiter is a per-key token bucket for the sign-in endpoints (N16 in spirit). The threat is a
// device on the home network guessing passwords; with Argon2id's cost per attempt, a small burst
// and a slow refill make that impractical. It is deliberately simple: no distributed state, and a
// size cap that drops an arbitrary entry rather than grow without bound.
type Limiter struct {
	Capacity    int
	RefillEvery time.Duration
	Now         func() time.Time

	mu sync.Mutex
	m  map[string]*bucket
}

type bucket struct {
	tokens int
	last   time.Time
}

const maxKeys = 4096

// LoginLimiter is the default for sign-in attempts: ten in a burst, then one every 20 seconds.
func LoginLimiter() *Limiter { return &Limiter{Capacity: 10, RefillEvery: 20 * time.Second} }

// Allow consumes one token for key and reports whether the attempt may proceed.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	if l.m == nil {
		l.m = map[string]*bucket{}
	}
	b, ok := l.m[key]
	if !ok {
		if len(l.m) >= maxKeys {
			for k := range l.m {
				delete(l.m, k)
				break
			}
		}
		b = &bucket{tokens: l.Capacity, last: now}
		l.m[key] = b
	}
	if el := now.Sub(b.last); el > 0 && l.RefillEvery > 0 {
		if add := int(el / l.RefillEvery); add > 0 {
			b.tokens += add
			if b.tokens > l.Capacity {
				b.tokens = l.Capacity
			}
			b.last = b.last.Add(time.Duration(add) * l.RefillEvery)
		}
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// Reset forgives a key after a successful sign-in.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}
