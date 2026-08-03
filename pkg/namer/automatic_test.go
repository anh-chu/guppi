package namer

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixedClock(start time.Time) func() time.Time {
	return func() time.Time { return start }
}

func testPolicy() AutomaticPolicy {
	return AutomaticPolicy{
		NormalInterval: 45 * time.Second,
		BackoffSteps: []time.Duration{
			time.Minute,
			2 * time.Minute,
			4 * time.Minute,
			8 * time.Minute,
			15 * time.Minute,
		},
	}
}

func TestAutomaticPolicy_FailureBackoff(t *testing.T) {
	p := testPolicy()
	cases := []struct {
		count int
		want  time.Duration
	}{
		{-1, p.NormalInterval},
		{0, p.NormalInterval},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 15 * time.Minute},
		{6, 15 * time.Minute},
		{100, 15 * time.Minute},
	}
	for _, c := range cases {
		got := p.backoff(c.count)
		if got != c.want {
			t.Errorf("backoff(%d) = %v, want %v", c.count, got, c.want)
		}
	}
}

func TestAutomaticGate_Begin_NormalInterval(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	g := NewAutomaticGate(AutomaticPolicy{NormalInterval: 10 * time.Second})
	g.clock = fixedClock(now)

	if ok, _ := g.Begin("s"); !ok {
		t.Fatal("first Begin should succeed")
	}
	if ok, reason := g.Begin("s"); ok {
		t.Fatalf("second Begin should be blocked, got ok=true")
	} else if reason == "" {
		t.Fatal("blocked Begin should return a reason")
	}

	// After advancing past the normal interval, Begin is eligible again.
	g.clock = fixedClock(now.Add(11 * time.Second))
	if ok, _ := g.Begin("s"); !ok {
		t.Fatal("Begin after interval should succeed")
	}
}

func TestAutomaticGate_FailureBackoff(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	wantBackoff := []time.Duration{
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		15 * time.Minute,
		15 * time.Minute,
	}

	for i, want := range wantBackoff {
		count := i + 1
		g := NewAutomaticGate(testPolicy())
		g.clock = fixedClock(now)

		if ok, _ := g.Begin("s"); !ok {
			t.Fatalf("count=%d: first Begin should succeed", count)
		}
		for j := 0; j < count; j++ {
			g.Failure("s")
		}

		g.clock = fixedClock(now.Add(1 * time.Second))
		if ok, _ := g.Begin("s"); ok {
			t.Fatalf("count=%d: Begin should be blocked for %v", count, want)
		}
		got := g.NextEligible("s").Sub(now)
		if got != want {
			t.Fatalf("count=%d: NextEligible offset = %v, want %v", count, got, want)
		}
		g.clock = fixedClock(now.Add(want + time.Second))
		if ok, _ := g.Begin("s"); !ok {
			t.Fatalf("count=%d: Begin should succeed after %v", count, want)
		}
	}
}

func TestAutomaticGate_Begin_Concurrency(t *testing.T) {
	g := NewAutomaticGate(AutomaticPolicy{NormalInterval: time.Hour})

	const n = 100
	var wg sync.WaitGroup
	var passed int64

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := g.Begin("s"); ok {
				atomic.AddInt64(&passed, 1)
			}
		}()
	}
	wg.Wait()

	if passed != 1 {
		t.Fatalf("expected exactly one Begin to succeed, got %d", passed)
	}
}

func TestAutomaticGate_DifferentKeysDoNotBlock(t *testing.T) {
	g := NewAutomaticGate(AutomaticPolicy{NormalInterval: time.Hour})

	okA, _ := g.Begin("a")
	okB, _ := g.Begin("b")
	if !okA || !okB {
		t.Fatalf("different keys should not block each other: a=%v b=%v", okA, okB)
	}
}

func TestAutomaticGate_SuccessResetsFailuresAndCooldown(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	g := NewAutomaticGate(testPolicy())
	g.clock = fixedClock(now)

	if ok, _ := g.Begin("s"); !ok {
		t.Fatal("first Begin should succeed")
	}
	g.Failure("s")
	failureEligible := g.NextEligible("s")
	if failureEligible.Equal(now) || failureEligible.Before(now.Add(time.Second)) {
		t.Fatalf("failure should push NextEligible forward, got %v", failureEligible)
	}

	g.Success("s")
	successEligible := g.NextEligible("s")
	want := now.Add(g.policy.NormalInterval)
	if successEligible != want {
		t.Fatalf("success should reset to normal interval; NextEligible = %v, want %v",
			successEligible, want)
	}
	if !successEligible.Before(failureEligible) {
		t.Fatalf("success cooldown should be shorter than failure backoff: %v vs %v", successEligible, failureEligible)
	}

	g.clock = fixedClock(now.Add(g.policy.NormalInterval + time.Second))
	if ok, _ := g.Begin("s"); !ok {
		t.Fatal("Begin should succeed after success interval")
	}
}

func TestAutomaticGate_Reset(t *testing.T) {
	g := NewAutomaticGate(AutomaticPolicy{NormalInterval: time.Hour})
	g.Begin("s")
	g.Failure("s")
	if !g.NextEligible("s").After(time.Now()) {
		t.Fatal("expected active backoff before Reset")
	}

	g.Reset("s")
	if !g.NextEligible("s").IsZero() {
		t.Fatalf("Reset should clear state, NextEligible = %v", g.NextEligible("s"))
	}
	if ok, _ := g.Begin("s"); !ok {
		t.Fatal("Begin should succeed after Reset")
	}
}

func TestAutomaticGate_DefaultPolicyApplied(t *testing.T) {
	g := NewAutomaticGate(AutomaticPolicy{})
	if g.policy.NormalInterval != 45*time.Second {
		t.Fatalf("default NormalInterval = %v, want 45s", g.policy.NormalInterval)
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute}
	if len(g.policy.BackoffSteps) != len(want) {
		t.Fatalf("default BackoffSteps = %v, want %v", g.policy.BackoffSteps, want)
	}
	for i, d := range want {
		if g.policy.BackoffSteps[i] != d {
			t.Fatalf("default BackoffSteps[%d] = %v, want %v", i, g.policy.BackoffSteps[i], d)
		}
	}
}

func TestAutomaticGate_NextEligible_NoState(t *testing.T) {
	g := NewAutomaticGate(testPolicy())
	if !g.NextEligible("never").IsZero() {
		t.Fatalf("NextElapsed for unknown key should be zero, got %v", g.NextEligible("never"))
	}
}

func TestAutomaticGate_BeginAfterSuccess_RespectsNormalInterval(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	g := NewAutomaticGate(AutomaticPolicy{NormalInterval: 30 * time.Second})
	g.clock = fixedClock(now)

	g.Success("s")
	if ok, _ := g.Begin("s"); ok {
		t.Fatal("Begin should be blocked immediately after Success")
	}

	g.clock = fixedClock(now.Add(31 * time.Second))
	if ok, _ := g.Begin("s"); !ok {
		t.Fatal("Begin should succeed after normal interval")
	}
}
