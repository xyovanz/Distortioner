package tools

import (
	"sync"
	"time"
)

// Throttle allows one event per key per interval (e.g. one "you are banned" reply per hour).
type Throttle struct {
	interval time.Duration
	mu       sync.Mutex
	last     map[int64]time.Time
}

func NewThrottle(interval time.Duration) *Throttle {
	return &Throttle{interval: interval, last: make(map[int64]time.Time)}
}

// Allow reports whether the key may fire at now, and records it if so.
func (t *Throttle) Allow(key int64, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.interval {
		return false
	}
	t.last[key] = now
	if len(t.last) > 10_000 {
		for k, v := range t.last {
			if now.Sub(v) >= t.interval {
				delete(t.last, k)
			}
		}
	}
	return true
}

// Reset forgets the key, so its next event is allowed immediately.
func (t *Throttle) Reset(key int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.last, key)
}
