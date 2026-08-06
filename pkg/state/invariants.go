package state

import (
	"fmt"
	"time"
)

// ErrorCode is a stable machine-readable invariant violation.
type ErrorCode string

const (
	ErrBadSchema                ErrorCode = "bad_schema"
	ErrFutureSchema             ErrorCode = "future_schema"
	ErrInvalidIdentity          ErrorCode = "invalid_identity"
	ErrDuplicateIdentity        ErrorCode = "duplicate_identity"
	ErrUnknownLayout            ErrorCode = "unknown_layout"
	ErrDuplicateLayout          ErrorCode = "duplicate_layout"
	ErrDuplicateLayoutOrder     ErrorCode = "duplicate_layout_order"
	ErrDuplicateLeaf            ErrorCode = "duplicate_leaf"
	ErrMalformedSplit           ErrorCode = "malformed_split"
	ErrSessionInMultipleLayouts ErrorCode = "session_in_multiple_layouts"
	ErrInvalidRatio             ErrorCode = "invalid_ratio"
	ErrMissingTarget            ErrorCode = "missing_target"
	ErrDuplicateMembership      ErrorCode = "duplicate_membership"
	ErrMalformedOrder           ErrorCode = "malformed_order"
	ErrStaleSplitID             ErrorCode = "stale_split_id"
	ErrRevisionConflict         ErrorCode = "revision_conflict"
	ErrCommandExpired           ErrorCode = "command_expired"
	ErrTooManyCommands          ErrorCode = "too_many_commands"
	ErrWorkspaceOwnerOffline    ErrorCode = "workspace_owner_offline"
	ErrLegacyPeerUnsupported    ErrorCode = "legacy_peer_unsupported"
	ErrGenerationMismatch       ErrorCode = "generation_mismatch"
	// ErrOwnershipMismatch is returned when a peer-originated command's
	// stated owner/requester/target ref does not match the authenticated
	// identity that must own it (e.g. a remote peer's SessionCommand.Ref.Owner
	// does not match this node's own catalog owner, or a RemoteCreateRequest's
	// Requester does not match the authenticated sender). This is a peer-trust
	// violation, not a client input mistake, and must never be silently ignored.
	ErrOwnershipMismatch        ErrorCode = "ownership_mismatch"
	// ErrOrphanedSessionRef is returned when a SessionRef embedded in a layout
	// or workspace pane-tree leaf (or an active key) either claims an owner
	// other than the document's own owner, or names a session ID that has no
	// corresponding LocalSessionRecord in the document. Session identity
	// (Owner, Session ID) is immutable for a session's lifetime; nothing may
	// rewrite a leaf's ref without the catalog's session record moving with
	// it, and nothing may leave a leaf pointing at a session that was never
	// created or has already been removed.
	ErrOrphanedSessionRef       ErrorCode = "orphaned_session_ref"
	// ErrDeprecatedAction is returned when a caller submits a workspace action
	// that has been intentionally removed because it mutated data it must
	// never touch (e.g. the old "rename" action rewrote SessionRef identity
	// inside pane-tree leaves). The error detail names the replacement.
	ErrDeprecatedAction         ErrorCode = "deprecated_action"
	// ErrOwnerRefMismatch is returned when a LocalSessionRecord's own Owner
	// field disagrees with its Ref.Owner. Both are supposed to name the same
	// owning node; a mismatch means the record was corrupted or constructed
	// incorrectly (e.g. a copy that updated one field but not the other).
	ErrOwnerRefMismatch ErrorCode = "owner_ref_mismatch"
	// ErrMissingGeneration is returned when a session record's lifecycle phase
	// (active or starting) requires a live daemon generation identity but
	// none is recorded. A session cannot be considered live without a
	// generation: it is what exact-generation termination/adoption and
	// mayRemoveClean (reconciler.go) key off of.
	ErrMissingGeneration ErrorCode = "missing_generation"
	// ErrInvalidPresentation is returned when a PresentationRecord carries an
	// invalid field (an empty ref, or a negative z-index).
	ErrInvalidPresentation ErrorCode = "invalid_presentation"
	// ErrInconsistentScheduleOwnership is returned when more than one pending
	// create (local or remote) in the same document carries the same non-empty
	// ScheduleID. A schedule may own at most one in-flight create at a time.
	ErrInconsistentScheduleOwnership ErrorCode = "inconsistent_schedule_ownership"
)

