package child

import (
	"sync"
	"time"
)

// failureLimiter rate-limits repeated auth failures per key prefix and source
// address: once limit failures were seen within the window, further requests
// from that prefix/source are rejected with 429 until the failures age out.
type failureLimiter struct {
	window time.Duration
	limit  int

	mu   sync.Mutex
	seen map[string][]time.Time
}

func newFailureLimiter(limit int, window time.Duration) *failureLimiter {
	return &failureLimiter{
		window: window,
		limit:  limit,
		seen:   map[string][]time.Time{},
	}
}

func (f *failureLimiter) allow(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	failures := f.pruneLocked(id, now)
	return len(failures) < f.limit
}

func (f *failureLimiter) record(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	f.seen[id] = append(f.pruneLocked(id, now), now)
}

func (f *failureLimiter) pruneLocked(id string, now time.Time) []time.Time {
	failures := f.seen[id]
	kept := failures[:0]
	for _, ts := range failures {
		if now.Sub(ts) < f.window {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(f.seen, id)
	} else {
		f.seen[id] = kept
	}
	return kept
}
