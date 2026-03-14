package toolevents

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Detector periodically scans tmux panes for known agent processes by
// inspecting the process tree. When an agent is detected in a pane that
// doesn't already have a tracked event, a synthetic "active" event is
// recorded. This provides passive agent detection for tools that may not
// have hooks configured (or whose hooks haven't fired yet).
type Detector struct {
	tracker  *Tracker
	listPane PaneListFunc
	interval time.Duration
	log      *logrus.Entry
	hostID   string // local host fingerprint (for multi-host)
	hostName string // local host display name

	// seen tracks panes where an agent was previously detected, so we
	// don't re-broadcast every scan cycle. Entries are removed when the
	// agent process is no longer found.
	mu   sync.Mutex
	seen map[PaneKey]Tool
}

// NewDetector creates a new agent detector.
func NewDetector(tracker *Tracker, listPane PaneListFunc, interval time.Duration) *Detector {
	return &Detector{
		tracker:  tracker,
		listPane: listPane,
		interval: interval,
		log:      logrus.WithField("component", "agent-detector"),
		seen:     make(map[PaneKey]Tool),
	}
}

// SetHost sets the local host identity for multi-host event stamping.
func (d *Detector) SetHost(id, name string) {
	d.hostID = id
	d.hostName = name
}

// DetectedPanes returns the set of pane IDs where an agent was detected
// via process tree inspection.
func (d *Detector) DetectedPanes() map[string]Tool {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := make(map[string]Tool, len(d.seen))
	for key, tool := range d.seen {
		if key.Pane != "" {
			result[key.Pane] = tool
		}
	}
	return result
}

// PaneInfo returns the session/window context for a detected pane.
func (d *Detector) PaneInfo(paneID string) PaneInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	for key := range d.seen {
		if key.Pane == paneID {
			return PaneInfo{
				PaneID:  key.Pane,
				Session: key.Session,
				Window:  key.Window,
			}
		}
	}
	return PaneInfo{}
}

// Run starts the detection loop. It blocks until ctx is cancelled.
func (d *Detector) Run(ctx context.Context) {
	d.log.WithField("interval", d.interval).Info("starting agent detector")

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.log.Info("stopping agent detector")
			return
		case <-ticker.C:
			d.detect()
		}
	}
}

// detect scans all panes and looks for agent processes.
func (d *Detector) detect() {
	panes := d.listPane()
	if len(panes) == 0 {
		return
	}

	// Get currently tracked events (hook-based) to avoid interfering
	tracked := make(map[PaneKey]bool)
	for _, evt := range d.tracker.GetAll() {
		tracked[PaneKey{
			Host:    evt.Host,
			Session: evt.Session,
			Window:  evt.Window,
			Pane:    evt.Pane,
		}] = true
	}
	d.tracker.mu.RLock()
	for key := range d.tracker.lastActive {
		tracked[key] = true
	}
	d.tracker.mu.RUnlock()

	// Track which panes still have agents this cycle
	stillPresent := make(map[PaneKey]bool)

	for _, pane := range panes {
		if pane.PID == 0 || pane.Session == "" {
			continue
		}

		key := PaneKey{
			Session: pane.Session,
			Window:  pane.Window,
			Pane:    pane.PaneID,
		}

		// Skip panes with hook-based tracking
		if tracked[key] {
			continue
		}

		tool, found := DetectAgentInProcessTree(pane.PID)
		if !found {
			continue
		}

		stillPresent[key] = true

		d.mu.Lock()
		_, alreadySeen := d.seen[key]
		d.seen[key] = tool
		d.mu.Unlock()

		if alreadySeen {
			continue
		}

		d.log.WithFields(logrus.Fields{
			"tool":    tool,
			"session": pane.Session,
			"window":  pane.Window,
			"pane":    pane.PaneID,
			"pid":     pane.PID,
		}).Debug("detected agent via process tree")

		d.tracker.Record(&Event{
			Tool:         tool,
			Status:       StatusActive,
			Host:         d.hostID,
			HostName:     d.hostName,
			Session:      pane.Session,
			Window:       pane.Window,
			Pane:         pane.PaneID,
			Message:      "auto-detected",
			AutoDetected: true,
		})
	}

	// Clean up panes where the agent is no longer detected
	d.mu.Lock()
	for key := range d.seen {
		if !stillPresent[key] {
			d.log.WithFields(logrus.Fields{
				"session": key.Session,
				"window":  key.Window,
				"pane":    key.Pane,
			}).Debug("agent no longer detected in pane")
			delete(d.seen, key)
		}
	}
	d.mu.Unlock()
}
