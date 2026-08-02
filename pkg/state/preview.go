package state

import (
	"time"

	"github.com/anh-chu/termyard/pkg/model"
)

// previewCacheEntry holds a debounced prompt-preview snapshot for a single
// session. It is guarded by Manager.previewMu.
type previewCacheEntry struct {
	preview     string
	lastAttempt time.Time
	refreshing  bool
	emptyStreak int
	generation  uint64
}

// lazyPreviews initializes the previews map for struct-literal Manager values.
func (m *Manager) lazyPreviews() {
	if m.previews == nil {
		m.previews = make(map[string]*previewCacheEntry)
	}
}

// preview returns the latest cached preview snapshot for a session. The
// returned string is a copy captured under previewMu, so callers read a stable
// value without racing async refresh goroutines.
func (m *Manager) preview(name string) string {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	m.lazyPreviews()
	if entry := m.previews[name]; entry != nil {
		return entry.preview
	}
	return ""
}

// shouldRefreshPreview reports whether a new capture is due for the session.
func (m *Manager) shouldRefreshPreview(name string) bool {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	m.lazyPreviews()
	entry := m.previews[name]
	if entry == nil {
		return true
	}
	if entry.refreshing {
		return false
	}
	return time.Since(entry.lastAttempt) >= promptPreviewInterval
}

// refreshPreview triggers an asynchronous refresh of the cached preview.
func (m *Manager) refreshPreview(name string) {
	m.refreshPreviewSync(name)
}

// refreshPreviewSync performs a synchronous refresh, respecting the throttle.
// Tests use this helper for deterministic assertions.
func (m *Manager) refreshPreviewSync(name string) {
	m.runPreviewRefresh(name)
}

// previewForceRefresh bypasses the throttle and refreshes synchronously.
// Intended for tests that need to check error behaviour deterministically.
func (m *Manager) previewForceRefresh(name string) {
	m.previewMu.Lock()
	m.lazyPreviews()
	entry := m.previews[name]
	if entry == nil {
		entry = &previewCacheEntry{}
		m.previews[name] = entry
	}
	entry.lastAttempt = time.Time{}
	m.previewMu.Unlock()
	m.refreshPreviewSync(name)
}

// startPreviewRefresh claims the refresh slot for a session. It returns false
// when another refresh is already running or the last attempt was recent.
func (m *Manager) startPreviewRefresh(name string) (uint64, bool) {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	m.lazyPreviews()
	entry := m.previews[name]
	if entry == nil {
		entry = &previewCacheEntry{}
		m.previews[name] = entry
	}
	now := time.Now()
	if entry.refreshing || now.Sub(entry.lastAttempt) < promptPreviewInterval {
		return 0, false
	}
	entry.refreshing = true
	entry.lastAttempt = now
	m.previewGen++
	entry.generation = m.previewGen
	return entry.generation, true
}

// runPreviewRefresh captures prompt data and stores the extracted preview.
func (m *Manager) runPreviewRefresh(name string) {
	gen, ok := m.startPreviewRefresh(name)
	if !ok {
		return
	}
	text, usedTail, err := m.capturePreview(name)
	if err != nil {
		m.previewUpdateError(name, gen)
		return
	}
	if !usedTail && len(text) > promptPreviewTailBytes {
		text = truncateBytesTail(text, promptPreviewTailBytes)
	}
	m.previewUpdateResult(name, gen, model.ExtractPromptPreview(text))
}

// capturePreview prefers TailCapturer when available and falls back to the
// full Capture method implemented by every DaemonRegistry.
func (m *Manager) capturePreview(name string) (string, bool, error) {
	if m.daemonReg == nil {
		return "", false, nil
	}
	if tc, ok := m.daemonReg.(TailCapturer); ok && tc != nil {
		text, err := tc.CaptureTail(name, promptPreviewTailBytes)
		return text, true, err
	}
	text, err := m.daemonReg.Capture(name)
	return text, false, err
}

// previewUpdateResult stores a successful extraction, clearing the cache only
// after several consecutive empty extractions. If the session's cache entry
// was evicted or recreated while the refresh was in flight, the result is
// dropped so a removed session cannot resurrect stale preview state.
func (m *Manager) previewUpdateResult(name string, gen uint64, extracted string) {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	entry := m.previews[name]
	if entry == nil || entry.generation != gen {
		return
	}
	entry.refreshing = false
	if extracted == "" {
		entry.emptyStreak++
		if entry.emptyStreak >= promptPreviewEmptyLimit {
			entry.preview = ""
			entry.emptyStreak = 0
		}
	} else {
		entry.preview = extracted
		entry.emptyStreak = 0
	}
}

// previewUpdateError keeps the last cached preview on capture failure so the
// UI still has a fallback, but records that an attempt occurred. If the
// session's cache entry was evicted or recreated while the refresh was in
// flight, it stays gone and no new stale state is introduced.
func (m *Manager) previewUpdateError(name string, gen uint64) {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	entry := m.previews[name]
	if entry == nil || entry.generation != gen {
		return
	}
	entry.refreshing = false
}

// evictPreview removes a session's cached preview.
func (m *Manager) evictPreview(name string) {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	delete(m.previews, name)
}

// truncateBytesTail returns the last n bytes of s, trimming to a valid UTF-8
// boundary so ExtractPromptPreview never sees a broken rune.
func truncateBytesTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := []byte(s)
	b = b[len(b)-n:]
	for len(b) > 0 && b[0]&0xC0 == 0x80 {
		b = b[1:]
	}
	return string(b)
}
