package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

func mustNewWorkspaceCatalog(t *testing.T) (*Catalog, OwnerID, func()) {
	t.Helper()
	dir := t.TempDir()
	owner := OwnerID("ownerwork1234567890abcd")
	store, err := OpenStore(dir, "testnode", StoreOptions{Owner: owner})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cat := NewCatalog(owner, store)
	if err := cat.Load(); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return cat, owner, func() {}
}

func ref(owner OwnerID, name string) SessionRef {
	return SessionRef{Owner: owner, Session: SessionID(name)}
}

func leafs(tree PaneNode) []string {
	var out []string
	forEachLeaf(tree, func(r SessionRef) { out = append(out, r.MapKey()) })
	return out
}

func mustPutLayout(t *testing.T, c *Catalog, owner OwnerID, id string, order int64, tree PaneNode) LayoutID {
	t.Helper()
	lid := LayoutID(id)
	rec := LayoutRecord{ID: lid, Owner: owner, Order: order, Tree: tree, Revision: 0}
	if err := c.PutLayout(rec); err != nil {
		t.Fatalf("put layout: %v", err)
	}
	return lid
}

func workspaceParams(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func assertCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", want)
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != want {
		t.Fatalf("expected code %s, got %v (%T)", want, err, err)
	}
}

func TestWorkspaceSplitOnePane(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1, Leaf(ref(owner, "s1")))

	cmd := WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionSplit,
		Params: workspaceParams(map[string]interface{}{
			"target":    ref(owner, "s1"),
			"direction": DirectionHorizontal,
			"new":       ref(owner, "s2"),
		}),
	}
	before := c.Revision()
	if err := c.ApplyWorkspaceCommand(cmd); err != nil {
		t.Fatalf("split: %v", err)
	}
	if got := c.Revision(); got != before+1 {
		t.Fatalf("revision %d, want %d", got, before+1)
	}

	snap, err := c.WorkspaceSnapshot(lID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	leaves := leafs(snap.Record.Tree)
	if len(leaves) != 2 || leaves[0] != ref(owner, "s1").MapKey() || leaves[1] != ref(owner, "s2").MapKey() {
		t.Fatalf("unexpected leaves: %v", leaves)
	}

	// Receipt appended.
	doc := c.store.Snapshot()
	if len(doc.Commands) != 1 || doc.Commands[0].ID != cmd.ID {
		t.Fatalf("expected one receipt with command id, got %+v", doc.Commands)
	}
}

func TestWorkspaceMissingTarget(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()
	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1, Leaf(ref(owner, "s1")))

	cmd := WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionSplit,
		Params: workspaceParams(map[string]interface{}{
			"target":    ref(owner, "missing"),
			"direction": DirectionHorizontal,
			"new":       ref(owner, "s2"),
		}),
	}
	before := c.Revision()
	err := c.ApplyWorkspaceCommand(cmd)
	assertCode(t, err, ErrMissingTarget)
	if c.Revision() != before {
		t.Fatal("revision changed on invalid command")
	}
}

func TestWorkspaceDuplicateMembership(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()
	l1 := mustPutLayout(t, c, owner, "layout1234567890abcd", 1, Leaf(ref(owner, "s1")))
	mustPutLayout(t, c, owner, "layout2234567890abcd", 2, Leaf(ref(owner, "s2")))

	cmd := WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: l1,
		Action: WorkspaceActionSplit,
		Params: workspaceParams(map[string]interface{}{
			"target":    ref(owner, "s1"),
			"direction": DirectionHorizontal,
			"new":       ref(owner, "s2"),
		}),
	}
	err := c.ApplyWorkspaceCommand(cmd)
	assertCode(t, err, ErrDuplicateMembership)
}

func TestWorkspaceRevisionConflict(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()
	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1, Leaf(ref(owner, "s1")))

	badRev := int64(99)
	cmd := WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionSplit,
		Params: workspaceParams(map[string]interface{}{
			"expected_revision": &badRev,
			"target":            ref(owner, "s1"),
			"direction":         DirectionHorizontal,
			"new":               ref(owner, "s2"),
		}),
	}
	err := c.ApplyWorkspaceCommand(cmd)
	assertCode(t, err, ErrRevisionConflict)
}

