package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/socket"
	"github.com/anh-chu/termyard/pkg/ws"
)

// Run builds the router and starts the HTTP/Unix server. It blocks until ctx is
// cancelled or a fatal serve error occurs.
func Run(ctx context.Context, opts *Options) error {
	logger := logrus.WithField("component", "server")

	r, _, err := BuildRouter(ctx, opts)
	if err != nil {
		return err
	}

	return serveAndWait(ctx, opts, logger, r)
}
func setupHub(opts *Options) *ws.Hub {
	// Use a hub assembled by the runtime when available, otherwise build one
	// here to preserve legacy callers.
	hub := opts.Hub
	if hub == nil {
		hub = ws.NewHub(ws.AsStateSource(opts.StateMgr), opts.Tracker)
		opts.Hub = hub
	}
	var peerActivity ws.ActivitySource
	localHostID := ""
	if opts.PeerMgr != nil {
		peerActivity = opts.PeerMgr
		localHostID = opts.PeerMgr.LocalID()
	}
	if opts.ActivityTracker != nil || peerActivity != nil {
		hub.SetActivityTracker(opts.ActivityTracker, peerActivity, localHostID, false)
	}
	return hub
}

func wireSessionAttrsSync(opts *Options, hub *ws.Hub) {
	// Wire cross-machine sync. Peer subsystem applies inbound snapshots/deltas to
	// server-authoritative stores and bounces browser events through hub.
	if opts.StateMgr != nil {
		localHost := ""
		if opts.Identity != nil {
			localHost = opts.Identity.Fingerprint()
		}
		opts.StateMgr.SetRenameHook(func(oldName, newName string) {
			if opts.AttrsStore != nil {
				migrated, err := opts.AttrsStore.MigrateKey(localHost, oldName, newName)
				if err != nil {
					logrus.WithError(err).WithField("session", newName).Warn("failed to persist migrated session attrs")
				}
				for _, key := range migrated {
					attr := opts.AttrsStore.Get(key)
					fanoutAttrsDeltaToPeers(opts, key, attr)
					if hub != nil {
						hub.BroadcastJSON(map[string]interface{}{"type": "session-attrs-updated", "key": key})
					}
				}
			}
			if opts.OrderStore != nil {
				migrated, err := opts.OrderStore.MigrateKey(localHost, oldName, newName)
				if err != nil {
					logrus.WithError(err).WithField("session", newName).Warn("failed to persist migrated session order")
				}
				for _, key := range migrated {
					order := opts.OrderStore.Get(key)
					fanoutOrderDeltaToPeers(opts, key, order)
					if hub != nil {
						hub.BroadcastJSON(map[string]interface{}{"type": "session-order-updated", "key": key})
					}
				}
			}
			if opts.GroupStore != nil {
				changed, err := opts.GroupStore.MigrateKey(localHost, oldName, newName)
				if err != nil {
					logrus.WithError(err).WithField("session", newName).Warn("failed to persist migrated groups")
				}
				for _, id := range changed {
					group, ok := opts.GroupStore.Get(id)
					if !ok {
						continue
					}
					fanoutGroupDeltaToPeers(opts, id, group)
					if hub != nil {
						hub.BroadcastJSON(map[string]interface{}{"type": "groups-updated", "id": id})
					}
				}
			}
		})
	}
	if opts.AttrsStore != nil {
		sink := attrsStoreAdapter{store: opts.AttrsStore}
		if opts.LinkSupervisor != nil {
			opts.LinkSupervisor.SetAttrsSink(sink)
		}
		if opts.PeerHandler != nil {
			opts.PeerHandler.SetAttrsSink(sink)
		}
	}
	if opts.LinkSupervisor != nil {
		opts.LinkSupervisor.SetBrowserHub(hub)
	}
	if opts.PeerHandler != nil {
		opts.PeerHandler.SetBrowserHub(hub)
	}
	if opts.OrderStore != nil {
		sink := sessionOrderStoreAdapter{store: opts.OrderStore}
		if opts.LinkSupervisor != nil {
			opts.LinkSupervisor.SetOrderSink(sink)
		}
		if opts.PeerHandler != nil {
			opts.PeerHandler.SetOrderSink(sink)
		}
	}
	if opts.GroupStore != nil {
		sink := groupStoreAdapter{store: opts.GroupStore}
		if opts.LinkSupervisor != nil {
			opts.LinkSupervisor.SetGroupSink(sink)
		}
		if opts.PeerHandler != nil {
			opts.PeerHandler.SetGroupSink(sink)
		}
	}
}

func serveAndWait(ctx context.Context, opts *Options, logger *logrus.Entry, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ErrorLog:          log.New(logger.WriterLevel(logrus.WarnLevel), "", 0),
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		// Note: ReadTimeout and WriteTimeout are intentionally omitted.
		// They apply to the underlying net.Conn and would kill long-lived
		// WebSocket connections after the timeout period.
	}

	serverErr := make(chan error, 2)

	tlsCfg, err := tlsConfig(opts)
	if err != nil {
		return err
	}
	srv.TLSConfig = tlsCfg

	// Start TCP listener for browser connections
	tcpAddr := fmt.Sprintf(":%d", opts.Port)
	tcpListener, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("tcp listen: %w", err)
	}

	scheme := "http"
	go func() {
		var serveErr error
		if tlsCfg != nil {
			serveErr = srv.ServeTLS(tcpListener, "", "") // certs come from srv.TLSConfig
		} else {
			serveErr = srv.Serve(tcpListener)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.WithError(serveErr).Error("tcp listen error")
			serverErr <- serveErr
		}
	}()
	if tlsCfg != nil {
		scheme = "https"
	}

	logger.WithField("port", opts.Port).Info("starting termyard server")
	logger.Infof("open %s://localhost:%d in your browser", scheme, opts.Port)
	if opts.AuthEnabled {
		logger.Info("authentication is enabled")
	}

	// Start Unix socket listener for local notify CLI
	var unixListener net.Listener
	socketPath := opts.SocketPath
	if socketPath == "" {
		socketPath = socket.DefaultPath()
	}
	if err := socket.EnsureDir(socketPath); err != nil {
		logger.WithError(err).Warn("failed to create socket directory, notify via socket will be unavailable")
	} else {
		// Remove stale socket file from a previous run
		_ = socket.Cleanup(socketPath)

		unixListener, err = net.Listen("unix", socketPath)
		if err != nil {
			logger.WithError(err).Warn("failed to listen on unix socket, notify via socket will be unavailable")
		} else {
			go func() {
				if err := srv.Serve(unixListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.WithError(err).Error("unix socket listen error")
					serverErr <- err
				}
			}()
			logger.WithField("socket", socketPath).Info("listening on unix socket")
		}
	}

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	}

	logger.Info("shutting down server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.WithError(err).Error("unable to shutdown gracefully")
		return err
	}

	// Clean up socket file
	if unixListener != nil {
		_ = socket.Cleanup(socketPath)
	}

	// Stop any socat processes
	if opts.PortForwardStore != nil {
		opts.PortForwardStore.StopAll()
	}

	if opts.WikiLite != nil {
		opts.WikiLite.Stop()
	}

	return nil
}
