package state

import (
	"encoding/json"
	"os"
	"testing"
)

type fixtureFile struct {
	Schema       int             `json:"schema"`
	Owner        string          `json:"owner"`
	Revision     int64           `json:"revision"`
	Session      json.RawMessage `json:"session"`
	Layout       json.RawMessage `json:"layout"`
	Receipt      json.RawMessage `json:"receipt"`
	MalformedIDs struct {
		Empty     string `json:"empty"`
		Slash     string `json:"slash"`
		Backslash string `json:"backslash"`
		Unicode   string `json:"unicode"`
		TooLong   string `json:"too_long"`
		BadChars  string `json:"bad_chars"`
	} `json:"malformed_ids"`
}

func loadFixtures(t *testing.T) fixtureFile {
	t.Helper()
	data, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var f fixtureFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	return f
}

func TestGeneratedIDsAreValid(t *testing.T) {
	for _, id := range []interface{ Validate() error }{
		NewOwnerID(),
		NewSessionID(),
		NewLayoutID(),
		NewSplitID(),
		NewCommandID(),
	} {
		if err := id.Validate(); err != nil {
			t.Errorf("generated id invalid: %v", err)
		}
	}

	id := NewSessionID()
	if len(id) > maxIDLength {
		t.Errorf("session id length %d exceeds %d", len(id), maxIDLength)
	}
}

func TestSessionRefRoundTrip(t *testing.T) {
	f := loadFixtures(t)
	ref, err := ParseSessionRef(f.Owner + "/" + "sessionfixture1234567890:0.0")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ref.Owner != OwnerID(f.Owner) {
		t.Errorf("owner = %q, want %q", ref.Owner, f.Owner)
	}
	if ref.Session != "sessionfixture1234567890" {
		t.Errorf("session = %q", ref.Session)
	}
	if got := ref.String(); got != f.Owner+"/sessionfixture1234567890:0.0" {
		t.Errorf("String() = %q", got)
	}

	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `"`+f.Owner+`/sessionfixture1234567890:0.0"` {
		t.Errorf("json = %s", data)
	}

	var back SessionRef
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != ref {
		t.Errorf("round-trip = %+v, want %+v", back, ref)
	}
}

func TestSessionRefWithoutOwner(t *testing.T) {
	ref, err := ParseSessionRef("sessionabc:1.2")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ref.Owner != "" || ref.Session != "sessionabc" || ref.Window != 1 || ref.Pane != 2 {
		t.Errorf("got %+v", ref)
	}
}

func TestSessionRefErrors(t *testing.T) {
	cases := []string{
		"",
		":",
		"owner/",
		"/session:0.0",
		"owner/session:-1.0",
		"owner/session:0.-1",
		"owner/session:0.999999",
		"owner/session:0.0.0",
		"owner/session:xyz",
		"owner/bad.id:0.0",
		"bad owner/session:0.0",
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			if _, err := ParseSessionRef(s); err == nil {
				t.Errorf("ParseSessionRef(%q) expected error", s)
			}
		})
	}
}

func TestMalformedIDs(t *testing.T) {
	f := loadFixtures(t)
	cases := map[string]string{
		"empty":     f.MalformedIDs.Empty,
		"slash":     f.MalformedIDs.Slash,
		"backslash": f.MalformedIDs.Backslash,
		"unicode":   f.MalformedIDs.Unicode,
		"too_long":  f.MalformedIDs.TooLong,
		"bad_chars": f.MalformedIDs.BadChars,
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if err := SessionID(s).Validate(); err == nil {
				t.Errorf("SessionID(%q) expected error", s)
			}
		})
	}
	if len(f.MalformedIDs.TooLong) <= maxIDLength {
		t.Errorf("too_long fixture length %d is not oversized", len(f.MalformedIDs.TooLong))
	}
}

func TestIDRejectsPathSeparator(t *testing.T) {
	for _, s := range []string{"foo/bar", "foo\\\\bar"} {
		if err := OwnerID(s).Validate(); err == nil {
			t.Errorf("OwnerID(%q) should reject path separator", s)
		}
	}
}
