package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/anh-chu/termyard/pkg/state"
)

// maxCommandBodyBytes bounds decoded command request bodies. Session and
// workspace commands carry small, bounded params (refs, ratios, labels); this
// is generous headroom while still rejecting abusive payloads.
const maxCommandBodyBytes = 64 * 1024

// remoteCommandTimeout bounds how long handleSessionCommand waits for a
// forwarded command's reply from a remote peer over the command RPC
// (pkg/peer/rpc.go's SendCommand) before giving up and returning an error to
// the browser. Matches the timeout already used by the legacy REST
// label/rename/kill routes' equivalent remote forwarding in
// routes_sessions.go.
const remoteCommandTimeout = 10 * time.Second

// bootstrapResponse is the single complete runtime-snapshot response the
// browser needs to render durable UI state without any further sessions,
// hosts, groups, order, or attrs fetches.
//
// Local carries this node's own owner-authoritative catalog. Remote carries
// the latest cached catalog for every peer this node currently has a
// snapshot for (may be empty). Each entry -- Local and every Remote element
// -- carries its own independent Revision; they are never conflated. A peer
// that has gone offline (or been forgotten) simply does not appear in
// Remote on the NEXT bootstrap call; /ws/state is the stream that carries
// an explicit removal signal for a live connection (see catalogRemovedMessage
// in pkg/ws/state_stream.go).
type bootstrapResponse struct {
	Owner         state.OwnerID                     `json:"owner"`
	Revision      int64                             `json:"revision"`
	Local         state.OwnerCatalogSnapshot        `json:"local"`
	Remote        []state.OwnerCatalogSnapshot      `json:"remote,omitempty"`
	Hosts         interface{}                       `json:"hosts"`
	Workspace     *state.WorkspaceRecord            `json:"workspace,omitempty"`
	Presentations []state.PresentationRecord        `json:"presentations,omitempty"`
	Pending       []state.PendingCreateRecord       `json:"pending"`
	PendingRemote []state.PendingRemoteCreateRecord `json:"pending_remote,omitempty"`
}

// commandErrorResponse is the stable typed error shape for command endpoints.
// Code is one of invalid_input, not_found, revision_conflict, or
// generation_mismatch and is derived from typed state.StateError values, never
// from string matching.
type commandErrorResponse struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// registerStateRoutes mounts the bootstrap and typed command endpoints.
// Callers must apply auth middleware separately, matching the conventions of
// registerSessionsRoutes.
func registerStateRoutes(r chi.Router, opts *Options) {
	r.Get("/state/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		handleBootstrap(w, r, opts)
	})
	r.Post("/state/session-commands", func(w http.ResponseWriter, r *http.Request) {
		handleSessionCommand(w, r, opts)
	})
	r.Post("/state/workspace-commands", func(w http.ResponseWriter, r *http.Request) {
		handleWorkspaceCommand(w, r, opts)
	})
}

