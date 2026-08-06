package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/state"
)

func TestHandleDaemonSessionStableRejectsStaleGeneration(t *testing.T) {
	owner := state.NewOwnerID()
	catalog := state.NewCatalog(owner, nil)
	sid := state.NewSessionID()
	if err := catalog.PutSession(state.LocalSessionRecord{
		ID:    sid,
		Owner: owner,
		Ref:   state.SessionRef{Owner: owner, Session: sid},
		Phase: state.SessionPhaseActive,
		Name:  "label", Generation: "gen-live",
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	store, err := pty.NewLifecycleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLifecycleStore: %v", err)
	}
	reg := pty.NewRegistry(t.TempDir())
	reg.SetLifecycleStore(store)

	opts := &Options{V2Catalog: catalog, DaemonReg: reg}

	cases := []struct {
		name       string
		sessionID  string
		generation string
		wantStatus int
		wantCode   string
	}{
		{"stale generation rejected", string(sid), "gen-old", http.StatusConflict, "generation_changed"},
		{"missing sessionID with generation", "", "any", http.StatusBadRequest, "not_found"},
		{"no generation gate but daemon missing", string(sid), "", http.StatusInternalServerError, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws/daemon-session?sessionID="+tc.sessionID+"&generation="+tc.generation, nil)
			rr := httptest.NewRecorder()
			handleDaemonSession(rr, req, opts)
			if tc.wantCode == "" {
				if rr.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d (body %s)", rr.Code, tc.wantStatus, rr.Body.String())
				}
				return
			}
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, tc.wantStatus, rr.Body.String())
			}
			var payload map[string]string
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if payload["code"] != tc.wantCode {
				t.Fatalf("code = %q, want %q", payload["code"], tc.wantCode)
			}
		})
	}
}

func TestHandleDaemonSessionStableNotReadyForPendingPhase(t *testing.T) {
	owner := state.NewOwnerID()
	catalog := state.NewCatalog(owner, nil)
	sid := state.NewSessionID()
	if err := catalog.PutSession(state.LocalSessionRecord{
		ID:    sid,
		Owner: owner,
		Ref:   state.SessionRef{Owner: owner, Session: sid},
		Phase: state.SessionPhasePending,
		Name:  "label",
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	reg := pty.NewRegistry(t.TempDir())

	opts := &Options{V2Catalog: catalog, DaemonReg: reg}
	req := httptest.NewRequest(http.MethodGet, "/ws/daemon-session?sessionID="+string(sid)+"&generation=any", nil)
	rr := httptest.NewRecorder()
	handleDaemonSession(rr, req, opts)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rr.Code, rr.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["code"] != "not_ready" {
		t.Fatalf("code = %q, want not_ready", payload["code"])
	}
}

// TestHandleDaemonSessionStableRejectsCrashedWithGeneration proves that a
// session which has died (phase crashed) is rejected on attach even though it
// still has a recorded generation from before it crashed. The reject must not
// be gated on currentGen being empty: crashed/stopping/ended/dismissed
// sessions commonly retain a stale generation.
func TestHandleDaemonSessionStableRejectsCrashedWithGeneration(t *testing.T) {
	owner := state.NewOwnerID()
	catalog := state.NewCatalog(owner, nil)
	sid := state.NewSessionID()
	if err := catalog.PutSession(state.LocalSessionRecord{
		ID:         sid,
		Owner:      owner,
		Ref:        state.SessionRef{Owner: owner, Session: sid},
		Phase:      state.SessionPhaseCrashed,
		Name:       "label",
		Generation: "gen-stale",
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	reg := pty.NewRegistry(t.TempDir())

	opts := &Options{V2Catalog: catalog, DaemonReg: reg}
	req := httptest.NewRequest(http.MethodGet, "/ws/daemon-session?sessionID="+string(sid)+"&generation=gen-stale", nil)
	rr := httptest.NewRecorder()
	handleDaemonSession(rr, req, opts)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rr.Code, rr.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["code"] != "not_ready" {
		t.Fatalf("code = %q, want not_ready", payload["code"])
	}
}

// TestHandleDaemonSessionStableRejectsMaliciousSessionID proves the sessionID
// query param is validated as a canonical SessionID (lowercase base32) before
// it can be used as a daemon socket key. Path-traversal-style IDs are rejected
// with 400 and never reach SocketPath.
func TestHandleDaemonSessionStableRejectsMaliciousSessionID(t *testing.T) {
	opts := &Options{DaemonReg: pty.NewRegistry(t.TempDir())}

	malicious := []string{
		"../../etc/passwd",
		"..\\..\\other-service",
		"../other-service",
		"..",
		"a/b",
		"x\ny",
	}
	for _, id := range malicious {
		t.Run(id, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws/daemon-session?sessionID="+url.QueryEscape(id)+"&generation=any", nil)
			rr := httptest.NewRecorder()
			handleDaemonSession(rr, req, opts)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
			}
			var payload map[string]string
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if payload["code"] != "not_found" {
				t.Fatalf("code = %q, want not_found", payload["code"])
			}
		})
	}
}
