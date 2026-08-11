package state

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/pty"
)

// fakePreviewRegistry implements DaemonRegistry and TailCapturer.
type fakePreviewRegistry struct {
	mu           sync.Mutex
	dead         map[string]bool
	captureHook  func(name string) (string, error)
	tailHook     func(name string, maxBytes int) (string, error)
	captureCalls int
	tailCalls    int
}

func (f *fakePreviewRegistry) List() []pty.SessionInfo { return nil }

func (f *fakePreviewRegistry) Capture(name string) (string, error) {
	f.mu.Lock()
	f.captureCalls++
	f.mu.Unlock()
	if f.captureHook != nil {
		return f.captureHook(name)
	}
	return "", nil
}

func (f *fakePreviewRegistry) CaptureTail(name string, maxBytes int) (string, error) {
	f.mu.Lock()
	f.tailCalls++
	f.mu.Unlock()
	if f.tailHook != nil {
		return f.tailHook(name, maxBytes)
	}
	return "", nil
}

func (f *fakePreviewRegistry) counts() (captureCalls, tailCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.captureCalls, f.tailCalls
}

func newManagerWithPreview(reg DaemonRegistry) *Manager {
	m := &Manager{
		sessions:  make(map[string]*model.Session),
		meta:      make(map[string]SessionMetadata),
		daemonReg: reg,
	}
	return m
}

func sessionNamed(name string) *model.Session {
	return &model.Session{Name: name}
}

// enrich runs the production enrichment path on a session, which applies the
// cached preview and kicks an async refresh when due.
func enrich(m *Manager, s *model.Session) {
	m.enrichSessionInPlaceWithMeta(s, &pty.SessionInfo{ID: s.Name}, SessionMetadata{})
}

func TestPreview_TailCapturerBoundedTo64KiB(t *testing.T) {
	const big = 200_000
	const wantMax = promptPreviewTailBytes
	var gotMax int

	reg := &fakePreviewRegistry{
		tailHook: func(name string, maxBytes int) (string, error) {
			gotMax = maxBytes
			padding := strings.Repeat("x", big-maxBytes)
			return padding + "\n$ tail preview here\n", nil
		},
	}
	m := newManagerWithPreview(reg)
	s := sessionNamed("s1")

	enrich(m, s)
	m.refreshPreviewSync("s1")
	// The cache is populated; apply it to the session object.
	enrich(m, s)

	if gotMax != wantMax {
		t.Fatalf("CaptureTail maxBytes = %d, want %d", gotMax, wantMax)
	}
	if s.PromptPreview != "tail preview here" {
		t.Fatalf("PromptPreview = %q, want %q", s.PromptPreview, "tail preview here")
	}
	c, tl := reg.counts()
	if c != 0 {
		t.Fatalf("Capture called %d times, want 0", c)
	}
	if tl != 1 {
		t.Fatalf("CaptureTail called %d times, want 1", tl)
	}
}

func TestPreview_ThrottledToThirtySeconds(t *testing.T) {
	var calls atomic.Int32
	reg := &fakePreviewRegistry{
		tailHook: func(string, int) (string, error) {
			calls.Add(1)
			return "$ first\n", nil
		},
	}
	m := newManagerWithPreview(reg)

	m.refreshPreviewSync("s1")
	m.refreshPreviewSync("s1")

	if got := calls.Load(); got != 1 {
		t.Fatalf("tail capture calls = %d, want 1", got)
	}
}

func TestPreview_FullCaptureFallback(t *testing.T) {
	// fakeFallbackRegistry only implements DaemonRegistry, not TailCapturer.
	reg := &fakeFallbackRegistry{
		captureHook: func(name string) (string, error) {
			padding := strings.Repeat("z", promptPreviewTailBytes+1000)
			return padding + "\n$ fallback prompt\n", nil
		},
	}
	m := newManagerWithPreview(reg)

	m.refreshPreviewSync("s1")

	if got := m.preview("s1"); got != "fallback prompt" {
		t.Fatalf("preview = %q, want %q", got, "fallback prompt")
	}
	if reg.captureCalls != 1 {
		t.Fatalf("Capture calls = %d, want 1", reg.captureCalls)
	}
}

// fakeFallbackRegistry implements DaemonRegistry but deliberately omits CaptureTail.
type fakeFallbackRegistry struct {
	captureCalls int
	captureHook  func(name string) (string, error)
}

func (f *fakeFallbackRegistry) List() []pty.SessionInfo { return nil }
func (f *fakeFallbackRegistry) Capture(name string) (string, error) {
	f.captureCalls++
	if f.captureHook != nil {
		return f.captureHook(name)
	}
	return "", nil
}

func TestPreview_StaleOnError(t *testing.T) {
	reg := &fakePreviewRegistry{
		tailHook: func(string, int) (string, error) {
			return "$ preserved\n", nil
		},
	}
	m := newManagerWithPreview(reg)
	m.refreshPreviewSync("s1")
	if got := m.preview("s1"); got != "preserved" {
		t.Fatalf("initial preview = %q, want preserved", got)
	}

	reg.tailHook = func(string, int) (string, error) {
		return "", errors.New("boom")
	}
	m.previewForceRefresh("s1")

	if got := m.preview("s1"); got != "preserved" {
		t.Fatalf("preview after error = %q, want preserved", got)
	}
}