// StateError reports a typed contract violation.
type StateError struct {
	Code   ErrorCode `json:"code"`
	Field  string    `json:"field,omitempty"`
	Detail string    `json:"detail"`
}

func (e StateError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("%s (%s): %s", e.Code, e.Field, e.Detail)
}

// ValidateDocument checks the top-level invariants of an AppDocument.
func ValidateDocument(doc *AppDocument) error {
	if doc == nil {
		return StateError{Code: ErrBadSchema, Detail: "document is nil"}
	}
	if doc.Schema != SchemaVersion {
		if doc.Schema > SchemaVersion {
			return StateError{Code: ErrFutureSchema, Field: "schema", Detail: fmt.Sprintf("schema %d is newer than supported %d", doc.Schema, SchemaVersion)}
		}
		return StateError{Code: ErrBadSchema, Field: "schema", Detail: fmt.Sprintf("schema %d is not supported", doc.Schema)}
	}
	if err := doc.Owner.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "owner", Detail: err.Error()}
	}
	if doc.Revision < 0 {
		return StateError{Code: ErrBadSchema, Field: "revision", Detail: "revision must be non-negative"}
	}

	seenSessions := make(map[SessionID]struct{}, len(doc.Sessions))
	for i := range doc.Sessions {
		s := &doc.Sessions[i]
		if err := ValidateLocalSession(s, doc.Owner); err != nil {
			return StateError{Code: err.(StateError).Code, Field: fmt.Sprintf("sessions[%d].%s", i, err.(StateError).Field), Detail: err.Error()}
		}
		if _, exists := seenSessions[s.ID]; exists {
			return StateError{Code: ErrDuplicateIdentity, Field: fmt.Sprintf("sessions[%d].id", i), Detail: fmt.Sprintf("duplicate session id %q", s.ID)}
		}
		seenSessions[s.ID] = struct{}{}
	}
	// A session ID also "exists" for pane-tree ref-integrity purposes while it
	// is still an in-flight create intent: create places the session's ref
	// into its target layout immediately, before the session record itself is
	// materialized in doc.Sessions once the underlying pty actually starts
	// (see executeCreate/placeSessionInWorkspace in session_commands.go and
	// the reconciler that later promotes a pending create to a real session
	// record). Only a leaf whose session ID appears in none of these three
	// sources is an orphan.
	for i := range doc.PendingCreates {
		seenSessions[doc.PendingCreates[i].Ref.Session] = struct{}{}
	}
	for i := range doc.PendingRemoteCreates {
		seenSessions[doc.PendingRemoteCreates[i].Ref.Session] = struct{}{}
	}

	seenLayouts := make(map[LayoutID]struct{}, len(doc.Layouts))
	seenOrders := make(map[int64]struct{}, len(doc.Layouts))
	for i := range doc.Layouts {
		l := &doc.Layouts[i]
		if err := ValidateLayout(l, doc.Owner); err != nil {
			return StateError{Code: err.(StateError).Code, Field: fmt.Sprintf("layouts[%d].%s", i, err.(StateError).Field), Detail: err.Error()}
		}
		if _, exists := seenLayouts[l.ID]; exists {
			return StateError{Code: ErrDuplicateIdentity, Field: fmt.Sprintf("layouts[%d].id", i), Detail: fmt.Sprintf("duplicate layout id %q", l.ID)}
		}
		if _, exists := seenOrders[l.Order]; exists {
			return StateError{Code: ErrDuplicateLayoutOrder, Field: fmt.Sprintf("layouts[%d].order", i), Detail: fmt.Sprintf("duplicate layout order %d", l.Order)}
		}
		seenLayouts[l.ID] = struct{}{}
		seenOrders[l.Order] = struct{}{}
		if err := validateSessionRefIntegrity(l.Tree, doc.Owner, seenSessions, fmt.Sprintf("layouts[%d].tree", i)); err != nil {
			return err
		}
	}

	for i := range doc.Workspaces {
		w := &doc.Workspaces[i]
		if err := ValidateWorkspace(w, doc.Owner, seenLayouts); err != nil {
			return StateError{Code: err.(StateError).Code, Field: fmt.Sprintf("workspaces[%d].%s", i, err.(StateError).Field), Detail: err.Error()}
		}
		if err := validateSessionRefIntegrity(w.Tree, doc.Owner, seenSessions, fmt.Sprintf("workspaces[%d].tree", i)); err != nil {
			return err
		}
		if w.ActiveKey != nil {
			if err := validateSingleSessionRefIntegrity(*w.ActiveKey, doc.Owner, seenSessions, fmt.Sprintf("workspaces[%d].active_key", i)); err != nil {
				return err
			}
		}
	}

	for i := range doc.PendingRemoteCreates {
		p := &doc.PendingRemoteCreates[i]
		if err := ValidatePendingRemoteCreate(p, doc.Owner); err != nil {
			return StateError{Code: err.(StateError).Code, Field: fmt.Sprintf("pending_remote_creates[%d].%s", i, err.(StateError).Field), Detail: err.Error()}
		}
	}

	if err := checkScheduleOwnership(doc); err != nil {
		return err
	}

	return nil
}