func TestWorkspaceMoveAndSwap(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	// Build a tree: split(s1, s2) horizontal.
	tree := Split(DirectionHorizontal, Ratio(0.5), Leaf(ref(owner, "s1")), Leaf(ref(owner, "s2")))
	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1, tree)

	// Move s1 below s2.
	cmd := WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionMove,
		Params: workspaceParams(map[string]interface{}{
			"source": ref(owner, "s1"),
			"target": ref(owner, "s2"),
			"edge":   "bottom",
		}),
	}
	if err := c.ApplyWorkspaceCommand(cmd); err != nil {
		t.Fatalf("move: %v", err)
	}
	snap, _ := c.WorkspaceSnapshot(lID)
	if leaves := leafs(snap.Record.Tree); len(leaves) != 2 {
		t.Fatalf("move lost leaves: %v", leaves)
	}
	// Move s1 below s2 produces a vertical split with s2 first in DFS order.
	if leaves := leafs(snap.Record.Tree); leaves[0] != ref(owner, "s2").MapKey() {
		t.Fatalf("move order wrong: %v", leaves)
	}

	cmdSwap := WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionSwap,
		Params: workspaceParams(map[string]interface{}{
			"a": ref(owner, "s1"),
			"b": ref(owner, "s2"),
		}),
	}
	if err := c.ApplyWorkspaceCommand(cmdSwap); err != nil {
		t.Fatalf("swap: %v", err)
	}
	snap, _ = c.WorkspaceSnapshot(lID)
	leaves := leafs(snap.Record.Tree)
	if leaves[0] != ref(owner, "s1").MapKey() || leaves[1] != ref(owner, "s2").MapKey() {
		t.Fatalf("swap order wrong: %v", leaves)
	}
}

func TestWorkspaceRemoveCollapsesSingleChild(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	// s1 | (s2 / s3 vertical)
	tree := Split(DirectionHorizontal, Ratio(0.5),
		Leaf(ref(owner, "s1")),
		Split(DirectionVertical, Ratio(0.5), Leaf(ref(owner, "s2")), Leaf(ref(owner, "s3"))),
	)
	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1, tree)

	cmd := WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionRemove,
		Params: workspaceParams(map[string]interface{}{"ref": ref(owner, "s2")}),
	}
	if err := c.ApplyWorkspaceCommand(cmd); err != nil {
		t.Fatalf("remove: %v", err)
	}
	snap, _ := c.WorkspaceSnapshot(lID)
	leaves := leafs(snap.Record.Tree)
	if len(leaves) != 2 || snap.Record.Tree.IsLeaf() {
		t.Fatalf("expected collapsed 2-leaf tree, got leaves %v tree type %s", leaves, snap.Record.Tree.Type)
	}

	// Removing the last leaf deletes the layout.
	if err := c.ApplyWorkspaceCommand(WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionRemove,
		Params: workspaceParams(map[string]interface{}{"ref": ref(owner, "s1")}),
	}); err != nil {
		t.Fatalf("remove s1: %v", err)
	}
	if err := c.ApplyWorkspaceCommand(WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionRemove,
		Params: workspaceParams(map[string]interface{}{"ref": ref(owner, "s3")}),
	}); err != nil {
		t.Fatalf("remove s3: %v", err)
	}
	if _, err := c.WorkspaceSnapshot(lID); err == nil {
		t.Fatal("expected layout deleted after last leaf removed")
	}
}

func TestWorkspacePopOut(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	tree := Split(DirectionHorizontal, Ratio(0.5),
		Leaf(ref(owner, "s1")),
		Leaf(ref(owner, "s2")),
	)
	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1, tree)

	cmd := WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionPopOut,
		Params: workspaceParams(map[string]interface{}{"ref": ref(owner, "s2")}),
	}
	if err := c.ApplyWorkspaceCommand(cmd); err != nil {
		t.Fatalf("pop out: %v", err)
	}
	snap, _ := c.WorkspaceSnapshot(lID)
	if leaves := leafs(snap.Record.Tree); len(leaves) != 1 || leaves[0] != ref(owner, "s2").MapKey() {
		t.Fatalf("expected single s2 leaf, got %v", leaves)
	}
}