func TestPreview_EmptyStreakClearsCache(t *testing.T) {
	reg := &fakePreviewRegistry{
		tailHook: func(string, int) (string, error) { return "$ first\n", nil },
	}
	m := newManagerWithPreview(reg)
	m.refreshPreviewSync("s1")
	if got := m.preview("s1"); got != "first" {
		t.Fatalf("initial preview = %q, want first", got)
	}

	reg.tailHook = func(string, int) (string, error) { return "no prompt here", nil }
	for i := 0; i < promptPreviewEmptyLimit; i++ {
		m.previewForceRefresh("s1")
	}

	if got := m.preview("s1"); got != "" {
		t.Fatalf("preview after %d empty captures = %q, want empty", promptPreviewEmptyLimit, got)
	}
}

func TestPreview_EnrichIsNonBlocking(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	reg := &fakePreviewRegistry{
		tailHook: func(string, int) (string, error) {
			started <- struct{}{}
			<-block
			return "$ ok", nil
		},
	}
	m := newManagerWithPreview(reg)
	s := sessionNamed("s1")

	done := make(chan struct{})
	go func() {
		enrich(m, s)
		close(done)
	}()

	select {
	case <-done:
		// expected: enrichment returns immediately
	case <-time.After(2 * time.Second):
		t.Fatal("enrichment blocked on capture")
	}

	<-started
	close(block)
	eventually(t, func() bool {
		_, tl := reg.counts()
		return tl == 1
	})
}

func TestPreview_RepeatedEnrichDoesOneCapture(t *testing.T) {
	reg := &fakePreviewRegistry{
		tailHook: func(string, int) (string, error) { return "$ p", nil },
	}
	m := newManagerWithPreview(reg)
	s := sessionNamed("s1")

	enrich(m, s)
	enrich(m, s)

	// Complete the one background refresh.
	m.refreshPreviewSync(s.Name)

	_, tl := reg.counts()
	if tl != 1 {
		t.Fatalf("CaptureTail calls = %d, want 1", tl)
	}
}

func TestPreview_SingleFlight(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	reg := &fakePreviewRegistry{
		tailHook: func(string, int) (string, error) {
			started <- struct{}{}
			<-block
			return "$ one", nil
		},
	}
	m := newManagerWithPreview(reg)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.refreshPreview("s1")
		}()
	}

	<-started
	// Only one goroutine should have started a capture; the rest are single-flighted away.
	if _, tl := reg.counts(); tl != 1 {
		t.Fatalf("capture count before unblock = %d, want 1", tl)
	}
	close(block)
	wg.Wait()

	_, tl := reg.counts()
	if tl != 1 {
		t.Fatalf("CaptureTail calls = %d, want 1", tl)
	}
}

func TestPreview_EvictedOnRemoveSession(t *testing.T) {
	reg := &fakePreviewRegistry{
		tailHook: func(string, int) (string, error) { return "$ x", nil },
	}
	m := newManagerWithPreview(reg)
	m.sessions["s1"] = &model.Session{Name: "s1"}
	m.refreshPreviewSync("s1")

	if m.preview("s1") == "" {
		t.Fatal("preview missing before RemoveSession")
	}

	m.RemoveSession("s1")
	if m.preview("s1") != "" {
		t.Fatal("preview string not evicted by RemoveSession")
	}
}

func TestPreview_UnattachedSessionStillRefreshed(t *testing.T) {
	reg := &fakePreviewRegistry{
		tailHook: func(string, int) (string, error) { return "$ unattached", nil },
	}
	m := newManagerWithPreview(reg)
	s := sessionNamed("orphan")

	enrich(m, s)
	m.refreshPreviewSync("orphan")
	enrich(m, s)

	if s.PromptPreview != "unattached" {
		t.Fatalf("PromptPreview = %q, want unattached", s.PromptPreview)
	}
}

func TestPreview_NoResurrectIfEvictedDuringRefresh(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	reg := &fakePreviewRegistry{
		tailHook: func(string, int) (string, error) {
			started <- struct{}{}
			<-block
			return "$ should-not-resurrect", nil
		},
	}
	m := newManagerWithPreview(reg)
	m.sessions["s1"] = &model.Session{Name: "s1"}
	m.previewMu.Lock()
	m.lazyPreviews()
	m.previews["s1"] = &previewCacheEntry{preview: "old", lastAttempt: time.Now().Add(-promptPreviewInterval)}
	m.previewMu.Unlock()

	// Start refresh asynchronously, then evict while capture is in flight.
	done := make(chan struct{})
	go func() {
		m.previewForceRefresh("s1")
		close(done)
	}()
	<-started
	m.evictPreview("s1")
	close(block)
	<-done

	if got := m.preview("s1"); got != "" {
		t.Fatalf("preview resurrected after eviction = %q, want empty", got)
	}
}

func TestPreview_TruncateBeforeExtract(t *testing.T) {
	// Build a string larger than the tail limit where a prompt appears only at
	// the very beginning, so truncating from the start gives an empty preview.
	padding := strings.Repeat("a", promptPreviewTailBytes+500)
	reg := &fakeFallbackRegistry{
		captureHook: func(string) (string, error) {
			return "$ early prompt\n" + padding, nil
		},
	}
	m := newManagerWithPreview(reg)
	m.refreshPreviewSync("s1")

	if got := m.preview("s1"); got == "early prompt" {
		t.Fatal("preview extracted from oversized beginning instead of tail")
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never satisfied")
}
