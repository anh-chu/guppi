package model

import (
	"fmt"
	"strconv"
	"strings"
)

// SessionRef identifies a daemon-backed session, and optionally a window and
// pane within it. The current daemon model is always single-window/single-pane,
// so canonical pane identifiers look like "<session>:0.0".
type SessionRef struct {
	Session string
	Window  int
	Pane    int
}

// PaneID returns the canonical pane identifier for the reference, e.g.
// "my-session:0.0". Window and pane default to zero when unset.
func (r SessionRef) PaneID() string {
	return fmt.Sprintf("%s:%d.%d", r.Session, r.Window, r.Pane)
}

// ParseSessionRef parses daemon-style session identifiers.
//
//   - "name"          -> {Session:"name"}
//   - "name:0"        -> {Session:"name", Window:0}
//   - "name:0.0"      -> {Session:"name", Window:0, Pane:0}
//
// Empty session names, negative indexes, and non-numeric window/pane parts are
// rejected. The separator ":" is only permitted once and "." is only permitted
// inside the window/pane suffix.
func ParseSessionRef(s string) (SessionRef, error) {
	if s == "" {
		return SessionRef{}, fmt.Errorf("empty session reference")
	}
	parts := strings.SplitN(s, ":", 2)
	ref := SessionRef{Session: parts[0]}
	if ref.Session == "" {
		return SessionRef{}, fmt.Errorf("empty session name in %q", s)
	}
	if len(parts) == 1 {
		return ref, nil
	}

	suffix := parts[1]
	if suffix == "" {
		return SessionRef{}, fmt.Errorf("missing window index after ':' in %q", s)
	}
	wpParts := strings.Split(suffix, ".")
	if len(wpParts) > 2 {
		return SessionRef{}, fmt.Errorf("too many window/pane parts in %q", s)
	}

	w, err := strconv.Atoi(wpParts[0])
	if err != nil || w < 0 {
		return SessionRef{}, fmt.Errorf("invalid window index %q in %q", wpParts[0], s)
	}
	ref.Window = w

	if len(wpParts) == 2 {
		p, err := strconv.Atoi(wpParts[1])
		if err != nil || p < 0 {
			return SessionRef{}, fmt.Errorf("invalid pane index %q in %q", wpParts[1], s)
		}
		ref.Pane = p
	}

	return ref, nil
}
