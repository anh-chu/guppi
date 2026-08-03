package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/namer"
	"github.com/anh-chu/termyard/pkg/ws"
)

// timerHandle is the subset of *time.Timer the coordinator needs so tests can
// inject fake timers.
type timerHandle interface {
	Stop() bool
}

// namingJob tracks one pending automatic naming attempt for a group.
type namingJob struct {
	wantedFp    string
	wantedKeys  []string
	currentName string
	timer       timerHandle
}

// groupNamingCoordinator watches group tree mutations and runs the AI namer
// automatically when membership crosses the threshold (>=2 members), coalesces
// bursty writes, and provides a synchronous Force path for explicit/manual
// renames.
type groupNamingCoordinator struct {
	ctx       context.Context
	opts      *Options
	hub       *ws.Hub
	gate      *namer.AutomaticGate
	debounce  time.Duration
	now       func() time.Time
	afterFunc func(time.Duration, func()) timerHandle

	mu   sync.Mutex
	jobs map[string]*namingJob
}

// newGroupNamingCoordinator builds a long-lived coordinator. It is safe for
// concurrent use and runs its own debounced background generation attempts.
func newGroupNamingCoordinator(ctx context.Context, opts *Options, hub *ws.Hub) *groupNamingCoordinator {
	return &groupNamingCoordinator{
		ctx:       ctx,
		opts:      opts,
		hub:       hub,
		gate:      namer.NewAutomaticGate(namer.DefaultAutomaticPolicy()),
		debounce:  500 * time.Millisecond,
		now:       time.Now,
		afterFunc: func(d time.Duration, f func()) timerHandle { return time.AfterFunc(d, f) },
		jobs:      make(map[string]*namingJob),
	}
}

// ObserveTreeMutation is the hook for automatic group naming. It is called
// after a group tree has been persisted. It no-ops unless the normalized
// membership set changed and the group is in auto mode.
func (c *groupNamingCoordinator) ObserveTreeMutation(id string, before, after groupsync.Group) {
	if c.opts == nil || c.opts.GroupStore == nil {
		return
	}

	fpBefore, _, beforeOK := membershipFingerprint(before.Tree)
	fpAfter, keysAfter, afterOK := membershipFingerprint(after.Tree)
	if !afterOK {
		return
	}
	if len(keysAfter) < 2 {
		return
	}
	if beforeOK && fpBefore == fpAfter {
		return
	}
	if groupsync.EffectiveNameMode(after) == groupsync.NameModeManual {
		c.Cancel(id)
		return
	}

	c.startJob(id, fpAfter, keysAfter, after.Name)
}

// Force runs group naming synchronously for id, bypassing the automatic gate
// and backoff. It resets the gate so future automatic attempts start clean.
// It switches the group's name mode back to auto, even if it was manual.
func (c *groupNamingCoordinator) Force(ctx context.Context, id string) (groupsync.Group, error) {
	if c.opts == nil || c.opts.GroupStore == nil {
		return groupsync.Group{}, fmt.Errorf("group store unavailable")
	}

	g, ok := c.opts.GroupStore.Get(id)
	if !ok {
		return groupsync.Group{}, fmt.Errorf("group %s not found", id)
	}
	if !g.DeletedAt.IsZero() {
		return groupsync.Group{}, fmt.Errorf("group %s is deleted", id)
	}

	keys, err := groupsync.MemberKeys(g.Tree)
	if err != nil {
		return groupsync.Group{}, fmt.Errorf("group %s has malformed tree: %w", id, err)
	}
	if len(keys) < 2 {
		return groupsync.Group{}, fmt.Errorf("group %s needs at least 2 members", id)
	}

	beforeFp, _, err := groupsync.MembershipFingerprint(g.Tree)
	if err != nil {
		return groupsync.Group{}, fmt.Errorf("group %s fingerprint failed: %w", id, err)
	}

	genCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	name, err := c.generate(genCtx, keys, g.Name)
	if err != nil {
		return groupsync.Group{}, err
	}

	// Re-verify the tree has not changed underneath us before applying.
	latest, ok := c.opts.GroupStore.Get(id)
	if !ok || !latest.DeletedAt.IsZero() {
		return groupsync.Group{}, fmt.Errorf("group %s disappeared during naming", id)
	}
	afterFp, _, err := groupsync.MembershipFingerprint(latest.Tree)
	if err != nil {
		return groupsync.Group{}, fmt.Errorf("group %s fingerprint failed: %w", id, err)
	}
	if afterFp != beforeFp {
		return groupsync.Group{}, fmt.Errorf("group %s membership changed during naming", id)
	}

	updated, err := c.opts.GroupStore.SetName(id, name, groupsync.NameModeAuto)
	if err != nil {
		return groupsync.Group{}, fmt.Errorf("persist group name: %w", err)
	}

	c.gate.Reset(id)
	c.broadcast(id, "ai-name")
	fanoutGroupDeltaToPeers(c.opts, id, updated)
	return updated, nil
}