func TestWorkspaceResizeBySplitID(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1,
		Split(DirectionHorizontal, Ratio(0.5), Leaf(ref(owner, "s1")), Leaf(ref(owner, "s2"))))

	// First split the root to get a child split with a stable ID.
	if err := c.ApplyWorkspaceCommand(WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionSplit,
		Params: workspaceParams(map[string]interface{}{
			"target":    ref(owner, "s1"),
			"direction": DirectionVertical,
			"new":       ref(owner, "s3"),
		}),
	}); err != nil {
		t.Fatalf("split: %v", err)
	}

	snap, _ := c.WorkspaceSnapshot(lID)
	var childID SplitID
	var walk func(PaneNode)
	walk = func(n PaneNode) {
		if n.IsSplit() {
			if n.Direction == DirectionVertical && n.ID != "" {
				childID = n.ID
			}
			if n.First != nil {
				walk(*n.First)
			}
			if n.Second != nil {
				walk(*n.Second)
			}
		}
	}
	walk(snap.Record.Tree)
	if childID == "" {
		t.Fatal("new split did not receive an id")
	}

	if err := c.ApplyWorkspaceCommand(WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionResize,
		Params: workspaceParams(map[string]interface{}{
			"split_id": childID,
			"ratio":    0.25,
		}),
	}); err != nil {
		t.Fatalf("resize: %v", err)
	}

	snap, _ = c.WorkspaceSnapshot(lID)
	var found bool
	walk = func(n PaneNode) {
		if n.IsSplit() && n.ID == childID {
			if n.Ratio != 0.25 {
				t.Fatalf("ratio unchanged: %v", n.Ratio)
			}
			found = true
		}
		if n.First != nil {
			walk(*n.First)
		}
		if n.Second != nil {
			walk(*n.Second)
		}
	}
	walk(snap.Record.Tree)
	if !found {
		t.Fatal("resized split not found")
	}

	// Stale split id and invalid ratio.
	err := c.ApplyWorkspaceCommand(WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionResize,
		Params: workspaceParams(map[string]interface{}{
			"split_id": NewSplitID(),
			"ratio":    0.25,
		}),
	})
	assertCode(t, err, ErrStaleSplitID)

	err = c.ApplyWorkspaceCommand(WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionResize,
		Params: workspaceParams(map[string]interface{}{
			"split_id": childID,
			"ratio":    1.5,
		}),
	})
	assertCode(t, err, ErrInvalidRatio)
}

func TestWorkspaceReorderLayouts(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	mustPutLayout(t, c, owner, "layout1234567890abcd", 10, Leaf(ref(owner, "a")))
	mustPutLayout(t, c, owner, "layout2234567890abcd", 20, Leaf(ref(owner, "b")))
	mustPutLayout(t, c, owner, "layout3234567890abcd", 30, Leaf(ref(owner, "c")))

	cmd := WorkspaceCommand{
		ID:     NewCommandID(),
		Action: WorkspaceActionReorderLayouts,
		Params: workspaceParams(map[string]interface{}{
			"source_index": 0,
			"target_index": 2,
		}),
	}
	if err := c.ApplyWorkspaceCommand(cmd); err != nil {
		t.Fatalf("reorder: %v", err)
	}

	layouts := c.Layouts()
	orders := make([]int64, len(layouts))
	for i, l := range layouts {
		orders[i] = l.Order
	}
	want := append([]int64(nil), orders...)
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	got := append([]int64(nil), orders...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orders not a permutation: %+v", orders)
		}
	}

	err := c.ApplyWorkspaceCommand(WorkspaceCommand{
		ID:     NewCommandID(),
		Action: WorkspaceActionReorderLayouts,
		Params: workspaceParams(map[string]interface{}{
			"source_index": 0,
			"target_index": 5,
		}),
	})
	assertCode(t, err, ErrMalformedOrder)
}

func TestWorkspaceRename(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1,
		Split(DirectionHorizontal, Ratio(0.5), Leaf(ref(owner, "s1")), Leaf(ref(owner, "s2"))))

	if err := c.ApplyWorkspaceCommand(WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionRename,
		Params: workspaceParams(map[string]interface{}{
			"old": ref(owner, "s1"),
			"new": ref(owner, "renamed"),
		}),
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}

	snap, _ := c.WorkspaceSnapshot(lID)
	leaves := leafs(snap.Record.Tree)
	if len(leaves) != 2 {
		t.Fatalf("leaf count: %v", leaves)
	}
	haveRenamed := false
	for _, l := range leaves {
		if l == ref(owner, "renamed").MapKey() {
			haveRenamed = true
		}
		if l == ref(owner, "s1").MapKey() {
			t.Fatal("old ref still present")
		}
	}
	if !haveRenamed {
		t.Fatalf("renamed ref missing: %v", leaves)
	}
}

