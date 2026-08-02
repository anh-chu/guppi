package model

import (
	"testing"
)

func TestParseSessionRef(t *testing.T) {
	cases := []struct {
		input string
		want  SessionRef
	}{
		{"abc", SessionRef{Session: "abc"}},
		{"abc:0", SessionRef{Session: "abc", Window: 0}},
		{"abc:1.2", SessionRef{Session: "abc", Window: 1, Pane: 2}},
		{"my-session:0.0", SessionRef{Session: "my-session", Window: 0, Pane: 0}},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseSessionRef(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ParseSessionRef(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseSessionRefErrors(t *testing.T) {
	cases := []string{
		"",
		":0.0",
		"abc:",
		"abc:-1",
		"abc:0.-1",
		"abc:0.0.0",
		"abc:xyz",
		"abc:0.xyz",
	}

	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseSessionRef(input); err == nil {
				t.Errorf("ParseSessionRef(%q) expected error", input)
			}
		})
	}
}

func TestSessionRefPaneID(t *testing.T) {
	ref := SessionRef{Session: "foo", Window: 0, Pane: 0}
	if got, want := ref.PaneID(), "foo:0.0"; got != want {
		t.Errorf("PaneID() = %q, want %q", got, want)
	}
}
