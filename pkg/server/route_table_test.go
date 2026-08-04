package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"



	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/portforward"
	"github.com/anh-chu/termyard/pkg/sessionattrs"
	"github.com/anh-chu/termyard/pkg/sessionorder"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/toolevents"
	"github.com/anh-chu/termyard/pkg/ws"
)

// TestRouteTableSnapshot pins the current HTTP/WS route set and auth policy
// using the real router construction. It builds the router with auth enabled
// and walks chi routes, then asserts that public routes stay public and
// protected routes require a session cookie.
func TestRouteTableSnapshot(t *testing.T) {
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
	tracker := toolevents.NewTracker()

	// Legacy mode: legacy stores are constructed, so legacy-only routes
	// (session-attrs, session-order, groups) must be present in the golden
	// route table below.
	attrsStore, err := sessionattrs.NewStore()
	if err != nil {
		t.Fatalf("sessionattrs.NewStore: %v", err)
	}
	orderStore, err := sessionorder.NewStore()
	if err != nil {
		t.Fatalf("sessionorder.NewStore: %v", err)
	}
	groupStore, err := groupsync.NewStore()
	if err != nil {
		t.Fatalf("groupsync.NewStore: %v", err)
	}

	opts := &Options{
		AuthEnabled:      true,
		Port:             7654,
		PasswordStore:    ps,
		SessionMgr:       sm,
		AuthLimiter:      auth.NewLimiter(),
		NotifyToken:      "notify-token-test",
		Tracker:          tracker,
		Hub:              ws.NewHub(state.NewManager(), tracker),
		AttrsStore:       attrsStore,
		OrderStore:       orderStore,
		GroupStore:       groupStore,
		PortForwardStore: portforward.NewStore(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, _, err := BuildRouter(ctx, opts)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}

	var routes []string
	chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+route)
		return nil
	})
	sort.Strings(routes)

	want := []string{
		"DELETE /api/crashed-sessions",
		"DELETE /api/crashed-sessions/{id}",
		"DELETE /api/peers/{fp}",
		"DELETE /api/portforward/{port}",
		"DELETE /api/schedules/{id}",
		"DELETE /api/tool-event",
		"DELETE /api/tool-events",
		"DELETE /proxy/{port}",
		"DELETE /proxy/{port}/*",
		"GET /*",
		"GET /api/active-turns",
		"GET /api/activity",
		"GET /api/agent-status",
		"GET /api/artifacts",
		"GET /api/auth/check",
		"GET /api/auth/status",
		"GET /api/crashed-sessions",
		"GET /api/groups",
		"GET /api/hosts",
		"GET /api/pane-capture",
		"GET /api/peers",
		"GET /api/portforwards",
		"GET /api/preferences",
		"GET /api/pty-benchmark",
		"GET /api/push/vapid-key",
		"GET /api/schedules",
		"GET /api/session-attrs",
		"GET /api/session-order",
		"GET /api/sessions",
		"GET /api/stats",
		"GET /api/tool-events",
		"GET /api/update",
		"GET /api/v2/bootstrap",
		"GET /api/version",
		"GET /api/wiki/status",
		"GET /debug/*",
		"GET /file",
		"GET /proxy/{port}",
		"GET /proxy/{port}/*",
		"GET /ws/daemon-session",
		"GET /ws/direct-session",
		"GET /ws/events",
		"GET /ws/session",
		"PATCH /api/peers/{fp}",
		"PATCH /proxy/{port}",
		"PATCH /proxy/{port}/*",
		"POST /api/auth/login",
		"POST /api/auth/logout",
		"POST /api/auth/setup",
		"POST /api/crashed-sessions/{id}/recover",
		"POST /api/group/name",
		"POST /api/groups",
		"POST /api/peers",
		"POST /api/peers/{fp}/reconnect",
		"POST /api/peers/bootstrap",
		"POST /api/portforwards",
		"POST /api/push/subscribe",
		"POST /api/push/unsubscribe",
		"POST /api/schedules",
		"POST /api/schedules/{id}/run",
		"POST /api/session/display-name",
		"POST /api/session/kill",
		"POST /api/session/new",
		"POST /api/session/regenerate-name",
		"POST /api/session/rename",
		"POST /api/session/select-window",
		"POST /api/session-attrs",
		"POST /api/session-order",
		"POST /api/tool-event",
		"POST /api/update/apply",
		"POST /api/update/check",
		"POST /api/upload",
		"POST /api/v2/session-commands",
		"POST /api/v2/workspace-commands",
		"POST /api/wiki/install",
		"POST /file/grant",
		"POST /proxy/{port}",
		"POST /proxy/{port}/*",
		"PUT /api/preferences",
		"PUT /api/schedules/{id}",
		"PUT /proxy/{port}",
		"PUT /proxy/{port}/*",
	}
	// Wiki routes only appear when opts.WikiLite is set.
	// Peer WebSocket routes only appear when opts.PeerHandler is set.

	missing, extra := diff(want, routes)
	if len(missing)+len(extra) > 0 {
		t.Errorf("route table mismatch\nmissing: %v\nextra: %v", missing, extra)
	}

	srv := httptest.NewServer(r)
	defer srv.Close()

	t.Run("public version", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/version")
		if err != nil {
			t.Fatalf("get version: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("version should be public; got %d", resp.StatusCode)
		}
	})

	t.Run("public tool-event with token", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"tool": "claude", "status": "active", "session": "s", "pane": "p"})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/tool-event", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer notify-token-test")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post tool-event: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("tool-event with token expected 204, got %d", resp.StatusCode)
		}
	})

	t.Run("protected sessions rejects anonymous", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/sessions")
		if err != nil {
			t.Fatalf("get sessions: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("sessions should require auth; got %d", resp.StatusCode)
		}
	})
}