func handleBootstrap(w http.ResponseWriter, r *http.Request, opts *Options) {
	if opts.Catalog == nil {
		writeError(w, http.StatusServiceUnavailable, "not_found", "", "state is not enabled on this server")
		return
	}

	var remoteSource state.RemoteCatalogSource
	if opts.PeerMgr != nil {
		remoteSource = opts.PeerMgr
	}
	agg := state.AggregateCatalog(opts.Catalog, remoteSource)
	resp := bootstrapResponse{
		Owner:         agg.Local.Owner,
		Revision:      agg.Local.Revision,
		Local:         agg.Local,
		Remote:        agg.Remote,
		Pending:       opts.Catalog.PendingCreates(),
		PendingRemote: opts.Catalog.PendingRemoteCreates(),
	}
	if opts.PeerMgr != nil {
		resp.Hosts = opts.PeerMgr.GetHosts()
	} else {
		resp.Hosts = []interface{}{}
	}
	if len(agg.Local.Layouts) > 0 {
		if wsRes, err := opts.Catalog.WorkspaceSnapshot(agg.Local.Layouts[0].ID); err == nil {
			ws := wsRes.Record
			resp.Workspace = &ws
			resp.Presentations = wsRes.Presentations
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// sessionCommandRequest is the strict tagged-union envelope decoded from
// POST /api/state/session-commands. Unknown fields are rejected.
//
// Ref is OPTIONAL: a create command carries no SessionRef at all (the server
// assigns the SessionID in executeCreate), so the browser never sends `ref`
// for action=create. Actions that target an existing session
// (kill/label/recover/dismiss/retry) must send it; each execute* method
// validates ref.Session and rejects an empty one with a typed error.
type sessionCommandRequest struct {
	ID     string           `json:"id,omitempty"`
	Ref    state.SessionRef `json:"ref,omitempty"`
	Action string           `json:"action"`
	Params json.RawMessage  `json:"params,omitempty"`

	// TargetOwner is ONLY meaningful for action=create. It names the owner
	// the browser wants the new session created on (populated from the host
	// the user picked in the New Session modal); empty (the common case)
	// means "create locally", matching the existing unchanged behavior.
	// Every other action already carries its target via Ref.Owner (see the
	// forwarding branch below) and ignores this field.
	TargetOwner state.OwnerID `json:"target_owner,omitempty"`
}

func handleSessionCommand(w http.ResponseWriter, r *http.Request, opts *Options) {
	if opts.CommandSvc == nil || opts.Catalog == nil {
		writeError(w, http.StatusServiceUnavailable, "not_found", "", "session commands are not enabled on this server")
		return
	}
	if !checkContentType(w, r) {
		return
	}

	var req sessionCommandRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "", "malformed request body: "+err.Error())
		return
	}
	if req.Action == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "action", "action is required")
		return
	}

	id := state.CommandID(req.ID)
	if id == "" {
		id = state.NewCommandID()
	}

	cmd := state.SessionCommand{
		ID:     id,
		Ref:    req.Ref,
		Action: req.Action,
		Params: req.Params,
	}

	// A create targeting a different owner (the browser's New Session modal
	// lets the user pick any known host) must be routed through the
	// distributed remote-create coordinator/RPC (peer.Manager.
	// RequestRemoteCreate), never executed against this node's own catalog:
	// that would silently create the session on the WRONG node while the
	// browser believes it asked for TargetOwner. Without this branch,
	// TargetOwner was accepted from the wire but never consulted, so every
	// remote-host create silently fell back to local.
	if req.Action == state.ActionCreate && req.TargetOwner != "" && req.TargetOwner != opts.CommandSvc.Owner() {
		if opts.PeerMgr == nil {
			writeError(w, http.StatusServiceUnavailable, "peer_offline", "target_owner", "this node has no peer manager configured; cannot reach remote owner")
			return
		}
		var params state.CreateParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_input", "params", "malformed create params: "+err.Error())
				return
			}
		}
		remoteReq := state.RemoteCreateRequest{
			IntentID:       id,
			Requester:      opts.CommandSvc.Owner(),
			Name:           params.Name,
			Shell:          params.Shell,
			Cwd:            params.Cwd,
			WorktreeBranch: params.WorktreeBranch,
			Cols:           params.Cols,
			Rows:           params.Rows,
			LayoutID:       params.LayoutID,
			Direction:      params.Direction,
			NewFirst:       params.NewFirst,
			AgentType:      params.AgentType,
		}
		if params.Target != nil {
			remoteReq.Target = params.Target
		}
		ctx, cancel := context.WithTimeout(r.Context(), remoteCommandTimeout)
		defer cancel()
		result, err := opts.PeerMgr.RequestRemoteCreate(ctx, req.TargetOwner, remoteReq)
		if err != nil {
			var se state.StateError
			if !errors.As(err, &se) {
				writeError(w, http.StatusServiceUnavailable, "peer_offline", "target_owner", "remote create failed: "+err.Error())
				return
			}
			writeCommandError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
		return
	}

	// Forward to the remote peer that actually owns this ref instead of
	// running it against our own catalog. Session commands sent by the
	// browser can target ANY session currently rendered in the sidebar,
	// including remote-owned ones (see App.tsx's remote-pane rendering);
	// only action=create structurally has no Ref (see sessionCommandRequest
	// doc comment above) and is always local. Without this branch, every
	// kill/label/recover/dismiss/retry against a remote session silently
	// mutated (or 404'd against) the WRONG node's catalog -- the local one --
	// even though the session was already visibly attached and usable in the
	// browser.
	if req.Ref.Owner != "" && req.Ref.Owner != opts.CommandSvc.Owner() {
		if opts.PeerMgr == nil {
			writeError(w, http.StatusServiceUnavailable, "peer_offline", "ref.owner", "this node has no peer manager configured; cannot reach remote owner")
			return
		}
		peerID := opts.PeerMgr.PeerIDForOwner(req.Ref.Owner)
		if peerID == "" {
			writeError(w, http.StatusServiceUnavailable, "peer_offline", "ref.owner", "remote owner is not currently reachable")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), remoteCommandTimeout)
		defer cancel()
		result, err := opts.PeerMgr.SendCommand(ctx, peerID, cmd)
		if err != nil {
			// SendCommand's own transport failures (dead connection, full
			// queue, RPC timeout) are plain errors, not state.StateError --
			// writeCommandError's fallback would otherwise misreport them
			// as a 500 invalid_input server bug rather than the transient
			// peer-reachability issue they actually are.
			var se state.StateError
			if !errors.As(err, &se) {
				writeError(w, http.StatusServiceUnavailable, "peer_offline", "ref.owner", "remote command failed: "+err.Error())
				return
			}
			writeCommandError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
		return
	}

	result, err := opts.CommandSvc.ExecuteSessionCommand(r.Context(), cmd)
	if err != nil {
		writeCommandError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// workspaceCommandRequest is the strict tagged-union envelope decoded from
// POST /api/state/workspace-commands. Unknown fields are rejected.
type workspaceCommandRequest struct {
	ID     string          `json:"id,omitempty"`
	Layout state.LayoutID  `json:"layout"`
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}

// workspaceCommandResult acknowledges an accepted workspace command.
// ApplyWorkspaceCommand itself returns only an error, so the response echoes
// back the identity the caller sent; queue acceptance is never reported as
// success -- a non-nil error always maps to a non-2xx typed error response.
type workspaceCommandResult struct {
	ID       state.CommandID `json:"id"`
	Layout   state.LayoutID  `json:"layout"`
	Accepted bool            `json:"accepted"`
}

func handleWorkspaceCommand(w http.ResponseWriter, r *http.Request, opts *Options) {
	if opts.Catalog == nil {
		writeError(w, http.StatusServiceUnavailable, "not_found", "", "workspace commands are not enabled on this server")
		return
	}
	if !checkContentType(w, r) {
		return
	}

	var req workspaceCommandRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "", "malformed request body: "+err.Error())
		return
	}
	if req.Action == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "action", "action is required")
		return
	}

	id := state.CommandID(req.ID)
	if id == "" {
		id = state.NewCommandID()
	}

	cmd := state.WorkspaceCommand{
		ID:     id,
		Layout: req.Layout,
		Action: req.Action,
		Params: req.Params,
	}
	if err := opts.Catalog.ApplyWorkspaceCommand(cmd); err != nil {
		writeCommandError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(workspaceCommandResult{ID: id, Layout: req.Layout, Accepted: true})
}

