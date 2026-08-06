package scheduler

import (
	"sync"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/sirupsen/logrus"
)

type stubPeers struct {
	localHosts map[string]bool
	conn       *peer.PeerConnection
}

func (s stubPeers) ResolveHostParam(host string) (string, bool) {
	if host == "" || s.localHosts[host] {
		return "", true
	}
	return host, false
}

func (s stubPeers) GetPeerConnection(id string) *peer.PeerConnection {
	if s.conn != nil {
		return s.conn
	}
	return nil
}

func TestRunnerFiresDueJob(t *testing.T) {
	s := newTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	job, err := s.Add(Job{
		Name:              "build",
		CronSpec:          "* * * * *",
		Command:           "echo hi",
		SessionNamePrefix: "cron",
		Enabled:           false,
	})
	if err != nil {
		t.Fatal(err)
	}
	job.Enabled = true
	job.NextRun = now.Add(-time.Minute)
	if _, err := s.Update(job); err != nil {
		t.Fatal(err)
	}
	job, _ = s.Get(job.ID)
	var mu sync.Mutex
	var got CreateSessionReq
	r := &Runner{
		store:   s,
		peerMgr: stubPeers{},
		createFn: func(req CreateSessionReq) error {
			mu.Lock()
			got = req
			mu.Unlock()
			return nil
		},
		log:   logrus.NewEntry(logrus.New()),
		nowFn: func() time.Time { return now },
	}

	r.runOnce(now)

	mu.Lock()
	defer mu.Unlock()
	if got.ScheduleID != job.ID {
		t.Fatalf("schedule id = %q; want %q", got.ScheduleID, job.ID)
	}
	if got.Name != "cron-1700000000" {
		t.Fatalf("name = %q", got.Name)
	}
	updated, ok := s.Get(job.ID)
	if !ok {
		t.Fatal("job missing after run")
	}
	if updated.RunCount != 1 || updated.LastRun.IsZero() || !updated.NextRun.After(now) {
		t.Fatalf("updated = %#v", updated)
	}
}

func TestRunnerSkipsDisabledJob(t *testing.T) {
	s := newTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	job, err := s.Add(Job{CronSpec: "* * * * *", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	r := &Runner{
		store:   s,
		peerMgr: stubPeers{},
		createFn: func(req CreateSessionReq) error {
			called = true
			return nil
		},
		log: logrus.NewEntry(logrus.New()),
	}

	r.runOnce(now)
	if called {
		t.Fatal("disabled job should not fire")
	}
	updated, _ := s.Get(job.ID)
	if !updated.NextRun.Equal(job.NextRun) {
		t.Fatalf("next run changed: %#v -> %#v", job.NextRun, updated.NextRun)
	}
}

func TestRunnerSkipsOfflinePeerAndAdvancesNextRun(t *testing.T) {
	s := newTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	job, err := s.Add(Job{
		Name:        "remote",
		CronSpec:    "* * * * *",
		TargetOwner: state.OwnerID("fp-remote"),
		Enabled:     false,
	})
	if err != nil {
		t.Fatal(err)
	}
	job.Enabled = true
	job.NextRun = now.Add(-time.Minute)
	if _, err := s.Update(job); err != nil {
		t.Fatal(err)
	}
	job, _ = s.Get(job.ID)
	called := false
	r := &Runner{
		store:   s,
		peerMgr: stubPeers{localHosts: map[string]bool{}, conn: nil},
		createFn: func(req CreateSessionReq) error {
			called = true
			return nil
		},
		log: logrus.NewEntry(logrus.New()),
	}

	r.runOnce(now)
	if called {
		t.Fatal("offline peer should skip create")
	}
	updated, _ := s.Get(job.ID)
	if updated.NextRun.Before(now) || updated.NextRun.Equal(job.NextRun) {
		t.Fatalf("next run not advanced: %#v", updated.NextRun)
	}
	if updated.RunCount != 0 {
		t.Fatalf("run count changed: %d", updated.RunCount)
	}
}

func TestRunnerReconcileStartupNextRun(t *testing.T) {
	s := newTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	job, err := s.Add(Job{
		CronSpec: "*/5 * * * *",
		Enabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	job.NextRun = time.Unix(0, 0)
	if _, err := s.Update(job); err != nil {
		t.Fatal(err)
	}
	job, _ = s.Get(job.ID)
	r := &Runner{store: s, log: logrus.NewEntry(logrus.New()), nowFn: func() time.Time { return now }}
	if err := r.store.reconcileNextRuns(now); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.Get(job.ID)
	if updated.NextRun.IsZero() || !updated.NextRun.After(now) {
		t.Fatalf("next run not recomputed: %#v", updated.NextRun)
	}
}


func TestRunJobNowFiresImmediatelyWithTargetOwner(t *testing.T) {
	s := newTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	job, err := s.Add(Job{
		Name:        "remote",
		CronSpec:    "0 0 1 1 *", // far in the future; RunJobNow must bypass this
		TargetOwner: state.OwnerID("owner-remote"),
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got CreateSessionReq
	r := &Runner{
		store:   s,
		peerMgr: stubPeers{},
		createFn: func(req CreateSessionReq) error {
			mu.Lock()
			got = req
			mu.Unlock()
			return nil
		},
		log:   logrus.NewEntry(logrus.New()),
		nowFn: func() time.Time { return now },
	}

	updated, err := r.RunJobNow(job.ID)
	if err != nil {
		t.Fatalf("RunJobNow: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Same canonical request field (TargetOwner) that the cron tick path
	// uses in TestRunnerFiresDueJob/TestRunnerSkipsOfflinePeerAndAdvancesNextRun.
	if got.TargetOwner != job.TargetOwner {
		t.Fatalf("TargetOwner = %q, want %q", got.TargetOwner, job.TargetOwner)
	}
	if got.ScheduleID != job.ID {
		t.Fatalf("schedule id = %q; want %q", got.ScheduleID, job.ID)
	}
	if updated.RunCount != 1 {
		t.Fatalf("run count = %d, want 1", updated.RunCount)
	}
}

func TestRunJobNowUnknownJobReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	r := &Runner{
		store:    s,
		peerMgr:  stubPeers{},
		createFn: func(req CreateSessionReq) error { return nil },
		log:      logrus.NewEntry(logrus.New()),
	}
	if _, err := r.RunJobNow("does-not-exist"); err == nil {
		t.Fatal("expected error for unknown job id")
	}
}
