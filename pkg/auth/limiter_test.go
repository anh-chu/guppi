package auth

import (
	"testing"
	"time"
)

func TestLimiter_SetupFivePerMinute(t *testing.T) {
	now := time.Now()
	clock := now
	lim := NewLimiter()
	lim.SetClock(func() time.Time { return clock })

	for i := 0; i < 5; i++ {
		if ok, _ := lim.Allow("setup", "127.0.0.1"); !ok {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if ok, retry := lim.Allow("setup", "127.0.0.1"); ok {
		t.Fatal("6th setup request should be rate limited")
	} else if retry <= 0 {
		t.Fatalf("expected positive retry, got %v", retry)
	}

	// Advancing past the window refills the bucket.
	clock = now.Add(2 * time.Minute)
	if ok, _ := lim.Allow("setup", "127.0.0.1"); !ok {
		t.Fatal("setup request should be allowed after window")
	}
}

func TestLimiter_LoginTenPerMinute(t *testing.T) {
	now := time.Now()
	clock := now
	lim := NewLimiter()
	lim.SetClock(func() time.Time { return clock })

	for i := 0; i < 10; i++ {
		if ok, _ := lim.Allow("login", "127.0.0.1"); !ok {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if ok, _ := lim.Allow("login", "127.0.0.1"); ok {
		t.Fatal("11th login request should be rate limited")
	}
}

func TestLimiter_PerIPIsolation(t *testing.T) {
	lim := NewLimiter()
	for i := 0; i < 5; i++ {
		if ok, _ := lim.Allow("setup", "127.0.0.1"); !ok {
			t.Fatalf("setup from 127.0.0.1 should be allowed")
		}
	}
	if ok, _ := lim.Allow("setup", "127.0.0.1"); ok {
		t.Fatal("127.0.0.1 should be limited")
	}
	if ok, _ := lim.Allow("setup", "127.0.0.2"); !ok {
		t.Fatal("127.0.0.2 should not share limit")
	}
}

func TestLimiter_IdleEviction(t *testing.T) {
	now := time.Now()
	clock := now
	lim := NewLimiter()
	lim.SetClock(func() time.Time { return clock })

	for i := 0; i < 5; i++ {
		lim.Allow("setup", "127.0.0.1")
	}
	if ok, _ := lim.Allow("setup", "127.0.0.1"); ok {
		t.Fatal("should be limited")
	}

	// 15+ minutes of inactivity evicts the bucket.
	clock = now.Add(16 * time.Minute)
	if ok, _ := lim.Allow("setup", "127.0.0.1"); !ok {
		t.Fatal("bucket should be reset after idle eviction")
	}
}

func TestLimiter_UnknownCategoryAllowed(t *testing.T) {
	lim := NewLimiter()
	if ok, _ := lim.Allow("other", "127.0.0.1"); !ok {
		t.Fatal("unknown categories should be allowed")
	}
}
