package state

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/namer"
)

// newTestNamer builds a namer backed by an HTTP test server so the Generate
// path can be exercised without real network calls. The returned server must be
// closed by the caller.
func newTestNamer(handler http.HandlerFunc) (*namer.Namer, *httptest.Server) {
	srv := httptest.NewServer(handler)
	return namer.New(namer.Config{Endpoint: srv.URL, Model: "test"}), srv
}

func TestAutomaticNamingGate_PolicyMatchesConstants(t *testing.T) {
	m := &Manager{
		sessions: map[string]*model.Session{},
		meta:     map[string]SessionMetadata{},
	}

	// Manager should initialize the gate with the default policy.
	if m.automaticGate() == nil {
		t.Fatal("automaticGate returned nil")
	}
	p := namer.DefaultAutomaticPolicy()
	if p.NormalInterval != nameRefreshInterval {
		t.Fatalf("default NormalInterval = %v, want %v", p.NormalInterval, nameRefreshInterval)
	}

	want := []time.Duration{
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		15 * time.Minute,
	}
	if len(p.BackoffSteps) != len(want) {
		t.Fatalf("default BackoffSteps = %v, want %v", p.BackoffSteps, want)
	}
	for i, d := range want {
		if p.BackoffSteps[i] != d {
			t.Fatalf("default BackoffSteps[%d] = %v, want %v", i, p.BackoffSteps[i], d)
		}
	}
}

func TestAutomaticNamingGate_BeginConcurrency(t *testing.T) {
	m := &Manager{
		sessions: map[string]*model.Session{},
		meta:     map[string]SessionMetadata{},
	}

	const n = 100
	var wg sync.WaitGroup
	var passed int64

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := m.automaticGate().Begin("s"); ok {
				atomic.AddInt64(&passed, 1)
			}
		}()
	}
	wg.Wait()

	if passed != 1 {
		t.Fatalf("expected exactly one Begin to succeed, got %d", passed)
	}
}

func TestTriggerAgentNaming_FailureIsSilent(t *testing.T) {
	var calls atomic.Int64
	n, srv := newTestNamer(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	m := &Manager{
		sessions: map[string]*model.Session{
			"s": {Name: "s"},
		},
		meta: map[string]SessionMetadata{
			"s": {AgentType: "claude", LastUserPrompt: "do it"},
		},
		namer: n,
	}
	ch := m.Subscribe()
	defer m.Unsubscribe(ch)

	m.triggerAgentNaming("s")

	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 Generate call, got %d", calls.Load())
	}

	m.mu.RLock()
	meta := m.meta["s"]
	m.mu.RUnlock()

	if meta.NamingFailureCount != 1 {
		t.Fatalf("expected failure count 1, got %d", meta.NamingFailureCount)
	}
	if meta.LastNamingAttemptAt.IsZero() {
		t.Fatal("expected LastNamingAttemptAt to be set")
	}
	if !meta.LastNamedAt.IsZero() {
		t.Fatalf("expected LastNamedAt to remain zero after failure, got %v", meta.LastNamedAt)
	}
	if meta.DisplayName != "" {
		t.Fatalf("expected DisplayName unchanged, got %q", meta.DisplayName)
	}

	select {
	case evt := <-ch:
		if evt.Type == "notice" {
			t.Fatalf("unexpected notice event on automatic naming failure: %+v", evt.Data)
		}
	default:
	}
}

func TestTriggerShellNaming_FailureIsSilent(t *testing.T) {
	var calls atomic.Int64
	n, srv := newTestNamer(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	m := &Manager{
		sessions: map[string]*model.Session{
			"s": {Name: "s"},
		},
		meta:  map[string]SessionMetadata{"s": {}},
		namer: n,
	}
	ch := m.Subscribe()
	defer m.Unsubscribe(ch)

	m.TriggerShellNaming("s", []string{"go", "test"})

	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 Generate call, got %d", calls.Load())
	}

	m.mu.RLock()
	meta := m.meta["s"]
	m.mu.RUnlock()
	if meta.NamingFailureCount != 1 {
		t.Fatalf("expected failure count 1, got %d", meta.NamingFailureCount)
	}
	if meta.LastNamingAttemptAt.IsZero() {
		t.Fatal("expected LastNamingAttemptAt to be set")
	}

	select {
	case evt := <-ch:
		if evt.Type == "notice" {
			t.Fatalf("unexpected notice event on automatic naming failure: %+v", evt.Data)
		}
	default:
	}
}

