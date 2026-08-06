package peer

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/anh-chu/termyard/pkg/identity"
)

func TestStreamCompression(t *testing.T) {
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

	t.Run("large compressed frames preserve payload", func(t *testing.T) {
		token := NewToken()
		ps := NewPendingStream("S-C1", "", 0, 0, "", "", dialerID.Fingerprint())
		reg.Register(token, ps)

		clientConn, resp, err := dialPeerStreamGetResponse(context.Background(), addr, dialerID, token)
		if err != nil {
			t.Fatal(err)
		}
		defer clientConn.Close()

		// Prove permessage-deflate was negotiated in the handshake response.
		if ext := resp.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(ext, "permessage-deflate") {
			t.Fatalf("Sec-WebSocket-Extensions missing permessage-deflate: %q", ext)
		}

		serverConn := waitResolvedConn(t, ps)
		defer serverConn.Close()

		// Host side: set compression level before enabling write compression
		// (gorilla/websocket requires SetCompressionLevel before EnableWriteCompression).
		if err := serverConn.SetCompressionLevel(flate.BestSpeed); err != nil {
			t.Fatalf("set compression level: %v", err)
		}
		serverConn.EnableWriteCompression(true)

		// Build a 64 KB frame of repetitive data (highly compressible).
		payload := []byte(strings.Repeat("ABCDEFGHIJKLMNOP", 4096)) // 16 * 4096 = 65536

		if err := serverConn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
			t.Fatal(err)
		}

		// Read from the client side — must get identical bytes.
		mt, got, err := mustReadMessage(t, clientConn, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if mt != websocket.BinaryMessage {
			t.Fatalf("message type = %d, want Binary", mt)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("length mismatch: got %d, want %d", len(got), len(payload))
		}
	})

	t.Run("compression-enabled server accepts no-compression peer", func(t *testing.T) {
		token := NewToken()
		ps := NewPendingStream("S-C2", "", 0, 0, "", "", dialerID.Fingerprint())
		reg.Register(token, ps)

		// Dial without EnableCompression to simulate an old peer.
		nonCompClient, err := dialPeerStreamNoCompression(context.Background(), addr, dialerID, token)
		if err != nil {
			t.Fatal(err)
		}
		defer nonCompClient.Close()

		serverConn := waitResolvedConn(t, ps)
		defer serverConn.Close()

		msg := []byte("hello uncompressed")
		if err := serverConn.WriteMessage(websocket.TextMessage, msg); err != nil {
			t.Fatal(err)
		}
		mt, got, err := mustReadMessage(t, nonCompClient, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if mt != websocket.TextMessage || !bytes.Equal(got, msg) {
			t.Fatalf("got %d / %q", mt, got)
		}

		reply := []byte("reply from old peer")
		if err := nonCompClient.WriteMessage(websocket.TextMessage, reply); err != nil {
			t.Fatal(err)
		}
		mt, got, err = mustReadMessage(t, serverConn, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if mt != websocket.TextMessage || !bytes.Equal(got, reply) {
			t.Fatalf("got %d / %q", mt, got)
		}
	})
}

// dialPeerStreamGetResponse is like DialPeerStream but returns the WebSocket
// handshake response so callers can verify compression negotiation.
func dialPeerStreamGetResponse(ctx context.Context, addr string, id *identity.Identity, token string) (*websocket.Conn, *http.Response, error) {
	if addr == "" {
		return nil, nil, fmt.Errorf("peer has no address")
	}
	if token == "" {
		return nil, nil, fmt.Errorf("missing stream token")
	}
	ctx, cancel := context.WithTimeout(ctx, streamSetupTimeout)
	defer cancel()

	u := &url.URL{Scheme: "ws", Host: addr, Path: "/ws/peer-stream"}
	dialer := &websocket.Dialer{
		Proxy:             websocket.DefaultDialer.Proxy,
		HandshakeTimeout:  streamSetupTimeout,
		ReadBufferSize:    1024 * 32,
		WriteBufferSize:   1024 * 32,
		EnableCompression: true,
	}
	conn, resp, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", u.String(), err)
	}

	var challengeMsg Message
	conn.SetReadDeadline(time.Now().Add(streamSetupTimeout))
	if err := conn.ReadJSON(&challengeMsg); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("read challenge: %w", err)
	}
	if challengeMsg.Type != MsgChallenge {
		conn.Close()
		return nil, nil, fmt.Errorf("expected challenge got %s", challengeMsg.Type)
	}
	var ch ChallengePayload
	if err := json.Unmarshal(challengeMsg.Payload, &ch); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("parse challenge: %w", err)
	}
	challengeBytes, err := base64.StdEncoding.DecodeString(ch.Challenge)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("decode challenge: %w", err)
	}
	sig, err := id.Sign(challengeBytes)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("sign: %w", err)
	}
	authMsg, _ := NewMessage(MsgAuth, AuthPayload{
		PublicKey:       id.PublicKey,
		Signature:       base64.StdEncoding.EncodeToString(sig),
		ProtocolVersion: ProtocolVersion,
	})
	if err := conn.WriteJSON(authMsg); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("send auth: %w", err)
	}

	var result Message
	conn.SetReadDeadline(time.Now().Add(streamSetupTimeout))
	if err := conn.ReadJSON(&result); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("read auth result: %w", err)
	}
	conn.SetReadDeadline(time.Time{})
	if result.Type != MsgAuthOK {
		conn.Close()
		return nil, nil, fmt.Errorf("unexpected auth response: %s", result.Type)
	}

	tokenMsg, _ := NewMessage(MsgStreamToken, StreamTokenPayload{Token: token})
	if err := conn.WriteJSON(tokenMsg); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("send stream token: %w", err)
	}

	conn.EnableWriteCompression(false)

	return conn, resp, nil
}

