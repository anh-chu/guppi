package state

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	WorkspaceActionSplit          = "split"
	WorkspaceActionMove           = "move"
	WorkspaceActionSwap           = "swap"
	WorkspaceActionPopOut         = "pop_out"
	WorkspaceActionRemove         = "remove"
	WorkspaceActionResize         = "resize"
	WorkspaceActionSelect         = "select"
)

// WorkspaceSnapshotResult is the read-only view returned by WorkspaceSnapshot.
type WorkspaceSnapshotResult struct {
	Record WorkspaceRecord
}

// workspaceBaseParams carries the optimistic concurrency check used by most
// workspace tree commands.
type workspaceBaseParams struct {
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
}

type workspaceSplitParams struct {
	workspaceBaseParams
	Target    SessionRef     `json:"target"`
	Direction SplitDirection `json:"direction"`
	New       SessionRef     `json:"new"`
	NewFirst  bool           `json:"new_first,omitempty"`
}

type workspaceMoveParams struct {
	workspaceBaseParams
	Source SessionRef `json:"source"`
	Target SessionRef `json:"target"`
	Edge   string     `json:"edge"`
}

type workspaceSwapParams struct {
	workspaceBaseParams
	A SessionRef `json:"a"`
	B SessionRef `json:"b"`
}

type workspacePopOutParams struct {
	workspaceBaseParams
	Ref SessionRef `json:"ref"`
}

type workspaceRemoveParams struct {
	workspaceBaseParams
	Ref SessionRef `json:"ref"`
}

type workspaceResizeParams struct {
	workspaceBaseParams
	SplitID SplitID `json:"split_id"`
	Ratio   float64 `json:"ratio"`
}

type workspaceSelectParams struct {
	workspaceBaseParams
	Ref SessionRef `json:"ref"`
}

// ApplyWorkspaceCommand applies one atomic workspace command. Accepted commands
// append a bounded receipt and increment the catalog revision exactly once.
// Invalid commands return typed StateError values and leave the document
// unchanged.
func (c *Catalog) ApplyWorkspaceCommand(cmd WorkspaceCommand) error {
	if err := cmd.ID.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "id", Detail: err.Error()}
	}

	err := c.apply("workspace/"+cmd.Action, func(doc *AppDocument) error {
		return applyLayoutTreeCommand(doc, c, cmd)
	})
	if err == nil {
		c.publishWorkspace(cmd.Layout)
	}
	return err
}

// ApplyWorkspaceCommandFromPeer applies a workspace command that arrived over
// an authenticated peer connection. peerID must be the connection's
// authenticated identity (empty means "trusted local caller", which is left
// to ApplyWorkspaceCommand's existing, unchanged behavior). Because this
// catalog always mutates its own local layouts, every SessionRef embedded in
// a peer-originated command's params (split target/new, move source/target,
// swap a/b, pop_out/remove/select ref, rename old/new) must be owned by this
// node; a forged or foreign Owner is rejected BEFORE any mutation. Refs that
// are expected to already exist in the tree (target/source/old/ref, but not
// the brand-new refs introduced by split/rename) are additionally required to
// resolve to a real leaf in the local trusted layout, not just be nonempty;
// that existence check already happens inside ApplyWorkspaceCommand via
// findLeaf, so this method only adds the ownership check ahead of it. Local
// (non-peer) callers must keep calling ApplyWorkspaceCommand directly so this
// check is never applied to already-trusted local paths.
func (c *Catalog) ApplyWorkspaceCommandFromPeer(cmd WorkspaceCommand, peerID string) error {
	if peerID != "" {
		if err := c.validatePeerWorkspaceRefOwnership(cmd); err != nil {
			return err
		}
	}
	return c.ApplyWorkspaceCommand(cmd)
}

