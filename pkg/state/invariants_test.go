package state

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestInvariantDuplicateSessionIDs(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	doc := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Sessions: []LocalSessionRecord{
			mkSession(owner, "sessinv1234567890ab"),
			mkSession(owner, "sessinv1234567890ab"),
		},
		Layouts: []LayoutRecord{
			mkLayout(owner, "layoutinv1234567890"),
		},
	}
	err := ValidateDocument(&doc)
	if err == nil {
		t.Fatal("expected duplicate session error")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrDuplicateIdentity {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestInvariantInvalidOwner(t *testing.T) {
	doc := AppDocument{
		Schema:   SchemaVersion,
		Owner:    "bad/owner",
		Revision: 1,
	}
	err := ValidateDocument(&doc)
	if err == nil {
		t.Fatal("expected invalid owner error")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrInvalidIdentity || !strings.Contains(se.Field, "owner") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestInvariantSessionOwnerMismatch(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	s := mkSession(owner, "sessinv1234567890ab")
	s.Owner = "otherowner1234567890ab"
	doc := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Sessions: []LocalSessionRecord{s},
	}
	err := ValidateDocument(&doc)
	if err == nil {
		t.Fatal("expected session owner mismatch")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrInvalidIdentity {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestInvariantDuplicateLayoutIDs(t *testing.T) {
	// A document can never legitimately carry two layouts (see
	// TestInvariantMultipleLayoutsRejected below), so a document with two
	// layouts -- duplicate ID or not -- always fails ValidateDocument with
	// ErrMultipleLayouts before any per-layout duplicate-ID check is reached.
	owner := OwnerID("ownerinv1234567890ab")
	doc := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Sessions: []LocalSessionRecord{
			mkSession(owner, "sessdoc1234567890ab"),
		},
		Layouts: []LayoutRecord{
			mkLayout(owner, "layoutinv1234567890"),
			{ID: "layoutinv1234567890", Owner: owner, Tree: Leaf(SessionRef{Owner: owner, Session: "sessdoc1234567890ab"})},
		},
	}
	err := ValidateDocument(&doc)
	if err == nil {
		t.Fatal("expected an error for a document with two layouts")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrMultipleLayouts {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestInvariantMultipleLayoutsRejected is the acceptance test for the
// one-workspace-layout invariant: a document with two DISTINCT layouts (no
// duplicate IDs, no shared leaves) must still fail closed with
// ErrMultipleLayouts, never persisted, since the product exposes no
// group/multi-layout controls.
func TestInvariantMultipleLayoutsRejected(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	doc := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Sessions: []LocalSessionRecord{
			mkSession(owner, "sessinv1234567890ab"),
			mkSession(owner, "otherinv1234567890ab"),
		},
		Layouts: []LayoutRecord{
			{ID: "layoutinv1234567890a", Owner: owner, Tree: Leaf(SessionRef{Owner: owner, Session: "sessinv1234567890ab"})},
			{ID: "layoutinv1234567890b", Owner: owner, Tree: Leaf(SessionRef{Owner: owner, Session: "otherinv1234567890ab"})},
		},
	}
	err := ValidateDocument(&doc)
	if err == nil {
		t.Fatal("expected a document with two layouts to be rejected")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrMultipleLayouts {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestInvariantDuplicateLeaves(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	tree := Split(
		DirectionHorizontal,
		Ratio(0.5),
		Leaf(SessionRef{Owner: owner, Session: "sessinv1234567890ab"}),
		Leaf(SessionRef{Owner: owner, Session: "sessinv1234567890ab"}),
	)
	err := ValidatePaneTree(tree)
	if err == nil {
		t.Fatal("expected duplicate leaf error")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrDuplicateLeaf {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestInvariantMalformedSplit(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	cases := []struct {
		name  string
		tree  PaneNode
		codes []ErrorCode
	}{
		{
			name:  "ratio out of range",
			tree:  Split(DirectionHorizontal, Ratio(1.5), Leaf(SessionRef{Owner: owner, Session: "a"}), Leaf(SessionRef{Owner: owner, Session: "b"})),
			codes: []ErrorCode{ErrInvalidRatio},
		},
		{
			name:  "non-finite ratio",
			tree:  Split(DirectionHorizontal, Ratio(math.NaN()), Leaf(SessionRef{Owner: owner, Session: "a"}), Leaf(SessionRef{Owner: owner, Session: "b"})),
			codes: []ErrorCode{ErrInvalidRatio},
		},
		{
			name:  "bad direction",
			tree:  PaneNode{Type: "split", Direction: "diagonal", Ratio: 0.5, First: ptrLeaf(SessionRef{Owner: owner, Session: "a"}), Second: ptrLeaf(SessionRef{Owner: owner, Session: "b"})},
			codes: []ErrorCode{ErrMalformedSplit},
		},
		{
			name:  "nil child",
			tree:  PaneNode{Type: "split", Direction: DirectionHorizontal, Ratio: 0.5, First: nil, Second: ptrLeaf(SessionRef{Owner: owner, Session: "b"})},
			codes: []ErrorCode{ErrMalformedSplit},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePaneTree(tc.tree)
			if err == nil {
				t.Fatal("expected malformed split error")
			}
			var se StateError
			if !errors.As(err, &se) {
				t.Fatalf("wrong error type: %v", err)
			}
			found := false
			for _, c := range tc.codes {
				if se.Code == c {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("got code %q, want one of %v", se.Code, tc.codes)
			}
		})
	}
}

func ptrLeaf(ref SessionRef) *PaneNode {
	p := Leaf(ref)
	return &p
}

func TestInvariantSessionInMultipleLayouts(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	ref := SessionRef{Owner: owner, Session: "sessinv1234567890ab"}
	doc := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Layouts: []LayoutRecord{
			{ID: "layoutinv1234567890a", Owner: owner, Tree: Leaf(ref)},
			{ID: "layoutinv1234567890b", Owner: owner, Tree: Leaf(ref)},
		},
	}
	err := CheckSessionMembershipAcrossLayouts(&doc)
	if err == nil {
		t.Fatal("expected session-in-multiple-layouts error")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrSessionInMultipleLayouts {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestValidateDocumentRejectsOrphanedSessionRef is the regression-prevention
// test for the whole class of bug fixed here: a layout pane-tree leaf whose
// SessionRef.Session names no LocalSessionRecord anywhere in the document
// (not doc.Sessions, not a PendingCreate, not a PendingRemoteCreate) must be
// rejected by ValidateDocument, whether or not this specific leaf was ever
// touched by a workspace "rename" command. This guards against any future
// code path (not just WorkspaceActionRename) leaving a leaf pointing at a
// session that was never created or has already been removed.
func TestValidateDocumentRejectsOrphanedSessionRef(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	real := SessionRef{Owner: owner, Session: "sessdoc1234567890ab"}
	orphan := SessionRef{Owner: owner, Session: "orphaninv1234567890a"}

	doc := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Sessions: []LocalSessionRecord{
			mkSession(owner, "sessdoc1234567890ab"),
		},
		Layouts: []LayoutRecord{
			{
				ID:    "layoutinv1234567890a",
				Owner: owner,
				Tree:  Split(DirectionHorizontal, Ratio(0.5), Leaf(real), Leaf(orphan)),
			},
		},
	}

	err := ValidateDocument(&doc)
	if err == nil {
		t.Fatal("expected orphaned session ref to be rejected")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrOrphanedSessionRef {
		t.Fatalf("wrong error: %v", err)
	}

	// A leaf whose owner does not match the document's own owner is rejected
	// the same way, even if the session ID happens to collide with a real one.
	foreignOwnerLeaf := SessionRef{Owner: "foreignownerabcd12345", Session: "sessdoc1234567890ab"}
	doc2 := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Sessions: []LocalSessionRecord{
			mkSession(owner, "sessdoc1234567890ab"),
		},
		Layouts: []LayoutRecord{
			{ID: "layoutinv1234567890b", Owner: owner, Tree: Leaf(foreignOwnerLeaf)},
		},
	}
	err2 := ValidateDocument(&doc2)
	if err2 == nil {
		t.Fatal("expected foreign-owned leaf to be rejected")
	}
	var se2 StateError
	if !errors.As(err2, &se2) || se2.Code != ErrOrphanedSessionRef {
		t.Fatalf("wrong error: %v", err2)
	}

	// The same session ID is accepted when it is only a pending (not yet
	// materialized) create -- this is the legitimate case executeCreate
	// relies on (see session_commands.go), and must not be confused with an
	// orphaned ref.
	pendingRef := SessionRef{Owner: owner, Session: "pendinginv1234567890"}
	doc3 := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		PendingCreates: []PendingCreateRecord{
			{IntentID: NewCommandID(), Ref: pendingRef},
		},
		Layouts: []LayoutRecord{
			{ID: "layoutinv1234567890c", Owner: owner, Tree: Leaf(pendingRef)},
		},
	}
	if err := ValidateDocument(&doc3); err != nil {
		t.Fatalf("pending-create-backed leaf should be accepted: %v", err)
	}
}

func TestShouldAcceptSnapshot(t *testing.T) {
	if !ShouldAcceptSnapshot(5, 5, true) {
		t.Error("first snapshot should be accepted unconditionally")
	}
	if !ShouldAcceptSnapshot(5, 5, false) {
		t.Error("same generation snapshot should be accepted")
	}
	if ShouldAcceptSnapshot(3, 5, false) {
		t.Error("stale generation snapshot should be rejected")
	}
}
