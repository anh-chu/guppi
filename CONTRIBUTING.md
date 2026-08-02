# Contributing to Termyard

Termyard is a Go backend + React/Vite frontend shipped as a single static binary.

## Quick commands

| Task | Command |
| ---- | ------- |
| Build everything | `make build` |
| Build frontend only | `make frontend` |
| Run dev server | `make dev` |
| Run Go tests | `go test ./...` |
| Run Go race tests | `go test -race ./...` |
| Run frontend typecheck | `npm --prefix web run typecheck` |
| Run frontend tests | `npm --prefix web run test:ci` |
| Clean build artifacts | `make clean` |

Frontend tooling is npm only. Run `npm --prefix web ci` for a reproducible install.

## Package manager and versions

- Go 1.25+
- Node 24+ and npm 11+

`web/package.json` pins `packageManager` and the Node engine range. PRs should keep `web/package-lock.json` in sync with version bumps.

## Module and conventions

- Go module: `github.com/anh-chu/termyard`
- CLI framework: urfave/cli v3
- Commands register in `init()` via `common.RegisterCommand()` and are imported as blank imports in `main.go`
- HTTP router: chi v5
- WebSockets: gorilla/websocket
- Logging: logrus; use `LOG_LEVEL=trace|debug|info|warn` to control verbosity
- Environment variables: prefix with `TERMYARD_` (e.g. `TERMYARD_PORT`, `TERMYARD_SOCKET`, `TERMYARD_NO_AUTH`)
- Table-driven tests with `t.Run()` subtests are preferred

## Releases

Stable releases are published by the `v*` tag-triggered GoReleaser workflow. To cut a release:

1. Update `pkg/common/version.go` and `web/package.json` to the new version.
2. Run `./scripts/release.sh` to validate the version files are consistent.
3. Commit, tag `vX.Y.Z`, and push the tag.

The workflow validates that the tag matches `pkg/common/version.go` before publishing.

Nightly builds run on schedule and by manual trigger only; they produce artifacts with 7-day retention and do not mint immutable releases per commit.

See `docs/architecture.md` for the runtime and ownership map.