// dialPeerStreamNoCompression is a copy of DialPeerStream without compression
// negotiation, simulating an older peer that doesn't know about permessage-deflate.
func dialPeerStreamNoCompression(ctx context.Context, addr string, id *identity.Identity, token string) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, streamSetupTimeout)
	defer cancel()

	u, _ := url.Parse("ws://" + addr + "/ws/peer-stream")
	dialer := &websocket.Dialer{
		Proxy:            websocket.DefaultDialer.Proxy,
		HandshakeTimeout: streamSetupTimeout,
		ReadBufferSize:   1024 * 32,
		WriteBufferSize:  1024 * 32,
		// EnableCompression is intentionally absent.
	}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}

	var challengeMsg Message
	conn.SetReadDeadline(time.Now().Add(streamSetupTimeout))
	if err := conn.ReadJSON(&challengeMsg); err != nil {
		conn.Close()
		return nil, err
	}
	if challengeMsg.Type != MsgChallenge {
		conn.Close()
		return nil, err
	}
	var ch ChallengePayload
	if err := json.Unmarshal(challengeMsg.Payload, &ch); err != nil {
		conn.Close()
		return nil, err
	}
	challengeBytes, err := base64.StdEncoding.DecodeString(ch.Challenge)
	if err != nil {
		conn.Close()
		return nil, err
	}
	sig, err := id.Sign(challengeBytes)
	if err != nil {
		conn.Close()
		return nil, err
	}
	authMsg, _ := NewMessage(MsgAuth, AuthPayload{
		PublicKey:       id.PublicKey,
		Signature:       base64.StdEncoding.EncodeToString(sig),
		ProtocolVersion: ProtocolVersion,
	})
	if err := conn.WriteJSON(authMsg); err != nil {
		conn.Close()
		return nil, err
	}

	var result Message
	conn.SetReadDeadline(time.Now().Add(streamSetupTimeout))
	if err := conn.ReadJSON(&result); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetReadDeadline(time.Time{})
	if result.Type != MsgAuthOK {
		conn.Close()
		return nil, err
	}

	tokenMsg, _ := NewMessage(MsgStreamToken, StreamTokenPayload{Token: token})
	if err := conn.WriteJSON(tokenMsg); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

// mustReadMessage reads a full WebSocket message with a deadline.
func mustReadMessage(t *testing.T, conn *websocket.Conn, timeout time.Duration) (int, []byte, error) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, nil, err
	}
	return conn.ReadMessage()
}
