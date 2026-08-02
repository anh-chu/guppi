package server

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/anh-chu/termyard/pkg/auth"
)

// registerPeerBootstrapRoute mounts the public, password-protected peer
// bootstrap endpoint at /api/peers/bootstrap.
func registerPeerBootstrapRoute(r chi.Router, opts *Options) {
	bootstrapLimit := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if opts.AuthLimiter == nil {
				next.ServeHTTP(w, r)
				return
			}
			if ok, retry := opts.AuthLimiter.Allow("bootstrap", auth.ClientIP(r)); !ok {
				seconds := int(retry.Seconds())
				if seconds < 1 {
					seconds = 1
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprintf(w, `{"error":"rate limit","retry_after":%d}`, seconds)
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	// Peer bootstrap endpoint -- password-authenticated (no session cookie).
	// Lets two nodes establish mutual trust via the dashboard password.
	r.With(bootstrapLimit).Post("/peers/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		handlePeersBootstrap(w, r, opts)
	})
}

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
