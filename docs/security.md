# Security Model

This page describes termyard's authentication boundaries, how credentials and
session state are handled, and where extra care is required when running on a
network.

## Authentication boundaries

By default termyard runs with authentication enabled.

- **Password store**: credentials are kept in `~/.config/termyard/auth.json`
  as a bcrypt hash. The password itself is never stored on disk.
- **Session cookie**: after login a `termyard_session` cookie is issued. It is
  `HttpOnly`, `SameSite=Strict`, and uses a 24-hour sliding TTL persisted in
  `~/.config/termyard/sessions.json`. The cookie is refreshed on every
  authenticated request.
- **Protected surface**: the `auth.Middleware` wrapper guards API routes. It
  allows requests that arrive over the local Unix socket unauthenticated and
  otherwise requires a valid session cookie.
- **Public endpoints**: `/api/auth/status` and `/api/version` are intentionally
  public so the browser UI can show the setup state before login.
- **Peer bootstrap**: `POST /api/peers/bootstrap` is authenticated with the
  dashboard password (not a session cookie) because peers must establish trust
  before a session exists.

## Secure cookies, TLS, and proxy behavior

The `Secure` flag on the session cookie is set only when termyard believes the
request arrived over a secure transport. `auth.RequestIsSecure` returns true
whenever the connection uses TLS **or** when a trusted loopback proxy terminates
TLS and sets `X-Forwarded-Proto: https`. Proxies on non-loopback addresses are
not trusted for this purpose.

To enable secure-context browser features over the LAN you can start the server
with TLS:

- `--tls` generates an in-memory ECDSA self-signed certificate valid for roughly
  one year, with SANs for `localhost`, `127.0.0.1`, `::1`, the local hostname,
  and the host's non-loopback IP addresses. The server pins HTTP/1.1 so the
  terminal WebSocket bridge continues to work.
- `--tls-cert` and `--tls-key` load a real certificate/key pair (for example
  from `mkcert` or an ACME issuer).

Without TLS the cookie is not marked `Secure`, which is fine for purely local
use but means sessions may be sent in the clear if the server is exposed to a
network.

### Wiki proxy

The same-origin `/wiki` proxy forwards requests to a local `wiki-viewer-lite`
child. Before forwarding, the proxy strips the `termyard_session` cookie. The
child runs with auth disabled and never reads the cookie; removing it keeps a
separate trust boundary closed so a credential that can impersonate the user is
not passed to an independently installed package.

## Notify token usage

Agent hook events are usually delivered over the Unix socket, which is trusted
because only the local user can write to it. When the socket is unavailable the
notify CLI falls back to HTTP/TCP.

- The server creates a 256-bit hex token with `auth.LoadOrCreateNotifyToken()`
  and stores it at `~/.config/termyard/notify.token` with mode `0600`.
- The notify CLI reads that file with `auth.ReadNotifyToken()` **only when the
  HTTP fallback is used** and attaches `Authorization: Bearer <token>`.
- The server's `POST /api/tool-event` handler skips bearer validation for Unix
  socket requests and validates the header for TCP requests when auth is
  enabled.

The token is machine-local secret. Keep `~/.config/termyard` readable only by
the owner.

## Rate-limit behavior

Authentication-related endpoints use a token-bucket limiter keyed by
`category + client IP`. The current limits are:

| Category    | Capacity | Window |
|-------------|----------|--------|
| `setup`     | 5        | 1 minute |
| `login`     | 10       | 1 minute |
| `bootstrap` | 5        | 1 minute |

When a client exceeds a limit the server responds with
`429 Too Many Requests` and a `Retry-After` header. Idle buckets are evicted
after 15 minutes of inactivity. The tool-event ingest endpoint is **not** part
of this rate limiter; it relies on socket trust or the bearer token.

## Warning: `--no-auth` open mode

`termyard server --no-auth` disables password authentication entirely. In that
mode:

- any client that can reach the TCP port can access the dashboard and its APIs;
- `POST /api/tool-event` accepts TCP events without a bearer token;
- the session cookie middleware is not mounted, so there is no authenticated
  session surface left to protect.

Use `--no-auth` only for isolated local development or on a single-user machine
where the port is not reachable by untrusted hosts. It is **not recommended** for
remote access, shared machines, or production deployments.
