package webpush

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	wp "github.com/SherClockHolmes/webpush-go"
)

// VAPIDKeys holds the public/private VAPID key pair
type VAPIDKeys struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

// dataDir returns the directory for storing guppi data files
func dataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "guppi")
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "guppi")
	}
	return filepath.Join(home, ".local", "share", "guppi")
}

// LoadOrCreateKeys loads VAPID keys from disk, or generates and persists new ones
func LoadOrCreateKeys() (*VAPIDKeys, error) {
	dir := dataDir()
	keyFile := filepath.Join(dir, "vapid-keys.json")

	// Try loading existing keys
	data, err := os.ReadFile(keyFile)
	if err == nil {
		var keys VAPIDKeys
		if err := json.Unmarshal(data, &keys); err == nil && keys.PublicKey != "" && keys.PrivateKey != "" {
			return &keys, nil
		}
	}

	// Generate new keys
	priv, pub, err := wp.GenerateVAPIDKeys()
	if err != nil {
		return nil, fmt.Errorf("generate VAPID keys: %w", err)
	}

	keys := &VAPIDKeys{
		PublicKey:  pub,
		PrivateKey: priv,
	}

	// Persist to disk
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	data, err = json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal VAPID keys: %w", err)
	}

	if err := os.WriteFile(keyFile, data, 0600); err != nil {
		return nil, fmt.Errorf("write VAPID keys: %w", err)
	}

	return keys, nil
}
