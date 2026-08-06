package state

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ID kinds for typed validation errors.
const (
	KindOwner   = "owner"
	KindSession = "session"
	KindSplit   = "split"
	KindCommand = "command"
)

// idEncoding produces URL-safe, case-insensitive identifiers.
var idEncoding = base32.HexEncoding.WithPadding(base32.NoPadding)

// maxIDLength caps identifier strings so they remain safe for URLs and map keys.
const maxIDLength = 64

// idPattern restricts generated and parsed identifiers to lowercase base32.
var idPattern = regexp.MustCompile(`^[a-z0-9]+$`)

// OwnerID identifies a node/user that owns sessions and workspace state.
type OwnerID string

// SessionID identifies a durable terminal session. It never changes when a
// session is renamed, so identity stays stable while display labels stay mutable.
type SessionID string

// SplitID identifies a split node inside a pane tree.
type SplitID string

// CommandID identifies a user-issued command or intent.
type CommandID string

func NewOwnerID() OwnerID     { return OwnerID(newID()) }
func NewSessionID() SessionID { return SessionID(newID()) }
func NewSplitID() SplitID     { return SplitID(newID()) }
func NewCommandID() CommandID { return CommandID(newID()) }

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is extremely rare. Fall back to a time-based
		// nonce that is still unique within a single process.
		return idEncoding.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return strings.ToLower(idEncoding.EncodeToString(b))
}

// OwnerIDFromFingerprint is the single canonical, deterministic conversion
// from an authenticated peer identity fingerprint (identity.Identity.
// Fingerprint(), base64.RawURLEncoding of 8 raw ed25519-pubkey bytes -- which
// may contain uppercase letters, '-', or '_') into a valid OwnerID (lowercase
// base32, see idPattern/validateID above). It transcodes the same raw bytes
// through idEncoding rather than hashing, so it is a stable bijection: the
// same fingerprint always yields the same OwnerID and different fingerprints
// never collide (barring the encoding always succeeding, see fallback below).
//
// Both directions of the v2 peer-trust boundary MUST call this exact function
// so they stay consistent:
//   - when opening/creating this node's own v2 store, pass
//     OwnerIDFromFingerprint(selfFingerprint) as StoreOptions.Owner, so this
//     node's catalog Owner IS its own authenticated identity;
//   - when validating a remote snapshot/request, compare snap.Owner (or
//     req.Requester) against OwnerIDFromFingerprint(peerID), where peerID is
//     the authenticated identity fingerprint of that connection.
func OwnerIDFromFingerprint(fingerprint string) OwnerID {
	raw, err := base64.RawURLEncoding.DecodeString(fingerprint)
	if err != nil || len(raw) == 0 {
		// Fingerprint() always produces valid, non-empty base64url. If some
		// caller ever passes something else, fall back to transcoding the raw
		// input bytes directly so this function never panics and stays
		// deterministic for the same bad input rather than silently matching
		// nothing.
		raw = []byte(fingerprint)
	}
	return OwnerID(strings.ToLower(idEncoding.EncodeToString(raw)))
}

func (id OwnerID) String() string   { return string(id) }
func (id SessionID) String() string { return string(id) }
func (id SplitID) String() string   { return string(id) }
func (id CommandID) String() string { return string(id) }

func (id OwnerID) Validate() error   { return validateID(string(id), KindOwner) }
func (id SessionID) Validate() error { return validateID(string(id), KindSession) }
func (id SplitID) Validate() error   { return validateID(string(id), KindSplit) }
func (id CommandID) Validate() error { return validateID(string(id), KindCommand) }

func validateID(s, kind string) error {
	if s == "" {
		return fmt.Errorf("%s id is empty", kind)
	}
	if len(s) > maxIDLength {
		return fmt.Errorf("%s id %q exceeds max length %d", kind, s, maxIDLength)
	}
	if strings.ContainsAny(s, "/\\\\") {
		return fmt.Errorf("%s id %q contains path separator", kind, s)
	}
	for _, r := range s {
		if r > 127 {
			return fmt.Errorf("%s id %q contains non-ascii character", kind, s)
		}
	}
	if !idPattern.MatchString(s) {
		return fmt.Errorf("%s id %q contains invalid character", kind, s)
	}
	return nil
}

