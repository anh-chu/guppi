package server

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/ws"
)

// BuildRouter assembles the complete termyard HTTP/WebSocket router.
//
// It validates options, sets up the WebSocket hub, wires cross-machine sync,
// mounts pprof (when enabled), and registers all domain route groups. The hub
// and update-check goroutines are started before returning so the caller only
// needs to pass the returned router to a server.
func BuildRouter(ctx context.Context, opts *Options) (chi.Router, *ws.Hub, error) {
	if err := opts.Validate(); err != nil {
		return nil, nil, err
	}

	hub := setupHub(opts)

	r := chi.NewRouter()
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.StripSlashes)
	r.Use(chimiddleware.RequestID)

	// /debug/pprof is off by default; when enabled it requires session auth
	// (if auth is on) and a loopback source. When disabled, block /debug so the
	// SPA catch-all does not serve index.html for pprof URLs.
	if opts.DebugPprof {
		debugRouter := chi.NewRouter()
		debugRouter.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !auth.IsLoopbackRequest(r) {
					http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		if opts.AuthEnabled {
			debugRouter.Use(auth.Middleware(opts.SessionMgr))
		}
		debugRouter.Mount("/", chimiddleware.Profiler())
		r.Mount("/debug", debugRouter)
	} else {
		r.Get("/debug/*", http.NotFound)
	}

	// Start background goroutines exactly where Run used to start them.
	go hub.Run(ctx)
	go runUpdateChecker(opts)

	registerAPIRoutes(r, opts, hub)
	registerWSRoutes(r, opts, hub)
	registerPeerWSRoutes(r, opts)
	registerProxyFileRoutes(r, opts)
	registerWikiRoutes(r, opts)
	if err := registerFrontendRoutes(r); err != nil {
		return nil, nil, err
	}

	return r, hub, nil
}