func diff(a, b []string) (missing, extra []string) {
	ma := map[string]bool{}
	mb := map[string]bool{}
	for _, v := range a {
		ma[v] = true
	}
	for _, v := range b {
		mb[v] = true
	}
	for _, v := range a {
		if !mb[v] {
			missing = append(missing, v)
		}
	}
	for _, v := range b {
		if !ma[v] {
			extra = append(extra, v)
		}
	}
	return
}

// TestProxyMethodsForwards requests verifies that /proxy/{port}/* now accepts
// GET, POST, PUT, PATCH and DELETE and forwards the method to the upstream.
func TestProxyMethodsForwarded(t *testing.T) {
	called := make(map[string]bool)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called[r.Method+" "+r.URL.Path] = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	port := strings.TrimPrefix(upstream.URL, "http://127.0.0.1:")

	opts := &Options{Port: 7654, PortForwardStore: portforward.NewStore()}
	r := chi.NewRouter()
	registerProxyFileRoutes(r, opts)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/proxy/"+port+"/hello", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s proxy got status %d", method, rec.Code)
		}
		if !called[method+" /hello"] {
			t.Errorf("%s upstream was not called", method)
		}
	}
}

// TestProxyHTMLRewriteCap verifies that HTML bodies larger than maxHTMLRewrite
// pass through unchanged while small HTML bodies are still rewritten.
func TestProxyHTMLRewriteCap(t *testing.T) {
	rw := makeHTMLRewriter(1234)

	// Large body: one byte over the cap should be forwarded unchanged.
	large := bytes.Repeat([]byte("x"), maxHTMLRewrite+1)
	resp := &http.Response{
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(bytes.NewReader(large)),
		StatusCode: http.StatusOK,
	}
	if err := rw(resp); err != nil {
		t.Fatalf("large body rewrite error: %v", err)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read large body: %v", err)
	}
	if !bytes.Equal(out, large) {
		t.Errorf("large body was modified: got %d bytes, want %d bytes", len(out), len(large))
	}

	// Small body with an absolute path should be rewritten.
	small := []byte(`<html><a href="/foo">link</a></html>`)
	resp = &http.Response{
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(bytes.NewReader(small)),
		StatusCode: http.StatusOK,
	}
	if err := rw(resp); err != nil {
		t.Fatalf("small body rewrite error: %v", err)
	}
	out, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read small body: %v", err)
	}
	if !bytes.Contains(out, []byte("/proxy/1234/foo")) {
		t.Errorf("small body not rewritten: %s", string(out))
	}
}

// TestLegacyStoreRoutesNotRegisteredWhenNil verifies that legacy-only routes
// (session-attrs, session-order, groups) are not registered at all -- and so
// return 404, not merely 503 -- when their backing stores are nil, which is
// the case in v2 mode (direct-cutover: no legacy route surface exists).
func TestLegacyStoreRoutesNotRegisteredWhenNil(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	tracker := toolevents.NewTracker()
	hub := ws.NewHub(state.NewManager(), tracker)

	// Create options with nil legacy stores (simulating v2 mode).
	opts := &Options{
		Port:             7654,
		Tracker:          tracker,
		Hub:              hub,
		PortForwardStore: portforward.NewStore(),
		// AttrsStore, OrderStore, GroupStore all remain nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, _, err := BuildRouter(ctx, opts)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}

	srv := httptest.NewServer(r)
	defer srv.Close()

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"GET /api/session-attrs", http.MethodGet, "/api/session-attrs"},
		{"POST /api/session-attrs", http.MethodPost, "/api/session-attrs"},
		{"GET /api/session-order", http.MethodGet, "/api/session-order"},
		{"POST /api/session-order", http.MethodPost, "/api/session-order"},
		{"GET /api/groups", http.MethodGet, "/api/groups"},
		{"POST /api/groups", http.MethodPost, "/api/groups"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, srv.URL+tt.path, bytes.NewReader([]byte("{}")))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s returned %d, want %d (Not Found -- route must not be registered)",
					tt.name, resp.StatusCode, http.StatusNotFound)
			}
		})
	}
}

// TestLegacyStoreRoutesWorkWhenPresent verifies that in legacy mode (stores
// constructed and non-nil), session-attrs/session-order/groups routes are
// registered and functional, so gating registration on nil does not regress
// legacy-mode behavior.
func TestLegacyStoreRoutesWorkWhenPresent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	tracker := toolevents.NewTracker()
	hub := ws.NewHub(state.NewManager(), tracker)

	attrsStore, err := sessionattrs.NewStore()
	if err != nil {
		t.Fatalf("sessionattrs.NewStore: %v", err)
	}
	orderStore, err := sessionorder.NewStore()
	if err != nil {
		t.Fatalf("sessionorder.NewStore: %v", err)
	}
	groupStore, err := groupsync.NewStore()
	if err != nil {
		t.Fatalf("groupsync.NewStore: %v", err)
	}

	opts := &Options{
		Port:             7654,
		Tracker:          tracker,
		Hub:              hub,
		PortForwardStore: portforward.NewStore(),
		AttrsStore:       attrsStore,
		OrderStore:       orderStore,
		GroupStore:       groupStore,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, _, err := BuildRouter(ctx, opts)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}

	srv := httptest.NewServer(r)
	defer srv.Close()

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"GET /api/session-attrs", http.MethodGet, "/api/session-attrs"},
		{"GET /api/session-order", http.MethodGet, "/api/session-order"},
		{"GET /api/groups", http.MethodGet, "/api/groups"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tt.path)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s returned %d, want 200", tt.name, resp.StatusCode)
			}
		})
	}
}
