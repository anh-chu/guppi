package namer

import (
	"sync"
	"time"
)

// AutomaticPolicy configures the cadence and failure backoff for automatic
// naming attempts. The zero value is not usable; use DefaultAutomaticPolicy or
// construct one with explicit fields.
type AutomaticPolicy struct {
	// NormalInterval is the minimum time between successful automatic naming
	// attempts. A successful attempt is not eligible again until this interval
	// has elapsed.
	NormalInterval time.Duration

	// BackoffSteps is the list of cooldown durations applied after consecutive
	// failures. Failure count 1 uses BackoffSteps[0], count 2 uses [1], etc.
	// Attempts beyond the end of the slice use the final value.
	BackoffSteps []time.Duration
}

// DefaultAutomaticPolicy returns the policy used by the state manager:
// a 45-second normal interval and 1/2/4/8/15 minute failure backoffs.
func DefaultAutomaticPolicy() AutomaticPolicy {
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

// backoff returns the cooldown for a given failure count. Zero or negative
// counts use the normal interval.
func (p AutomaticPolicy) backoff(failureCount int) time.Duration {
	if failureCount <= 0 {
		return p.NormalInterval
	}
	idx := failureCount - 1
	if idx >= len(p.BackoffSteps) {
		idx = len(p.BackoffSteps) - 1
	}
	if idx < 0 {
		return p.NormalInterval
	}
	return p.BackoffSteps[idx]
}

// gateState holds the timing/count state for a single key.
type gateState struct {
	lastSuccess  time.Time
	lastAttempt  time.Time
	failureCount int
}

// AutomaticGate enforces automatic naming cadence: a normal interval after
// successes and an escalating backoff after failures. It also serializes
// attempts for the same key so only one concurrent attempt may proceed.
//
// The gate is safe for concurrent use by multiple goroutines.
type AutomaticGate struct {
	mu      sync.Mutex
	policy  AutomaticPolicy
	states  map[string]*gateState
	clock   func() time.Time
}

// NewAutomaticGate creates a gate with the given policy. Missing or zero
// fields are filled from DefaultAutomaticPolicy.
func NewAutomaticGate(policy AutomaticPolicy) *AutomaticGate {
	policy = normalizePolicy(policy)
	return &AutomaticGate{
		policy: policy,
		states: make(map[string]*gateState),
		clock:  time.Now,
	}
}

func normalizePolicy(p AutomaticPolicy) AutomaticPolicy {
	def := DefaultAutomaticPolicy()
	if p.NormalInterval <= 0 {
		p.NormalInterval = def.NormalInterval
	}
	if len(p.BackoffSteps) == 0 {
		p.BackoffSteps = def.BackoffSteps
	}
	return p
}

// Begin reports whether an automatic naming attempt may proceed for key.
// It returns true when neither the normal interval nor the current failure
// backoff is active and no other attempt is already recorded for key. On
// success, it records an attempt timestamp so subsequent calls are blocked
// until the cooldown elapses.
func (g *AutomaticGate) Begin(key string) (ok bool, reason string) {
	now := g.now()

	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.states[key]
	if st == nil {
		st = &gateState{}
		g.states[key] = st
	}

	backoff := g.policy.backoff(st.failureCount)
	if !st.lastAttempt.IsZero() && now.Before(st.lastAttempt.Add(backoff)) {
		return false, "attempt blocked by failure backoff"
	}
	if !st.lastSuccess.IsZero() && now.Before(st.lastSuccess.Add(g.policy.NormalInterval)) {
		return false, "attempt blocked by normal success interval"
	}

	st.lastAttempt = now
	return true, ""
}

// Success records a successful automatic naming attempt for key. It resets
// the failure count and updates the last-success timestamp so the normal
// interval applies to the next attempt.
func (g *AutomaticGate) Success(key string) {
	now := g.now()

	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.states[key]
	if st == nil {
		st = &gateState{}
		g.states[key] = st
	}
	st.lastSuccess = now
	st.failureCount = 0
}

// Failure records an automatic naming failure for key. It increments the
// consecutive failure count, which causes the next eligible attempt to use
// the matching backoff step.
func (g *AutomaticGate) Failure(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.states[key]
	if st == nil {
		st = &gateState{}
		g.states[key] = st
	}
	st.failureCount++
}

// Reset clears all cooldown and failure state for key. Explicit/forced
// actions use it before bypassing normal automatic gating.
func (g *AutomaticGate) Reset(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.states, key)
}

// NextEligible returns the earliest time at which key may next attempt
// automatic naming according to the gate's policy, or the zero time if key
// has no recorded state. Callers can use this to schedule retries without
// duplicating the backoff formula.
func (g *AutomaticGate) NextEligible(key string) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.states[key]
	if st == nil {
		return time.Time{}
	}

	var next time.Time
	if !st.lastAttempt.IsZero() {
		next = st.lastAttempt.Add(g.policy.backoff(st.failureCount))
	}
	if !st.lastSuccess.IsZero() {
		if t := st.lastSuccess.Add(g.policy.NormalInterval); t.After(next) {
			next = t
		}
	}
	return next
}

func (g *AutomaticGate) now() time.Time {
	if g.clock != nil {
		return g.clock()
	}
	return time.Now()
}
