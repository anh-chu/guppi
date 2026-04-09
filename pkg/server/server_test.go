package server

import "testing"

func TestEnsureUniqueSessionName(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		want     string
	}{
		{name: "codex-guppi", existing: nil, want: "codex-guppi"},
		{name: "codex-guppi", existing: []string{"codex-guppi"}, want: "codex-guppi-2"},
		{name: "codex-guppi", existing: []string{"codex-guppi", "codex-guppi-2"}, want: "codex-guppi-3"},
	}

	for _, tt := range tests {
		if got := ensureUniqueSessionName(tt.name, tt.existing); got != tt.want {
			t.Fatalf("ensureUniqueSessionName(%q, %v) = %q, want %q", tt.name, tt.existing, got, tt.want)
		}
	}
}