// checkContentType enforces application/json bodies for command endpoints,
// consistent with other strict-decode routes in this package. It writes a
// typed error and returns false when the request should stop.
func checkContentType(w http.ResponseWriter, r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return true
	}
	// Allow an optional charset parameter (e.g. "application/json; charset=utf-8").
	for i := 0; i < len(ct); i++ {
		if ct[i] == ';' {
			ct = ct[:i]
			break
		}
	}
	if ct != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_input", "", "content-type must be application/json")
		return false
	}
	return true
}

// writeError writes a typed JSON error response with the given HTTP status
// and stable code.
func writeError(w http.ResponseWriter, status int, code, field, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(commandErrorResponse{Code: code, Field: field, Message: message})
}

// writeCommandError maps a typed state.StateError (or any other error) to a
// stable HTTP status and error code without string matching.
func writeCommandError(w http.ResponseWriter, err error) {
	var se state.StateError
	if errors.As(err, &se) {
		status, code := mapErrorCode(se.Code)
		writeError(w, status, code, se.Field, se.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "invalid_input", "", err.Error())
}

// mapErrorCode maps a typed state error code to the stable HTTP
// status/code pair. Unmapped codes default to invalid_input/400.
func mapErrorCode(code state.ErrorCode) (int, string) {
	switch code {
	case state.ErrRevisionConflict:
		return http.StatusConflict, "revision_conflict"
	case state.ErrGenerationMismatch:
		return http.StatusConflict, "generation_mismatch"
	case state.ErrUnknownLayout, state.ErrMissingTarget:
		return http.StatusNotFound, "not_found"
	case state.ErrWorkspaceOwnerOffline, state.ErrLegacyPeerUnsupported:
		// Both mean "the remote owner/peer this command needed to reach is
		// not currently usable" (no live connection bound to that owner, or
		// the bound peer does not support this command) -- the same transient,
		// retry-later condition as the plain-error peer_offline branch each
		// remote-forwarding call site already handles for transport
		// failures. Mapping these to 400 invalid_input would tell the
		// browser this was a malformed request, which it was not.
		return http.StatusServiceUnavailable, "peer_offline"
	default:
		return http.StatusBadRequest, "invalid_input"
	}
}
