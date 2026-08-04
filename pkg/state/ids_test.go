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

// sessionRefFixtureCase mirrors one entry of testdata/session_ref_fixtures.json,
// the cross-language golden fixture also read by
// web/src/state/v2/wireCodec.test.ts. Both sides must agree that this exact
// wire string is what MarshalJSON produces (Go) / encodeSessionRef produces
// (TS), and that UnmarshalJSON (Go) / decodeSessionRef (TS) parses it back to
// this exact decoded shape. This is the test that would have caught the
// object-vs-string SessionRef wire mismatch bug.
type sessionRefFixtureCase struct {
	Name    string `json:"name"`
	Wire    string `json:"wire"`
	Decoded struct {
		Owner   *string `json:"owner"`
		Session string  `json:"session"`
		Window  uint16  `json:"window"`
		Pane    uint16  `json:"pane"`
	} `json:"decoded"`
}

type sessionRefFixtureFile struct {
	Cases []sessionRefFixtureCase `json:"cases"`
}

func loadSessionRefFixtures(t *testing.T) sessionRefFixtureFile {
	t.Helper()
	data, err := os.ReadFile("../../testdata/session_ref_fixtures.json")
	if err != nil {
		t.Fatalf("read session_ref_fixtures.json: %v", err)
	}
	var f sessionRefFixtureFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse session_ref_fixtures.json: %v", err)
	}
	return f
}

func TestSessionRefGoldenFixture(t *testing.T) {
	f := loadSessionRefFixtures(t)
	if len(f.Cases) == 0 {
		t.Fatal("no fixture cases loaded")
	}
	for _, c := range f.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			want := SessionRef{
				Session: SessionID(c.Decoded.Session),
				Window:  c.Decoded.Window,
				Pane:    c.Decoded.Pane,
			}
			if c.Decoded.Owner != nil {
				want.Owner = OwnerID(*c.Decoded.Owner)
			}

			// UnmarshalJSON of the wire string must produce the decoded shape.
			var got SessionRef
			if err := json.Unmarshal([]byte(`"`+c.Wire+`"`), &got); err != nil {
				t.Fatalf("unmarshal %q: %v", c.Wire, err)
			}
			if got != want {
				t.Errorf("unmarshal(%q) = %+v, want %+v", c.Wire, got, want)
			}

			// MarshalJSON of the decoded shape must produce exactly the wire string.
			data, err := json.Marshal(want)
			if err != nil {
				t.Fatalf("marshal %+v: %v", want, err)
			}
			if string(data) != `"`+c.Wire+`"` {
				t.Errorf("marshal(%+v) = %s, want %q", want, data, c.Wire)
			}
		})
	}
}