// validatePeerWorkspaceRefOwnership checks that every SessionRef embedded in
// cmd's params is owned by this catalog before any mutation is attempted. It
// intentionally does not duplicate the existence check that
// applyLayoutTreeCommand already performs via findLeaf.
func (c *Catalog) validatePeerWorkspaceRefOwnership(cmd WorkspaceCommand) error {
	owner := c.Owner()
	checkRef := func(field string, ref SessionRef) error {
		if ref.Session == "" {
			return nil
		}
		if ref.Owner == "" || ref.Owner != owner {
			return StateError{
				Code:   ErrOwnershipMismatch,
				Field:  field,
				Detail: fmt.Sprintf("ref owner %q does not match this node's owner %q", ref.Owner, owner),
			}
		}
		return nil
	}
	switch cmd.Action {
	case WorkspaceActionSplit:
		var p workspaceSplitParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		if err := checkRef("target", p.Target); err != nil {
			return err
		}
		return checkRef("new", p.New)
	case WorkspaceActionMove:
		var p workspaceMoveParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		if err := checkRef("source", p.Source); err != nil {
			return err
		}
		return checkRef("target", p.Target)
	case WorkspaceActionSwap:
		var p workspaceSwapParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		if err := checkRef("a", p.A); err != nil {
			return err
		}
		return checkRef("b", p.B)
	case WorkspaceActionPopOut:
		var p workspacePopOutParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		return checkRef("ref", p.Ref)
	case WorkspaceActionRemove:
		var p workspaceRemoveParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		return checkRef("ref", p.Ref)
	case WorkspaceActionSelect:
		var p workspaceSelectParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		return checkRef("ref", p.Ref)
	default:
		// Reorder/present/etc. carry no SessionRef to check here.
		return nil
	}
}

// WorkspaceSnapshot returns the active workspace view for a layout. If the
// document has no active WorkspaceRecord for that layout, the snapshot is
// synthesized from the persisted LayoutRecord.
func (c *Catalog) WorkspaceSnapshot(layout LayoutID) (WorkspaceSnapshotResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	rec, ok := c.layouts[layout]
	if !ok {
		return WorkspaceSnapshotResult{}, StateError{Code: ErrUnknownLayout, Field: "layout", Detail: fmt.Sprintf("layout %q not found", layout)}
	}

	ws := WorkspaceRecord{
		ID:       layout,
		Owner:    c.owner,
		Revision: rec.Revision,
		Tree:     clonePaneNode(rec.Tree),
	}
	if active, ok := c.activeKeys[layout]; ok {
		ws.ActiveKey = active
	}

	return WorkspaceSnapshotResult{Record: ws}, nil
}

// RemoveSessionRef removes a session from every layout in the same atomic
// transaction. Layouts that become empty are deleted. This is the internal
// cleanup command used when a session is permanently removed.
func (c *Catalog) RemoveSessionRef(ref SessionRef) error {
	return c.apply("workspace/remove-session-ref", func(doc *AppDocument) error {
		return removeSessionFromWorkspacesLocked(doc, ref)
	})
}

