package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterAllows(t *testing.T) {
	l := New(3, time.Second)
	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("expected Allow() = true on attempt %d", i)
		}
	}
	if l.Allow() {
		t.Error("expected Allow() = false after hitting limit")
	}
}

func TestLimiterWindowExpiry(t *testing.T) {
	l := New(1, 50*time.Millisecond)
	if !l.Allow() {
		t.Fatal("first Allow should succeed")
	}
	if l.Allow() {
		t.Fatal("second Allow should fail")
	}
	// sync-primitive-justified: waiting for the rate-limiter sliding window (50ms) to expire; the limiter has no channel or callback to signal expiry — wall-clock advancement is the observable under test.
	time.Sleep(60 * time.Millisecond)
	if !l.Allow() {
		t.Error("Allow should succeed after window expires")
	}
}

func TestLimiterReset(t *testing.T) {
	l := New(1, time.Minute)
	l.Allow()
	l.Reset()
	if !l.Allow() {
		t.Error("Allow should succeed after Reset")
	}
}

func TestExceeded(t *testing.T) {
	l := New(2, time.Second)
	if l.Exceeded() {
		t.Fatal("fresh limiter should not be exceeded")
	}
	l.Record()
	l.Record()
	if !l.Exceeded() {
		t.Fatal("limiter should be exceeded after 2 records with max=2")
	}
}

func TestExceededWindowExpiry(t *testing.T) {
	l := New(1, 50*time.Millisecond)
	l.Record()
	if !l.Exceeded() {
		t.Fatal("should be exceeded after record")
	}
	// sync-primitive-justified: waiting for the rate-limiter sliding window (50ms) to expire; the limiter has no channel or callback to signal expiry — wall-clock advancement is the observable under test.
	time.Sleep(60 * time.Millisecond)
	if l.Exceeded() {
		t.Fatal("should not be exceeded after window expiry")
	}
}

func TestRecord(t *testing.T) {
	l := New(5, time.Second)
	l.Record()
	l.Record()
	l.Record()
	if l.EventCount() != 3 {
		t.Fatalf("expected 3 events, got %d", l.EventCount())
	}
}

func TestEventCount(t *testing.T) {
	l := New(5, 50*time.Millisecond)
	l.Record()
	l.Record()
	if l.EventCount() != 2 {
		t.Fatalf("expected 2 events, got %d", l.EventCount())
	}
	// sync-primitive-justified: waiting for the rate-limiter sliding window (50ms) to expire; the limiter has no channel or callback to signal expiry — wall-clock advancement is the observable under test.
	time.Sleep(60 * time.Millisecond)
	if l.EventCount() != 0 {
		t.Fatalf("expected 0 events after expiry, got %d", l.EventCount())
	}
}
