package sessionattrs

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSetsIncludesScheduleIDs(t *testing.T) {
	s := &Store{attrs: map[string]Attr{
		"host-1/session-a": {
			Background: true,
			ScheduleID: "sched-123",
		},
	}}

	got := s.Sets()
	if len(got.Background) != 1 || got.Background[0] != "host-1/session-a" || len(got.Hidden) != 0 {
		t.Fatalf("sets = %#v", got)
	}
	if got.ScheduleIDs["host-1/session-a"] != "sched-123" {
		t.Fatalf("schedule ids = %#v", got.ScheduleIDs)
	}
}

func TestMigrateKeyPreservesAttrsAcrossRename(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "attrs.json"), attrs: map[string]Attr{
		"local-fp/old":  {Background: true, ScheduleID: "sched-1"},
		"bare-old":      {Hidden: true, ScheduleID: "sched-2"},
		"local-fp/keep": {ScheduleID: "sched-3"},
		"peer-fp/old":   {ScheduleID: "peer-sched"},
	}}

	// Local host-qualified rename keeps the host prefix.
	migrated, err := s.MigrateKey("local-fp", "old", "new")
	if err != nil {
		t.Fatalf("MigrateKey err: %v", err)
	}
	if len(migrated) != 1 || migrated[0] != "local-fp/new" {
		t.Fatalf("migrated = %#v", migrated)
	}
	if _, ok := s.attrs["local-fp/old"]; ok {
		t.Fatal("old key still present")
	}
	if got := s.attrs["local-fp/new"]; !got.Background || got.ScheduleID != "sched-1" {
		t.Fatalf("migrated attr = %#v", got)
	}

	// A peer-owned session with the same name must NOT be touched.
	if _, ok := s.attrs["peer-fp/old"]; !ok {
		t.Fatal("peer-owned key was wrongly migrated")
	}

	// Bare (single-host) key migrates without a prefix.
	migrated, _ = s.MigrateKey("local-fp", "bare-old", "bare-new")
	if len(migrated) != 1 || migrated[0] != "bare-new" {
		t.Fatalf("bare migrated = %#v", migrated)
	}
	if got := s.attrs["bare-new"]; !got.Hidden || got.ScheduleID != "sched-2" {
		t.Fatalf("bare attr = %#v", got)
	}

	// Unrelated keys untouched.
	if s.attrs["local-fp/keep"].ScheduleID != "sched-3" {
		t.Fatal("unrelated key mutated")
	}

	// No-op cases.
	if m, _ := s.MigrateKey("local-fp", "missing", "x"); m != nil {
		t.Fatal("expected nil for missing source")
	}
	if m, _ := s.MigrateKey("local-fp", "same", "same"); m != nil {
		t.Fatal("expected nil for identical names")
	}
}

func TestPruneKeepsScheduleOnlyAttrPastTombstoneTTL(t *testing.T) {
	old := time.Now().Add(-2 * tombstoneTTL)
	s := &Store{path: filepath.Join(t.TempDir(), "attrs.json"), attrs: map[string]Attr{
		// Schedule-only attr for a still-live session, older than tombstoneTTL.
		"host-1/run-1": {ScheduleID: "sched-1", UpdatedAt: old},
		// A genuine expired tombstone should still be dropped.
		"host-1/dead": {UpdatedAt: old},
	}}

	live := map[string]bool{"host-1/run-1": true}
	online := map[string]bool{"host-1": true}
	if _, _, err := s.Prune(live, online); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if got := s.attrs["host-1/run-1"].ScheduleID; got != "sched-1" {
		t.Fatalf("schedule-only attr was pruned: %#v", s.attrs["host-1/run-1"])
	}
	if _, ok := s.attrs["host-1/dead"]; ok {
		t.Fatalf("expired tombstone should have been dropped")
	}
}

func TestApplyRemoteCarriesAndPreservesScheduleID(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "attrs.json"), attrs: map[string]Attr{}}

	// First peer delta for a peer-owned scheduled session carries the schedule id.
	t10 := time.Unix(10, 0)
	if _, accepted, err := s.ApplyRemote("peer-fp/run-1", Attr{ScheduleID: "sched-9", UpdatedAt: t10}); err != nil || !accepted {
		t.Fatalf("first delta: accepted=%v err=%v", accepted, err)
	}
	if got := s.attrs["peer-fp/run-1"].ScheduleID; got != "sched-9" {
		t.Fatalf("schedule id not stored: %q", got)
	}

	// A later background/hidden delta with no schedule id must not wipe it.
	t20 := time.Unix(20, 0)
	if _, accepted, err := s.ApplyRemote("peer-fp/run-1", Attr{Background: true, UpdatedAt: t20}); err != nil || !accepted {
		t.Fatalf("second delta: accepted=%v err=%v", accepted, err)
	}
	if got := s.attrs["peer-fp/run-1"]; got.ScheduleID != "sched-9" || !got.Background {
		t.Fatalf("preserve failed: %#v", got)
	}
}
