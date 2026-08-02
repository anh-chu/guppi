package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anh-chu/termyard/pkg/config"
	"golang.org/x/crypto/bcrypt"
)

// storedAuth is the on-disk format for auth credentials.
type storedAuth struct {
	PasswordHash string `json:"password_hash"`
}

// PasswordStore manages password hashing and verification with file persistence.
type PasswordStore struct {
	mu   sync.RWMutex
	path string
	hash []byte
}

// NewPasswordStore creates a store backed by ~/.config/termyard/auth.json.
// If a credential file already exists but cannot be read or decoded, the
// store returns an error instead of falling back to setup mode.
func NewPasswordStore() (*PasswordStore, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ps := &PasswordStore{
		path: filepath.Join(dir, "auth.json"),
	}
	data, err := os.ReadFile(ps.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ps, nil
		}
		return nil, fmt.Errorf("read existing auth credentials: %w", err)
	}
	var stored storedAuth
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("corrupt auth credentials: %w", err)
	}
	if stored.PasswordHash == "" {
		return nil, fmt.Errorf("corrupt auth credentials: empty password hash")
	}
	ps.hash = []byte(stored.PasswordHash)
	return ps, nil
}

// HasPassword returns true if a password hash is stored.
func (ps *PasswordStore) HasPassword() bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return len(ps.hash) > 0
}

// SetPassword hashes and persists the given password. It overwrites any
// existing credential file. Use SetPasswordIfUnset for the first-run race.
func (ps *PasswordStore) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.hash = hash
	stored := storedAuth{PasswordHash: string(hash)}
	return writeAtomicJSON(ps.path, stored, 0o600)
}

// SetPasswordIfUnset sets the password only if no password hash is currently
// stored. It returns true when the password was written by this call. The
// operation is atomic under the store lock so concurrent setup attempts have
// exactly one winner.
func (ps *PasswordStore) SetPasswordIfUnset(password string) (bool, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if len(ps.hash) > 0 {
		return false, nil
	}
	// Re-read the file while holding the lock to close the TOCTOU window.
	if data, err := os.ReadFile(ps.path); err == nil {
		var stored storedAuth
		if err := json.Unmarshal(data, &stored); err == nil && stored.PasswordHash != "" {
			ps.hash = []byte(stored.PasswordHash)
			return false, nil
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, err
	}
	ps.hash = hash
	stored := storedAuth{PasswordHash: string(hash)}
	if err := writeAtomicJSON(ps.path, stored, 0o600); err != nil {
		ps.hash = nil
		return false, err
	}
	return true, nil
}

// Verify checks a password against the stored hash.
func (ps *PasswordStore) Verify(password string) bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if len(ps.hash) == 0 {
		return false
	}
	return bcrypt.CompareHashAndPassword(ps.hash, []byte(password)) == nil
}

func writeAtomicJSON(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// SessionManager manages session tokens with expiry, persisted to disk so
// they survive server restarts.
type SessionManager struct {
	mu        sync.RWMutex
	sessions  map[string]time.Time
	lastSaved map[string]time.Time
	ttl       time.Duration
	path      string
	clock     func() time.Time
}

// NewSessionManager creates a session manager with the given TTL. Sessions are
// persisted to ~/.config/termyard/sessions.json and reloaded on startup, with
// already-expired entries pruned.
func NewSessionManager(ttl time.Duration) *SessionManager {
	sm := &SessionManager{
		sessions:  make(map[string]time.Time),
		lastSaved: make(map[string]time.Time),
		ttl:       ttl,
	}
	if dir, err := config.Dir(); err == nil {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			sm.path = filepath.Join(dir, "sessions.json")
			sm.load()
		}
	}
	return sm
}

// SetClock is used by tests to inject a deterministic clock.
func (sm *SessionManager) SetClock(now func() time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.clock = now
}

func (sm *SessionManager) now() time.Time {
	if sm.clock != nil {
		return sm.clock()
	}
	return time.Now()
}

// load reads persisted sessions from disk, dropping expired entries. Caller
// must not hold the lock. Best-effort: a missing file is ignored; a corrupt
// file is treated as empty.
func (sm *SessionManager) load() {
	data, err := os.ReadFile(sm.path)
	if err != nil {
		return
	}
	var stored map[string]time.Time
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	now := sm.now()
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for token, expiry := range stored {
		if now.Before(expiry) {
			sm.sessions[token] = expiry
		}
	}
}

// save writes the current sessions to disk. Caller must not hold sm.mu.
func (sm *SessionManager) save() {
	sm.mu.Lock()
	if sm.path == "" {
		sm.mu.Unlock()
		return
	}
	sessions := make(map[string]time.Time, len(sm.sessions))
	for k, v := range sm.sessions {
		sessions[k] = v
	}
	sm.mu.Unlock()

	data, err := json.Marshal(sessions)
	if err != nil {
		return
	}
	_ = os.WriteFile(sm.path, data, 0o600)
}

// Create generates a new session token and persists it immediately.
func (sm *SessionManager) Create() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	now := sm.now()
	sm.mu.Lock()
	sm.sessions[token] = now.Add(sm.ttl)
	sm.lastSaved[token] = now
	sm.mu.Unlock()
	sm.save()
	return token, nil
}

