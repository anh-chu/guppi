package peer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/state"
)

type fakeDaemonReg struct {
	captureText string
	captureErr  error
	killErr     error
	list        []pty.SessionInfo
}

func (f *fakeDaemonReg) Create(name, shell, cwd string, cols, rows uint16) error { return nil }
func (f *fakeDaemonReg) Kill(name string) error                                  { return f.killErr }
func (f *fakeDaemonReg) Capture(name string) (string, error)                     { return f.captureText, f.captureErr }
func (f *fakeDaemonReg) SocketPath(name string) string                           { return "" }
func (f *fakeDaemonReg) List() []pty.SessionInfo                                 { return f.list }
func (f *fakeDaemonReg) GenerationFor(name string) string                        { return "" }

func TestHandleFileReadNotFound(t *testing.T) {
	pc := NewPeerConnection("peer", 1)
	deps := SessionDeps{}
	msg, err := NewMessage(MsgFileRead, FileReadPayload{
		Token:   "t",
		Path:    "/no/such/file",
		Session: "",
	})
	if err != nil {
		t.Fatal(err)
	}

	handleStreamMessage("peer", msg, pc, deps, logrus.NewEntry(logrus.New()))

	select {
	case f := <-pc.LoLane():
		var msg Message
		if err := json.Unmarshal(f.data, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != MsgFileReadResult {
			t.Fatalf("expected %s, got %s", MsgFileReadResult, msg.Type)
		}
		var res FileReadResultPayload
		if err := json.Unmarshal(msg.Payload, &res); err != nil {
			t.Fatal(err)
		}
		if res.Error == "" {
			t.Fatal("expected error for missing file")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for file-read result")
	}
}

func TestHandleCapturePane(t *testing.T) {
	pc := NewPeerConnection("peer", 1)
	reg := &fakeDaemonReg{captureText: "line1\nline2\nline3\n"}
	deps := SessionDeps{DaemonReg: reg}
	msg, err := NewMessage(MsgCapturePane, CapturePanePayload{
		Token:   "t",
		Session: "s",
		Lines:   2,
	})
	if err != nil {
		t.Fatal(err)
	}

	handleStreamMessage("peer", msg, pc, deps, logrus.NewEntry(logrus.New()))

	select {
	case f := <-pc.LoLane():
		var msg Message
		if err := json.Unmarshal(f.data, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != MsgCapturePaneResult {
			t.Fatalf("expected %s, got %s", MsgCapturePaneResult, msg.Type)
		}
		var res CapturePaneResultPayload
		if err := json.Unmarshal(msg.Payload, &res); err != nil {
			t.Fatal(err)
		}
		if res.Error != "" {
			t.Fatalf("unexpected error: %s", res.Error)
		}
		if res.Text == "" {
			t.Fatal("expected captured text")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for capture-pane result")
	}
}

// recordingDaemonReg captures the socket key (and generation lookup) passed to
// SocketPath/GenerationFor so tests can assert the resolved daemon key.
type recordingDaemonReg struct {
	mu           sync.Mutex
	socketKey    string
	generationID string
	genReturn    string
}

func (r *recordingDaemonReg) Create(name, shell, cwd string, cols, rows uint16) error { return nil }
func (r *recordingDaemonReg) Kill(name string) error                                  { return nil }
func (r *recordingDaemonReg) Capture(name string) (string, error)                     { return "", nil }
func (r *recordingDaemonReg) List() []pty.SessionInfo                                 { return nil }
func (r *recordingDaemonReg) GenerationFor(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generationID = name
	return r.genReturn
}
func (r *recordingDaemonReg) SocketPath(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.socketKey = name
	return ""
}
func (r *recordingDaemonReg) lastSocketKey() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.socketKey
}
func (r *recordingDaemonReg) lastGenerationFor() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generationID
}

func waitForSocketKey(t *testing.T, r *recordingDaemonReg, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.lastSocketKey() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("SocketPath never called with %q (last %q)", want, r.lastSocketKey())
}

// TestOpenTerminal_NameOnlyPayloadResolvesDaemonKey is a same-process peer
// roundtrip: a name-only OpenTerminalPayload (no SessionID -- the pre-v2 /
// display-name path) must still resolve a daemon socket key by falling back to
// the session name instead of passing an empty SessionID to SocketPath.
func TestOpenTerminal_NameOnlyPayloadResolvesDaemonKey(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	_ = os.MkdirAll(filepath.Join(tmpHome, ".config", "termyard"), 0o700)

	dialerID, err := identity.Generate("dialer")
	if err != nil {
		t.Fatal(err)
	}
	store, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(identity.Peer{Name: "dialer", PublicKey: dialerID.PublicKey, Enabled: true, InitiatedByUs: true}); err != nil {
		t.Fatal(err)
	}

	reg := NewStreamRegistry()
	handler := NewHandler(SessionDeps{PeerStore: store}, reg)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/peer-stream", handler.HandlePeerStream)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	addr := mustHostPort(t, srv.URL)

	hostID := dialerID.Fingerprint()

	// Name-only payload (display name, no SessionID). The host must fall back
	// to the display name as its daemon key instead of SocketPath("").
	t.Run("name-only payload", func(t *testing.T) {
		rec := &recordingDaemonReg{}
		pc := NewPeerConnection(hostID, 8)
		pc.Role = RoleDialer

		mgr := NewManager(dialerID, store, state.NewManager())
		mgr.RegisterPeerWithAddress(hostID, "dialer", dialerID.PublicKey, addr, pc)

		token := NewToken()
		reg.Register(token, NewPendingStream("OPEN-NAME-ONLY", "", 0, 0, "", "", hostID))

		payload := OpenTerminalPayload{StreamID: "OPEN-NAME-ONLY", Session: "legacy-display-name", Cols: 80, Rows: 24, Token: token}
		deps := SessionDeps{Manager: mgr, Identity: dialerID, DaemonReg: rec}
		go handleOpenTerminal(payload, pc, deps, logrus.NewEntry(logrus.New()))

		waitForSocketKey(t, rec, "legacy-display-name")
		// No GenerationFor lookup happens for a name-only payload.
		if rec.lastGenerationFor() != "" {
			t.Fatalf("unexpected generation lookup for name-only payload: %q", rec.lastGenerationFor())
		}
	})

	// Stable payload (immutable SessionID) must prefer the SessionID as the
	// daemon key, even when a display name is also present. The generation
	// gate looks the current generation up by the same SessionID key.
	t.Run("sessionID payload", func(t *testing.T) {
		rec := &recordingDaemonReg{genReturn: "gen-current"}
		pc := NewPeerConnection(hostID, 8)
		pc.Role = RoleDialer

		mgr := NewManager(dialerID, store, state.NewManager())
		mgr.RegisterPeerWithAddress(hostID, "dialer", dialerID.PublicKey, addr, pc)

		token := NewToken()
		reg.Register(token, NewPendingStream("OPEN-STABLE", "", 0, 0, "", "", hostID))

		payload := OpenTerminalPayload{StreamID: "OPEN-STABLE", Session: "display-name", SessionID: "stable-session-id-abc", Generation: "gen-current", Cols: 80, Rows: 24, Token: token}
		deps := SessionDeps{Manager: mgr, Identity: dialerID, DaemonReg: rec}
		go handleOpenTerminal(payload, pc, deps, logrus.NewEntry(logrus.New()))

		waitForSocketKey(t, rec, "stable-session-id-abc")
		// The generation lookup must use the SessionID, not the display name.
		if got := rec.lastGenerationFor(); got != "stable-session-id-abc" {
			t.Fatalf("generation lookup used %q, want the SessionID", got)
		}
	})
}