// checkScheduleOwnership rejects a document where the same non-empty
// ScheduleID owns more than one in-flight pending create (local or remote)
// at once.
func checkScheduleOwnership(doc *AppDocument) error {
	seen := make(map[string]string, len(doc.PendingCreates)+len(doc.PendingRemoteCreates))
	for i := range doc.PendingCreates {
		id := doc.PendingCreates[i].ScheduleID
		if id == "" {
			continue
		}
		field := fmt.Sprintf("pending_creates[%d].schedule_id", i)
		if prev, exists := seen[id]; exists {
			return StateError{Code: ErrInconsistentScheduleOwnership, Field: field, Detail: fmt.Sprintf("schedule %q already owns pending create %q", id, prev)}
		}
		seen[id] = field
	}
	for i := range doc.PendingRemoteCreates {
		id := doc.PendingRemoteCreates[i].ScheduleID
		if id == "" {
			continue
		}
		field := fmt.Sprintf("pending_remote_creates[%d].schedule_id", i)
		if prev, exists := seen[id]; exists {
			return StateError{Code: ErrInconsistentScheduleOwnership, Field: field, Detail: fmt.Sprintf("schedule %q already owns pending create %q", id, prev)}
		}
		seen[id] = field
	}
	return nil
}

// ValidateLocalSession checks a session record belongs to the document owner
// and carries a valid identity.
func ValidateLocalSession(s *LocalSessionRecord, owner OwnerID) error {
	if err := s.ID.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "id", Detail: err.Error()}
	}
	if err := s.Owner.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "owner", Detail: err.Error()}
	}
	if s.Owner != owner {
		return StateError{Code: ErrInvalidIdentity, Field: "owner", Detail: fmt.Sprintf("session owner %q does not match document owner %q", s.Owner, owner)}
	}
	if err := s.Ref.Session.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "ref.session", Detail: err.Error()}
	}
	if s.Ref.Owner != s.Owner {
		return StateError{Code: ErrOwnerRefMismatch, Field: "ref.owner", Detail: fmt.Sprintf("session ref owner %q does not match session owner %q", s.Ref.Owner, s.Owner)}
	}
	if s.Revision < 0 {
		return StateError{Code: ErrBadSchema, Field: "revision", Detail: "revision must be non-negative"}
	}
	if (s.Phase == SessionPhaseActive || s.Phase == SessionPhaseStarting) && s.Generation == "" {
		return StateError{Code: ErrMissingGeneration, Field: "generation", Detail: fmt.Sprintf("session phase %q requires a non-empty generation", s.Phase)}
	}
	return nil
}

// ValidateLayout checks a saved layout and its pane tree.
func ValidateLayout(l *LayoutRecord, owner OwnerID) error {
	if err := l.ID.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "id", Detail: err.Error()}
	}
	if l.Owner != owner {
		return StateError{Code: ErrInvalidIdentity, Field: "owner", Detail: fmt.Sprintf("layout owner %q does not match document owner %q", l.Owner, owner)}
	}
	if l.Revision < 0 {
		return StateError{Code: ErrBadSchema, Field: "revision", Detail: "revision must be non-negative"}
	}
	if err := ValidatePaneTree(l.Tree); err != nil {
		return err
	}
	return nil
}