// Cancel drops any pending automatic naming job for id.
func (c *groupNamingCoordinator) Cancel(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if job, ok := c.jobs[id]; ok {
		job.timer.Stop()
		delete(c.jobs, id)
	}
}

// startJob replaces any existing debounce timer for id and schedules a new one.
func (c *groupNamingCoordinator) startJob(id, fp string, keys []string, currentName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if old, ok := c.jobs[id]; ok {
		old.timer.Stop()
	}

	job := &namingJob{
		wantedFp:    fp,
		wantedKeys:  keys,
		currentName: currentName,
	}
	job.timer = c.afterFunc(c.debounce, func() {
		c.runGeneration(id, fp, keys)
	})
	c.jobs[id] = job
}

// runGeneration executes one automatic naming attempt after the debounce fires.
func (c *groupNamingCoordinator) runGeneration(id, fp string, keys []string) {
	ok, reason := c.gate.Begin(id)
	if !ok {
		logrus.WithFields(logrus.Fields{
			"group":  id,
			"reason": reason,
		}).Debug("group naming gated")
		c.scheduleRetry(id, fp, keys)
		return
	}

	currentName := ""
	if g, ok := c.opts.GroupStore.Get(id); ok && g.DeletedAt.IsZero() {
		currentName = g.Name
	}

	ctx, cancel := context.WithTimeout(c.ctx, 12*time.Second)
	defer cancel()

	name, err := c.generate(ctx, keys, currentName)
	if err != nil {
		c.gate.Failure(id)
		logrus.WithError(err).WithField("group", id).Debug("group naming generation failed")
		return
	}

	if err := c.applyGeneratedName(id, fp, name); err != nil {
		c.gate.Failure(id)
		logrus.WithError(err).WithField("group", id).Warn("group naming persist failed")
		return
	}

	c.gate.Success(id)
}

// scheduleRetry schedules a single retry when the gate becomes eligible again.
func (c *groupNamingCoordinator) scheduleRetry(id, fp string, keys []string) {
	next := c.gate.NextEligible(id)
	delay := time.Until(next)
	if delay < 0 {
		delay = 0
	}
	c.afterFunc(delay, func() {
		c.mu.Lock()
		job := c.jobs[id]
		c.mu.Unlock()
		if job != nil && job.wantedFp == fp {
			c.runGeneration(id, fp, keys)
		}
	})
}

// generate asks the configured AI namer for a label for the given members.
func (c *groupNamingCoordinator) generate(ctx context.Context, keys []string, current string) (string, error) {
	if c.opts.StateMgr == nil {
		return "", fmt.Errorf("state manager unavailable")
	}
	members := c.buildMembers(keys)
	return c.opts.StateMgr.GenerateName(ctx, namer.Context{
		Kind:    namer.KindGroup,
		Members: members,
		Current: current,
	})
}

