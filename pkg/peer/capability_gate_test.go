package peer

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/state"
)

// canonicalCommandSvcForTest returns a non-nil *state.SessionCommandService
// pointer suitable only for identity checks (deps.CommandSvc != nil) in
// these tests; it is never invoked, so its internal fields are left zero.
func canonicalCommandSvcForTest() *state.SessionCommandService {
	return &state.SessionCommandService{}
}

// TestHandshake_ProtocolVersionMismatchIsRejected drives a real, fully
// authenticated (ed25519 challenge-response) client through the actual
// production listener handler (handler.go's HandlePeer, served over a real
// httptest websocket server) and proves a peer with a mismatched protocol
// version is rejected before registration: the listener responds with
// MsgAuthFail and closes the connection instead of MsgAuthOK.
func TestHandshake_ProtocolVersionMismatchIsRejected(t *testing.T) {
	depsB, _, psB, _ := makeTestDepsWithCatalog(t, "B")

	handlerB := NewHandler(depsB, NewStreamRegistry())
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/peer", handlerB.HandlePeer)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	clientID, err := identity.Generate("test-client")
	if err != nil {
		t.Fatal(err)
	}
	if err := psB.Add(identity.Peer{
		Name:      "test-client",
		PublicKey: clientID.PublicKey,
		Enabled:   true,
	}); err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Scheme = "ws"
	u.Path = "/ws/peer"

	dialAndHandshake := func(protocolVersion int, caps []string) (string, error) {
		conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			return "", err
		}
		defer conn.Close()

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var challengeMsg Message
		if err := conn.ReadJSON(&challengeMsg); err != nil {
			return "", err
		}
		if challengeMsg.Type != MsgChallenge {
			t.Fatalf("expected challenge, got %s", challengeMsg.Type)
		}
		var ch ChallengePayload
		if err := json.Unmarshal(challengeMsg.Payload, &ch); err != nil {
			return "", err
		}
		challengeBytes, err := base64.StdEncoding.DecodeString(ch.Challenge)
		if err != nil {
			return "", err
		}
		sig, err := clientID.Sign(challengeBytes)
		if err != nil {
			return "", err
		}
		authMsg, err := NewMessage(MsgAuth, AuthPayload{
			PublicKey:       clientID.PublicKey,
			Signature:       base64.StdEncoding.EncodeToString(sig),
			ProtocolVersion: protocolVersion,
			Capabilities:    caps,
		})
		if err != nil {
			return "", err
		}
		if err := conn.WriteJSON(authMsg); err != nil {
			return "", err
		}
		var result Message
		if err := conn.ReadJSON(&result); err != nil {
			return "", err
		}
		return result.Type, nil
	}

	// Protocol version mismatch (lower version) causes rejection
	gotType, err := dialAndHandshake(0, []string{CapPerStream, CapUpload})
	if err != nil {
		t.Fatal(err)
	}
	if gotType != MsgAuthFail {
		t.Fatalf("expected %s for protocol version 0, got %s", MsgAuthFail, gotType)
	}

	// Protocol version mismatch (higher version) causes rejection
	gotType, err = dialAndHandshake(999, []string{CapPerStream, CapUpload})
	if err != nil {
		t.Fatal(err)
	}
	if gotType != MsgAuthFail {
		t.Fatalf("expected %s for protocol version 999, got %s", MsgAuthFail, gotType)
	}

	// Matching protocol version allows handshake to proceed
	gotType, err = dialAndHandshake(ProtocolVersion, []string{CapPerStream, CapUpload})
	if err != nil {
		t.Fatal(err)
	}
	if gotType != MsgAuthOK {
		t.Fatalf("expected %s for matching protocol version, got %s", MsgAuthOK, gotType)
	}

	// Matching protocol version with no optional caps also succeeds
	gotType, err = dialAndHandshake(ProtocolVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotType != MsgAuthOK {
		t.Fatalf("expected %s for matching protocol version with no caps, got %s", MsgAuthOK, gotType)
	}
}

// TestAuthOKPayload_IncludesProtocolVersion verifies that AuthOKPayload
// sent by the listener includes the protocol version for the client to validate.
func TestAuthOKPayload_IncludesProtocolVersion(t *testing.T) {
	depsB, _, psB, _ := makeTestDepsWithCatalog(t, "B")

	handlerB := NewHandler(depsB, NewStreamRegistry())
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/peer", handlerB.HandlePeer)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	clientID, err := identity.Generate("test-client")
	if err != nil {
		t.Fatal(err)
	}
	if err := psB.Add(identity.Peer{
		Name:      "test-client",
		PublicKey: clientID.PublicKey,
		Enabled:   true,
	}); err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Scheme = "ws"
	u.Path = "/ws/peer"

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var challengeMsg Message
	if err := conn.ReadJSON(&challengeMsg); err != nil {
		t.Fatal(err)
	}
	var ch ChallengePayload
	if err := json.Unmarshal(challengeMsg.Payload, &ch); err != nil {
		t.Fatal(err)
	}
	challengeBytes, err := base64.StdEncoding.DecodeString(ch.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := clientID.Sign(challengeBytes)
	if err != nil {
		t.Fatal(err)
	}
	authMsg, err := NewMessage(MsgAuth, AuthPayload{
		PublicKey:       clientID.PublicKey,
		Signature:       base64.StdEncoding.EncodeToString(sig),
		ProtocolVersion: ProtocolVersion,
		Capabilities:    []string{CapPerStream, CapUpload},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(authMsg); err != nil {
		t.Fatal(err)
	}

	var result Message
	if err := conn.ReadJSON(&result); err != nil {
		t.Fatal(err)
	}
	if result.Type != MsgAuthOK {
		t.Fatalf("expected %s, got %s", MsgAuthOK, result.Type)
	}

	var okPayload AuthOKPayload
	if err := json.Unmarshal(result.Payload, &okPayload); err != nil {
		t.Fatal(err)
	}
	if okPayload.ProtocolVersion != ProtocolVersion {
		t.Fatalf("expected protocol version %d, got %d", ProtocolVersion, okPayload.ProtocolVersion)
	}
}
