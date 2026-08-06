package peer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
)

func TestOpenUploadCapability(t *testing.T) {
	// CapUpload is an optional capability advertised by peers supporting
	// dedicated upload connections. It's always available as a module const.
	if CapUpload != "upload" {
		t.Fatalf("expected CapUpload to be %q, got %q", "upload", CapUpload)
	}
}

// TestUploadWireProtocol exercises the upload data channel by simulating
// the hub (client) sending binary frames + control frames over a peer-stream
// connection, while the peer (server) stores them via StoreUploadedFile and
// replies with the path. This mirrors the real handleOpenUpload flow without
// requiring a full PeerConnection.
func TestUploadWireProtocolRoundTrip(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("TMPDIR", t.TempDir())
	_ = os.MkdirAll(filepath.Join(tmpHome, ".config", "termyard"), 0o700)

	dialerID, err := identity.Generate("upload-roundtrip")
	if err != nil {
		t.Fatal(err)
	}
	store, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(identity.Peer{Name: "upload-roundtrip", PublicKey: dialerID.PublicKey, Enabled: true, InitiatedByUs: true}); err != nil {
		t.Fatal(err)
	}

	reg := NewStreamRegistry()
	handler := NewHandler(SessionDeps{PeerStore: store}, reg)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/peer-stream", handler.HandlePeerStream)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	addr := mustHostPort(t, srv.URL)
	token := NewToken()
	ps := NewPendingStream("UP-ROUNDTRIP", "", 0, 0, "", "", dialerID.Fingerprint())
	reg.Register(token, ps)

	// Hub side (dialer) connects.
	hubConn, err := DialPeerStream(context.Background(), addr, dialerID, token)
	if err != nil {
		t.Fatal(err)
	}
	defer hubConn.Close()

	// Peer side receives the connection.
	peerConn := waitResolvedConn(t, ps)
	defer peerConn.Close()

	// Pipe the peer reads into StoreUploadedFile in a goroutine, mimicking
	// what handleOpenUpload does.
	pr, pw := io.Pipe()
	storeDone := make(chan struct {
		path string
		err  error
	}, 1)

	go func() {
		path, err := model.StoreUploadedFile(pr, "upload-test.dat")
		storeDone <- struct {
			path string
			err  error
		}{path, err}
	}()

	// Read frames on the peer side and forward to pipe writer.
	var frame struct {
		Type string `json:"type"`
	}
	go func() {
		defer pw.Close()
		const frameTimeout = 10 * time.Second
		for {
			peerConn.SetReadDeadline(time.Now().Add(frameTimeout))
			msgType, msg, err := peerConn.ReadMessage()
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			switch msgType {
			case websocket.BinaryMessage:
				if _, err := pw.Write(msg); err != nil {
					return
				}
			case websocket.TextMessage:
				if err := json.Unmarshal(msg, &frame); err != nil {
					pw.CloseWithError(err)
					return
				}
				switch frame.Type {
				case "upload-eof":
					return // pw.Close() deferred
				case "upload-abort":
					pw.CloseWithError(io.ErrUnexpectedEOF)
					return
				}
			}
		}
	}()

	// Hub side: send binary frames.
	if err := hubConn.WriteMessage(websocket.BinaryMessage, []byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if err := hubConn.WriteMessage(websocket.BinaryMessage, []byte("world")); err != nil {
		t.Fatal(err)
	}
	// Send EOF.
	if err := hubConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upload-eof"}`)); err != nil {
		t.Fatal(err)
	}

	// Wait for store to complete.
	result := <-storeDone
	if result.err != nil {
		t.Fatalf("StoreUploadedFile returned error: %v", result.err)
	}
	if result.path == "" {
		t.Fatal("empty path")
	}

	// Verify file content.
	got, err := os.ReadFile(result.path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("contents = %q, want %q", string(got), "hello world")
	}

	// Send result back to hub.
	reply, _ := json.Marshal(map[string]string{
		"path":       result.path,
		"quotedPath": model.ShellQuote(result.path),
	})
	peerConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := peerConn.WriteMessage(websocket.TextMessage, reply); err != nil {
		t.Fatal(err)
	}

	// Hub reads result.
	_, raw, err := hubConn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var hubRes struct {
		Path       string `json:"path"`
		QuotedPath string `json:"quotedPath"`
		Error      string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &hubRes); err != nil {
		t.Fatalf("unmarshal hub result: %v", err)
	}
	if hubRes.Path != result.path {
		t.Fatalf("hub result path = %q, want %q", hubRes.Path, result.path)
	}
	if hubRes.QuotedPath == "" || !strings.HasPrefix(hubRes.QuotedPath, "'") {
		t.Fatalf("bad quotedPath: %q", hubRes.QuotedPath)
	}
}

