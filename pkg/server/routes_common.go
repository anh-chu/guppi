package server

import (
	"context"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/state"
)

// sessionKey builds the global session identifier used by the frontend.
// Local sessions use the bare name; remote sessions are host-qualified as
// "<host>/<name>".
func sessionKey(host, name string) string {
	if host != "" {
		return host + "/" + name
	}
	return name
}

// EnforceScheduleCap prunes the sessions owned by scheduleID until at most
// keep remain, killing oldest first (by creation time). For a pre-spawn call
// pass max-1 to leave room for the incoming run; for an update-time prune
// pass max. A negative keep is treated as unlimited and is a no-op.
//
// Enforcement queries the canonical catalog directly by ScheduleID
// (state.Catalog.SessionsByScheduleID) and kills excess sessions by their
// stable SessionRef through the ordinary canonical kill command -- never by
// display name.
func EnforceScheduleCap(opts *Options, scheduleID string, keep int) {
	if opts == nil || keep < 0 || scheduleID == "" {
		return
	}
	if opts.Catalog == nil || opts.CommandSvc == nil {
		return
	}

	tagged := opts.Catalog.SessionsByScheduleID(scheduleID)
	for len(tagged) > keep {
		victim := tagged[0]
		tagged = tagged[1:]
		_, err := opts.CommandSvc.ExecuteSessionCommand(context.Background(), state.SessionCommand{
			ID:     state.NewCommandID(),
			Ref:    victim.Ref,
			Action: state.ActionKill,
		})
		if err != nil {
			logrus.WithError(err).WithField("session", victim.Ref.MapKey()).Warn("schedule cap: kill failed")
		}
	}
}
