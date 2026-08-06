package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return &Store{
		path: filepath.Join(t.TempDir(), "schedules.json"),
		jobs: map[string]Job{},
	}
}

func TestStoreRoundTripCRUD(t *testing.T) {
	s := newTestStore(t)
	job, err := s.Add(Job{
		Name:      "nightly",
		CronSpec:  "*/5 * * * *",
		Command:   "echo hi",
		Enabled:   true,
		CreatedAt: time.Unix(100, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID == "" {
		t.Fatal("expected id")
	}
	if job.NextRun.IsZero() {
		t.Fatal("expected next run")
	}

	reloaded := &Store{path: s.path, jobs: map[string]Job{}}
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get(job.ID)
	if !ok {
		t.Fatal("reloaded job missing")
	}
	if got.Name != "nightly" || got.Command != "echo hi" || !got.Enabled {
		t.Fatalf("reloaded job = %#v", got)
	}

	job.Command = "echo bye"
	job.Enabled = false
	job.NextRun = time.Time{}
	updated, err := reloaded.Update(job)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Command != "echo bye" || updated.Enabled {
		t.Fatalf("updated job = %#v", updated)
	}

	if err := reloaded.Remove(job.ID); err != nil {
		t.Fatal(err)
	}
	if jobs := reloaded.List(); len(jobs) != 0 {
		t.Fatalf("jobs = %#v", jobs)
	}
}

func TestStoreCronValidationRejectsGarbage(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add(Job{CronSpec: "nope"}); err == nil {
		t.Fatal("expected add error")
	}
	job, err := s.Add(Job{CronSpec: "* * * * *", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job.CronSpec = "also-nope"
	if _, err := s.Update(job); err == nil {
		t.Fatal("expected update error")
	}
}

func TestStoreUpdateKeepsSessionPrefixAndClearsNextRun(t *testing.T) {
	s := newTestStore(t)
	job, err := s.Add(Job{
		Name:              "nightly",
		CronSpec:          "*/5 * * * *",
		Command:           "echo hi",
		Enabled:           true,
		SessionNamePrefix: "nightly",
	})
	if err != nil {
		t.Fatal(err)
	}
	job.Enabled = false
	job.SessionNamePrefix = ""
	job.NextRun = time.Time{}
	updated, err := s.Update(job)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.NextRun.IsZero() {
		t.Fatalf("next run = %#v", updated.NextRun)
	}
	if updated.SessionNamePrefix != "nightly" {
		t.Fatalf("session prefix = %#v", updated.SessionNamePrefix)
	}
}

func TestStoreMarkRan(t *testing.T) {
	s := newTestStore(t)
	job, err := s.Add(Job{CronSpec: "* * * * *", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	next := job.NextRun.Add(time.Hour)
	updated, err := s.MarkRan(job.ID, time.Unix(200, 0), next)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RunCount != 1 || !updated.LastRun.Equal(time.Unix(200, 0)) || !updated.NextRun.Equal(next) {
		t.Fatalf("updated = %#v", updated)
	}
}

// TestStoreLoadRejectsLegacyHostField proves a pre-rewrite schedules.json
// record still carrying the old bare "host" fingerprint field is rejected
// with a clear error at load time rather than silently reinterpreted as an
// OwnerID (TargetOwner) or migrated automatically.
func TestStoreLoadRejectsLegacyHostField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	legacy := `[{"id":"job1","cron_spec":"* * * * *","host":"fp-remote","enabled":true}]`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{path: path, jobs: map[string]Job{}}
	err := s.load()
	if err == nil {
		t.Fatal("expected load to fail closed on legacy 'host' field")
	}
	if !strings.Contains(err.Error(), "legacy") || !strings.Contains(err.Error(), "job1") {
		t.Fatalf("expected error to name the legacy field and job id, got: %v", err)
	}
}
