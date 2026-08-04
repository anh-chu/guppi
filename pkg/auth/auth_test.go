package auth

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"net/http"
	"net/http/httptest"
)

func TestNewPasswordStore_CorruptFailsClosed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := filepath.Join(dir, ".config", "termyard")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "auth.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := NewPasswordStore(); err == nil {
		t.Fatal("expected error for corrupt auth.json")
	}
}

func TestNewPasswordStore_UnreadableFailsClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read unreadable files")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := filepath.Join(dir, ".config", "termyard")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(cfg, "auth.json")
	if err := os.WriteFile(path, []byte(`{"password_hash":"$2a$10$abc"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	if _, err := NewPasswordStore(); err == nil {
		t.Fatal("expected error for unreadable auth.json")
	}
}

func TestSetPasswordIfUnset_RaceOneWinner(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	ps, err := NewPasswordStore()
	if err != nil {
		t.Fatalf("NewPasswordStore: %v", err)
	}

	var winners int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := ps.SetPasswordIfUnset("hunter1234!")
			if err != nil {
				t.Errorf("SetPasswordIfUnset: %v", err)
				return
			}
			if ok {
				atomic.AddInt32(&winners, 1)
			}
		}()
	}
	wg.Wait()

	if winners != 1 {
		t.Fatalf("expected exactly 1 winner, got %d", winners)
	}
	if !ps.HasPassword() {
		t.Fatal("password not set after race")
	}
}

func TestRequestIsSecure(t *testing.T) {
	tests := []struct {
		name   string
		tls    bool
		fwd    string
		remote string
		want   bool
	}{
		{"plain http", false, "", "192.168.1.2:1234", false},
		{"tls direct", true, "", "192.168.1.2:1234", true},
		{"loopback forwarded proto", false, "https", "127.0.0.1:1234", true},
		{"non-loopback forwarded proto", false, "https", "192.168.1.2:1234", false},
		{"forwarded proto http", false, "http", "127.0.0.1:1234", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			if tt.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if tt.fwd != "" {
				req.Header.Set("X-Forwarded-Proto", tt.fwd)
			}
			if got := RequestIsSecure(req); got != tt.want {
				t.Fatalf("RequestIsSecure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetupHandler_SecureCookie(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	ps, err := NewPasswordStore()
	if err != nil {
		t.Fatalf("NewPasswordStore: %v", err)
	}
	sm := NewSessionManager(time.Hour)
	lim := NewLimiter()
	handler := SetupHandler(ps, sm, lim)

	body, _ := json.Marshal(map[string]string{"password": "hunter1234!"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()[0]
	if !cookie.Secure {
		t.Fatal("expected Secure cookie for TLS request")
	}
}

func TestSetupHandler_NonSecureCookiePlainHTTP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	ps, err := NewPasswordStore()
	if err != nil {
		t.Fatalf("NewPasswordStore: %v", err)
	}
	sm := NewSessionManager(time.Hour)
	lim := NewLimiter()
	handler := SetupHandler(ps, sm, lim)

	body, _ := json.Marshal(map[string]string{"password": "hunter1234!"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()[0]
	if cookie.Secure {
		t.Fatal("expected non-Secure cookie for plain HTTP request")
	}
}

func TestSessionManager_PersistsSlidingExpiryOncePerMinute(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	now := time.Now()
	sm := NewSessionManager(time.Hour)
	sm.SetClock(func() time.Time { return now })

	token, err := sm.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	firstExpiry := sm.sessions[token]

	// Validation inside the same minute must not rewrite the file.
	if !sm.Validate(token) {
		t.Fatal("token unexpectedly invalid")
	}

	// After advancing past the write throttle the next validation persists.
	now = now.Add(2 * time.Minute)
	if !sm.Validate(token) {
		t.Fatal("token invalid after advancing clock")
	}

	sm2 := NewSessionManager(time.Hour)
	sm2.SetClock(func() time.Time { return now })
	if !sm2.Validate(token) {
		t.Fatal("token not restored with refreshed expiry")
	}

	// The refreshed expiry must be later than the original TTL.
	if !sm2.sessions[token].After(firstExpiry) {
		t.Fatalf("expiry not refreshed: %v vs %v", sm2.sessions[token], firstExpiry)
	}
}

func TestSessionManager_ExpiredTokenRemovedFromDisk(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	sm := NewSessionManager(time.Millisecond)
	token, err := sm.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	sm.Cleanup()
	data, err := os.ReadFile(sm.path)
	if err != nil {
		t.Fatalf("read sessions: %v", err)
	}
	if strings.Contains(string(data), token) {
		t.Fatal("expired token still persisted")
	}
}