func TestAutomaticNaming_BackoffBlocksExtraCalls(t *testing.T) {
	var calls atomic.Int64
	n, srv := newTestNamer(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	m := &Manager{
		sessions: map[string]*model.Session{
			"s": {Name: "s"},
		},
		meta: map[string]SessionMetadata{
			"s": {AgentType: "claude", LastUserPrompt: "do it"},
		},
		namer: n,
	}

	m.triggerAgentNaming("s")
	m.triggerAgentNaming("s") // should be blocked by backoff

	if calls.Load() != 1 {
		t.Fatalf("expected 1 Generate call (second blocked by backoff), got %d", calls.Load())
	}

	m.mu.RLock()
	meta := m.meta["s"]
	m.mu.RUnlock()
	if meta.NamingFailureCount != 1 {
		t.Fatalf("expected failure count 1, got %d", meta.NamingFailureCount)
	}
}

func TestAutomaticNaming_ResetsFailureAfterSuccess(t *testing.T) {
	var calls atomic.Int64
	n, srv := newTestNamer(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if calls.Load() == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"name\":\"fixed-name\"}"}}]}`)
	})
	defer srv.Close()

	m := &Manager{
		sessions: map[string]*model.Session{
			"s": {Name: "s"},
		},
		meta: map[string]SessionMetadata{
			"s": {AgentType: "claude", LastUserPrompt: "do it"},
		},
		namer: n,
	}

	// Use a short failure backoff so the test can deterministically advance past it.
	m.autoNameGate = namer.NewAutomaticGate(namer.AutomaticPolicy{
		NormalInterval: time.Hour, // keep success interval out of the way
		BackoffSteps:   []time.Duration{50 * time.Millisecond},
	})

	m.triggerAgentNaming("s") // fails

	// Wait past the injected failure backoff so the second attempt is eligible.
	time.Sleep(100 * time.Millisecond)

	m.triggerAgentNaming("s") // succeeds

	if calls.Load() != 2 {
		t.Fatalf("expected 2 Generate calls, got %d", calls.Load())
	}

	m.mu.RLock()
	meta := m.meta["s"]
	m.mu.RUnlock()

	if meta.NamingFailureCount != 0 {
		t.Fatalf("expected failure count reset to 0, got %d", meta.NamingFailureCount)
	}
	if meta.DisplayName != "fixed-name" {
		t.Fatalf("expected DisplayName %q, got %q", "fixed-name", meta.DisplayName)
	}
	if meta.LastNamedAt.IsZero() {
		t.Fatal("expected LastNamedAt set after success")
	}
}

func TestRegenerateName_FailureStillNotices(t *testing.T) {
	var calls atomic.Int64
	n, srv := newTestNamer(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	m := &Manager{
		sessions: map[string]*model.Session{
			"s": {Name: "s"},
		},
		meta: map[string]SessionMetadata{
			"s": {AgentType: "claude", LastUserPrompt: "do it"},
		},
		namer: n,
	}
	ch := m.Subscribe()
	defer m.Unsubscribe(ch)

	_, err := m.RegenerateName("s")
	if err == nil {
		t.Fatal("expected RegenerateName to return an error")
	}

	if calls.Load() != 1 {
		t.Fatalf("expected 1 Generate call, got %d", calls.Load())
	}

	select {
	case evt := <-ch:
		if evt.Type != "notice" {
			t.Fatalf("expected notice event, got %s", evt.Type)
		}
		notice, ok := evt.Data.(Notice)
		if !ok {
			t.Fatalf("expected Notice data, got %T", evt.Data)
		}
		if notice.Source != "ai-naming" {
			t.Fatalf("expected notice source ai-naming, got %q", notice.Source)
		}
		if notice.Severity != "warn" {
			t.Fatalf("expected notice severity warn, got %q", notice.Severity)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected notice event for manual rename failure")
	}

	m.mu.RLock()
	meta := m.meta["s"]
	m.mu.RUnlock()
	if meta.NamingFailureCount != 0 {
		t.Fatalf("manual rename must not affect automatic failure count, got %d", meta.NamingFailureCount)
	}
}
