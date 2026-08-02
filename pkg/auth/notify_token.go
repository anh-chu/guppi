package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/anh-chu/termyard/pkg/config"
)

const notifyTokenFilename = "notify.token"

// LoadOrCreateNotifyToken returns the machine-local bearer token used for
// authenticated TCP event ingestion. It reads an existing 0600 token file or
// generates a 256-bit token and writes it to the config directory.
func LoadOrCreateNotifyToken() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, notifyTokenFilename)

	data, err := os.ReadFile(path)
	if err == nil {
		tok := strings.TrimSpace(string(data))
		if isHexToken(tok) {
			return tok, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read notify token: %w", err)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// ReadNotifyToken reads an existing notify token without generating one.
// It is used by the notify CLI when falling back to HTTP/TCP delivery.
func ReadNotifyToken() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, notifyTokenFilename))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// ValidBearer performs a constant-time comparison of the Authorization header
// against the stored notify token.
func ValidBearer(r *http.Request, token string) bool {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	provided := h[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(token), []byte(provided)) == 1
}

func isHexToken(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
