package preferences

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestUpdateDoesNotAliasCallerStruct reproduces the API key masking corruption:
// the handler called Update(&prefs) then masked prefs.AINaming.APIKey for the
// HTTP echo. When Update kept the caller's pointer, that mask leaked into the
// store and the next save's "restore from store" persisted the mask, clobbering
// the real key.
func TestUpdateDoesNotAliasCallerStruct(t *testing.T) {
	dir := t.TempDir()
	s := &Store{path: filepath.Join(dir, "preferences.json"), data: Default()}

	prefs := Default()
	prefs.AINaming.Enabled = true
	prefs.AINaming.Endpoint = "http://example/v1"
	prefs.AINaming.APIKey = "sk-real-secret"
	if err := s.Update(prefs); err != nil {
		t.Fatal(err)
	}

	// Simulate the handler mutating its local copy for the masked echo.
	prefs.AINaming.APIKey = APIKeyMask

	// Store must still hold the real key, not the mask.
	if got := s.Get().AINaming.APIKey; got != "sk-real-secret" {
		t.Fatalf("store key corrupted by caller mutation: got %q", got)
	}

	// And it must have been written to disk as the real key.
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || contains(raw, APIKeyMask) {
		t.Fatalf("mask leaked to disk: %s", raw)
	}
}


func contains(b []byte, sub string) bool {
	return len(sub) > 0 && len(b) >= len(sub) && indexOf(string(b), sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestWikiStaysEnabledForClientsThatOmitTheField pins down why the wiki panel
// preference is stored inverted as wiki_disabled instead of wiki_enabled.
//
// PUT /api/preferences decodes the request body into a zero-valued Preferences
// and replaces the store wholesale, and the frontend PUTs the entire object it
// is holding. A browser tab left open across a deploy therefore saves a body
// with no wiki key in it at all. Under a wiki_enabled field defaulting to true,
// that save would persist false and switch the feature off for good.
//
// Inverted, the same body means "not disabled", which is the safe reading.
func TestWikiStaysEnabledForClientsThatOmitTheField(t *testing.T) {
	legacyBody := []byte(`{"theme":"dark","wiki_viewer_url":"http://localhost:3000"}`)

	var prefs Preferences
	if err := json.Unmarshal(legacyBody, &prefs); err != nil {
		t.Fatalf("decode legacy body: %v", err)
	}
	if prefs.WikiDisabled {
		t.Fatal("a client that never heard of the field disabled the wiki panel")
	}

	dir := t.TempDir()
	s := &Store{path: filepath.Join(dir, "preferences.json"), data: Default()}
	if err := s.Update(&prefs); err != nil {
		t.Fatalf("update: %v", err)
	}
	if s.Get().WikiDisabled {
		t.Fatal("wiki panel disabled after a save from a client that omits the field")
	}

	// And the round trip: an explicit disable must survive a reload, otherwise
	// the toggle would silently reset itself.
	prefs.WikiDisabled = true
	if err := s.Update(&prefs); err != nil {
		t.Fatalf("update disabled: %v", err)
	}
	reloaded := &Store{path: s.path, data: Default()}
	if err := reloaded.load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded.Get().WikiDisabled {
		t.Fatal("explicit disable did not survive a reload")
	}
}
