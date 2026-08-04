package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/state"
)

func newV2TestCatalog(t *testing.T) (*state.Catalog, *state.SessionCommandService) {
	t.Helper()
	owner := state.NewOwnerID()
	catalog := state.NewCatalog(owner, nil)
	if err := catalog.Load(); err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	backend := &noopBackend{}
	svc := state.NewSessionCommandService(catalog, backend, nil, state.SessionCommandServiceOptions{Owner: owner})
	return catalog, svc
}

func newV2TestRouter(opts *Options) chi.Router {
	r := chi.NewRouter()
	registerStateV2Routes(r, opts)
	return r
}

func TestV2BootstrapReturnsOneCompleteSnapshot(t *testing.T) {
	catalog, svc := newV2TestCatalog(t)
	opts := &Options{V2Catalog: catalog, V2CommandSvc: svc}
	r := newV2TestRouter(opts)

	params, _ := json.Marshal(state.CreateParams{Name: "alpha"})
	if _, err := svc.ExecuteSessionCommand(t.Context(), state.SessionCommand{
		ID: state.NewCommandID(), Action: state.ActionCreate, Params: params,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/bootstrap", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp v2BootstrapResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Hosts == nil {
		t.Error("expected hosts field to be present (even if empty)")
	}
	if resp.Pending == nil {
		t.Error("expected pending field to be present (even if empty)")
	}
	if len(resp.Layouts) == 0 {
		t.Error("expected at least one layout after create")
	}
	if resp.Workspace == nil {
		t.Error("expected a workspace snapshot once a layout exists")
	}
}

func TestV2BootstrapUnavailableWithoutCatalog(t *testing.T) {
	r := newV2TestRouter(&Options{})
	req := httptest.NewRequest(http.MethodGet, "/v2/bootstrap", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestV2SessionCommandCreateAndTypedErrors(t *testing.T) {
	catalog, svc := newV2TestCatalog(t)
	opts := &Options{V2Catalog: catalog, V2CommandSvc: svc}
	r := newV2TestRouter(opts)

	body, _ := json.Marshal(map[string]any{
		"action": state.ActionCreate,
		"params": state.CreateParams{Name: "beta"},
	})
	req := httptest.NewRequest(http.MethodPost, "/v2/session-commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Invalid input: unknown field in the strict tagged-union envelope.
	bad := []byte(`{"action":"create","bogus_field":true}`)
	req = httptest.NewRequest(http.MethodPost, "/v2/session-commands", bytes.NewReader(bad))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown field, got %d", w.Code)
	}
	var errResp v2ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Code != "invalid_input" {
		t.Fatalf("expected invalid_input, got %q", errResp.Code)
	}

	// Not found: kill a session that does not exist.
	killBody, _ := json.Marshal(map[string]any{
		"ref":    state.SessionRef{Owner: catalog.Owner(), Session: state.NewSessionID()},
		"action": state.ActionKill,
	})
	req = httptest.NewRequest(http.MethodPost, "/v2/session-commands", bytes.NewReader(killBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown session, got %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Code != "not_found" {
		t.Fatalf("expected not_found, got %q", errResp.Code)
	}

	// Wrong content type is rejected.
	req = httptest.NewRequest(http.MethodPost, "/v2/session-commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", w.Code)
	}
}

func TestV2WorkspaceCommandRevisionConflict(t *testing.T) {
	catalog, svc := newV2TestCatalog(t)
	opts := &Options{V2Catalog: catalog, V2CommandSvc: svc}
	r := newV2TestRouter(opts)

	params, _ := json.Marshal(state.CreateParams{Name: "gamma"})
	if _, err := svc.ExecuteSessionCommand(t.Context(), state.SessionCommand{
		ID: state.NewCommandID(), Action: state.ActionCreate, Params: params,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	layouts := catalog.Layouts()
	if len(layouts) != 1 {
		t.Fatalf("expected one layout, got %d", len(layouts))
	}
	layoutID := layouts[0].ID
	sessions := catalog.PendingCreates()
	if len(sessions) != 1 {
		t.Fatalf("expected one pending create, got %d", len(sessions))
	}
	ref := sessions[0].Ref

	stale := int64(999)
	selectParams, _ := json.Marshal(map[string]any{"ref": ref, "expected_revision": &stale})
	body, _ := json.Marshal(map[string]any{
		"layout": layoutID,
		"action": state.WorkspaceActionSelect,
		"params": json.RawMessage(selectParams),
	})
	req := httptest.NewRequest(http.MethodPost, "/v2/workspace-commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 revision_conflict, got %d: %s", w.Code, w.Body.String())
	}
	var errResp v2ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp.Code != "revision_conflict" {
		t.Fatalf("expected revision_conflict, got %q", errResp.Code)
	}

	// A well-formed command with no revision guard succeeds and is never
	// reported as accepted unless ApplyWorkspaceCommand actually returned nil.
	okParams, _ := json.Marshal(map[string]any{"ref": ref})
	okBody, _ := json.Marshal(map[string]any{
		"layout": layoutID,
		"action": state.WorkspaceActionSelect,
		"params": json.RawMessage(okParams),
	})
	req = httptest.NewRequest(http.MethodPost, "/v2/workspace-commands", bytes.NewReader(okBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result v2WorkspaceCommandResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.Accepted {
		t.Error("expected accepted true")
	}
}

func TestV2BootstrapIncludesPerSessionDaemonGeneration(t *testing.T) {
	// Verify that the v2 bootstrap response exposes per-session daemon binding
	// generation (from Compat.Generation), NOT the websocket connection generation.
	// This is critical for terminal attachment identity resolution.
	catalog, svc := newV2TestCatalog(t)
	opts := &Options{V2Catalog: catalog, V2CommandSvc: svc}
	r := newV2TestRouter(opts)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = svc.Run(ctx) }()

	// Create a session; noopBackend.Start will return generation "gen-test"
	params, _ := json.Marshal(state.CreateParams{Name: "test-session"})
	cmd := state.SessionCommand{
		ID:     state.NewCommandID(),
		Action: state.ActionCreate,
		Params: params,
	}
	res, err := svc.ExecuteSessionCommand(t.Context(), cmd)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The create is durable-before-reply; the daemon start (and Compat.Generation)
	// is committed asynchronously by a background worker. Wait for it.
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if rec, ok := catalog.Session(res.Ref.Session); ok && rec.Phase == state.SessionPhaseActive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Request bootstrap snapshot
	req := httptest.NewRequest(http.MethodGet, "/v2/bootstrap", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp v2BootstrapResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Sessions) == 0 {
		t.Fatalf("expected at least one session in bootstrap response (worker did not activate in time); body=%s", w.Body.String())
	}

	// Verify the session includes per-session daemon generation in _compat
	session := resp.Sessions[0]
	if session.Compat.Generation == "" {
		t.Error("expected session.Compat.Generation to be non-empty (should be 'gen-test' from noopBackend)")
	}
	if session.Compat.Generation != "gen-test" {
		t.Errorf("expected session.Compat.Generation='gen-test', got %q", session.Compat.Generation)
	}
}

// noopBackend is a minimal state.DaemonBackend that reports every session as
// immediately live, so ExecuteSessionCommand's create path (which only
// commits the durable intent synchronously) does not need a running worker
// for these route-level tests.
type noopBackend struct{}

func (noopBackend) Probe(binding pty.StableBinding) pty.ProbeEvidence {
	return pty.ProbeEvidence{Status: pty.ProbeUnknown}
}

func (noopBackend) Start(ctx context.Context, req pty.StartRequest) (pty.ReadyInfo, error) {
	return pty.ReadyInfo{DaemonPID: 1, ShellPID: 1, Generation: "gen-test"}, nil
}

func (noopBackend) Terminate(ctx context.Context, binding pty.StableBinding) pty.TerminateOutcome {
	return pty.TerminateAcknowledged
}