// ValidateWorkspace checks an active workspace and that it references only
// layouts present in the document.
func ValidateWorkspace(w *WorkspaceRecord, owner OwnerID, layouts map[LayoutID]struct{}) error {
	if err := w.ID.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "id", Detail: err.Error()}
	}
	if w.Owner != owner {
		return StateError{Code: ErrInvalidIdentity, Field: "owner", Detail: fmt.Sprintf("workspace owner %q does not match document owner %q", w.Owner, owner)}
	}
	if _, ok := layouts[w.ID]; !ok {
		return StateError{Code: ErrUnknownLayout, Field: "id", Detail: fmt.Sprintf("workspace id %q is not present in layouts", w.ID)}
	}
	if w.Revision < 0 {
		return StateError{Code: ErrBadSchema, Field: "revision", Detail: "revision must be non-negative"}
	}
	if err := ValidatePaneTree(w.Tree); err != nil {
		return err
	}
	return nil
}

// ValidatePaneTree checks structural invariants of a pane tree: split ratios,
// finite numbers, and unique leaf references.
func ValidatePaneTree(tree PaneNode) error {
	leaves, err := collectLeaves(tree)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(leaves))
	for _, ref := range leaves {
		key := ref.MapKey()
		if _, exists := seen[key]; exists {
			return StateError{Code: ErrDuplicateLeaf, Field: "tree", Detail: fmt.Sprintf("duplicate leaf %q", key)}
		}
		seen[key] = struct{}{}
	}
	return nil
}

func collectLeaves(tree PaneNode) ([]SessionRef, error) {
	if tree.IsLeaf() {
		if tree.Ref == nil {
			return nil, StateError{Code: ErrMalformedSplit, Field: "leaf", Detail: "leaf pane has nil ref"}
		}
		if err := tree.Ref.Session.Validate(); err != nil {
			return nil, StateError{Code: ErrInvalidIdentity, Field: "leaf.ref", Detail: err.Error()}
		}
		return []SessionRef{*tree.Ref}, nil
	}
	if !tree.IsSplit() {
		return nil, StateError{Code: ErrMalformedSplit, Field: "type", Detail: fmt.Sprintf("unknown pane node type %q", tree.Type)}
	}
	if tree.Direction != DirectionHorizontal && tree.Direction != DirectionVertical {
		return nil, StateError{Code: ErrMalformedSplit, Field: "direction", Detail: fmt.Sprintf("invalid direction %q", tree.Direction)}
	}
	if err := tree.Ratio.Validate(); err != nil {
		return nil, StateError{Code: ErrInvalidRatio, Field: "ratio", Detail: err.Error()}
	}
	if tree.First == nil || tree.Second == nil {
		return nil, StateError{Code: ErrMalformedSplit, Field: "split", Detail: "split node missing child"}
	}
	first, err := collectLeaves(*tree.First)
	if err != nil {
		return nil, err
	}
	second, err := collectLeaves(*tree.Second)
	if err != nil {
		return nil, err
	}
	return append(first, second...), nil
}

// validateSessionRefIntegrity checks that every leaf ref in tree is owned by
// the document's own owner and names a session ID that has a corresponding
// LocalSessionRecord in the document. This is what catches an orphaned ref:
// a leaf that survived a mutation (e.g. a rename) without its session record
// moving with it, or a leaf pointing at a session that was already removed.
func validateSessionRefIntegrity(tree PaneNode, owner OwnerID, sessions map[SessionID]struct{}, field string) error {
	leaves, err := collectLeaves(tree)
	if err != nil {
		return err
	}
	for _, ref := range leaves {
		if err := validateSingleSessionRefIntegrity(ref, owner, sessions, field); err != nil {
			return err
		}
	}
	return nil
}