// applyLayoutTreeCommand handles commands that mutate one layout's pane tree.
func applyLayoutTreeCommand(doc *AppDocument, c *Catalog, cmd WorkspaceCommand) error {
	// Idempotent replay FIRST, before any lookup or mutation: a command ID
	// that was already durably accepted is a no-op that returns success
	// again, never a re-derived mutation. Without this check, retrying e.g.
	// a "split" whose reply was lost re-inserts the same leaf and is
	// rejected as a duplicate-leaf error instead of returning the original
	// successful result.
	if _, ok := findCommandReceipt(doc, cmd.ID); ok {
		return nil
	}

	layout := cmd.Layout
	idx := -1
	for i := range doc.Layouts {
		if doc.Layouts[i].ID == layout {
			idx = i
			break
		}
	}
	if idx == -1 {
		return StateError{Code: ErrUnknownLayout, Field: "layout", Detail: fmt.Sprintf("layout %q not found", layout)}
	}

	rec := &doc.Layouts[idx]

	nextRev := doc.Revision + 1
	membership := MembershipIndex(doc)

	switch cmd.Action {
	case WorkspaceActionSplit:
		var p workspaceSplitParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		if err := checkExpectedRevision(rec.Revision, p.ExpectedRevision); err != nil {
			return err
		}
		if !findLeaf(rec.Tree, p.Target) {
			return StateError{Code: ErrMissingTarget, Field: "target", Detail: fmt.Sprintf("target leaf %q not in layout", p.Target.MapKey())}
		}
		if p.New.Session == "" {
			return StateError{Code: ErrInvalidIdentity, Field: "new", Detail: "new session ref has empty session id"}
		}
		if key := p.New.MapKey(); conflictsMembership(membership, key, layout) {
			return StateError{Code: ErrDuplicateMembership, Field: "new", Detail: fmt.Sprintf("session %q already belongs to another layout", key)}
		}
		tree, err := splitTree(rec.Tree, p.Target, p.Direction, p.New, p.NewFirst)
		if err != nil {
			return err
		}
		rec.Tree = tree
		rec.Revision = nextRev

	case WorkspaceActionMove:
		var p workspaceMoveParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		if err := checkExpectedRevision(rec.Revision, p.ExpectedRevision); err != nil {
			return err
		}
		if !findLeaf(rec.Tree, p.Source) {
			return StateError{Code: ErrMissingTarget, Field: "source", Detail: fmt.Sprintf("source leaf %q not in layout", p.Source.MapKey())}
		}
		if !findLeaf(rec.Tree, p.Target) {
			return StateError{Code: ErrMissingTarget, Field: "target", Detail: fmt.Sprintf("target leaf %q not in layout", p.Target.MapKey())}
		}
		if p.Source.MapKey() == p.Target.MapKey() {
			return StateError{Code: ErrMalformedSplit, Field: "target", Detail: "source and target are the same leaf"}
		}
		tree, err := moveTree(rec.Tree, p.Source, p.Target, p.Edge)
		if err != nil {
			return err
		}
		rec.Tree = tree
		rec.Revision = nextRev

	case WorkspaceActionSwap:
		var p workspaceSwapParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		if err := checkExpectedRevision(rec.Revision, p.ExpectedRevision); err != nil {
			return err
		}
		if !findLeaf(rec.Tree, p.A) {
			return StateError{Code: ErrMissingTarget, Field: "a", Detail: fmt.Sprintf("leaf %q not in layout", p.A.MapKey())}
		}
		if !findLeaf(rec.Tree, p.B) {
			return StateError{Code: ErrMissingTarget, Field: "b", Detail: fmt.Sprintf("leaf %q not in layout", p.B.MapKey())}
		}
		rec.Tree = swapTree(rec.Tree, p.A, p.B)
		rec.Revision = nextRev

	case WorkspaceActionPopOut:
		var p workspacePopOutParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		if err := checkExpectedRevision(rec.Revision, p.ExpectedRevision); err != nil {
			return err
		}
		if !findLeaf(rec.Tree, p.Ref) {
			return StateError{Code: ErrMissingTarget, Field: "ref", Detail: fmt.Sprintf("leaf %q not in layout", p.Ref.MapKey())}
		}
		rec.Tree = Leaf(p.Ref)
		rec.Revision = nextRev

	case WorkspaceActionRemove:
		var p workspaceRemoveParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		if err := checkExpectedRevision(rec.Revision, p.ExpectedRevision); err != nil {
			return err
		}
		if !findLeaf(rec.Tree, p.Ref) {
			return StateError{Code: ErrMissingTarget, Field: "ref", Detail: fmt.Sprintf("leaf %q not in layout", p.Ref.MapKey())}
		}
		tree, _, err := removeTree(rec.Tree, p.Ref)
		if err != nil {
			return err
		}
		if tree.Type == "" {
			doc.Layouts = append(doc.Layouts[:idx], doc.Layouts[idx+1:]...)
			if c != nil {
				c.deleteActiveKeyLocked(layout)
			}
		} else {
			rec.Tree = tree
			rec.Revision = nextRev
		}

	case WorkspaceActionResize:
		var p workspaceResizeParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		if err := checkExpectedRevision(rec.Revision, p.ExpectedRevision); err != nil {
			return err
		}
		if err := Ratio(p.Ratio).Validate(); err != nil {
			return StateError{Code: ErrInvalidRatio, Field: "ratio", Detail: err.Error()}
		}
		tree, ok := resizeTree(rec.Tree, p.SplitID, Ratio(p.Ratio))
		if !ok {
			return StateError{Code: ErrStaleSplitID, Field: "split_id", Detail: fmt.Sprintf("split id %q not found", p.SplitID)}
		}
		rec.Tree = tree
		rec.Revision = nextRev

	case WorkspaceActionSelect:
		var p workspaceSelectParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
		if err := checkExpectedRevision(rec.Revision, p.ExpectedRevision); err != nil {
			return err
		}
		if !findLeaf(rec.Tree, p.Ref) {
			return StateError{Code: ErrMissingTarget, Field: "ref", Detail: fmt.Sprintf("leaf %q not in layout", p.Ref.MapKey())}
		}
		// Active key is stored on an in-memory workspace view so it does not
		// require a persisted layout mutation.
		if c != nil {
			c.setActiveKeyLocked(layout, p.Ref)
		}
		rec.Revision = nextRev

	default:
		return StateError{Code: ErrMalformedSplit, Field: "action", Detail: fmt.Sprintf("unknown workspace action %q", cmd.Action)}
	}

	if err := appendCommandReceipt(doc, cmd.ID, doc.Revision+1, "workspace:"+cmd.Action, string(layout)); err != nil {
		return err
	}
	return nil
}