// applyGeneratedName stores a generated name if the group still exists, is
// auto-named, and the membership fingerprint still matches the one the name
// was generated for. Stale results are discarded without counting as failures.
func (c *groupNamingCoordinator) applyGeneratedName(id, wantedFp, name string) error {
	if name == "" {
		return fmt.Errorf("empty generated name")
	}

	g, ok := c.opts.GroupStore.Get(id)
	if !ok {
		return nil // stale/disappeared; not a failure
	}
	if !g.DeletedAt.IsZero() {
		return nil // stale/deleted; not a failure
	}
	if groupsync.EffectiveNameMode(g) != groupsync.NameModeAuto {
		return nil // stale/manual; not a failure
	}
	fp, keys, err := groupsync.MembershipFingerprint(g.Tree)
	if err != nil {
		return err
	}
	if fp != wantedFp {
		return nil // stale/membership changed; not a failure
	}
	if len(keys) < 2 {
		return nil // stale/too small; not a failure
	}

	updated, err := c.opts.GroupStore.SetName(id, name, groupsync.NameModeAuto)
	if err != nil {
		return err
	}

	c.broadcast(id, "ai-name")
	fanoutGroupDeltaToPeers(c.opts, id, updated)
	return nil
}

// broadcast sends a groups-updated WebSocket event.
func (c *groupNamingCoordinator) broadcast(id, op string) {
	if c.hub == nil {
		return
	}
	c.hub.BroadcastJSON(map[string]interface{}{
		"type": "groups-updated",
		"id":   id,
		"op":   op,
	})
}

// buildMembers resolves leaf session keys to the session metadata the namer
// needs. It tolerates missing sessions by falling back to the raw key.
func (c *groupNamingCoordinator) buildMembers(keys []string) []namer.GroupMember {
	var sessions []*model.Session
	switch {
	case c.opts.PeerMgr != nil:
		sessions = c.opts.PeerMgr.GetAllSessions()
	case c.opts.StateMgr != nil:
		sessions = c.opts.StateMgr.GetSessions()
	}

	localHost := ""
	if c.opts.PeerMgr != nil {
		localHost = c.opts.PeerMgr.LocalID()
	}

	idx := make(map[string]*model.Session, len(sessions)*2)
	for _, s := range sessions {
		idx[sessionKey(s.Host, s.Name)] = s
		if localHost != "" && s.Host == "" {
			idx[sessionKey(localHost, s.Name)] = s
		}
	}

	members := make([]namer.GroupMember, 0, len(keys))
	for _, key := range keys {
		host, name := splitSessionKey(key)
		sess := idx[key]
		if sess == nil && (host == "" || host == localHost) {
			sess = idx[sessionKey("", name)]
			if sess == nil && localHost != "" {
				sess = idx[sessionKey(localHost, name)]
			}
		}

		label := key
		agent := ""
		project := ""
		prompt := ""
		if sess != nil {
			if sess.DisplayName != "" {
				label = sess.DisplayName
			} else if sess.Name != "" {
				label = sess.Name
			}
			agent = sess.AgentType
			project = sess.ProjectPath
			if sess.UserPrompt != "" {
				prompt = sess.UserPrompt
			} else {
				prompt = sess.PromptPreview
			}
		}
		members = append(members, namer.GroupMember{
			Label:   label,
			Agent:   agent,
			Project: project,
			Prompt:  prompt,
		})
	}
	return members
}

// membershipFingerprint wraps MemberKeys/ MembershipFingerprint and returns
// ok=false for malformed or empty trees.
func membershipFingerprint(tree []byte) (string, []string, bool) {
	if len(tree) == 0 {
		return "", nil, false
	}
	fp, keys, err := groupsync.MembershipFingerprint(tree)
	if err != nil {
		logrus.WithError(err).Debug("group naming: malformed tree")
		return "", nil, false
	}
	return fp, keys, true
}

// membershipFingerprintEqual reports whether two trees have the same
// membership fingerprint. Malformed/empty trees are treated as unequal.
func membershipFingerprintEqual(a, b []byte) bool {
	fpa, _, okA := membershipFingerprint(a)
	fpb, _, okB := membershipFingerprint(b)
	if !okA || !okB {
		return false
	}
	return fpa == fpb
}

// splitSessionKey separates a global session key into host and name. Bare keys
// have an empty host.
func splitSessionKey(key string) (host, name string) {
	i := strings.LastIndex(key, "/")
	if i < 0 {
		return "", key
	}
	return key[:i], key[i+1:]
}