func TestWorkspaceSelectAndPresent(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1,
		Split(DirectionHorizontal, Ratio(0.5), Leaf(ref(owner, "s1")), Leaf(ref(owner, "s2"))))

	if err := c.ApplyWorkspaceCommand(WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionSelect,
		Params: workspaceParams(map[string]interface{}{"ref": ref(owner, "s2")}),
	}); err != nil {
		t.Fatalf("select: %v", err)
	}

	snap, _ := c.WorkspaceSnapshot(lID)
	if snap.Record.ActiveKey == nil || snap.Record.ActiveKey.MapKey() != ref(owner, "s2").MapKey() {
		t.Fatalf("active key not set: %+v", snap.Record.ActiveKey)
	}

	if err := c.ApplyWorkspaceCommand(WorkspaceCommand{
		ID:     NewCommandID(),
		Layout: lID,
		Action: WorkspaceActionPresent,
		Params: workspaceParams(map[string]interface{}{
			"presents": []PresentationRecord{
				{Ref: ref(owner, "s1"), Selected: true, ZIndex: 1},
				{Ref: ref(owner, "s2"), Selected: false, ZIndex: 2},
			},
		}),
	}); err != nil {
		t.Fatalf("present: %v", err)
	}

	snap, _ = c.WorkspaceSnapshot(lID)
	if len(snap.Presentations) != 2 {
		t.Fatalf("expected 2 presentations, got %d", len(snap.Presentations))
	}

	// Presentation is not persisted.
	doc := c.store.Snapshot()
	if len(doc.Commands) != 1 {
		t.Fatalf("expected only select receipt persisted, got %d", len(doc.Commands))
	}
}

func TestWorkspaceAtomicRevisionIncrement(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1, Leaf(ref(owner, "s1")))
	for i := 0; i < 5; i++ {
		before := c.Revision()
		cmd := WorkspaceCommand{
			ID:     NewCommandID(),
			Layout: lID,
			Action: WorkspaceActionSplit,
			Params: workspaceParams(map[string]interface{}{
				"target":    ref(owner, fmt.Sprintf("s%d", i+1)),
				"direction": DirectionHorizontal,
				"new":       ref(owner, fmt.Sprintf("s%d", i+2)),
			}),
		}
		if err := c.ApplyWorkspaceCommand(cmd); err != nil {
			t.Fatalf("split %d: %v", i, err)
		}
		if got := c.Revision(); got != before+1 {
			t.Fatalf("step %d: revision %d, want %d", i, got, before+1)
		}
	}
}

func TestWorkspaceRemoveSessionRefInternal(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	l1 := mustPutLayout(t, c, owner, "layout1234567890abcd", 1,
		Split(DirectionHorizontal, Ratio(0.5), Leaf(ref(owner, "s1")), Leaf(ref(owner, "s2"))))
	l2 := mustPutLayout(t, c, owner, "layout2234567890abcd", 2,
		Leaf(ref(owner, "s3")))

	if err := c.RemoveSessionRef(ref(owner, "s1")); err != nil {
		t.Fatalf("remove session ref: %v", err)
	}

	snap1, _ := c.WorkspaceSnapshot(l1)
	for _, k := range leafs(snap1.Record.Tree) {
		if k == ref(owner, "s1").MapKey() {
			t.Fatal("s1 still in l1")
		}
	}
	snap2, _ := c.WorkspaceSnapshot(l2)
	for _, k := range leafs(snap2.Record.Tree) {
		if k == ref(owner, "s1").MapKey() {
			t.Fatal("s1 still in l2")
		}
	}
}

