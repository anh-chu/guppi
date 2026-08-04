package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/anh-chu/termyard/pkg/state"
)

// v2MaxCommandBodyBytes bounds decoded command request bodies. Session and
// workspace commands carry small, bounded params (refs, ratios, labels); this
// is generous headroom while still rejecting abusive payloads.
const v2MaxCommandBodyBytes = 64 * 1024

// v2BootstrapResponse is the single complete runtime-snapshot response the
// browser needs to render durable UI state without any further sessions,
// hosts, groups, order, or attrs fetches.
//
// Local carries this node's own owner-authoritative catalog. Remote carries
// the latest cached catalog for every peer this node currently has a
// snapshot for (may be empty). Each entry -- Local and every Remote element
// -- carries its own independent Revision; they are never conflated. A peer
// that has gone offline (or been forgotten) simply does not appear in
// Remote on the NEXT bootstrap call; /ws/v2/state is the stream that carries
// an explicit removal signal for a live connection (see catalogRemovedMessage
// in pkg/ws/state_stream.go).
type v2BootstrapResponse struct {
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

// v2ErrorResponse is the stable typed error shape for v2 command endpoints.
// Code is one of invalid_input, not_found, revision_conflict, or
// generation_mismatch and is derived from typed state.StateError values, never
// from string matching.
type v2ErrorResponse struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// registerStateV2Routes mounts the v2 bootstrap and typed command endpoints.
// Callers must apply auth middleware separately, matching the conventions of
// registerSessionsRoutes.
func registerStateV2Routes(r chi.Router, opts *Options) {
	r.Get("/v2/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		handleV2Bootstrap(w, r, opts)
	})
	r.Post("/v2/session-commands", func(w http.ResponseWriter, r *http.Request) {
		handleV2SessionCommand(w, r, opts)
	})
	r.Post("/v2/workspace-commands", func(w http.ResponseWriter, r *http.Request) {
		handleV2WorkspaceCommand(w, r, opts)
	})
}

