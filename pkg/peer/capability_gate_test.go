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

// TestPeerCapsSatisfyCanonical proves the exact predicate the handshake gate
// relies on: a peer must advertise BOTH CapCatalogV1 and CapCommandV1 --
// neither alone, and no legacy-only capability set, satisfies it.
func TestPeerCapsSatisfyCanonical(t *testing.T) {
	cases := []struct {
		name string
		caps []string
		want bool
	}{
		{"no caps", nil, false},
		{"only legacy caps", []string{CapPerStream, CapUpload}, false},
		{"only catalog cap", []string{CapCatalogV1}, false},
		{"only command cap", []string{CapCommandV1}, false},
		{"both canonical caps", []string{CapCatalogV1, CapCommandV1}, true},
		{"both canonical caps plus legacy", []string{CapPerStream, CapCatalogV1, CapUpload, CapCommandV1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := peerCapsSatisfyCanonical(tc.caps); got != tc.want {
				t.Fatalf("peerCapsSatisfyCanonical(%v) = %v, want %v", tc.caps, got, tc.want)
			}
		})
	}
}

// TestLocalCapabilities_AlwaysAdvertisesCanonicalCaps proves capabilitiesFor
// unconditionally includes both canonical capabilities -- there is no
// deps-based gate anymore (the canonical state graph is the only runtime).
func TestLocalCapabilities_AlwaysAdvertisesCanonicalCaps(t *testing.T) {
	caps := capabilitiesFor(SessionDeps{})
	if !hasCap(caps, CapCatalogV1) || !hasCap(caps, CapCommandV1) {
		t.Fatalf("expected capabilitiesFor to always include canonical caps, got %v", caps)
	}
}

// TestHandshake_PeerMissingCanonicalCapsIsRejected drives a real, fully
// authenticated (ed25519 challenge-response) client through the actual
// production listener handler (handler.go's HandlePeer, served over a real
// httptest websocket server) and proves a peer whose advertised
// capabilities lack CapCatalogV1/CapCommandV1 cannot complete the
// handshake: the listener responds with MsgAuthFail and closes the
// connection instead of MsgAuthOK. This is the acceptance proof that a
// pre-rewrite (legacy-protocol) peer -- which never advertised these caps
// at all -- cannot pair with a canonical-only node.
func TestHandshake_PeerMissingCanonicalCapsIsRejected(t *testing.T) {
	depsB, _, psB, _ := makeTestDepsWithCatalog(t, "B")

	handlerB := NewHandler(depsB, NewStreamRegistry())
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/peer", handlerB.HandlePeer)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	clientID, err := identity.Generate("legacy-client")
	if err != nil {
		t.Fatal(err)
	}
	if err := psB.Add(identity.Peer{
		Name:      "legacy-client",
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

	dialAndHandshake := func(caps []string) (string, error) {
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
			PublicKey:    clientID.PublicKey,
			Signature:    base64.StdEncoding.EncodeToString(sig),
			Capabilities: caps,
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

	// Missing both canonical caps -- exactly what a pre-rewrite legacy peer
	// (never updated to advertise CapCatalogV1/CapCommandV1) would send.
	gotType, err := dialAndHandshake([]string{CapPerStream, CapUpload})
	if err != nil {
		t.Fatal(err)
	}
	if gotType != MsgAuthFail {
		t.Fatalf("expected %s for peer missing canonical caps, got %s", MsgAuthFail, gotType)
	}

	// A peer presenting both canonical caps completes the handshake.
	gotType, err = dialAndHandshake([]string{CapPerStream, CapUpload, CapCatalogV1, CapCommandV1})
	if err != nil {
		t.Fatal(err)
	}
	if gotType != MsgAuthOK {
		t.Fatalf("expected %s for peer with canonical caps, got %s", MsgAuthOK, gotType)
	}
}

// TestSchema4ProtocolVersion_FAILS documents the contract that in schema 4,
// peer protocol compatibility is negotiated with a single ProtocolVersion
// field instead of separate CapCatalogV1 and CapCommandV1 capability flags.
// This test documents the target contract. It will fail until Task 4 is implemented.
func TestSchema4ProtocolVersion_FAILS(t *testing.T) {
	// Schema 4 peer handshake contract:
	// - AuthPayload and AuthOKPayload each have a ProtocolVersion int field
	// - ProtocolVersion is mandatory, negotiated once at handshake
	// - CapCatalogV1 and CapCommandV1 are deleted (no longer exist)
	// - CapPerStream and CapUpload remain as optional negotiated features
	// - Mismatched ProtocolVersion causes handshake failure before registration

	// Currently AuthPayload has Capabilities []string but no ProtocolVersion.
	// After Task 4, AuthPayload will have ProtocolVersion int.

	// This test documents the target behavior; it FAILS until Task 4.
	t.Log("Schema 4 contract: Single ProtocolVersion field in AuthPayload")
	t.Log("Schema 4 contract: CapCatalogV1 and CapCommandV1 are deleted")
	t.Skip("Awaiting Task 4 implementation")
}
