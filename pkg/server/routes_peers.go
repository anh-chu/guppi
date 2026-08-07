package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// registerPeerRoutes mounts protected peer management endpoints under /api.
// Callers must apply auth middleware separately.
func registerPeerRoutes(r chi.Router, opts *Options) {
	r.Get("/peers", func(w http.ResponseWriter, r *http.Request) {
		handleGetPeers(w, r, opts)
	})
	r.Post("/peers", func(w http.ResponseWriter, r *http.Request) {
		handlePostPeers(w, r, opts)
	})
	r.Patch("/peers/{fp}", func(w http.ResponseWriter, r *http.Request) {
		handlePatchPeer(w, r, opts)
	})
	r.Post("/peers/{fp}/reconnect", func(w http.ResponseWriter, r *http.Request) {
		handleReconnectPeer(w, r, opts)
	})
	r.Delete("/peers/{fp}", func(w http.ResponseWriter, r *http.Request) {
		handleDeletePeer(w, r, opts)
	})
}

// registerPeerWSRoutes mounts /ws/peer and /ws/peer-stream. These routes use
// peer mutual-auth and intentionally do NOT use browser session auth.
func registerPeerWSRoutes(r chi.Router, opts *Options) {
	if opts.PeerHandler == nil {
		return
	}
	r.Get("/ws/peer", opts.PeerHandler.HandlePeer)
	r.Get("/ws/peer-stream", opts.PeerHandler.HandlePeerStream)
}
