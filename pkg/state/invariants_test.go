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

// TestInvariantDuplicateLayoutIDs_OBSOLETE is removed; schema 4 has no layouts.
func TestInvariantDuplicateLayoutIDs_OBSOLETE(t *testing.T) {
	t.Skip("Schema 4: no layouts")
}

// TestInvariantMultipleLayoutsRejected_OBSOLETE is removed; schema 4 has one workspace.
func TestInvariantMultipleLayoutsRejected_OBSOLETE(t *testing.T) {
	t.Skip("Schema 4: one workspace")
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

// TestInvariantSessionInMultipleLayouts_OBSOLETE is removed; schema 4 has one workspace.
func TestInvariantSessionInMultipleLayouts_OBSOLETE(t *testing.T) {
	t.Skip("Schema 4: one workspace")
}

// TestValidateDocumentRejectsOrphanedSessionRef is the regression-prevention
// test for the whole class of bug fixed here: a workspace pane-tree leaf whose
// SessionRef.Session names no LocalSessionRecord anywhere in the document
// (not doc.Sessions, not a PendingCreate, not a PendingRemoteCreate) must be
// rejected by ValidateDocument. This guards against any future code path
// leaving a leaf pointing at a session that was never created or has already
// been removed.
func TestValidateDocumentRejectsOrphanedSessionRef(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	orphanLeaf := Leaf(SessionRef{Owner: owner, Session: "orphaninv1234567890a"})

	doc := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Sessions: []LocalSessionRecord{
			mkSession(owner, "sessdoc1234567890ab"),
		},
		Workspace: &WorkspaceRecord{Revision: 1, Tree: &orphanLeaf},
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
	foreignLeaf := Leaf(SessionRef{Owner: "foreignownerabcd12345", Session: "sessdoc1234567890ab"})
	doc2 := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Sessions: []LocalSessionRecord{
			mkSession(owner, "sessdoc1234567890ab"),
		},
		Workspace: &WorkspaceRecord{Revision: 1, Tree: &foreignLeaf},
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
	pendingLeaf := Leaf(pendingRef)
	doc3 := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		PendingCreates: []PendingCreateRecord{
			{IntentID: NewCommandID(), Ref: pendingRef},
		},
		Workspace: &WorkspaceRecord{Revision: 1, Tree: &pendingLeaf},
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

// TestSchema4SingletonWorkspace proves the contract that a workspace tree
// is optional (Tree == nil is valid) and is the ONLY workspace representation.
func TestSchema4SingletonWorkspace(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	
	// Empty workspace with nil tree should be valid in schema 4.
	emptyWorkspace := AppDocument{
		Schema:    SchemaVersion,
		Owner:     owner,
		Revision:  0,
		Sessions:  []LocalSessionRecord{},
		Workspace: &WorkspaceRecord{Revision: 0, Tree: nil},
	}
	
	if err := ValidateDocument(&emptyWorkspace); err != nil {
		t.Fatalf("empty workspace should be valid: %v", err)
	}
	
	// Document with a workspace tree and a leaf referencing a pending session
	// should be valid.
	pendingRef := SessionRef{Owner: owner, Session: "pendinginv1234567890"}
	pendingLeaf := Leaf(pendingRef)
	withTree := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Sessions: []LocalSessionRecord{},
		PendingCreates: []PendingCreateRecord{
			{IntentID: NewCommandID(), Ref: pendingRef},
		},
		Workspace: &WorkspaceRecord{
		  Revision: 1,
		  Tree: &pendingLeaf,
		},
	}
	
	if err := ValidateDocument(&withTree); err != nil {
		t.Fatalf("workspace with pending-create-backed leaf should be valid: %v", err)
	}
}
