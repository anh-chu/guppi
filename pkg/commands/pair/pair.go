package pair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/ekristen/guppi/pkg/common"
	"github.com/ekristen/guppi/pkg/identity"
	"github.com/ekristen/guppi/pkg/socket"
)

func init() {
	cmd := &cli.Command{
		Name:  "pair",
		Usage: "pair with a hub or generate a pairing code",
		Description: `On the hub: run 'guppi pair' to generate a pairing code.
On the peer: run 'guppi pair --hub <address> --code <code>' to complete pairing.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "hub",
				Usage:   "Hub address to pair with (e.g. desktop.ts.net:7654)",
				Sources: cli.EnvVars("GUPPI_HUB"),
			},
			&cli.StringFlag{
				Name:  "code",
				Usage: "Pairing code from the hub",
			},
			&cli.BoolFlag{
				Name:    "insecure",
				Usage:   "Skip TLS certificate verification",
				Sources: cli.EnvVars("GUPPI_INSECURE"),
			},
			&cli.StringFlag{
				Name:    "socket",
				Usage:   "Path to guppi server socket (for generating codes)",
				Sources: cli.EnvVars("GUPPI_SOCKET"),
			},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			hubAddr := c.String("hub")
			code := c.String("code")

			if hubAddr != "" && code != "" {
				return pairWithHub(hubAddr, code, c.Bool("insecure"))
			}

			if hubAddr == "" && code == "" {
				return generatePairingCode(c.String("socket"))
			}

			return fmt.Errorf("provide both --hub and --code, or neither (to generate a code)")
		},
	}

	common.RegisterCommand(cmd)
}

// normalizeURL takes a bare host:port or full URL and returns a normalized https URL.
func normalizeURL(addr string) (*url.URL, error) {
	if !strings.Contains(addr, "://") {
		addr = "https://" + addr
	}
	return url.Parse(addr)
}

// pairWithHub completes the pairing handshake with a remote hub via HTTP POST
func pairWithHub(hubAddr, code string, insecure bool) error {
	// Split code into word-code and optional TLS fingerprint prefix
	// Format: "AMBER-TIGER-SEVEN:a3b2c4d5e6f78901" or just "AMBER-TIGER-SEVEN"
	wordCode := code
	var certFingerprintPrefix string
	if idx := strings.LastIndex(code, ":"); idx != -1 {
		wordCode = code[:idx]
		certFingerprintPrefix = strings.ToLower(code[idx+1:])
	}

	hostname, _ := os.Hostname()
	id, err := identity.LoadOrCreate(hostname)
	if err != nil {
		return fmt.Errorf("load identity: %w", err)
	}

	peerStore, err := identity.NewPeerStore()
	if err != nil {
		return fmt.Errorf("load peer store: %w", err)
	}

	u, err := normalizeURL(hubAddr)
	if err != nil {
		return fmt.Errorf("invalid hub address %q: %w", hubAddr, err)
	}
	u.Path = "/api/pair/complete"

	fmt.Printf("Connecting to %s...\n", u.Host)

	reqBody, _ := json.Marshal(map[string]string{
		"code":       wordCode,
		"name":       id.Name,
		"public_key": id.PublicKey,
	})

	// Build TLS config:
	// - If fingerprint is in the code, verify the server cert matches it (TOFU)
	// - Capture the full cert PEM for pinning in future connections
	var pinnedCertPEM string
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return fmt.Errorf("server presented no TLS certificates")
				}

				// Compute fingerprint of the leaf cert
				leaf := cs.PeerCertificates[0]
				hash := sha256.Sum256(leaf.Raw)
				fingerprint := hex.EncodeToString(hash[:])

				// If we have a fingerprint prefix from the code, verify it
				if certFingerprintPrefix != "" {
					if !strings.HasPrefix(fingerprint, certFingerprintPrefix) {
						return fmt.Errorf("TLS certificate fingerprint mismatch: expected prefix %s, got %s",
							certFingerprintPrefix, fingerprint[:len(certFingerprintPrefix)])
					}
					fmt.Println("TLS certificate fingerprint verified.")
				}

				// Capture cert as PEM for pinning
				block := &pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}
				pinnedCertPEM = string(pem.EncodeToMemory(block))

				// If system-trusted, no need to pin
				pool, err := x509.SystemCertPool()
				if err == nil {
					_, verifyErr := leaf.Verify(x509.VerifyOptions{
						Roots:         pool,
						Intermediates: x509.NewCertPool(),
					})
					if verifyErr == nil {
						pinnedCertPEM = ""
					}
				}

				return nil
			},
		},
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport}

	resp, err := client.Post(u.String(), "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pairing failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Status    string `json:"status"`
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
		CACertPEM string `json:"ca_cert_pem"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}

	if result.Status != "paired" {
		return fmt.Errorf("pairing failed: %s", result.Status)
	}

	// Store the hub as a peer
	peer := identity.Peer{
		Name:      result.Name,
		PublicKey: result.PublicKey,
		PairedAt:  time.Now(),
	}
	if result.CACertPEM != "" {
		// Hub provided its CA cert — use CA-based trust (no pin rotation needed)
		peer.CACertPEM = result.CACertPEM
		fmt.Println("Stored hub's CA certificate for future connections.")
	} else if pinnedCertPEM != "" {
		// No CA available — fall back to TOFU cert pinning
		peer.TLSCertPEM = pinnedCertPEM
		fmt.Println("Pinned hub's TLS certificate for future connections.")
	}
	if err := peerStore.Add(peer); err != nil {
		return fmt.Errorf("store hub peer: %w", err)
	}

	fmt.Printf("Paired with \"%s\" successfully!\n", result.Name)
	fmt.Printf("\nTo connect, run:\n  guppi server --hub %s\n", u.Host)

	return nil
}

// generatePairingCode calls the running guppi server via unix socket to generate a pairing code
func generatePairingCode(socketPath string) error {
	if socketPath == "" {
		socketPath = socket.DefaultPath()
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	resp, err := client.Post("http://localhost/api/pair", "application/json", nil)
	if err != nil {
		return fmt.Errorf("failed to reach guppi server via socket %s: %w\nMake sure 'guppi server' is running", socketPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}

	fmt.Printf("Pairing code: %s\n", result.Code)
	fmt.Printf("Expires in %s\n", time.Until(result.ExpiresAt).Round(time.Second))
	fmt.Println("\nOn the remote machine, run:")
	fmt.Printf("  guppi pair --hub <this-machine-address> --code %s\n", result.Code)

	return nil
}