func handleV2Bootstrap(w http.ResponseWriter, r *http.Request, opts *Options) {
	if opts.V2Catalog == nil {
		writeV2Error(w, http.StatusServiceUnavailable, "not_found", "", "v2 state is not enabled on this server")
		return
	}

	var remoteSource state.RemoteCatalogSource
	if opts.PeerMgr != nil {
		remoteSource = opts.PeerMgr
	}
	agg := state.AggregateCatalog(opts.V2Catalog, remoteSource)
	resp := v2BootstrapResponse{
		Owner:         agg.Local.Owner,
		Revision:      agg.Local.Revision,
		Local:         agg.Local,
		Remote:        agg.Remote,
		Pending:       opts.V2Catalog.PendingCreates(),
		PendingRemote: opts.V2Catalog.PendingRemoteCreates(),
	}
	if opts.PeerMgr != nil {
		resp.Hosts = opts.PeerMgr.GetHosts()
	} else {
		resp.Hosts = []interface{}{}
	}
	if len(agg.Local.Layouts) > 0 {
		if wsRes, err := opts.V2Catalog.WorkspaceSnapshot(agg.Local.Layouts[0].ID); err == nil {
			ws := wsRes.Record
			resp.Workspace = &ws
			resp.Presentations = wsRes.Presentations
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// v2SessionCommandRequest is the strict tagged-union envelope decoded from
// POST /api/v2/session-commands. Unknown fields are rejected.
//
// Ref is OPTIONAL: a create command carries no SessionRef at all (the server
// assigns the SessionID in executeCreate), so the browser never sends `ref`
// for action=create. Actions that target an existing session
// (kill/label/recover/dismiss/retry) must send it; each execute* method
// validates ref.Session and rejects an empty one with a typed error.
type v2SessionCommandRequest struct {
	ID     string           `json:"id,omitempty"`
	Ref    state.SessionRef `json:"ref,omitempty"`
	Action string           `json:"action"`
	Params json.RawMessage  `json:"params,omitempty"`
}

func handleV2SessionCommand(w http.ResponseWriter, r *http.Request, opts *Options) {
	if opts.V2CommandSvc == nil || opts.V2Catalog == nil {
		writeV2Error(w, http.StatusServiceUnavailable, "not_found", "", "v2 session commands are not enabled on this server")
		return
	}
	if !checkV2ContentType(w, r) {
		return
	}

	var req v2SessionCommandRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, v2MaxCommandBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeV2Error(w, http.StatusBadRequest, "invalid_input", "", "malformed request body: "+err.Error())
		return
	}
	if req.Action == "" {
		writeV2Error(w, http.StatusBadRequest, "invalid_input", "action", "action is required")
		return
	}

	id := state.CommandID(req.ID)
	if id == "" {
		id = state.NewCommandID()
	}

	result, err := opts.V2CommandSvc.ExecuteSessionCommand(r.Context(), state.SessionCommand{
		ID:     id,
		Ref:    req.Ref,
		Action: req.Action,
		Params: req.Params,
	})
	if err != nil {
		writeV2CommandError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// v2WorkspaceCommandRequest is the strict tagged-union envelope decoded from
// POST /api/v2/workspace-commands. Unknown fields are rejected.
type v2WorkspaceCommandRequest struct {
	ID     string          `json:"id,omitempty"`
	Layout state.LayoutID  `json:"layout"`
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}

// v2WorkspaceCommandResult acknowledges an accepted workspace command.
// ApplyWorkspaceCommand itself returns only an error, so the response echoes
// back the identity the caller sent; queue acceptance is never reported as
// success -- a non-nil error always maps to a non-2xx typed error response.
type v2WorkspaceCommandResult struct {
	ID       state.CommandID `json:"id"`
	Layout   state.LayoutID  `json:"layout"`
	Accepted bool            `json:"accepted"`
}

func handleV2WorkspaceCommand(w http.ResponseWriter, r *http.Request, opts *Options) {
	if opts.V2Catalog == nil {
		writeV2Error(w, http.StatusServiceUnavailable, "not_found", "", "v2 workspace commands are not enabled on this server")
		return
	}
	if !checkV2ContentType(w, r) {
		return
	}

	var req v2WorkspaceCommandRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, v2MaxCommandBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeV2Error(w, http.StatusBadRequest, "invalid_input", "", "malformed request body: "+err.Error())
		return
	}
	if req.Action == "" {
		writeV2Error(w, http.StatusBadRequest, "invalid_input", "action", "action is required")
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
	if err := opts.V2Catalog.ApplyWorkspaceCommand(cmd); err != nil {
		writeV2CommandError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v2WorkspaceCommandResult{ID: id, Layout: req.Layout, Accepted: true})
}

// checkV2ContentType enforces application/json bodies for command endpoints,
// consistent with other strict-decode routes in this package. It writes a
// typed error and returns false when the request should stop.
func checkV2ContentType(w http.ResponseWriter, r *http.Request) bool {
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
		writeV2Error(w, http.StatusUnsupportedMediaType, "invalid_input", "", "content-type must be application/json")
		return false
	}
	return true
}

// writeV2Error writes a typed JSON error response with the given HTTP status
// and stable code.
func writeV2Error(w http.ResponseWriter, status int, code, field, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v2ErrorResponse{Code: code, Field: field, Message: message})
}

// writeV2CommandError maps a typed state.StateError (or any other error) to a
// stable HTTP status and error code without string matching.
func writeV2CommandError(w http.ResponseWriter, err error) {
	var se state.StateError
	if errors.As(err, &se) {
		status, code := mapV2ErrorCode(se.Code)
		writeV2Error(w, status, code, se.Field, se.Error())
		return
	}
	writeV2Error(w, http.StatusInternalServerError, "invalid_input", "", err.Error())
}

// mapV2ErrorCode maps a typed state error code to the stable v2 HTTP
// status/code pair. Unmapped codes default to invalid_input/400.
func mapV2ErrorCode(code state.ErrorCode) (int, string) {
	switch code {
	case state.ErrRevisionConflict:
		return http.StatusConflict, "revision_conflict"
	case state.ErrGenerationMismatch:
		return http.StatusConflict, "generation_mismatch"
	case state.ErrUnknownLayout, state.ErrMissingTarget:
		return http.StatusNotFound, "not_found"
	default:
		return http.StatusBadRequest, "invalid_input"
	}
}
