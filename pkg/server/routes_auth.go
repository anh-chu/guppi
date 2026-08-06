package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/portforward"
	"github.com/anh-chu/termyard/pkg/toolevents"
	"github.com/anh-chu/termyard/pkg/ws"
)

// registerAPIRoutes mounts the /api tree, including public auth/version/tool
// endpoints, the protected session/peer/scheduler group, and portforwards.
func registerAPIRoutes(r chi.Router, opts *Options, hub *ws.Hub) {
	r.Route("/api", func(r chi.Router) {
		// Public auth endpoints (no middleware)
		r.Get("/auth/status", auth.StatusHandler(opts.AuthEnabled, opts.PasswordStore))
		if opts.AuthEnabled {
			r.Post("/auth/setup", auth.SetupHandler(opts.PasswordStore, opts.SessionMgr, opts.AuthLimiter))
			r.Post("/auth/login", auth.LoginHandler(opts.PasswordStore, opts.SessionMgr, opts.AuthLimiter))
			r.Post("/auth/logout", auth.LogoutHandler(opts.SessionMgr))
			r.Get("/auth/check", auth.CheckHandler(opts.SessionMgr))
		}

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

		// Version endpoint -- public, no auth required
		r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"version": common.VERSION,
				"commit":  common.COMMIT,
			})
		})
		// Tool event ingest. Unix-socket requests are trusted; TCP requests must
		// present the local notify bearer token when auth is enabled.
		r.Post("/tool-event", func(w http.ResponseWriter, r *http.Request) {
			if !auth.IsUnixSocket(r) && opts.AuthEnabled {
				if !auth.ValidBearer(r, opts.NotifyToken) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprintf(w, `{"error":"unauthorized"}`)
					return
				}
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, 16384))
			if err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}

			var evt toolevents.Event
			if err := json.Unmarshal(body, &evt); err != nil {
				logrus.WithError(err).WithField("request_id", chimiddleware.GetReqID(r.Context())).Trace("tool-event API: JSON parse failed")
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}

			if evt.Tool == "" || evt.Status == "" || evt.Session == "" {
				logrus.WithFields(logrus.Fields{
					"tool":       evt.Tool,
					"status":     evt.Status,
					"session":    evt.Session,
					"request_id": chimiddleware.GetReqID(r.Context()),
				}).Trace("tool-event API: missing required fields")
				http.Error(w, "tool, status, and session are required", http.StatusBadRequest)
				return
			}

			// Stamp local host identity when running in multi-host mode
			if opts.PeerMgr != nil && evt.Host == "" {
				evt.Host = opts.PeerMgr.LocalID()
				evt.HostName = opts.PeerMgr.LocalName()
			}

			// Stamp durable session identity when available
			if opts.CommandSvc != nil && evt.SessionID == "" {
				if ref, ok := opts.CommandSvc.LookupRefByDisplayName(evt.Session); ok {
					evt.SessionID = string(ref.Session)
				}
			}

			if len(evt.Files) > 0 {
				cwd := toolevents.ResolveSessionCWD(opts.CWDResolver, evt.Session)
				evt.Artifacts = toolevents.EnrichArtifacts(evt.Files, cwd, evt.Tool, "hook")
			}

			logrus.WithFields(logrus.Fields{
				"tool":       evt.Tool,
				"status":     evt.Status,
				"session":    evt.Session,
				"window":     evt.Window,
				"pane":       evt.Pane,
				"request_id": chimiddleware.GetReqID(r.Context()),
				"host":       evt.Host,
			}).Debug("received tool event via API")

			opts.Tracker.Record(&evt)
			// Session metadata enrichment happens via the canonical runtime
			// enricher; there is no legacy state manager to shadow-write into.
			w.WriteHeader(http.StatusNoContent)
		})

		// Protected API routes
		r.Group(func(r chi.Router) {
			if opts.AuthEnabled {
				r.Use(auth.Middleware(opts.SessionMgr))
			}

			registerSessionsRoutes(r, opts, hub)
			registerSchedulerRoutes(r, opts)
			registerPeerRoutes(r, opts)
			registerStateV2Routes(r, opts)
		})
		// Port forward registry (local single-host).
		r.Group(func(r chi.Router) {
			if opts.AuthEnabled {
				r.Use(auth.Middleware(opts.SessionMgr))
			}
			r.Get("/portforwards", func(w http.ResponseWriter, r *http.Request) {
				if opts.PortForwardStore == nil {
					http.Error(w, "port forwarding not available", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(opts.PortForwardStore.List())
			})
			r.Post("/portforwards", func(w http.ResponseWriter, r *http.Request) {
				if opts.PortForwardStore == nil {
					http.Error(w, "port forwarding not available", http.StatusServiceUnavailable)
					return
				}
				var req struct {
					Port         int              `json:"port"`
					Label        string           `json:"label"`
					Mode         portforward.Mode `json:"mode"`
					ExternalPort int              `json:"external_port"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Port < 1 || req.Port > 65535 {
					http.Error(w, "port (1-65535) required", http.StatusBadRequest)
					return
				}
				if req.Mode == "" {
					req.Mode = portforward.ModeProxy
				}
				if err := opts.PortForwardStore.Add(req.Port, req.Label, req.Mode, req.ExternalPort); err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(opts.PortForwardStore.List())
			})
			r.Delete("/portforward/{port}", func(w http.ResponseWriter, r *http.Request) {
				if opts.PortForwardStore == nil {
					http.Error(w, "port forwarding not available", http.StatusServiceUnavailable)
					return
				}
				port, err := strconv.Atoi(chi.URLParam(r, "port"))
				if err != nil {
					http.Error(w, "invalid port", http.StatusBadRequest)
					return
				}
				opts.PortForwardStore.Remove(port)
				w.WriteHeader(http.StatusNoContent)
			})
		})

		// PTY benchmark: compare direct PTY throughput and latency
		r.Get("/pty-benchmark", func(w http.ResponseWriter, r *http.Request) {
			handlePTYBenchmark(w, r, opts)
		})
	})
}
