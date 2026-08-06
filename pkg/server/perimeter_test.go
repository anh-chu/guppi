package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/toolevents"
	"github.com/anh-chu/termyard/pkg/ws"
)

func toolEventRouter(t *testing.T, authEnabled bool) (chi.Router, *Options) {
	t.Helper()
	tracker := toolevents.NewTracker()
	opts := &Options{
		AuthEnabled: authEnabled,
		Tracker:     tracker,
	}
	if authEnabled {
		opts.AuthLimiter = auth.NewLimiter()
		opts.NotifyToken = "supersecrettoken"
	}
	hub := ws.NewHub(tracker)
	r := chi.NewRouter()
	registerAPIRoutes(r, opts, hub)
	return r, opts
}

func TestToolEvent_TCPAnonymousRejected(t *testing.T) {
	r, _ := toolEventRouter(t, true)
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{
		"tool": "claude", "status": "active", "session": "s", "pane": "p",
	})
	resp, err := http.Post(srv.URL+"/api/tool-event", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestToolEvent_TCPWithBearerAccepted(t *testing.T) {
	r, opts := toolEventRouter(t, true)
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{
		"tool": "claude", "status": "active", "session": "s", "pane": "p",
	})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/tool-event", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opts.NotifyToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestToolEvent_UnixSocketAccepted(t *testing.T) {
	r, _ := toolEventRouter(t, true)

	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: r}
	go srv.Serve(ln)
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{
		"tool": "claude", "status": "active", "session": "s", "pane": "p",
	})
	client := &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
	resp, err := client.Post("http://localhost/api/tool-event", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post unix: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for unix socket, got %d", resp.StatusCode)
	}
}

func TestPprof_AbsentByDefault(t *testing.T) {
	r := chi.NewRouter()
	registerAPIRoutes(r, &Options{}, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestPprof_EnabledRequiresLoopbackAndAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	ps, err := auth.NewPasswordStore()
	if err != nil {
		t.Fatalf("NewPasswordStore: %v", err)
	}
	if err := ps.SetPassword("hunter1234!"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	sm := auth.NewSessionManager(time.Hour)

	opts := &Options{
		AuthEnabled:   true,
		DebugPprof:    true,
		PasswordStore: ps,
		SessionMgr:    sm,
		AuthLimiter:   auth.NewLimiter(),
	}

	r := chi.NewRouter()
	// Simulate the mount in Run(): loopback + auth + profiler.
	if opts.DebugPprof {
		debugRouter := chi.NewRouter()
		debugRouter.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !auth.IsLoopbackRequest(r) {
					http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		if opts.AuthEnabled {
			debugRouter.Use(auth.Middleware(opts.SessionMgr))
		}
		debugRouter.Mount("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		r.Mount("/debug", debugRouter)
	}

	// Non-loopback should be rejected before auth.
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-loopback, got %d", rec.Code)
	}

	// Loopback without cookie should be rejected by auth.
	req = httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.RemoteAddr = "127.0.0.1:5678"
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", rec.Code)
	}

	// Loopback with valid cookie should pass.
	token, err := sm.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.RemoteAddr = "127.0.0.1:5678"
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with cookie, got %d", rec.Code)
	}
}

func TestPeerBootstrap_RateLimited(t *testing.T) {
	r, _ := toolEventRouter(t, true)
	srv := httptest.NewServer(r)
	defer srv.Close()

	for i := 0; i < 6; i++ {
		body := bytes.NewReader([]byte(`{"password":"x"}`))
		resp, err := http.Post(srv.URL+"/api/peers/bootstrap", "application/json", body)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		resp.Body.Close()
		if i < 5 {
			if resp.StatusCode == http.StatusTooManyRequests {
				t.Fatalf("request %d unexpectedly rate limited", i+1)
			}
		} else {
			if resp.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("expected 429, got %d", resp.StatusCode)
			}
			if resp.Header.Get("Retry-After") == "" {
				t.Fatal("missing Retry-After header")
			}
		}
	}
}
