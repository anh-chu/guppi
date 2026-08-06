package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/state"
)

// CreateSessionReq reuses the session-spawn contract across HTTP and cron.
type CreateSessionReq struct {
	Name           string
	TargetOwner    state.OwnerID
	Path           string
	Command        string
	AgentType      string
	WorktreeBranch string
	ScheduleID     string
	CommandID      string // stable command id derived from schedule identity
}

// CreateSessionFunc spawns one session.
type CreateSessionFunc func(CreateSessionReq) error

// PeerLookup resolves a job's TargetOwner (an OwnerID, or -- for legacy-only
// nodes with no v2 catalog -- a raw peer fingerprint; see Job.TargetOwner's
// doc) to a live peer connection. ResolveHostParam already accepts either
// form (see peer.Manager.ResolveHostParam's doc); *peer.Manager satisfies
// this interface directly.
type PeerLookup interface {
	ResolveHostParam(host string) (peerID string, isLocal bool)
	GetPeerConnection(id string) *peer.PeerConnection
}

// Runner fires due jobs on a 1s tick.
type Runner struct {
	store    *Store
	peerMgr  PeerLookup
	createFn CreateSessionFunc
	capFn    func(job Job)
	log      *logrus.Entry
	nowFn    func() time.Time
	Owner    state.OwnerID // v2 owner id used to derive stable command IDs
}

// SetCapEnforcer installs an optional pre-spawn hook that prunes a schedule's
// existing sessions down to its MaxConcurrency before a new one is spawned.
func (r *Runner) SetCapEnforcer(fn func(job Job)) {
	if r == nil {
		return
	}
	r.capFn = fn
}

func NewRunner(store *Store, peerMgr PeerLookup, createFn CreateSessionFunc, log *logrus.Entry) *Runner {
	if log == nil {
		log = logrus.NewEntry(logrus.StandardLogger())
	}
	return &Runner{
		store:    store,
		peerMgr:  peerMgr,
		createFn: createFn,
		log:      log,
		nowFn:    time.Now,
	}
}

func (r *Runner) Run(ctx context.Context) {
	if r == nil || r.store == nil || r.createFn == nil {
		return
	}
	nowFn := r.nowFn
	if nowFn == nil {
		nowFn = time.Now
	}
	if err := r.store.reconcileNextRuns(nowFn()); err != nil {
		r.log.WithError(err).Warn("scheduler startup reconcile failed")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce(nowFn())
		}
	}
}

// peerOnline reports whether a job's TargetOwner currently resolves to a
// live peer connection. Local targets ('' or resolved local) are always
// online. This is a liveness check only -- the resolved peer id is never
// threaded into CreateSessionReq, which carries TargetOwner (an OwnerID);
// only peer.Manager maps OwnerID to a connection, done at launch time.
func (r *Runner) peerOnline(job Job) bool {
	if job.TargetOwner == "" || r.peerMgr == nil {
		return true
	}
	peerID, isLocal := r.peerMgr.ResolveHostParam(string(job.TargetOwner))
	if isLocal {
		return true
	}
	return peerID != "" && r.peerMgr.GetPeerConnection(peerID) != nil
}

func (r *Runner) runOnce(now time.Time) {
	for _, job := range r.store.List() {
		if !job.Enabled || job.CronSpec == "" {
			continue
		}
		if job.NextRun.After(now) {
			continue
		}
		schedule, err := cron.ParseStandard(job.CronSpec)
		if err != nil {
			r.log.WithError(err).WithField("job_id", job.ID).Warn("scheduler job disabled: invalid cron")
			if disErr := r.store.disable(job.ID); disErr != nil {
				r.log.WithError(disErr).WithField("job_id", job.ID).Warn("scheduler disable update failed")
			}
			continue
		}
		next := schedule.Next(now)
		if next.IsZero() {
			next = now.Add(time.Minute)
		}

		if !r.peerOnline(job) {
			r.log.WithField("job_id", job.ID).WithField("target_owner", job.TargetOwner).Warn("scheduler peer offline, skipping fire")
			job.NextRun = next
			if _, updErr := r.store.Update(job); updErr != nil {
				r.log.WithError(updErr).WithField("job_id", job.ID).Warn("scheduler next-run update failed")
			}
			continue
		}

		cmdID := ""
		if r.Owner != "" {
			cmdID = string(state.NewCommandIDFromSchedule(r.Owner, job.ID, now))
		}

		name := job.SessionNamePrefix
		if name == "" {
			name = job.Name
		}
		if name == "" {
			name = "schedule"
		}
		req := CreateSessionReq{
			Name:           fmt.Sprintf("%s-%d", name, now.Unix()),
			TargetOwner:    job.TargetOwner,
			Path:           job.Path,
			Command:        job.Command,
			AgentType:      job.AgentType,
			WorktreeBranch: job.WorktreeBranch,
			ScheduleID:     job.ID,
			CommandID:      cmdID,
		}
		if job.MaxConcurrency > 0 && r.capFn != nil {
			r.capFn(job)
		}
		if err := r.createFn(req); err != nil {
			r.log.WithError(err).WithField("job_id", job.ID).Warn("scheduler fire failed")
			job.NextRun = next
			if _, updErr := r.store.Update(job); updErr != nil {
				r.log.WithError(updErr).WithField("job_id", job.ID).Warn("scheduler next-run update failed")
			}
			continue
		}
		if _, err := r.store.MarkRan(job.ID, now, next); err != nil {
			r.log.WithError(err).WithField("job_id", job.ID).Warn("scheduler mark-ran failed")
		}
	}
}

// RunJobNow immediately fires the given job's session, bypassing its cron
// schedule, and records the run. This is the sole path a manual "run now"
// HTTP endpoint uses to trigger a job: it goes through the same createFn the
// ticker uses, so no HTTP handler needs direct access to the launch service.
func (r *Runner) RunJobNow(id string) (Job, error) {
	if r == nil || r.store == nil || r.createFn == nil {
		return Job{}, fmt.Errorf("scheduler not available")
	}
	job, ok := r.store.Get(id)
	if !ok {
		return Job{}, fmt.Errorf("job not found")
	}

	nowFn := r.nowFn
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	if job.MaxConcurrency > 0 && r.capFn != nil {
		r.capFn(job)
	}

	name := job.SessionNamePrefix
	if name == "" {
		name = job.Name
	}
	if name == "" {
		name = "schedule"
	}
	cmdID := ""
	if r.Owner != "" {
		cmdID = string(state.NewCommandIDFromSchedule(r.Owner, job.ID, now))
	}
	req := CreateSessionReq{
		Name:           fmt.Sprintf("%s-%d", name, now.Unix()),
		TargetOwner:    job.TargetOwner,
		Path:           job.Path,
		Command:        job.Command,
		AgentType:      job.AgentType,
		WorktreeBranch: job.WorktreeBranch,
		ScheduleID:     job.ID,
		CommandID:      cmdID,
	}
	if err := r.createFn(req); err != nil {
		return Job{}, err
	}
	return r.store.MarkRan(job.ID, now, job.NextRun)
}
