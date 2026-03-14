package identity

import (
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// PairingCodeTTL is how long a pairing code is valid
	PairingCodeTTL = 5 * time.Minute
)

// wordlist for generating human-readable pairing codes
var words = []string{
	"alpha", "amber", "arrow", "atlas", "azure",
	"blaze", "bloom", "brave", "brook", "cedar",
	"chase", "cloud", "coral", "crane", "delta",
	"drift", "eagle", "ember", "fable", "flame",
	"frost", "ghost", "gleam", "grace", "haven",
	"ivory", "jewel", "karma", "lemon", "lunar",
	"maple", "marsh", "noble", "ocean", "olive",
	"pearl", "pilot", "plume", "prism", "quake",
	"raven", "ridge", "river", "robin", "scout",
	"shade", "sigma", "solar", "spark", "steel",
	"storm", "swift", "thorn", "tiger", "titan",
	"torch", "trail", "vapor", "vivid", "whale",
	"winter", "zephyr",
}

// PairingCode represents a pending pairing request
type PairingCode struct {
	Code      string
	ExpiresAt time.Time
}

// PairingManager manages active pairing codes
type PairingManager struct {
	mu    sync.Mutex
	codes map[string]*PairingCode
}

// NewPairingManager creates a new pairing manager
func NewPairingManager() *PairingManager {
	return &PairingManager{
		codes: make(map[string]*PairingCode),
	}
}

// Generate creates a new pairing code (3 random words)
func (pm *PairingManager) Generate() (*PairingCode, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Clean up expired codes
	now := time.Now()
	for code, pc := range pm.codes {
		if now.After(pc.ExpiresAt) {
			delete(pm.codes, code)
		}
	}

	parts := make([]string, 3)
	for i := range parts {
		b := make([]byte, 1)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generate random: %w", err)
		}
		parts[i] = words[int(b[0])%len(words)]
	}
	code := strings.ToUpper(strings.Join(parts, "-"))

	pc := &PairingCode{
		Code:      code,
		ExpiresAt: time.Now().Add(PairingCodeTTL),
	}
	pm.codes[code] = pc
	return pc, nil
}

// Validate checks if a pairing code is valid and consumes it (one-time use)
func (pm *PairingManager) Validate(code string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	code = strings.ToUpper(strings.TrimSpace(code))
	pc, ok := pm.codes[code]
	if !ok {
		return false
	}

	// Remove immediately (one-time use)
	delete(pm.codes, code)

	return time.Now().Before(pc.ExpiresAt)
}