func TestWorkspaceRandomizedCommandSequence(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1, Leaf(ref(owner, "s0")))

	rng := rand.New(rand.NewSource(42))
	refs := []SessionRef{ref(owner, "s0")}
	nextID := 1

	for step := 0; step < 200; step++ {
		action := rng.Intn(5)
		var cmd WorkspaceCommand
		switch action {
		case 0, 1: // split existing leaf with a fresh session
			target := refs[rng.Intn(len(refs))]
			name := fmt.Sprintf("s%d", nextID)
			nextID++
			newRef := ref(owner, name)
			refs = append(refs, newRef)
			cmd = WorkspaceCommand{
				ID:     NewCommandID(),
				Layout: lID,
				Action: WorkspaceActionSplit,
				Params: workspaceParams(map[string]interface{}{
					"target":    target,
					"direction": []SplitDirection{DirectionHorizontal, DirectionVertical}[rng.Intn(2)],
					"new":       newRef,
				}),
			}
		case 2: // remove a leaf
			if len(refs) <= 1 {
				continue
			}
			idx := rng.Intn(len(refs))
			target := refs[idx]
			refs = append(refs[:idx], refs[idx+1:]...)
			cmd = WorkspaceCommand{
				ID:     NewCommandID(),
				Layout: lID,
				Action: WorkspaceActionRemove,
				Params: workspaceParams(map[string]interface{}{"ref": target}),
			}
		case 3: // swap two leaves
			if len(refs) < 2 {
				continue
			}
			i, j := rng.Intn(len(refs)), rng.Intn(len(refs))
			if i == j {
				continue
			}
			cmd = WorkspaceCommand{
				ID:     NewCommandID(),
				Layout: lID,
				Action: WorkspaceActionSwap,
				Params: workspaceParams(map[string]interface{}{
					"a": refs[i],
					"b": refs[j],
				}),
			}
		case 4: // rename a leaf
			if len(refs) == 0 {
				continue
			}
			old := refs[rng.Intn(len(refs))]
			name := fmt.Sprintf("s%d", nextID)
			nextID++
			newRef := ref(owner, name)
			for i := range refs {
				if refs[i].MapKey() == old.MapKey() {
					refs[i] = newRef
				}
			}
			cmd = WorkspaceCommand{
				ID:     NewCommandID(),
				Layout: lID,
				Action: WorkspaceActionRename,
				Params: workspaceParams(map[string]interface{}{
					"old": old,
					"new": newRef,
				}),
			}
		}

		if cmd.ID == "" {
			continue
		}
		err := c.ApplyWorkspaceCommand(cmd)
		if err != nil {
			// A remove that empties the layout is valid if it does not leave
			// the catalog with zero layouts? Here it may delete the only layout.
			var se StateError
			if errors.As(err, &se) && se.Code == ErrUnknownLayout {
				// layout was deleted; recreate to keep fuzz running
				name := fmt.Sprintf("s%d", nextID)
				nextID++
				r := ref(owner, name)
				refs = []SessionRef{r}
				mustPutLayout(t, c, owner, "layout1234567890abcd", 1, Leaf(r))
				continue
			}
			t.Fatalf("step %d action %d: %v", step, action, err)
		}

		snap := c.store.Snapshot()
		if err := ValidateDocument(&snap); err != nil {
			t.Fatalf("step %d invalid document: %v", step, err)
		}
		if err := CheckSessionMembershipAcrossLayouts(&snap); err != nil {
			t.Fatalf("step %d membership conflict: %v", step, err)
		}
		if len(snap.Commands) > MaxPendingCommands {
			t.Fatalf("step %d too many receipts: %d", step, len(snap.Commands))
		}
	}
}

func BenchmarkWorkspaceSplitOperations(b *testing.B) {
	owner := OwnerID("ownerwork1234567890abcd")
	dir := b.TempDir()
	store, err := OpenStore(dir, "bench", StoreOptions{Owner: owner})
	if err != nil {
		b.Fatal(err)
	}
	_ = store
	cat := NewCatalog(owner, store)
	if err := cat.Load(); err != nil {
		b.Fatal(err)
	}

	// Build a balanced tree with 128 leaves.
	leaves := make([]PaneNode, 128)
	for i := range leaves {
		leaves[i] = Leaf(ref(owner, fmt.Sprintf("leaf%03d", i)))
	}
	tree := buildBalanced(leaves)
	lID := LayoutID("layout1234567890abcd")
	if err := cat.PutLayout(LayoutRecord{ID: lID, Owner: owner, Order: 1, Tree: tree, Revision: 0}); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := fmt.Sprintf("new%06d", i)
		cmd := WorkspaceCommand{
			ID:     NewCommandID(),
			Layout: lID,
			Action: WorkspaceActionSplit,
			Params: workspaceParams(map[string]interface{}{
				"target":    ref(owner, "leaf000"),
				"direction": DirectionHorizontal,
				"new":       ref(owner, name),
			}),
		}
		if err := cat.ApplyWorkspaceCommand(cmd); err != nil {
			b.Fatal(err)
		}
	}
}

func buildBalanced(nodes []PaneNode) PaneNode {
	if len(nodes) == 1 {
		return nodes[0]
	}
	mid := len(nodes) / 2
	left := buildBalanced(nodes[:mid])
	right := buildBalanced(nodes[mid:])
	return PaneNode{
		Type:      "split",
		ID:        NewSplitID(),
		Direction: DirectionHorizontal,
		Ratio:     0.5,
		First:     &left,
		Second:    &right,
	}
}

// Verify WorkspaceSnapshot synthesis works when no active WorkspaceRecord
// exists in the persisted document.
func TestWorkspaceSnapshotSynthesized(t *testing.T) {
	c, owner, cleanup := mustNewWorkspaceCatalog(t)
	defer cleanup()

	lID := mustPutLayout(t, c, owner, "layout1234567890abcd", 1, Leaf(ref(owner, "s1")))
	snap, err := c.WorkspaceSnapshot(lID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Record.ID != lID {
		t.Fatalf("workspace id mismatch")
	}
	if len(leafs(snap.Record.Tree)) != 1 {
		t.Fatalf("expected one leaf")
	}
}
