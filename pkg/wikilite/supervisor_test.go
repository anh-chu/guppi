package wikilite

import (
	"testing"
	"time"
)

func TestParsePortLineValid(t *testing.T) {
	port, ok := parsePortLine("WIKI_LITE_PORT=41234")
	if !ok {
		t.Fatal("parsePortLine rejected valid line")
	}
	if port != 41234 {
		t.Fatalf("port = %d, want 41234", port)
	}
}

func TestParsePortLineRejectsNoise(t *testing.T) {
	cases := []string{
		"Some log message",
		"",
		"WIKI_LITE_PORT=not_a_number",
		"WIKI_LITE_PORT=",
		"WIKI_LITE_PORT=99999",
		"WIKI_LITE_PORT=0",
		"WIKI_LITE_PORT=-1",
		"WIKI_LITE_PORT = 41234",
	}
	for _, line := range cases {
		if _, ok := parsePortLine(line); ok {
			t.Fatalf("parsePortLine accepted invalid line: %q", line)
		}
	}
}

func TestNextBackoffSequence(t *testing.T) {
	var d time.Duration
	expect := []time.Duration{
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
	}
	for i, want := range expect {
		d = nextBackoff(d)
		if d != want {
			t.Fatalf("backoff[%d] = %v, want %v", i, d, want)
		}
	}
	// Cap persists.
	d = nextBackoff(d)
	if d != 30*time.Second {
		t.Fatalf("after cap = %v, want 30s", d)
	}
}

func TestSupervisorStatusFresh(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := NewSupervisor()
	st := s.Status()
	if st.Installed {
		t.Fatal("fresh supervisor reports installed true")
	}
	if st.Running {
		t.Fatal("fresh supervisor reports running true")
	}
	if st.DefaultRoot == "" {
		t.Log("default_root empty (no HOME?); ok on CI")
	}
}