// SessionRef identifies a pane by owner, session, window, and pane.
// It is the canonical identity used by Go, TypeScript, URLs, and map keys.
type SessionRef struct {
	Owner   OwnerID   `json:"owner,omitempty"`
	Session SessionID `json:"session"`
	Window  uint16    `json:"window"`
	Pane    uint16    `json:"pane"`
}

// String returns the canonical encoding.
//   - with owner: "<owner>/<session>:<window>.<pane>"
//   - local:      "<session>:<window>.<pane>"
func (r SessionRef) String() string {
	if r.Owner != "" {
		return fmt.Sprintf("%s/%s:%d.%d", r.Owner, r.Session, r.Window, r.Pane)
	}
	return fmt.Sprintf("%s:%d.%d", r.Session, r.Window, r.Pane)
}

// MapKey returns the canonical form that can be used as a stable map key.
func (r SessionRef) MapKey() string { return r.String() }

// ParseSessionRef parses the canonical encoding.
func ParseSessionRef(s string) (SessionRef, error) {
	if s == "" {
		return SessionRef{}, fmt.Errorf("empty session reference")
	}
	owner := OwnerID("")
	rest := s
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		owner = OwnerID(s[:idx])
		if err := owner.Validate(); err != nil {
			return SessionRef{}, err
		}
		rest = s[idx+1:]
	}
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) == 0 || parts[0] == "" {
		return SessionRef{}, fmt.Errorf("missing session id in %q", s)
	}
	session := SessionID(parts[0])
	if err := session.Validate(); err != nil {
		return SessionRef{}, err
	}
	ref := SessionRef{Owner: owner, Session: session}
	if len(parts) == 1 {
		return ref, nil
	}
	wp := parts[1]
	if wp == "" {
		return SessionRef{}, fmt.Errorf("missing window index in %q", s)
	}
	wpParts := strings.Split(wp, ".")
	if len(wpParts) > 2 {
		return SessionRef{}, fmt.Errorf("too many window/pane parts in %q", s)
	}
	w, err := strconv.Atoi(wpParts[0])
	if err != nil || w < 0 || w > math.MaxUint16 {
		return SessionRef{}, fmt.Errorf("invalid window index in %q", s)
	}
	ref.Window = uint16(w)
	if len(wpParts) == 2 {
		p, err := strconv.Atoi(wpParts[1])
		if err != nil || p < 0 || p > math.MaxUint16 {
			return SessionRef{}, fmt.Errorf("invalid pane index in %q", s)
		}
		ref.Pane = uint16(p)
	}
	return ref, nil
}

// MarshalJSON encodes SessionRef as its canonical string form.
func (r SessionRef) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.String())
}

// UnmarshalJSON decodes SessionRef from its canonical string form.
func (r *SessionRef) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := ParseSessionRef(s)
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}

// SplitDirection is the geometric direction of a split node.
type SplitDirection string

const (
	DirectionHorizontal SplitDirection = "h"
	DirectionVertical   SplitDirection = "v"
)

// Ratio is the first-child size fraction of a split. It must be a finite
// number strictly between 0 and 1.
type Ratio float64

// Valid reports whether the ratio is a finite number in the allowed range.
func (r Ratio) Valid() bool {
	f := float64(r)
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f > 0.0 && f < 1.0
}

// Validate returns a descriptive error for invalid ratios.
func (r Ratio) Validate() error {
	f := float64(r)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("split ratio is not finite")
	}
	if f <= 0.0 || f >= 1.0 {
		return fmt.Errorf("split ratio %v must be in (0,1)", f)
	}
	return nil
}

// MarshalJSON encodes the ratio as a plain JSON number.
func (r Ratio) MarshalJSON() ([]byte, error) {
	return json.Marshal(float64(r))
}

// UnmarshalJSON decodes the ratio and rejects non-finite or out-of-range values.
func (r *Ratio) UnmarshalJSON(data []byte) error {
	var f float64
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*r = Ratio(f)
	if err := r.Validate(); err != nil {
		return err
	}
	return nil
}
