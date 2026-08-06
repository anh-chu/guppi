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

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/portforward"
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
	catalog, svc := newStateTestCatalog(t)

	opts := &Options{
		AuthEnabled:      true,
		Port:             7654,
		PasswordStore:    ps,
		SessionMgr:       sm,
		AuthLimiter:      auth.NewLimiter(),
		NotifyToken:      "notify-token-test",
		Tracker:          tracker,
		Hub:              ws.NewHub(tracker),
		Catalog:          catalog,
		CommandSvc:       svc,
		StateStream:      ws.NewStateStreamHub(catalog, nil),
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
		"GET /api/hosts",
		"GET /api/pane-capture",
		"GET /api/peers",
		"GET /api/portforwards",
		"GET /api/preferences",
		"GET /api/pty-benchmark",
		"GET /api/push/vapid-key",
		"GET /api/schedules",
		"GET /api/stats",
		"GET /api/tool-events",
		"GET /api/update",
		"GET /api/state/bootstrap",
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
		"GET /ws/state",
		"PATCH /api/peers/{fp}",
		"PATCH /proxy/{port}",
		"PATCH /proxy/{port}/*",
		"POST /api/auth/login",
		"POST /api/auth/logout",
		"POST /api/auth/setup",
		"POST /api/crashed-sessions/{id}/recover",
		"POST /api/peers",
		"POST /api/peers/{fp}/reconnect",
		"POST /api/peers/bootstrap",
		"POST /api/portforwards",
		"POST /api/push/subscribe",
		"POST /api/push/unsubscribe",
		"POST /api/schedules",
		"POST /api/schedules/{id}/run",
		"POST /api/tool-event",
		"POST /api/update/apply",
		"POST /api/update/check",
		"POST /api/upload",
		"POST /api/state/session-commands",
		"POST /api/state/workspace-commands",
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

	// Note: Task 0 documents that in the final schema, /api/hosts and
	// /api/activity routes will be deleted because:
	// - Host state comes from bootstrap and /ws/state only.
	// - Activity is replaced by canonical runtime snapshots.
	// Until Task 5-6 implement those changes, these routes remain.

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

	t.Run("protected state session-commands rejects anonymous", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/api/state/session-commands", "application/json", bytes.NewReader([]byte("{}")))
		if err != nil {
			t.Fatalf("post state/session-commands: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("state/session-commands should require auth; got %d", resp.StatusCode)
		}
	})

	// The old per-verb session routes were deleted in favor of the single
	// canonical POST /api/state/session-commands lifecycle API (create/kill/
	// rename/label all go through it as an action). They must return 404
	// unconditionally, not merely reject for lack of auth, proving they are
	// not registered at all.
	t.Run("deleted duplicate session routes return 404", func(t *testing.T) {
		deleted := []struct {
			method string
			path   string
		}{
			{http.MethodGet, "/api/sessions"},
			{http.MethodPost, "/api/session/new"},
			{http.MethodPost, "/api/session/display-name"},
			{http.MethodPost, "/api/session/regenerate-name"},
			{http.MethodPost, "/api/session/rename"},
			{http.MethodPost, "/api/session/select-window"},
			{http.MethodPost, "/api/session/kill"},
		}
		for _, d := range deleted {
			req, err := http.NewRequest(d.method, srv.URL+d.path, bytes.NewReader([]byte("{}")))
			if err != nil {
				t.Fatalf("build request for %s %s: %v", d.method, d.path, err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", d.method, d.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s: got %d, want 404 (route must not be registered)", d.method, d.path, resp.StatusCode)
			}
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

// TestLegacyStoreRoutesNeverExist verifies that the legacy session-attrs,
// session-order, and groups sync routes -- which only ever existed to serve
// AppLegacy -- are not registered at all in the canonical-only architecture,
// regardless of options. There is no store to gate them on any more; they
// must return 404 unconditionally.
func TestLegacyStoreRoutesNeverExist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	tracker := toolevents.NewTracker()
	hub := ws.NewHub(tracker)
	catalog, svc := newStateTestCatalog(t)

	opts := &Options{
		Port:             7654,
		Tracker:          tracker,
		Hub:              hub,
		Catalog:          catalog,
		CommandSvc:       svc,
		StateStream:      ws.NewStateStreamHub(catalog, nil),
		PortForwardStore: portforward.NewStore(),
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
		{"POST /api/group/name", http.MethodPost, "/api/group/name"},
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

// TestSchema4HostsAndActivityRoutesDELETED_FAILS documents the contract that
// /api/hosts and /api/activity routes are deleted in the final canonical design.
// These routes will be removed during Task 5-6 when host and runtime state
// moves to bootstrap and /ws/state streaming.
func TestSchema4HostsAndActivityRoutesDELETED_FAILS(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	tracker := toolevents.NewTracker()
	hub := ws.NewHub(tracker)
	catalog, svc := newStateTestCatalog(t)

	opts := &Options{
		Port:             7654,
		Tracker:          tracker,
		ActivityTracker:  activity.NewTracker(),
		Hub:              hub,
		Catalog:          catalog,
		CommandSvc:       svc,
		StateStream:      ws.NewStateStreamHub(catalog, nil),
		PortForwardStore: portforward.NewStore(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, _, err := BuildRouter(ctx, opts)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}

	srv := httptest.NewServer(r)
	defer srv.Close()

	// After Task 5-6, these routes must return 404 (not be registered at all).
	// Currently they exist, so this test documents what MUST be true.
	neededRoutes := []struct {
		name   string
		path   string
		method string
	}{
		{"GET /api/hosts must be deleted", "/api/hosts", http.MethodGet},
		{"GET /api/activity must be deleted", "/api/activity", http.MethodGet},
	}

	for _, tt := range neededRoutes {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, srv.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()

			// In the final schema, these must return 404.
			// Currently they return 200/data, so this test documents the target contract.
			if resp.StatusCode == http.StatusOK {
				t.Logf("EXPECTED FAILURE: %s currently returns 200; will be deleted in Task 5-6", tt.name)
				t.Fail()
			}
		})
	}
}