// removeSessionFromWorkspacesLocked removes ref from the sole layout's pane
// tree (a no-op if it is not present). The layout itself is dropped from the
// document when removing the leaf leaves it empty.
func removeSessionFromWorkspacesLocked(doc *AppDocument, ref SessionRef) error {
	newLayouts := doc.Layouts[:0]
	for i := range doc.Layouts {
		if !findLeaf(doc.Layouts[i].Tree, ref) {
			newLayouts = append(newLayouts, doc.Layouts[i])
			continue
		}
		tree, _, err := removeTree(doc.Layouts[i].Tree, ref)
		if err != nil {
			return err
		}
		if tree.Type == "" {
			continue
		}
		rec := doc.Layouts[i]
		rec.Tree = tree
		rec.Revision = doc.Revision + 1
		newLayouts = append(newLayouts, rec)
	}
	doc.Layouts = newLayouts
	return nil
}

func checkExpectedRevision(current int64, expected *int64) error {
	if expected == nil {
		return nil
	}
	if current != *expected {
		return StateError{Code: ErrRevisionConflict, Field: "expected_revision", Detail: fmt.Sprintf("expected %d, got %d", *expected, current)}
	}
	return nil
}

func conflictsMembership(membership map[string]LayoutID, key string, layout LayoutID) bool {
	owner, exists := membership[key]
	// A leaf that already belongs to the target layout is a duplicate leaf,
	// not a cross-layout membership conflict; it is caught by ValidatePaneTree.
	if !exists {
		return false
	}
	return owner != layout
}

func appendCommandReceipt(doc *AppDocument, id CommandID, seq int64, kind, target string) error {
	if err := id.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "id", Detail: err.Error()}
	}
	if seq < 1 {
		seq = 1
		for _, r := range doc.Commands {
			if r.Seq >= seq {
				seq = r.Seq + 1
			}
		}
	}
	receipt, err := newSuccessReceipt(id, kind, target, seq, time.Now(), struct {
		Accepted bool `json:"accepted"`
	}{true})
	if err != nil {
		return err
	}
	doc.Commands = append(doc.Commands, receipt)
	pruneReceipts(doc, MaxCommandReceiptAge, MaxPendingCommands)
	return nil
}

func (c *Catalog) setActiveKeyLocked(layout LayoutID, ref SessionRef) {
	if c.activeKeys == nil {
		c.activeKeys = make(map[LayoutID]*SessionRef)
	}
	cp := ref
	c.activeKeys[layout] = &cp
}

func (c *Catalog) deleteActiveKeyLocked(layout LayoutID) {
	if c.activeKeys == nil {
		return
	}
	delete(c.activeKeys, layout)
}

type workspaceSubscription struct {
	id int
	fn func(layout LayoutID, rec WorkspaceRecord)
}

// WorkspaceSubscriberCount returns the number of live workspace
// subscriptions. Used to detect leaked subscriptions (e.g. a subscribe call
// that is never paired with its unsubscribe on an early-return error path).
func (c *Catalog) WorkspaceSubscriberCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.workspaceSubs)
}

// SubscribeWorkspace registers a callback that receives the complete
// WorkspaceRecord for a layout after every accepted command. The returned
// function unsubscribes. Subscriptions prevent stale remote caches by
// emitting whole snapshots, not incremental steps.
func (c *Catalog) SubscribeWorkspace(fn func(layout LayoutID, rec WorkspaceRecord)) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextWorkspaceSubID++
	sub := workspaceSubscription{id: c.nextWorkspaceSubID, fn: fn}
	c.workspaceSubs = append(c.workspaceSubs, sub)
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		filtered := c.workspaceSubs[:0]
		for _, s := range c.workspaceSubs {
			if s.id != sub.id {
				filtered = append(filtered, s)
			}
		}
		c.workspaceSubs = filtered
	}
}

