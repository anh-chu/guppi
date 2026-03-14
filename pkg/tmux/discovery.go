package tmux

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
)

// Discovery periodically polls for tmux sessions and updates the state manager
type Discovery struct {
	client   *Client
	onChange func([]*Session)
	interval time.Duration
	log      *logrus.Entry
	resetCh  chan time.Duration
}

// NewDiscovery creates a new session discovery service
func NewDiscovery(client *Client, interval time.Duration, onChange func([]*Session)) *Discovery {
	return &Discovery{
		client:   client,
		onChange: onChange,
		interval: interval,
		log:      logrus.WithField("component", "discovery"),
		resetCh:  make(chan time.Duration, 1),
	}
}

// SetInterval changes the polling interval at runtime
func (d *Discovery) SetInterval(interval time.Duration) {
	select {
	case d.resetCh <- interval:
	default:
	}
}

// Run starts the discovery loop. It blocks until ctx is cancelled.
func (d *Discovery) Run(ctx context.Context) {
	d.log.WithField("interval", d.interval).Info("starting session discovery")

	// Do an immediate discovery on start
	d.discover()

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.log.Info("stopping session discovery")
			return
		case <-ticker.C:
			d.discover()
		case newInterval := <-d.resetCh:
			d.log.WithField("interval", newInterval).Info("discovery interval changed")
			d.interval = newInterval
			ticker.Reset(newInterval)
		}
	}
}

// discover performs a single discovery pass
func (d *Discovery) discover() {
	sessions, err := d.client.ListSessions()
	if err != nil {
		d.log.WithError(err).Warn("failed to list sessions")
		return
	}

	// Filter out the control mode session
	filtered := make([]*Session, 0, len(sessions))
	for _, s := range sessions {
		if s.Name != ControlSessionName() {
			filtered = append(filtered, s)
		}
	}

	if d.onChange != nil {
		d.onChange(filtered)
	}
}
