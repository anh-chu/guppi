package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/robfig/cron/v3"

	"github.com/anh-chu/termyard/pkg/scheduler"
	"github.com/anh-chu/termyard/pkg/sessionlaunch"
)

// registerSchedulerRoutes mounts schedule CRUD/fire endpoints under /api.
// Callers must apply auth middleware separately.
func registerSchedulerRoutes(r chi.Router, opts *Options) {
	r.Get("/schedules", func(w http.ResponseWriter, r *http.Request) {
		if opts.SchedulerStore == nil {
			http.Error(w, "scheduler not available", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(opts.SchedulerStore.List())
	})
	r.Post("/schedules", func(w http.ResponseWriter, r *http.Request) {
		if opts.SchedulerStore == nil {
			http.Error(w, "scheduler not available", http.StatusServiceUnavailable)
			return
		}
		var job scheduler.Job
		if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		created, err := opts.SchedulerStore.Add(job)
		if err != nil {
			if strings.Contains(err.Error(), "invalid cron spec") || strings.Contains(err.Error(), "cron spec is required") {
				http.Error(w, err.Error(), http.StatusBadRequest)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(created)
	})
	r.Put("/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if opts.SchedulerStore == nil {
			http.Error(w, "scheduler not available", http.StatusServiceUnavailable)
			return
		}
		id := chi.URLParam(r, "id")
		cur, ok := opts.SchedulerStore.Get(id)
		if !ok {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		var job scheduler.Job
		if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if job.ID != "" && job.ID != id {
			http.Error(w, "id mismatch", http.StatusBadRequest)
			return
		}
		job.ID = id
		job.CreatedAt = cur.CreatedAt
		job.LastRun = cur.LastRun
		job.RunCount = cur.RunCount
		if job.SessionNamePrefix == "" {
			job.SessionNamePrefix = cur.SessionNamePrefix
		}
		if job.Enabled {
			schedule, err := cron.ParseStandard(job.CronSpec)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			job.NextRun = schedule.Next(time.Now())
		} else {
			job.NextRun = time.Time{}
		}
		updated, err := opts.SchedulerStore.Update(job)
		if err != nil {
			if strings.Contains(err.Error(), "invalid cron spec") || strings.Contains(err.Error(), "cron spec is required") {
				http.Error(w, err.Error(), http.StatusBadRequest)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		// Lowering the cap prunes existing over-limit runs immediately
		// instead of waiting for the next fire. Leave exactly max alive.
		if updated.MaxConcurrency > 0 {
			EnforceScheduleCap(opts, updated.ID, updated.MaxConcurrency)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(updated)
	})
	r.Delete("/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if opts.SchedulerStore == nil {
			http.Error(w, "scheduler not available", http.StatusServiceUnavailable)
			return
		}
		if err := opts.SchedulerStore.Remove(chi.URLParam(r, "id")); err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	r.Post("/schedules/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		if opts.SchedulerStore == nil || opts.SchedulerRunner == nil {
			http.Error(w, "scheduler not available", http.StatusServiceUnavailable)
			return
		}
		id := chi.URLParam(r, "id")
		// RunJobNow goes through the same createFn the scheduler ticker uses
		// (constructed once in the runtime, wrapping launchSvc.Create); no
		// HTTP handler holds a reference to the launch service itself.
		updated, err := opts.SchedulerRunner.RunJobNow(id)
		if err != nil {
			switch {
			case strings.Contains(err.Error(), "not found"):
				http.Error(w, err.Error(), http.StatusNotFound)
			case errors.Is(err, sessionlaunch.ErrPeerUnavailable), errors.Is(err, sessionlaunch.ErrPeerQueueFull):
				http.Error(w, err.Error(), http.StatusBadGateway)
			default:
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(updated)
	})
}
