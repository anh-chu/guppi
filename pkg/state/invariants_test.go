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
		Schema:   2,
		Owner:    owner,
		Revision: 1,
		Sessions: []LocalSessionRecord{
			mkSession(owner, "sessinv1234567890ab"),
			mkSession(owner, "sessinv1234567890ab"),
		},
		Layouts: []LayoutRecord{
			mkLayout(owner, "layoutinv1234567890", 1),
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
		Schema:   2,
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
		Schema:   2,
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
	owner := OwnerID("ownerinv1234567890ab")
	doc := AppDocument{
		Schema:   2,
		Owner:    owner,
		Revision: 1,
		Layouts: []LayoutRecord{
			mkLayout(owner, "layoutinv1234567890", 1),
			mkLayout(owner, "layoutinv1234567890", 2),
		},
	}
	err := ValidateDocument(&doc)
	if err == nil {
		t.Fatal("expected duplicate layout error")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrDuplicateIdentity {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestInvariantDuplicateLayoutOrder(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	doc := AppDocument{
		Schema:   2,
		Owner:    owner,
		Revision: 1,
		Layouts: []LayoutRecord{
			{ID: "layoutinv1234567890a", Owner: owner, Order: 1, Tree: Leaf(SessionRef{Owner: owner, Session: "a"})},
			{ID: "layoutinv1234567890b", Owner: owner, Order: 1, Tree: Leaf(SessionRef{Owner: owner, Session: "b"})},
		},
	}
	err := ValidateDocument(&doc)
	if err == nil {
		t.Fatal("expected duplicate layout order error")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrDuplicateLayoutOrder {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestInvariantUnknownWorkspaceLayoutID(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	doc := AppDocument{
		Schema:   2,
		Owner:    owner,
		Revision: 1,
		Workspaces: []WorkspaceRecord{
			mkWorkspace(owner, "missinglayout1234567"),
		},
	}
	err := ValidateDocument(&doc)
	if err == nil {
		t.Fatal("expected unknown layout error")
	}
	var se StateError
	if !errors.As(err, &se) || se.Code != ErrUnknownLayout {
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
		Schema:   2,
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

func TestInvariantSessionInWorkspaceAndLayout(t *testing.T) {
	owner := OwnerID("ownerinv1234567890ab")
	ref := SessionRef{Owner: owner, Session: "sessinv1234567890ab"}
	doc := AppDocument{
		Schema:   2,
		Owner:    owner,
		Revision: 1,
		Workspaces: []WorkspaceRecord{
			{ID: "workspaceinv123456789", Owner: owner, Tree: Leaf(ref)},
		},
		Layouts: []LayoutRecord{
			{ID: "workspaceinv123456789", Owner: owner, Order: 1, Tree: Leaf(ref)},
			{ID: "layoutinv1234567890ab", Owner: owner, Order: 2, Tree: Leaf(SessionRef{Owner: owner, Session: "otherinv1234567890ab"})},
		},
	}
	if err := ValidateDocument(&doc); err != nil {
		t.Fatalf("document should be structurally valid before membership check: %v", err)
	}
	err := CheckSessionMembershipAcrossLayouts(&doc)
	if err == nil {
		t.Fatal("expected session-in-multiple-layouts error")
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