// Validate checks if a token is valid, refreshes its sliding-window expiry,
// and persists the new expiry at most once per token per minute.
func (sm *SessionManager) Validate(token string) bool {
	now := sm.now()
	sm.mu.Lock()
	expiry, ok := sm.sessions[token]
	if !ok || now.After(expiry) {
		delete(sm.sessions, token)
		delete(sm.lastSaved, token)
		sm.mu.Unlock()
		return false
	}
	sm.sessions[token] = now.Add(sm.ttl)
	needSave := sm.lastSaved[token].Before(now.Add(-time.Minute))
	if needSave {
		sm.lastSaved[token] = now
	}
	sm.mu.Unlock()
	if needSave {
		sm.save()
	}
	return true
}

// Revoke removes a session token and persists immediately.
func (sm *SessionManager) Revoke(token string) {
	sm.mu.Lock()
	delete(sm.sessions, token)
	delete(sm.lastSaved, token)
	sm.mu.Unlock()
	sm.save()
}

// Cleanup removes expired sessions and their bookkeeping. Call periodically.
func (sm *SessionManager) Cleanup() {
	now := sm.now()
	sm.mu.Lock()
	for token, expiry := range sm.sessions {
		if now.After(expiry) {
			delete(sm.sessions, token)
			delete(sm.lastSaved, token)
		}
	}
	sm.mu.Unlock()
	sm.save()
}

// CookieName is termyard's session cookie. Exported so the wiki proxy can
// strip it before forwarding a request to the wiki-viewer-lite child.
const CookieName = "termyard_session"
const cookieMaxAge = 86400 // 24h, matches SessionManager TTL

// writeSessionCookie sets the session cookie with the standard attributes.
func writeSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   cookieMaxAge,
	})
}

// RequestIsSecure reports whether the request is on a secure transport.
// It treats the connection as secure when it arrived over TLS or when a
// trusted loopback proxy terminates TLS and sets X-Forwarded-Proto: https.
func RequestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if !strings.EqualFold(proto, "https") {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return isLoopback(host)
}

// IsLoopbackRequest reports whether the request originated from a loopback
// address. It treats unix sockets as loopback-local.
func IsLoopbackRequest(r *http.Request) bool {
	if IsUnixSocket(r) {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return isLoopback(host)
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// IsUnixSocket returns true if the request arrived over a unix socket.
func IsUnixSocket(r *http.Request) bool {
	addr := r.Context().Value(http.LocalAddrContextKey)
	if addr == nil {
		return false
	}
	_, ok := addr.(*net.UnixAddr)
	return ok
}

// ClientIP returns the IP part of r.RemoteAddr.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// Middleware returns chi-compatible middleware that enforces session auth.
// Requests arriving over unix sockets bypass auth.
func Middleware(sm *SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Unix socket connections are trusted (local CLI)
			if IsUnixSocket(r) {
				next.ServeHTTP(w, r)
				return
			}
			cookie, err := r.Cookie(CookieName)
			if err != nil || !sm.Validate(cookie.Value) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"error":"unauthorized"}`)
				return
			}
			// Refresh the cookie so it tracks the sliding session; the cookie
			// is Secure only when the request arrived over a secure transport.
			writeSessionCookie(w, cookie.Value, RequestIsSecure(r))
			next.ServeHTTP(w, r)
		})
	}
}

// rateLimitJSON writes a rate-limit error response with a Retry-After header.
func rateLimitJSON(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(retryAfter.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
	w.WriteHeader(http.StatusTooManyRequests)
	fmt.Fprintf(w, `{"error":"rate limit","retry_after":%d}`, seconds)
}

// SetupHandler returns a handler for POST /api/auth/setup.
// Sets the initial password. Rejects if a password is already set.
func SetupHandler(ps *PasswordStore, sm *SessionManager, limiter *Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, retry := limiter.Allow("setup", ClientIP(r)); !ok {
			rateLimitJSON(w, retry)
			return
		}

		var req struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
			http.Error(w, `{"error":"password is required"}`, http.StatusBadRequest)
			return
		}
		if len(req.Password) < 8 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"password must be at least 8 characters"}`)
			return
		}

		set, err := ps.SetPasswordIfUnset(req.Password)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if !set {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":"password already set"}`)
			return
		}

		// Auto-login after setup
		token, err := sm.Create()
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeSessionCookie(w, token, RequestIsSecure(r))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true}`)
	}
}

// LoginHandler returns a handler for POST /api/auth/login.
func LoginHandler(ps *PasswordStore, sm *SessionManager, limiter *Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, retry := limiter.Allow("login", ClientIP(r)); !ok {
			rateLimitJSON(w, retry)
			return
		}

		var req struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
			http.Error(w, `{"error":"password is required"}`, http.StatusBadRequest)
			return
		}
		if !ps.Verify(req.Password) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":"invalid password"}`)
			return
		}
		token, err := sm.Create()
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeSessionCookie(w, token, RequestIsSecure(r))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true}`)
	}
}

// LogoutHandler returns a handler for POST /api/auth/logout.
func LogoutHandler(sm *SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(CookieName); err == nil {
			sm.Revoke(cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     CookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// CheckHandler returns a handler for GET /api/auth/check.
func CheckHandler(sm *SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(CookieName)
		if err != nil || !sm.Validate(cookie.Value) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"authenticated":false}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"authenticated":true}`)
	}
}

// StatusHandler returns a handler for GET /api/auth/status.
// Always public — tells the frontend whether auth is enabled and if setup is needed.
func StatusHandler(authEnabled bool, ps *PasswordStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		needsSetup := false
		if authEnabled && ps != nil {
			needsSetup = !ps.HasPassword()
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"auth_required":%v,"needs_setup":%v}`, authEnabled, needsSetup)
	}
}
