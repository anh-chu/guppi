package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ekristen/guppi/pkg/common"
)

// DefaultPort is guppi's default HTTP port (also peer port).
const DefaultPort = "7654"

// BootstrapRequest is what the dialer posts to /api/peers/bootstrap.
type BootstrapRequest struct {
	Password    string `json:"password"`
	Name        string `json:"name"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	// ListenPort is the dialer's HTTP listening port. The listener stores it
	// as the dialer's address (combined with the request's remote host) so
	// later PTY back-dials reach the dialer's actual server, not the
	// ephemeral source port of this bootstrap request.
	ListenPort int `json:"listen_port,omitempty"`
}

// BootstrapResponse is what the listener returns on success.
type BootstrapResponse struct {
	Name        string `json:"name"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	Version     string `json:"version,omitempty"`
}

// NormalizeAddress accepts "host", "host:port", or "scheme://host[:port]" and
// returns "host:port". Defaults port to DefaultPort.
func NormalizeAddress(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("address is empty")
	}
	// Strip scheme if present.
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", fmt.Errorf("parse address: %w", err)
		}
		if u.Host == "" {
			return "", fmt.Errorf("address has no host")
		}
		addr = u.Host
	}

	// Try to split host:port.
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port — apply default.
		// Handle bare IPv6 ("::1") by wrapping.
		if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "[") {
			addr = "[" + addr + "]"
		}
		return net.JoinHostPort(strings.Trim(addr, "[]"), DefaultPort), nil
	}
	if port == "" {
		port = DefaultPort
	}
	return net.JoinHostPort(host, port), nil
}

// BootstrapError carries an HTTP status hint so the local handler can
// propagate a meaningful code to the UI.
type BootstrapError struct {
	Status  int
	Message string
}

func (e *BootstrapError) Error() string { return e.Message }

// SendBootstrap dials peer at addr, POSTs BootstrapRequest, parses response.
// Plain http only. 10s overall timeout. Returns *BootstrapError on remote-side
// rejection so callers can preserve status codes.
func SendBootstrap(ctx context.Context, addr string, req BootstrapRequest) (*BootstrapResponse, error) {
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	u := "http://" + addr + "/api/peers/bootstrap"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "guppi/"+common.VERSION)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
	case http.StatusUnauthorized:
		return nil, &BootstrapError{Status: http.StatusUnauthorized, Message: "password rejected by remote machine"}
	case http.StatusBadRequest:
		return nil, &BootstrapError{Status: http.StatusBadRequest, Message: "bootstrap rejected by remote machine"}
	case http.StatusServiceUnavailable:
		return nil, &BootstrapError{Status: http.StatusServiceUnavailable, Message: "remote machine has no password configured yet"}
	default:
		return nil, &BootstrapError{Status: http.StatusBadGateway, Message: fmt.Sprintf("bootstrap failed: HTTP %d", resp.StatusCode)}
	}
	var out BootstrapResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}
