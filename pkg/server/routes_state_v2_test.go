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
	if len(resp.Local.Layouts) == 0 {
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

	if len(resp.Local.Sessions) == 0 {
		t.Fatalf("expected at least one session in bootstrap response (worker did not activate in time); body=%s", w.Body.String())
	}

	// Verify the session includes per-session daemon generation in _compat
	session := resp.Local.Sessions[0]
	if session.Compat.Generation == "" {
		t.Error("expected session.Compat.Generation to be non-empty (should be 'gen-test' from noopBackend)")
	}
	if session.Compat.Generation != "gen-test" {
		t.Errorf("expected session.Compat.Generation='gen-test', got %q", session.Compat.Generation)
	}
}

// TestV2CommandBodyAsBrowserWouldProduceIt feeds literal JSON bytes shaped
// exactly like web/src/state/v2/commands.ts's V2CommandClient produces (ref
// as a canonical STRING, e.g. "owner/session:0.0", never a JSON object; all
// non-action fields nested under "params") through the real route handlers.
// This is the end-to-end proof that the browser wire contract actually
// decodes: pkg/state.SessionRef's UnmarshalJSON only accepts a JSON string,
// so if commands.ts ever regressed to sending an object-shaped ref (the bug
// this fix addresses), this test would fail with invalid_input.
func TestV2CommandBodyAsBrowserWouldProduceIt(t *testing.T) {
	catalog, svc := newV2TestCatalog(t)
	opts := &Options{V2Catalog: catalog, V2CommandSvc: svc}
	r := newV2TestRouter(opts)

	// 1. Seed a real session/layout via the service directly (create's own
	// empty-ref placeholder semantics are a separate, pre-existing concern
	// unrelated to the string-vs-object wire format this test covers).
	params, _ := json.Marshal(state.CreateParams{Name: "browser-created"})
	if _, err := svc.ExecuteSessionCommand(t.Context(), state.SessionCommand{
		ID: state.NewCommandID(), Action: state.ActionCreate, Params: params,
	}); err != nil {
		t.Fatalf("seed create: %v", err)
	}

	layouts := catalog.Layouts()
	if len(layouts) != 1 {
		t.Fatalf("expected one layout after create, got %d", len(layouts))
	}
	layoutID := layouts[0].ID
	pending := catalog.PendingCreates()
	if len(pending) != 1 {
		t.Fatalf("expected one pending create, got %d", len(pending))
	}
	realRef := pending[0].Ref
	wireRef := realRef.String()

	// 2. Workspace command: select, exactly as V2CommandClient.workspaceCommand
	// would serialize { id, layout, action, params: { ref, expected_revision? } }
	// -- this is encodeWorkspaceCommandAction's 'select' case in commands.ts.
	selectBody := []byte(`{"id":"cmdbrowser3","layout":"` + string(layoutID) + `","action":"select","params":{"ref":"` + wireRef + `"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v2/workspace-commands", bytes.NewReader(selectBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("select: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result v2WorkspaceCommandResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode select result: %v", err)
	}
	if !result.Accepted {
		t.Error("expected select command to be accepted")
	}

	// 3. Session command: kill, targeting the real ref by its canonical wire
	// string, exactly as V2CommandClient.sessionCommand would serialize it.
	killBody := []byte(`{"id":"cmdbrowser2","ref":"` + wireRef + `","action":"kill","params":{}}`)
	req = httptest.NewRequest(http.MethodPost, "/v2/session-commands", bytes.NewReader(killBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("kill: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Negative control: an object-shaped ref (the bug this test guards
	// against) must be rejected as invalid_input, not silently accepted.
	objectRefBody := []byte(`{"id":"cmdbrowser4","ref":{"owner":"","session":"x","window":0,"pane":0},"action":"kill","params":{}}`)
	req = httptest.NewRequest(http.MethodPost, "/v2/session-commands", bytes.NewReader(objectRefBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("object-shaped ref: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestV2CreateCommandBodyAsBrowserWouldProduceIt feeds the exact JSON bytes
// V2CommandClient.createSession (web/src/state/v2/commands.ts) produces for a
// create: { id, action: "create", params } with NO `ref` member at all. A
// create cannot know its SessionID before the server assigns one, and
// executeCreate synthesizes it (NewSessionID) when cmd.Ref is the zero value.
//
// Before this test, useV2State sent a placeholder ref {session:''} which, once
// wire-encoded to its canonical string ":0.0", was rejected by ParseSessionRef
// with "missing session id" (invalid_input) -- breaking browser session create.
func TestV2CreateCommandBodyAsBrowserWouldProduceIt(t *testing.T) {
	catalog, svc := newV2TestCatalog(t)
	opts := &Options{V2Catalog: catalog, V2CommandSvc: svc}
	r := newV2TestRouter(opts)

	// Exact shape V2CommandClient.createSession produces. Crucially there is
	// NO "ref" member -- sending the old placeholder would be rejected with
	// invalid_input ("missing session id") by SessionRef.UnmarshalJSON.
	body := []byte(`{"id":"cmdbrowsercreate1","action":"create","params":{"name":"browser-create","cwd":"/tmp"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v2/session-commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result state.CommandResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode create result: %v", err)
	}
	if !result.Accepted {
		t.Error("expected create to be accepted")
	}
	if result.Ref.Session == "" {
		t.Error("expected server-assigned session id in result.Ref.Session")
	}
	if result.Ref.Owner != catalog.Owner() {
		t.Errorf("expected owner %q in result.Ref.Owner, got %q", catalog.Owner(), result.Ref.Owner)
	}

	// The durable intent is committed before the reply: the pending create
	// must already exist in the catalog under the returned ref.
	pending := catalog.PendingCreates()
	if len(pending) != 1 {
		t.Fatalf("expected one pending create, got %d", len(pending))
	}
	if pending[0].Ref.Session != result.Ref.Session {
		t.Errorf("expected pending create ref %q to match result ref %q", pending[0].Ref, result.Ref)
	}

	// Negative control: sending a placeholder ref shaped like the pre-fix
	// browser ({session:''} encoded to ":0.0") must be rejected as
	// invalid_input, not silently accepted.
	oldPlaceholder := []byte(`{"id":"cmdbrowsercreate2","ref":":0.0","action":"create","params":{}}`)
	req = httptest.NewRequest(http.MethodPost, "/v2/session-commands", bytes.NewReader(oldPlaceholder))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("placeholder ref: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var errResp v2ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Code != "invalid_input" {
		t.Fatalf("expected invalid_input, got %q", errResp.Code)
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
