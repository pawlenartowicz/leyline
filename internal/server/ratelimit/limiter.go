package ratelimit

import (
	"sync"
	"time"
)

// Limiter tracks events per window. Not a token bucket — just a sliding window counter.
type Limiter struct {
	mu       sync.Mutex
	events   []time.Time
	maxCount int
	window   time.Duration
}

// New creates a Limiter that allows at most maxCount events within window.
func New(maxCount int, window time.Duration) *Limiter {
	return &Limiter{
		maxCount: maxCount,
		window:   window,
	}
}

// prune removes expired events. Caller must hold l.mu.
func (l *Limiter) prune() {
	cutoff := time.Now().Add(-l.window)
	valid := 0
	for _, t := range l.events {
		if t.After(cutoff) {
			l.events[valid] = t
			valid++
		}
	}
	l.events = l.events[:valid]
}

// Allow returns true if the event is within rate limits. Records the event.
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.prune()
	if len(l.events) >= l.maxCount {
		return false
	}
	l.events = append(l.events, time.Now())
	return true
}

// Exceeded returns true if the limit has been reached (check-only, no record).
func (l *Limiter) Exceeded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	return len(l.events) >= l.maxCount
}

// Record adds an event without checking the limit.
func (l *Limiter) Record() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	l.events = append(l.events, time.Now())
}

// EventCount returns the number of events in the current window.
func (l *Limiter) EventCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	return len(l.events)
}

// Reset clears all recorded events.
func (l *Limiter) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = l.events[:0]
}
