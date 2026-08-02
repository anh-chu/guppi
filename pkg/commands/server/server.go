package server

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/server"
)

// Execute assembles the server runtime, waits for it to report ready, and then
// runs the HTTP/WebSocket server until the context is cancelled or the server
// errors out. All monitor lifecycles are owned by Runtime; Execute only wires
// start/stop around server.Run.
func Execute(ctx context.Context, c *cli.Command) error {
	rt, err := newRuntime(c)
	if err != nil {
		return fmt.Errorf("runtime assembly failed: %w", err)
	}
	if err := rt.Start(ctx); err != nil {
		return err
	}

	select {
	case <-rt.Ready():
	case <-ctx.Done():
		rt.Stop()
		return ctx.Err()
	}

	err = server.Run(ctx, rt.Options())
	rt.Stop()
	return err
}

func init() {
	flags := []cli.Flag{
		&cli.IntFlag{
			Name:    "port",
			Aliases: []string{"p"},
			Usage:   "HTTP server port",
			Sources: cli.EnvVars("TERMYARD_PORT"),
			Value:   7654,
		},

		&cli.StringFlag{
			Name:    "socket",
			Usage:   "Unix socket path for local notify CLI (auto-detected if omitted)",
			Sources: cli.EnvVars("TERMYARD_SOCKET"),
		},
		&cli.BoolFlag{
			Name:    "no-auth",
			Usage:   "Disable authentication (not recommended for remote access)",
			Sources: cli.EnvVars("TERMYARD_NO_AUTH"),
		},

		&cli.BoolFlag{
			Name:    "tls",
			Usage:   "Serve HTTPS with a self-signed cert (enables secure-context browser features over LAN)",
			Sources: cli.EnvVars("TERMYARD_TLS"),
		},
		&cli.StringFlag{
			Name:    "tls-cert",
			Usage:   "Path to a TLS certificate file (PEM); pair with --tls-key for a real cert",
			Sources: cli.EnvVars("TERMYARD_TLS_CERT"),
		},
		&cli.StringFlag{
			Name:    "tls-key",
			Usage:   "Path to a TLS private key file (PEM); pair with --tls-cert",
			Sources: cli.EnvVars("TERMYARD_TLS_KEY"),
		},
		&cli.BoolFlag{
			Name:    "debug-pprof",
			Usage:   "Mount /debug/pprof behind auth and loopback (off by default)",
			Sources: cli.EnvVars("TERMYARD_DEBUG_PPROF"),
		},
	}

	cmd := &cli.Command{
		Name:        "server",
		Usage:       "start the termyard web server",
		Description: "starts the web dashboard for monitoring and interacting with coding agent sessions",
		Flags:       flags,
		Action:      Execute,
	}

	common.RegisterCommand(cmd)
}
