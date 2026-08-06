package peer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/anh-chu/termyard/pkg/identity"
)

func DialPeerStream(ctx context.Context, addr string, id *identity.Identity, token string) (*websocket.Conn, error) {
	if addr == "" {
		return nil, fmt.Errorf("peer has no address")
	}
	if token == "" {
		return nil, fmt.Errorf("missing stream token")
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
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", u.String(), err)
	}

	var challengeMsg Message
	conn.SetReadDeadline(streamDeadline(ctx, streamSetupTimeout))
	if err := conn.ReadJSON(&challengeMsg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read challenge: %w", err)
	}
	if challengeMsg.Type != MsgChallenge {
		conn.Close()
		return nil, fmt.Errorf("expected challenge got %s", challengeMsg.Type)
	}
	var ch ChallengePayload
	if err := json.Unmarshal(challengeMsg.Payload, &ch); err != nil {
		conn.Close()
		return nil, fmt.Errorf("parse challenge: %w", err)
	}
	challengeBytes, err := base64.StdEncoding.DecodeString(ch.Challenge)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("decode challenge: %w", err)
	}
	sig, err := id.Sign(challengeBytes)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("sign: %w", err)
	}
	authMsg, _ := NewMessage(MsgAuth, AuthPayload{
		PublicKey:       id.PublicKey,
		Signature:       base64.StdEncoding.EncodeToString(sig),
		ProtocolVersion: ProtocolVersion,
	})
	if err := conn.WriteJSON(authMsg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send auth: %w", err)
	}

	var result Message
	conn.SetReadDeadline(streamDeadline(ctx, streamSetupTimeout))
	if err := conn.ReadJSON(&result); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read auth result: %w", err)
	}
	conn.SetReadDeadline(time.Time{})
	if result.Type == MsgAuthFail {
		var reason struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(result.Payload, &reason)
		conn.Close()
		return nil, fmt.Errorf("%s", reason.Reason)
	}
	if result.Type != MsgAuthOK {
		conn.Close()
		return nil, fmt.Errorf("unexpected auth response: %s", result.Type)
	}

	tokenMsg, _ := NewMessage(MsgStreamToken, StreamTokenPayload{Token: token})
	if err := conn.WriteJSON(tokenMsg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send stream token: %w", err)
	}

	// Default writes uncompressed — host role re-enables in BridgePTY path.
	conn.EnableWriteCompression(false)

	return conn, nil
}