// publishWorkspace emits the current workspace record for layout to all
// subscribers. It drops the event silently when no such layout exists.
func (c *Catalog) publishWorkspace(layout LayoutID) {
	c.mu.RLock()
	rec, ok := c.workspaceRecordLocked(layout)
	subs := make([]workspaceSubscription, len(c.workspaceSubs))
	copy(subs, c.workspaceSubs)
	c.mu.RUnlock()
	if !ok {
		return
	}
	for _, s := range subs {
		s.fn(layout, rec)
	}
}

func (c *Catalog) workspaceRecordLocked(layout LayoutID) (WorkspaceRecord, bool) {
	rec, ok := c.layouts[layout]
	if !ok {
		return WorkspaceRecord{}, false
	}
	ws := WorkspaceRecord{
		ID:       layout,
		Owner:    c.owner,
		Revision: rec.Revision,
		Tree:     clonePaneNode(rec.Tree),
	}
	if active, ok := c.activeKeys[layout]; ok {
		cp := *active
		ws.ActiveKey = &cp
	}
	return ws, true
}

// clonePaneNode returns a deep copy of a pane tree.
func clonePaneNode(n PaneNode) PaneNode {
	if n.IsLeaf() {
		if n.Ref == nil {
			return n
		}
		cp := *n.Ref
		return Leaf(cp)
	}
	if n.First == nil || n.Second == nil {
		return PaneNode{Type: n.Type, ID: n.ID, Direction: n.Direction, Ratio: n.Ratio}
	}
	cpFirst := clonePaneNode(*n.First)
	cpSecond := clonePaneNode(*n.Second)
	return PaneNode{
		Type:      n.Type,
		ID:        n.ID,
		Direction: n.Direction,
		Ratio:     n.Ratio,
		First:     &cpFirst,
		Second:    &cpSecond,
	}
}

func findLeaf(tree PaneNode, ref SessionRef) bool {
	if tree.IsLeaf() {
		return tree.Ref != nil && tree.Ref.MapKey() == ref.MapKey()
	}
	if tree.First != nil && findLeaf(*tree.First, ref) {
		return true
	}
	if tree.Second != nil && findLeaf(*tree.Second, ref) {
		return true
	}
	return false
}

func splitTree(tree PaneNode, target SessionRef, dir SplitDirection, newRef SessionRef, newFirst bool) (PaneNode, error) {
	if tree.IsLeaf() {
		if tree.Ref.MapKey() == target.MapKey() {
			left := tree
			right := Leaf(newRef)
			if newFirst {
				left, right = right, left
			}
			return PaneNode{
				Type:      "split",
				ID:        NewSplitID(),
				Direction: dir,
				Ratio:     0.5,
				First:     &left,
				Second:    &right,
			}, nil
		}
		return tree, nil
	}
	first, err := splitTree(*tree.First, target, dir, newRef, newFirst)
	if err != nil {
		return PaneNode{}, err
	}
	second, err := splitTree(*tree.Second, target, dir, newRef, newFirst)
	if err != nil {
		return PaneNode{}, err
	}
	tree.First = &first
	tree.Second = &second
	return tree, nil
}

func insertBesideTree(tree PaneNode, target SessionRef, dir SplitDirection, newRef SessionRef, newFirst bool) (PaneNode, error) {
	if tree.IsLeaf() {
		if tree.Ref.MapKey() == target.MapKey() {
			left := tree
			right := Leaf(newRef)
			if newFirst {
				left, right = right, left
			}
			return PaneNode{
				Type:      "split",
				ID:        NewSplitID(),
				Direction: dir,
				Ratio:     0.5,
				First:     &left,
				Second:    &right,
			}, nil
		}
		return tree, nil
	}
	first, err := insertBesideTree(*tree.First, target, dir, newRef, newFirst)
	if err != nil {
		return PaneNode{}, err
	}
	second, err := insertBesideTree(*tree.Second, target, dir, newRef, newFirst)
	if err != nil {
		return PaneNode{}, err
	}
	tree.First = &first
	tree.Second = &second
	return tree, nil
}

