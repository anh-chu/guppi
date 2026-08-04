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
	Host           string
	Path           string
	Command        string
	AgentType      string
	WorktreeBranch string
	ScheduleID     string
	CommandID      string // stable command id derived from schedule identity
}

// CreateSessionFunc spawns one session.
type CreateSessionFunc func(CreateSessionReq) error

type PeerLookup interface {
	IsLocal(hostID string) bool
	GetPeerConnection(id string) *peer.PeerConnection
}

// Runner fires due jobs on a 1s tick.
type Runner struct {
	store    *Store
	stateMgr *state.Manager
	peerMgr  PeerLookup
	createFn CreateSessionFunc
	capFn    func(job Job)
	log      *logrus.Entry
	nowFn    func() time.Time
	Owner    string // v2 owner id used to derive stable command IDs
}

// SetCapEnforcer installs an optional pre-spawn hook that prunes a schedule's
// existing sessions down to its MaxConcurrency before a new one is spawned.
func (r *Runner) SetCapEnforcer(fn func(job Job)) {
	if r == nil {
		return
	}
	r.capFn = fn
}

func NewRunner(store *Store, stateMgr *state.Manager, peerMgr PeerLookup, createFn CreateSessionFunc, log *logrus.Entry) *Runner {
	if log == nil {
		log = logrus.NewEntry(logrus.StandardLogger())
	}
	return &Runner{
		store:    store,
		stateMgr: stateMgr,
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

		cmdID := ""
		if r.Owner != "" {
			cmdID = string(state.NewCommandIDFromSchedule(state.OwnerID(r.Owner), job.ID, now))
		}

		if job.Host != "" && r.peerMgr != nil && !r.peerMgr.IsLocal(job.Host) {
			if r.peerMgr.GetPeerConnection(job.Host) == nil {
				r.log.WithField("job_id", job.ID).WithField("host", job.Host).Warn("scheduler peer offline, skipping fire")
				job.NextRun = next
				if _, updErr := r.store.Update(job); updErr != nil {
					r.log.WithError(updErr).WithField("job_id", job.ID).Warn("scheduler next-run update failed")
				}
				continue
			}
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
			Host:           job.Host,
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