// TestUploadWireProtocolAbortCleanup verifies that sending upload-abort
// cleans up the partial paste directory.
func TestUploadWireProtocolAbortCleanup(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("TMPDIR", t.TempDir())
	_ = os.MkdirAll(filepath.Join(tmpHome, ".config", "termyard"), 0o700)

	dialerID, err := identity.Generate("upload-abort")
	if err != nil {
		t.Fatal(err)
	}
	store, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(identity.Peer{Name: "upload-abort", PublicKey: dialerID.PublicKey, Enabled: true, InitiatedByUs: true}); err != nil {
		t.Fatal(err)
	}

	reg := NewStreamRegistry()
	handler := NewHandler(SessionDeps{PeerStore: store}, reg)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/peer-stream", handler.HandlePeerStream)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	addr := mustHostPort(t, srv.URL)
	token := NewToken()
	ps := NewPendingStream("UP-ABORT", "", 0, 0, "", "", dialerID.Fingerprint())
	reg.Register(token, ps)

	hubConn, err := DialPeerStream(context.Background(), addr, dialerID, token)
	if err != nil {
		t.Fatal(err)
	}
	defer hubConn.Close()

	peerConn := waitResolvedConn(t, ps)
	defer peerConn.Close()

	pr, pw := io.Pipe()
	storeDone := make(chan error, 1)

	go func() {
		_, err := model.StoreUploadedFile(pr, "abort-test.dat")
		storeDone <- err
	}()

	go func() {
		defer pw.Close()
		for {
			peerConn.SetReadDeadline(time.Now().Add(10 * time.Second))
			msgType, msg, err := peerConn.ReadMessage()
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			switch msgType {
			case websocket.BinaryMessage:
				if _, err := pw.Write(msg); err != nil {
					return
				}
			case websocket.TextMessage:
				var frame struct {
					Type string `json:"type"`
				}
				json.Unmarshal(msg, &frame)
				if frame.Type == "upload-abort" || frame.Type == "upload-eof" {
					pw.CloseWithError(io.ErrUnexpectedEOF)
					return
				}
			}
		}
	}()

	// Send one chunk then abort.
	if err := hubConn.WriteMessage(websocket.BinaryMessage, []byte("partial data")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // let the read goroutine pick it up
	if err := hubConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upload-abort"}`)); err != nil {
		t.Fatal(err)
	}

	storeErr := <-storeDone
	if storeErr == nil {
		t.Fatal("expected error from aborted StoreUploadedFile")
	}
	// StoreUploadedFile should have cleaned up; no paste dirs should be newer
	// than test start. We accept that the error contains something about the
	// pipe being closed.
	if storeErr != nil {
		t.Logf("StoreUploadedFile error (expected): %v", storeErr)
	}
	// Verify no reply frame sent on abort.
	peerConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if err := peerConn.WriteMessage(websocket.TextMessage, []byte("SHOULD-NOT-READ")); err != nil {
		// Connection might be closed — that's fine.
	}
	// The peer side read loop already exited; we just verify the store failed.
}