// validateSingleSessionRefIntegrity applies the same ownership/existence
// check as validateSessionRefIntegrity to one ref (e.g. an active key)
// instead of a whole tree.
func validateSingleSessionRefIntegrity(ref SessionRef, owner OwnerID, sessions map[SessionID]struct{}, field string) error {
	if ref.Owner != owner {
		return StateError{Code: ErrOrphanedSessionRef, Field: field, Detail: fmt.Sprintf("ref owner %q does not match document owner %q", ref.Owner, owner)}
	}
	if _, ok := sessions[ref.Session]; !ok {
		return StateError{Code: ErrOrphanedSessionRef, Field: field, Detail: fmt.Sprintf("ref %q does not correspond to any session record in the document", ref.MapKey())}
	}
	return nil
}

// CheckSessionMembershipAcrossLayouts returns an error if any session appears
// in more than one layout within the same document. This is a document-level
// invariant, not a per-tree invariant.
func CheckSessionMembershipAcrossLayouts(doc *AppDocument) error {
	membership := make(map[string]LayoutID)
	for _, l := range doc.Layouts {
		leaves, err := collectLeaves(l.Tree)
		if err != nil {
			return err
		}
		for _, ref := range leaves {
			key := ref.MapKey()
			if prev, exists := membership[key]; exists {
				return StateError{Code: ErrSessionInMultipleLayouts, Field: "layouts", Detail: fmt.Sprintf("session %q appears in layouts %q and %q", key, prev, l.ID)}
			}
			membership[key] = l.ID
		}
	}
	for _, w := range doc.Workspaces {
		leaves, err := collectLeaves(w.Tree)
		if err != nil {
			return err
		}
		for _, ref := range leaves {
			key := ref.MapKey()
			if prev, exists := membership[key]; exists {
				return StateError{Code: ErrSessionInMultipleLayouts, Field: "workspaces", Detail: fmt.Sprintf("session %q appears in layout/workspace %q and %q", key, prev, w.ID)}
			}
			membership[key] = w.ID
		}
	}
	return nil
}

// ValidateCommandReceipt checks age and count bounds for command receipts.
func ValidateCommandReceipt(receipt CommandReceipt, now time.Time, count int) error {
	if err := receipt.ID.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "id", Detail: err.Error()}
	}
	if err := receipt.IntentID.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "intent_id", Detail: err.Error()}
	}
	if receipt.Seq < 0 {
		return StateError{Code: ErrBadSchema, Field: "seq", Detail: "seq must be non-negative"}
	}
	if now.Sub(receipt.Created) > MaxCommandReceiptAge {
		return StateError{Code: ErrCommandExpired, Field: "created_at", Detail: fmt.Sprintf("command %q is older than %v", receipt.ID, MaxCommandReceiptAge)}
	}
	if count > MaxPendingCommands {
		return StateError{Code: ErrTooManyCommands, Field: "commands", Detail: fmt.Sprintf("more than %d pending commands", MaxPendingCommands)}
	}
	return nil
}

// ValidatePendingRemoteCreate checks the invariant fields of a pending remote
// create. A nil record is rejected; owner must match the document owner.
func ValidatePendingRemoteCreate(p *PendingRemoteCreateRecord, owner OwnerID) error {
	if p == nil {
		return StateError{Code: ErrBadSchema, Field: "record", Detail: "pending remote create is nil"}
	}
	if err := p.IntentID.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "intent_id", Detail: err.Error()}
	}
	if err := p.Owner.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "owner", Detail: err.Error()}
	}
	if p.Owner != owner {
		return StateError{Code: ErrInvalidIdentity, Field: "owner", Detail: fmt.Sprintf("pending owner %q does not match document owner %q", p.Owner, owner)}
	}
	if err := p.Ref.Session.Validate(); err != nil {
		return StateError{Code: ErrInvalidIdentity, Field: "ref.session", Detail: err.Error()}
	}
	if p.Status == "" {
		return StateError{Code: ErrBadSchema, Field: "status", Detail: "pending remote create status is empty"}
	}
	return nil
}

// ShouldAcceptSnapshot returns true when a snapshot on a transport generation
// should be accepted unconditionally. The first snapshot after a transport
// reset is always accepted so stale generation tracking does not stall recovery.
func ShouldAcceptSnapshot(generation int, currentGeneration int, isFirst bool) bool {
	if isFirst {
		return true
	}
	return generation == currentGeneration
}