func removeTree(tree PaneNode, ref SessionRef) (PaneNode, bool, error) {
	if tree.IsLeaf() {
		if tree.Ref != nil && tree.Ref.MapKey() == ref.MapKey() {
			return PaneNode{}, true, nil
		}
		return tree, false, nil
	}
	var removedFirst bool
	var removedSecond bool
	var newFirst PaneNode
	var newSecond PaneNode
	if tree.First != nil {
		var err error
		newFirst, removedFirst, err = removeTree(*tree.First, ref)
		if err != nil {
			return PaneNode{}, false, err
		}
	}
	if tree.Second != nil {
		var err error
		newSecond, removedSecond, err = removeTree(*tree.Second, ref)
		if err != nil {
			return PaneNode{}, false, err
		}
	}
	if !removedFirst && !removedSecond {
		return tree, false, nil
	}
	// Normalize zero/one-child splits.
	if newFirst.Type == "" && newSecond.Type == "" {
		return PaneNode{}, true, nil
	}
	if newFirst.Type == "" {
		return newSecond, true, nil
	}
	if newSecond.Type == "" {
		return newFirst, true, nil
	}
	tree.First = &newFirst
	tree.Second = &newSecond
	return tree, true, nil
}

func swapTree(tree PaneNode, a, b SessionRef) PaneNode {
	if tree.IsLeaf() {
		if tree.Ref == nil {
			return tree
		}
		key := tree.Ref.MapKey()
		if key == a.MapKey() {
			cp := b
			return Leaf(cp)
		}
		if key == b.MapKey() {
			cp := a
			return Leaf(cp)
		}
		return tree
	}
	if tree.First != nil {
		first := swapTree(*tree.First, a, b)
		tree.First = &first
	}
	if tree.Second != nil {
		second := swapTree(*tree.Second, a, b)
		tree.Second = &second
	}
	return tree
}

func moveTree(tree PaneNode, source, target SessionRef, edge string) (PaneNode, error) {
	var dir SplitDirection
	var newFirst bool
	switch edge {
	case "left":
		dir = DirectionHorizontal
		newFirst = true
	case "right":
		dir = DirectionHorizontal
		newFirst = false
	case "top":
		dir = DirectionVertical
		newFirst = true
	case "bottom":
		dir = DirectionVertical
		newFirst = false
	default:
		return PaneNode{}, StateError{Code: ErrMalformedSplit, Field: "edge", Detail: fmt.Sprintf("invalid move edge %q", edge)}
	}

	pruned, removed, err := removeTree(tree, source)
	if err != nil {
		return PaneNode{}, err
	}
	if !removed {
		return PaneNode{}, StateError{Code: ErrMissingTarget, Field: "source", Detail: fmt.Sprintf("source leaf %q not in layout", source.MapKey())}
	}

	// If removing the source collapsed the whole tree, the move is a no-op
	// that leaves the source as the only leaf.
	if pruned.Type == "" {
		return Leaf(source), nil
	}

	return insertBesideTree(pruned, target, dir, source, newFirst)
}

func resizeTree(tree PaneNode, id SplitID, ratio Ratio) (PaneNode, bool) {
	if tree.IsLeaf() {
		return tree, false
	}
	if tree.ID == id {
		tree.Ratio = ratio
		return tree, true
	}
	if tree.First != nil {
		first, ok := resizeTree(*tree.First, id, ratio)
		if ok {
			tree.First = &first
			return tree, true
		}
	}
	if tree.Second != nil {
		second, ok := resizeTree(*tree.Second, id, ratio)
		if ok {
			tree.Second = &second
			return tree, true
		}
	}
	return tree, false
}

// MembershipIndex returns the derived membership map from session ref key to
// owning layout ID.
func MembershipIndex(doc *AppDocument) map[string]LayoutID {
	m := make(map[string]LayoutID)
	for _, l := range doc.Layouts {
		forEachLeaf(l.Tree, func(ref SessionRef) {
			m[ref.MapKey()] = l.ID
		})
	}
	return m
}

func forEachLeaf(tree PaneNode, fn func(SessionRef)) {
	if tree.IsLeaf() {
		if tree.Ref != nil {
			fn(*tree.Ref)
		}
		return
	}
	if tree.First != nil {
		forEachLeaf(*tree.First, fn)
	}
	if tree.Second != nil {
		forEachLeaf(*tree.Second, fn)
	}
}
