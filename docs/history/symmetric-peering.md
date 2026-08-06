# Symmetric Peer-to-Peer Refactor — Implementation Plan

> **Status**: Plan (not yet implemented)
> **Author**: derived from user discussion 2026-05-29
> **Target audience**: implementing agent with **zero prior context** about termyard

This document is the **complete specification** for replacing termyard's current
asymmetric hub/peer model with a symmetric peer-to-peer model, removing all
TLS/cert plumbing, and exposing all multi-host functionality through the web UI.
Everything needed to implement it is in this file (plus the source files it
references). Do not skim — every section matters.

---

## 0. Glossary (read first)

| Term                   | Meaning                                                                                                                                                                                                                                                                                                                                      |
| ---------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **node**               | One running `termyard server` process. Every machine is just a node. There is no "hub" or "peer-role" distinction anymore.                                                                                                                                                                                                                      |
| **identity**           | A persistent ed25519 keypair stored at `~/.config/termyard/identity.json`. Already exists. Loaded by `identity.LoadOrCreate(hostname)` in `pkg/commands/server/server.go`.                                                                                                                                                                      |
| **fingerprint**        | Short ID derived from a node's public key. `pkg/identity/identity.go::Fingerprint()`.                                                                                                                                                                                                                                                        |
| **peer record**        | An entry in `~/.config/termyard/peers.json` describing another node we trust. Loaded via `identity.NewPeerStore()`.                                                                                                                                                                                                                             |
| **dashboard password** | The user-set password protecting the web UI. Stored as bcrypt hash in `~/.config/termyard/auth.json`. Managed by `pkg/auth/auth.go`.                                                                                                                                                                                                            |
| **bootstrap**          | One-time exchange that establishes mutual trust between two previously-unknown nodes. After bootstrap, only ed25519 identity is used (password is never sent again).                                                                                                                                                                         |
| **link**               | The active WebSocket connection between two paired nodes.                                                                                                                                                                                                                                                                                    |
| **dialer / listener**  | For a given pair (A, B): the node where the user originally clicked **Connect** is the **dialer** (initiates and maintains the WebSocket). The other side, which received the bootstrap POST, is the **listener**. Stored per-peer as `InitiatedByUs bool` in the local peer record. See §3.2 for the simultaneous-initiate race resolution. |

---

## 1. Goals & Non-Goals

### Goals

1. Both machines run identical `termyard server` — no flags, no CLI ceremony.
2. From the dashboard of node A, user enters `host:port` and **node B's
   dashboard password** to connect.
3. Connection is **bidirectional**: once A↔B is up, A sees B's sessions
   **and** B sees A's sessions, simultaneously.
4. If the link drops, both nodes auto-retry forever (with backoff) until the
   user explicitly disables the link.
5. Either node going offline must not impair the other; local sessions stay
   functional.
6. **All multi-host config happens through the UI**. No `termyard pair`, no
   `--hub`, no `--tls-san`, no `termyard peers list` is needed by the user.
7. **TLS and self-signed cert generation are removed from termyard entirely.**
   termyard serves plain HTTP. Users put a reverse proxy (Caddy, Tailscale,
   nginx) in front if they want TLS. The peer-to-peer link runs over
   plain WebSocket (`ws://`) — encryption is the user's network's job.

### Non-Goals (out of scope for this refactor)

- Transitive peer discovery (A↔B↔C does **not** make A see C's sessions through B).
- Any UI for editing the identity keypair manually.
- Migration of existing paired peers from old format to new format (we will
  simply invalidate them — user re-pairs through UI).

---

## 2. Current State (what exists today)

### 2.1 Files / packages already in place

| Path                                      | Role                                                                                                                                                                                                   |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `pkg/identity/identity.go`                | ed25519 keypair generation & sign/verify. **Keep.**                                                                                                                                                    |
| `pkg/identity/peers.go`                   | `Peer` struct, `PeerStore` with `~/.config/termyard/peers.json`. **Modify** (add fields, drop TLS fields).                                                                                                |
| `pkg/identity/pairing.go`                 | Time-limited pairing codes. **Delete.**                                                                                                                                                                |
| `pkg/peer/manager.go`                     | `Manager` — aggregates `HostState` from local tmux + remote nodes. Already symmetric. **Keep, minor edits.**                                                                                           |
| `pkg/peer/handler.go`                     | HTTP/WS handler for incoming peer connections. Implements challenge-response auth on `/ws/peer`. Also has `HandlePairing` for `/api/pair/complete`. **Modify** (drop pairing, generalize accept side). |
| `pkg/peer/client.go`                      | Outgoing peer client — dials hub, runs reconnect loop, has TLS cert pinning logic. **Heavy modification** (strip TLS, generalize to peer-to-peer, remove "hub" framing).                               |
| `pkg/peer/protocol.go`                    | Wire message types. **Keep, minor additions.**                                                                                                                                                         |
| `pkg/peer/pty_relay.go`                   | Relays browser WS ↔ peer WS for remote PTY. **Keep** (already host-id keyed).                                                                                                                          |
| `pkg/peer/pty_manager.go`                 | On the spawn side, manages local PTYs and connects PTY WS back to the dialer. **Modify** (drop TLS config call).                                                                                       |
| `pkg/auth/auth.go`                        | Dashboard password (bcrypt) + session tokens. **Keep**, add a `VerifyConstantTime` helper if not present (bcrypt.Compare is already CT).                                                               |
| `pkg/tlscert/`                            | Self-signed cert generation, hot-reload. **DELETE THIS ENTIRE PACKAGE.**                                                                                                                               |
| `pkg/server/server.go`                    | Main HTTP server. Mounts all routes including `/ws/peer`, `/api/pair/complete`, TLS listener. **Modify** (drop TLS, drop pair routes, add new peers routes).                                           |
| `pkg/commands/server/server.go`           | `termyard server` CLI command. **Modify** (drop `--hub`, `--tls-*`, `--insecure`, `--local-only`, `--no-tls`).                                                                                            |
| `pkg/commands/pair/pair.go`               | `termyard pair` CLI. **DELETE.**                                                                                                                                                                          |
| `pkg/commands/peers/peers.go`             | `termyard peers list/remove` CLI. **DELETE.** (UI replaces it.)                                                                                                                                           |
| `web/src/components/TrustCertificate.tsx` | Frontend cert-trust flow. **DELETE.**                                                                                                                                                                  |
| `web/src/App.tsx`                         | Mounts `TrustCertificate`. **Edit** (remove import + route).                                                                                                                                           |
| `web/src/components/Settings.tsx`         | Settings page. **Edit** (add Peers panel).                                                                                                                                                             |
| `main.go`                                 | Registers commands. **Edit** (remove `pair` and `peers` imports).                                                                                                                                      |
| `docs/multi-host.md`                      | Existing docs assume hub/peer + TLS. **Rewrite.**                                                                                                                                                      |
| `README.md`                               | Mentions `--hub`, `TERMYARD_HUB`, etc. **Edit.**                                                                                                                                                          |

### 2.2 Existing wire protocol (keep, with additions)

Messages on `/ws/peer` (envelope = `{type: string, payload: object}`):

Inbound (sent from dialer to listener):

- `auth` — `{public_key, signature}` (response to challenge)
- `state-update` — full session snapshot from sender
- `state-event` — incremental change notification
- `tool-event` — agent tool event from sender
- `activity-update` — sparkline data from sender
- `stats` — system stats from sender

Outbound (sent from listener to dialer):

- `challenge` — `{challenge: base64-32-bytes}`
- `auth-ok` / `auth-fail` — `auth-fail` carries `{reason: string}`; reserved
  reasons: `"unknown peer"`, `"already connected"` (§3.2), `"invalid signature"`,
  `"expected auth message"`, `"invalid auth payload"`
- `peer-state` — aggregated state of _all_ hosts known to listener
- `peer-connected` / `peer-disconnected` — notify
- `pty-open` / `pty-close` / `pty-resize` — request dialer to spawn/manage local PTY for a remote browser
- `session-action` — request dialer to execute a tmux action
- `request-state` — ask dialer for fresh snapshot

**New for symmetric model** (either direction):

- `forget` — sender is forgetting the receiver. Receiver should remove the
  sender from its peer store and close the link. Payload: `{}`. Const name:
  `MsgForget`. See §10 entry 11 for behaviour.

**Problem with current model**: the listener never sends its own
`state-update`/`tool-event`/`activity-update`/`stats` — only the dialer pushes
data. The listener only sends `peer-state` snapshots. This is fine because
the listener IS local to itself, but in the new symmetric model, when A
dials B, **A needs to also receive B's local state**, not just B's aggregated
view of "other peers".

This is solved by making the listener send the same family of messages back
(`state-update`, `state-event`, `tool-event`, `activity-update`, `stats`)
stamped with the listener's own host ID. See §6.

---

## 3. New Architecture

### 3.1 Bootstrap flow (one-time, from UI)

User opens dashboard on **node A** (already logged in). Clicks
**Settings → Peers → Connect to peer**. Enters:

- `address`: `devvm-b.local` or `192.168.1.50:7654` (port optional, default 7654)
- `password`: node B's dashboard password
- `auto_reconnect`: checkbox (default on)

A's browser → A's backend:

```
POST /api/peers
Content-Type: application/json
Cookie: <A's session cookie>

{
  "address": "devvm-b.local:7654",
  "password": "<B's dashboard password>",
  "auto_reconnect": true
}
```

A's backend then makes a server-to-server call to B:

```
POST http://devvm-b.local:7654/api/peers/bootstrap
Content-Type: application/json

{
  "password":      "<B's dashboard password>",
  "name":          "<A's hostname>",
  "public_key":    "<A's ed25519 pubkey, base64>",
  "fingerprint":   "<A's fingerprint>"
}
```

B's `/api/peers/bootstrap` handler:

1. Verify password via `passwordStore.Verify(req.password)`. If wrong: 401.
2. If `auth.HasPassword() == false`, return 503 (B must complete its own
   first-run setup before accepting peers).
3. Look up `peerStore.GetByPublicKey(req.public_key)`. If exists: just refresh
   the address and return 200 (idempotent).
4. Otherwise add a new `Peer` record with:
   ```
   { name, public_key, paired_at: now, address: <remote_addr>, enabled: true }
   ```
5. Respond:
   ```json
   {
     "name": "<B's hostname>",
     "public_key": "<B's ed25519 pubkey, base64>",
     "fingerprint": "<B's fingerprint>"
   }
   ```

A's backend, on success:

1. Add B to A's `peerStore` with same shape.
2. Save `address`, `enabled=true`.
3. Start a reconnector goroutine for B (see §3.3). Because A is the side that
   ran Connect, A's record has `InitiatedByUs = true` and A is the dialer.
   B's record (created by its `/api/peers/bootstrap` handler) has
   `InitiatedByUs = false`, so B does not run a reconnector for A — it just
   waits for inbound `/ws/peer` connections.
4. Return 201 with the new peer record (without `public_key` in full —
   trimmed for display) so the UI can render it.

Bootstrap is **one-shot**: password is never stored, never re-sent, and not
needed for reconnect or to handle rotation. From that moment on, every
WebSocket handshake uses pure ed25519 challenge-response.

### 3.2 Dialer / listener determination

The **dialer is whichever node's user clicked "Connect to another machine"**.
This matches the user's mental model — _"I told A to connect to B, so A reaches
out to B"_ — and correctly handles asymmetric reachability. If only A can reach
B (B is behind NAT), the user runs Connect on A, A becomes the dialer, done.

Stored on each peer record as `InitiatedByUs bool`:

- On the side that called `POST /api/peers` (user clicked Connect): `InitiatedByUs = true`.
- On the side that received `POST /api/peers/bootstrap`: `InitiatedByUs = false`.

**The reconnector only runs where `InitiatedByUs == true`.** The other side
simply accepts inbound connections on its existing `/ws/peer` endpoint.

**Simultaneous-initiate race**: if user A clicks Connect to B _and_ user B
clicks Connect to A around the same moment, both ends end up with
`InitiatedByUs = true` and both reconnectors dial. Two `/ws/peer` connections
appear per pair. Resolution:

1. On the accept side (`handler.HandlePeer`), after challenge-response auth
   succeeds, check `peerMgr.GetPeerConnection(fp)`. If a session is already
   live for this peer fingerprint, close the **newer** one with
   `MsgAuthFail{ reason: "already connected" }` and return.
2. The dialer goroutine that receives `auth-fail` with reason
   `"already connected"` treats it as **terminal** (no retry). It also flips
   its own peer record to `InitiatedByUs = false` and persists. The
   surviving connection (driven by the other side) is now the canonical link.
3. Future restarts: the now-`false` side will simply listen; the still-`true`
   side keeps dialing. The race is resolved permanently after first contact.

Self-pair guard: if `req.public_key == localIdentity.PublicKey`, the
bootstrap handler returns 400 with message `"cannot pair with self"`.

### 3.3 Reconnector (per-peer goroutine on dialer side)

For each enabled peer where we are the dialer, run:

```
State machine:
  Idle ──(enabled=true)──▶ Dialing
  Dialing ──(success)──▶ Connected
  Dialing ──(fail)──▶ Backoff ──(timer)──▶ Dialing
  Connected ──(read/write error)──▶ Backoff ──(timer)──▶ Dialing
  *  ──(enabled=false)──▶ Idle
```

Backoff: start 1s, double on each failure, cap at 30s, full jitter (±25%).
Reset to 1s after any connection that stayed up ≥ 30s.

The reconnector is owned by a new top-level type `peer.LinkSupervisor`
(§5.1). One supervisor manages N reconnectors. Adding a peer registers it;
removing a peer cancels its context.

### 3.4 Session lifecycle on the link

Once the WebSocket is up and authenticated:

1. **Both sides** start sending their own:
   - `state-update` on connect + on every local change
   - `state-event` on local change (incremental)
   - `tool-event` on every local tool event
   - `activity-update` every 5 s
   - `stats` every 30 s
   - `peer-state` from the listener side every time anything else changes
     (to expose other connected peers — but transitive sharing is OFF;
     this snapshot only contains the listener's own host and the dialer.
     See §3.5.)

2. **Either side** can request PTY operations on the other:
   - `pty-open {stream_id, session, cols, rows}` — peer should spawn a tmux
     attach for `session`, then dial back `/ws/peer-pty?stream=<stream_id>`
     to pipe terminal data.
   - `pty-resize`, `pty-close`.

3. **Either side** can request `session-action` (new session, rename, kill,
   select-window). Already implemented; no change needed.

4. Liveness: ping every 15 s, read deadline of 30 s. Already implemented in
   both `handler.go` and `client.go`.

### 3.5 No transitivity (phase 1)

If A is connected to B, and A is also connected to C, then **B does not see C
through A**. Each node's dashboard only shows hosts to which it has a direct
link. This keeps the protocol simple and avoids loop detection.

Concretely: when the listener emits `peer-state` to the dialer, the `hosts`
array contains **only the listener itself**, not every host the listener
knows about. (Currently it sends `GetHosts()` which returns everything —
this needs to be filtered.)

---

## 4. Data Model Changes

### 4.1 `identity.Peer` (`pkg/identity/peers.go`)

**Before:**

```go
type Peer struct {
    Name       string    `json:"name"`
    PublicKey  string    `json:"public_key"`
    PairedAt   time.Time `json:"paired_at"`
    TLSCertPEM string    `json:"tls_cert_pem,omitempty"`
    CACertPEM  string    `json:"ca_cert_pem,omitempty"`
}
```

**After:**

```go
type Peer struct {
    Name        string    `json:"name"`
    PublicKey   string    `json:"public_key"`
    PairedAt    time.Time `json:"paired_at"`
    Address       string    `json:"address"`              // host:port last successfully used
    Enabled       bool      `json:"enabled"`              // auto-reconnect on/off (governs outbound dials only)
    InitiatedByUs bool      `json:"initiated_by_us"`      // true ⇒ we dial; false ⇒ we wait for inbound
    LastSeen      time.Time `json:"last_seen,omitempty"`  // updated on every successful connect
}
```

Remove `TLSCertPEM`, `CACertPEM`, `TLSConfig(insecure bool)`, and
`UpdateTLSCert`. Add:

```go
// UpdateAddress sets the address last successfully used to reach this peer
func (ps *PeerStore) UpdateAddress(publicKey, address string) error

// SetEnabled toggles auto-reconnect for a peer
func (ps *PeerStore) SetEnabled(publicKey string, enabled bool) error

// SetInitiatedByUs flips the dialer/listener role for a peer. Used by the
// "already connected" race-resolution path in §3.2.
func (ps *PeerStore) SetInitiatedByUs(publicKey string, initiated bool) error

// UpdateLastSeen sets the LastSeen timestamp to now
func (ps *PeerStore) UpdateLastSeen(publicKey string) error

// RemoveByPublicKey removes a peer by public key (replaces Remove which used name)
func (ps *PeerStore) RemoveByPublicKey(publicKey string) error
```

**Migration**: If existing `peers.json` files contain `tls_cert_pem` or
`ca_cert_pem` fields, just ignore them (Go's JSON decoder will drop unknown
fields — make sure there's no `DisallowUnknownFields()` call). Existing
records remain valid. On load:

- If `Enabled` field absent ⇒ default to `true` so existing pairings keep working.
- If `InitiatedByUs` field absent ⇒ default to `true`. Both sides will then
  set themselves as dialer; the §3.2 race-resolution path reconciles them on
  first successful connection. Acceptable one-time wobble.

Detection: read raw JSON as `map[string]any` first to check key presence,
then unmarshal into the typed struct. If migration applied, re-save the file.

### 4.2 Removed types

- `identity.PairingCode` and `identity.PairingManager` (delete file
  `pkg/identity/pairing.go`).
- All of `pkg/tlscert/`.

### 4.3 `peer.HostState` (`pkg/peer/manager.go`)

Add an `Address` field so the UI can display where this host is reachable:

```go
type HostState struct {
    // ... existing fields ...
    Address string // peer's network address (empty for local)
}
```

### 4.4 `server.Options` (`pkg/server/server.go`)

Remove:

- `TLSConfig *tls.Config`
- `TLSFingerprint string`
- `CertReloader *tlscert.CertReloader`
- `CACertPEM string`
- `PairingMgr *identity.PairingManager`
- `LocalOnly bool` (no longer makes sense — every node is symmetric)

Add:

- `LinkSupervisor *peer.LinkSupervisor`

---

## 5. New Code

### 5.1 `pkg/peer/supervisor.go` (NEW FILE)

```go
package peer

import (
    "context"
    "fmt"
    "math/rand"
    "sync"
    "time"

    "github.com/sirupsen/logrus"

    "github.com/anh-chu/termyard/pkg/activity"
    "github.com/anh-chu/termyard/pkg/identity"
    "github.com/anh-chu/termyard/pkg/state"
    "github.com/anh-chu/termyard/pkg/tmux"
    "github.com/anh-chu/termyard/pkg/toolevents"
)

// LinkSupervisor owns per-peer reconnector goroutines. It is the only place
// outgoing peer connections are managed.
type LinkSupervisor struct {
    mu       sync.Mutex
    links    map[string]*peerLink // keyed by peer public key

    identity   *identity.Identity
    peerStore  *identity.PeerStore
    localMgr   *state.Manager
    peerMgr    *Manager
    actTracker *activity.Tracker
    toolTracker *toolevents.Tracker
    tmuxClient *tmux.Client
    tmuxPath   string

    rootCtx context.Context // canceled on shutdown
}

type peerLink struct {
    peer       identity.Peer
    cancel     context.CancelFunc
    statusMu   sync.RWMutex
    status     LinkStatus
    lastErr    string
    nextRetry  time.Time
}

type LinkStatus string

const (
    StatusIdle       LinkStatus = "idle"        // disabled
    StatusDialing    LinkStatus = "dialing"
    StatusConnected  LinkStatus = "connected"
    StatusBackoff    LinkStatus = "backoff"
    StatusListener   LinkStatus = "listener"    // we are not the dialer; passive
)

func NewLinkSupervisor(...) *LinkSupervisor { ... }

// Start begins supervision. Reads the peer store and spawns goroutines for
// every enabled peer where we are the dialer.
func (s *LinkSupervisor) Start(ctx context.Context) {
    s.rootCtx = ctx
    for _, p := range s.peerStore.List() {
        if p.Enabled {
            s.spawnLink(p)
        }
    }
}

// AddPeer registers a peer record + spawns a reconnector if appropriate.
// Idempotent: a duplicate Add just refreshes the stored Peer.
func (s *LinkSupervisor) AddPeer(p identity.Peer) error {
    if err := s.peerStore.Add(p); err != nil { return err }
    s.spawnLink(p) // no-op if not dialer or not enabled
    return nil
}

// RemovePeer disconnects and removes a peer.
func (s *LinkSupervisor) RemovePeer(publicKey string) error {
    s.mu.Lock()
    if l, ok := s.links[publicKey]; ok {
        l.cancel()
        delete(s.links, publicKey)
    }
    s.mu.Unlock()
    // Disconnect any inbound connection too
    if peer := s.peerStore.GetByPublicKey(publicKey); peer != nil {
        s.peerMgr.UnregisterPeer(peer.Fingerprint())
    }
    return s.peerStore.RemoveByPublicKey(publicKey)
}

// SetEnabled toggles auto-reconnect for a peer.
func (s *LinkSupervisor) SetEnabled(publicKey string, enabled bool) error {
    if err := s.peerStore.SetEnabled(publicKey, enabled); err != nil {
        return err
    }
    p := s.peerStore.GetByPublicKey(publicKey)
    if p == nil { return fmt.Errorf("peer not found") }
    if enabled {
        s.spawnLink(*p)
    } else {
        s.mu.Lock()
        if l, ok := s.links[publicKey]; ok {
            l.cancel()
            delete(s.links, publicKey)
        }
        s.mu.Unlock()
        s.peerMgr.UnregisterPeer(p.Fingerprint())
    }
    return nil
}

// ReconnectNow forces an immediate dial attempt (skips current backoff).
func (s *LinkSupervisor) ReconnectNow(publicKey string) {
    // Implementation: cancel + respawn the link goroutine.
}

// Status returns current per-peer status for the UI.
func (s *LinkSupervisor) Status() []LinkSnapshot {
    // Walk s.links + s.peerStore.List() and return merged view.
}

type LinkSnapshot struct {
    PublicKey   string     `json:"public_key"`
    Fingerprint string     `json:"fingerprint"`
    Name        string     `json:"name"`
    Address     string     `json:"address"`
    Enabled     bool       `json:"enabled"`
    Status      LinkStatus `json:"status"`
    LastError   string     `json:"last_error,omitempty"`
    NextRetry   time.Time  `json:"next_retry,omitempty"`
    LastSeen    time.Time  `json:"last_seen,omitempty"`
    PairedAt    time.Time  `json:"paired_at"`
    IsDialer    bool       `json:"is_dialer"`
}

// spawnLink creates a reconnector goroutine if we are the dialer; otherwise
// just records the peer as a listener (no goroutine).
func (s *LinkSupervisor) spawnLink(p identity.Peer) {
    s.mu.Lock()
    defer s.mu.Unlock()

    // Cancel existing
    if l, ok := s.links[p.PublicKey]; ok {
        l.cancel()
        delete(s.links, p.PublicKey)
    }

    if !p.Enabled {
        s.links[p.PublicKey] = &peerLink{peer: p, status: StatusIdle}
        return
    }
    if !p.InitiatedByUs {
        s.links[p.PublicKey] = &peerLink{peer: p, status: StatusListener}
        return
    }

    ctx, cancel := context.WithCancel(s.rootCtx)
    link := &peerLink{peer: p, cancel: cancel, status: StatusBackoff}
    s.links[p.PublicKey] = link
    go s.runReconnector(ctx, link)
}

func (s *LinkSupervisor) runReconnector(ctx context.Context, link *peerLink) {
    backoff := time.Second
    const maxBackoff = 30 * time.Second
    for {
        select { case <-ctx.Done(): return; default: }

        link.setStatus(StatusDialing, "", time.Time{})
        start := time.Now()
        err := s.dialOnce(ctx, link)
        upDuration := time.Since(start)

        if err != nil {
            link.setStatus(StatusBackoff, err.Error(), time.Now().Add(backoff))
        } else {
            link.setStatus(StatusBackoff, "", time.Now().Add(backoff))
        }

        if upDuration > 30*time.Second {
            backoff = time.Second
        }
        sleep := backoff + jitter(backoff)
        select {
        case <-ctx.Done(): return
        case <-time.After(sleep):
        }
        backoff = min(backoff*2, maxBackoff)
    }
}

func (s *LinkSupervisor) dialOnce(ctx context.Context, link *peerLink) error {
    // 1. Open WebSocket to ws://<address>/ws/peer (plain ws, never wss).
    // 2. Perform challenge-response auth using s.identity.
    // 3. On success: link.setStatus(StatusConnected, "", time.Time{})
    //    Update peerStore.UpdateLastSeen + UpdateAddress.
    //    Run the bidirectional session (see §6) until error or ctx cancel.
    // 4. Return error from session.
}
```

`min` and `jitter`: use `math/rand` for jitter (`backoff/4` random spread).

### 5.2 `pkg/peer/bootstrap.go` (NEW FILE)

Server-to-server bootstrap helpers. Functions used by the `/api/peers` (UI-side
initiator) and `/api/peers/bootstrap` (target-side receiver) handlers.

```go
package peer

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net"
    "net/http"
    "time"

    "github.com/anh-chu/termyard/pkg/common"
    "github.com/anh-chu/termyard/pkg/identity"
)

type BootstrapRequest struct {
    Password    string `json:"password"`
    Name        string `json:"name"`
    PublicKey   string `json:"public_key"`
    Fingerprint string `json:"fingerprint"`
}

type BootstrapResponse struct {
    Name        string `json:"name"`
    PublicKey   string `json:"public_key"`
    Fingerprint string `json:"fingerprint"`
    Version     string `json:"version,omitempty"`
}

// NormalizeAddress accepts "host", "host:port", or "scheme://host:port"
// and returns "host:port", defaulting port to 7654.
func NormalizeAddress(addr string) (string, error) { ... }

// SendBootstrap dials peer at addr, posts BootstrapRequest, and parses response.
// Plain http only. Times out after 10 s.
func SendBootstrap(ctx context.Context, addr string, req BootstrapRequest) (*BootstrapResponse, error) {
    httpClient := &http.Client{
        Timeout: 10 * time.Second,
        Transport: &http.Transport{
            // Disallow redirects across hosts; explicit Dialer for short connect timeout
            DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
        },
    }
    body, _ := json.Marshal(req)
    url := "http://" + addr + "/api/peers/bootstrap"
    httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
    if err != nil { return nil, err }
    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("User-Agent", "termyard/"+common.VERSION)
    resp, err := httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("dial: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode == http.StatusUnauthorized {
        return nil, fmt.Errorf("password rejected")
    }
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("bootstrap failed: HTTP %d", resp.StatusCode)
    }
    var out BootstrapResponse
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return nil, fmt.Errorf("decode response: %w", err)
    }
    return &out, nil
}
```

### 5.3 New HTTP routes (in `pkg/server/server.go`)

All under the existing authenticated subrouter (the one that has
`auth.Middleware`), **except** `/api/peers/bootstrap` which uses the password
directly.

```go
// AUTHENTICATED (browser session required)

// GET /api/peers
//   Return []LinkSnapshot from supervisor.
//   Response: { "self": { "name": ..., "fingerprint": ..., "public_key": ... }, "peers": [...] }

// POST /api/peers
//   Body: { "address": "...", "password": "...", "auto_reconnect": bool }
//   Calls peer.SendBootstrap to remote, on success adds to local store, spawns link.
//   Response 201: LinkSnapshot

// PATCH /api/peers/{publicKey}
//   Body: { "enabled": bool }
//   Toggles auto_reconnect for the peer.

// POST /api/peers/{publicKey}/reconnect
//   Forces immediate dial (no body)

// DELETE /api/peers/{publicKey}
//   Forgets the peer entirely.

// UNAUTHENTICATED (password is the auth)

// POST /api/peers/bootstrap
//   Body: BootstrapRequest
//   - Verify password (constant-time).
//   - Add peer to store (idempotent on public_key).
//   - Spawn link if we end up being the dialer.
//   - Respond BootstrapResponse.
```

URL-encode public keys when used in path params (they are base64 which can
contain `+/=` characters — use `base64.RawURLEncoding` representation for
URLs, or accept either by walking the store and matching).

**Recommendation: use `fingerprint` in URLs instead of `public_key`** — it's
short, URL-safe (base64 raw-url already), and unique. Add
`PeerStore.GetByFingerprint(fp string)` helper.

### 5.4 Removed HTTP routes

- `POST /api/pair/complete` — delete (and `HandlePairing` in `pkg/peer/handler.go`).
- `POST /api/pair` (in the auth-gated subrouter, generates pairing code) — delete.

### 5.5 `pkg/peer/handler.go` changes

After successful challenge-response on `/ws/peer`:

- Continue to call `RegisterPeer(...)`. ✅
- Continue to send a periodic `peer-state` snapshot. BUT: **filter `Hosts` to
  include only the local host** (`m.localID`), not all peers. (See §3.5.)
- After `RegisterPeer`, **also start the same goroutines the dialer side runs**:
  `forwardStateEvents`, `forwardToolEvents`, `periodicActivity`,
  `periodicStats`, `sendStateUpdate`. Extract these from `client.go` into a
  shared `runSession(conn, peer, role)` function in a new file
  `pkg/peer/session.go` that both handlers use.

- On the read side: handle the same messages the dialer currently handles
  (`MsgStateUpdate`, `MsgStateEvent`, `MsgToolEvent`, `MsgActivityUpdate`,
  `MsgStats`, plus `MsgPTYOpen/Close/Resize`, `MsgSessionAction`,
  `MsgRequestState`). Today, the listener side in `handler.go` only handles
  the first five. **Add the PTY/session-action/request-state cases** so
  either direction can drive PTYs.

### 5.6 `pkg/peer/client.go` cleanup (large)

After unifying with handler via `runSession`:

- Delete `tlsConfig()`, `TLSConfig()`, `getCACert()`, `getPinnedCert()`,
  `encodeCertPEM`, `isSystemTrusted`, `pendingCertPEM` field.
- Delete the `insecure` field and all references.
- The dialer logic becomes much smaller — basically: build a plain `ws://`
  URL, dial, handshake, then `runSession`.
- The `Run(ctx)` loop moves into `LinkSupervisor.runReconnector`. The
  `Client` type can be deleted entirely or kept as a thin helper.

### 5.7 `pkg/peer/pty_manager.go` changes

```go
// Before:
if tlsCfg := pm.client.TLSConfig(); tlsCfg != nil { ... }

// After: always plain dialer.
dialer := websocket.DefaultDialer
```

Remove the TLS branch. `pm.client.HubURL()` becomes `pm.peerAddr` (passed at
construction by the supervisor — the remote address where we should connect
back for PTY data). Also rename `HubURL` → `PeerAddress` semantically.

### 5.8 `pkg/server/server.go` — TLS removal

Delete all of:

```go
TLSConfig       *tls.Config
TLSFingerprint  string
CertReloader    *tlscert.CertReloader
CACertPEM       string
```

from `Options`. Delete the `Run` function's TLS branch (uses
`http.Server.ServeTLS`). Replace with a single `http.Server.ListenAndServe`.

Drop imports of `crypto/tls`, `github.com/anh-chu/termyard/pkg/tlscert`.

Update `Run` signature/body so it only does plain HTTP. The default port
stays 7654.

### 5.9 `pkg/commands/server/server.go` — flag removal

Remove the following CLI flags and their environment variables:

- `--no-tls` / `TERMYARD_NO_TLS`
- `--tls-cert` / `TERMYARD_TLS_CERT`
- `--tls-key` / `TERMYARD_TLS_KEY`
- `--tls-san` / `TERMYARD_TLS_SAN`
- `--tls-reload-interval` / `TERMYARD_TLS_RELOAD_INTERVAL`
- `--hub` / `TERMYARD_HUB`
- `--insecure` / `TERMYARD_INSECURE`
- `--local-only` / `TERMYARD_LOCAL_ONLY`

Remove the `tlscert.LoadOrGenerate(...)` call and the corresponding lines that
set `opts.TLSConfig`, `opts.TLSFingerprint`, `opts.CACertPEM`,
`opts.CertReloader`.

Remove the `if hubURL != ""` block that constructs `peer.NewClient` and
`go peerClient.Run(ctx)`.

Replace with construction of `LinkSupervisor` and pass it through `Options`:

```go
supervisor := peer.NewLinkSupervisor(nodeIdentity, peerStore, stateMgr,
    peerMgr, actTracker, tracker, client, client.TmuxPath())
supervisor.Start(ctx)
opts.LinkSupervisor = supervisor
```

### 5.10 `main.go` — remove deleted commands

```go
// Delete these imports:
_ "github.com/anh-chu/termyard/pkg/commands/pair"
_ "github.com/anh-chu/termyard/pkg/commands/peers"
```

Then `rm -rf pkg/commands/pair pkg/commands/peers pkg/tlscert pkg/identity/pairing.go`.

Also rename `pkg/server/server_test.go` cases / remove tests covering TLS,
fingerprint, pairing.

---

## 6. Shared peer-session function (`pkg/peer/session.go`, NEW)

This is the unification point so the same Go code runs on both sides of the
link. Sketch:

```go
package peer

// Role tells the session which side it is. Affects only the initial
// peer-state push.
type Role int
const (
    RoleDialer Role = iota
    RoleListener
)

// runSession owns the post-auth lifetime of one peer connection.
// It blocks until conn is closed or ctx is canceled. It is safe to call
// concurrently for different peers.
func runSession(
    ctx context.Context,
    role Role,
    conn *websocket.Conn,
    peerInfo identity.Peer,
    deps SessionDeps,
) error {
    peerID := peerInfo.Fingerprint()

    // 1. Register the peer in the manager.
    pc := &PeerConnection{
        HostID: peerID,
        Send:   make(chan *Message, 64),
    }
    deps.PeerMgr.RegisterPeer(peerID, peerInfo.Name, peerInfo.PublicKey, pc)
    defer deps.PeerMgr.UnregisterPeer(peerID)

    // 2. Configure liveness.
    setupLiveness(conn)

    // 3. Start writer.
    writerErr := make(chan error, 1)
    go writer(conn, pc.Send, writerErr)

    // 4. Listener sends an initial peer-state containing only itself.
    //    Dialer sends a state-update first to expose its sessions.
    //    Both also send stats and activity periodically.
    if role == RoleListener {
        sendInitialPeerState(pc, deps)
    }
    sendStateUpdate(pc, deps)

    // 5. Background loops: state events, tool events, activity, stats.
    sessionCtx, cancel := context.WithCancel(ctx)
    defer cancel()
    go pingLoop(sessionCtx, conn)
    go periodicActivity(sessionCtx, pc, deps)
    go periodicStats(sessionCtx, pc, deps)
    go forwardStateEvents(sessionCtx, pc, deps)
    go forwardToolEvents(sessionCtx, pc, deps)

    // 6. Read loop.
    for {
        var msg Message
        if err := conn.ReadJSON(&msg); err != nil {
            close(pc.Send)
            return err
        }
        handleSessionMessage(peerID, &msg, pc, deps)
    }
}

type SessionDeps struct {
    Manager     *Manager
    LocalMgr    *state.Manager
    Identity    *identity.Identity
    ActTracker  *activity.Tracker
    ToolTracker *toolevents.Tracker
    PTYManager  *PTYManager        // local PTY ops triggered by remote MsgPTYOpen
    PeerStore   *identity.PeerStore
}

func handleSessionMessage(peerID string, msg *Message, pc *PeerConnection, d SessionDeps) {
    // Switch over msg.Type — union of all messages currently handled in
    // client.go::handleHubMessage AND handler.go::handlePeerMessage.
}
```

This replaces the bulk of the read-loop bodies in both files.

---

## 7. UI Changes

### 7.1 Routing

Add a new sub-route in `web/src/components/Settings.tsx` (existing settings
page). Use the existing tab/section pattern.

Section title: **Connected Machines** (or whatever wording matches the existing
Settings copy).

### 7.2 Peers list

```
┌─ Connected Machines ───────────────────────────────────────────────┐
│                                                                    │
│  This machine                                                      │
│    Name:        devvm-a                                            │
│    Fingerprint: 3qY4nA7Z                                           │
│                                                                    │
│  [ Connect to another machine ]                                    │
│                                                                    │
│  ┌──────────────────────────────────────────────────────────────┐ │
│  │ ● devvm-b — 192.168.1.50:7654                                │ │
│  │   connected · 4 sessions · ed25519:bP7c…                     │ │
│  │   Auto-reconnect [✓]   [Reconnect now]  [Forget]             │ │
│  ├──────────────────────────────────────────────────────────────┤ │
│  │ ○ laptop-c — laptop.local:7654                               │ │
│  │   offline · retrying in 12s · last seen 4m ago               │ │
│  │   Auto-reconnect [✓]   [Reconnect now]  [Forget]             │ │
│  ├──────────────────────────────────────────────────────────────┤ │
│  │ ◌ server-d — server.local:7654                               │ │
│  │   disabled                                                    │ │
│  │   Auto-reconnect [ ]   [Forget]                              │ │
│  └──────────────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────────┘
```

Status dots:

- `●` green: connected
- `○` yellow: dialing or backoff
- `◌` gray: disabled
- `▶` gray: listener (passive — peer is responsible for dialing us)

### 7.3 Connect dialog

Modal triggered by **Connect to another machine** button:

```
┌─ Connect to another machine ──────────────────────────┐
│                                                       │
│ Address      [ devvm-b.local:7654                  ]  │
│ Password     [ ••••••••••                          ]  │
│              The dashboard password of the other      │
│              machine. Used once to establish trust.   │
│                                                       │
│ [✓] Auto-reconnect if the connection drops            │
│                                                       │
│                              [ Cancel ]  [ Connect ]  │
└───────────────────────────────────────────────────────┘
```

On submit:

```js
fetch("/api/peers", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ address, password, auto_reconnect }),
});
```

Error handling:

- HTTP 401: "Password rejected by remote machine"
- HTTP 503: "Remote machine has no password configured yet"
- network error: "Could not reach <address>. Check the hostname and port."
- HTTP 409: "Already paired with this machine"

### 7.4 Live status

The peers list polls `GET /api/peers` every 5 s. Optional improvement (phase
2): push status updates via the existing `/ws/events` channel by adding a new
event type `link-status-changed` — emit it from `LinkSupervisor` when a link's
status field mutates. **Phase 1: polling is fine.**

### 7.5 Files to delete in web/

- `web/src/components/TrustCertificate.tsx` — entirely.
- In `web/src/App.tsx`:
  - Remove `import { TrustCertificate } from './components/TrustCertificate'`
  - Remove the `setShowTrust` state and the `if (showTrust)` block that renders it.
  - Remove any `/trust` route handling.

---

## 8. Removed CLI / docs

### 8.1 Delete

- `pkg/commands/pair/` (entire directory)
- `pkg/commands/peers/` (entire directory)
- `pkg/tlscert/` (entire directory)
- `pkg/identity/pairing.go`
- `web/src/components/TrustCertificate.tsx`

### 8.2 Edit `main.go`

Remove `pair` and `peers` imports.

### 8.3 Rewrite `docs/multi-host.md`

Replace with concise version describing:

1. Run `termyard server` on each machine.
2. Open each dashboard, set a password.
3. In Settings → Connected Machines on node A, click "Connect to another
   machine", enter B's address + B's dashboard password.
4. (Optional) Reverse direction is automatic — no further action needed.
5. Use Tailscale / WireGuard for cross-network reachability and encryption.

Drop all mentions of TLS SANs, `tailscale cert`, pair codes, `--hub`,
`--local-only`, `--no-tls`, `--insecure`.

### 8.4 Edit `README.md`

Delete the `TERMYARD_HUB`, `TERMYARD_INSECURE`, `--hub`, `--insecure`, `--no-tls`,
`--tls-*`, `--tls-san` rows from the env-vars / flags tables.

---

## 9. Implementation Order

This order minimizes broken intermediate states.

### Step 1 — Rip out TLS plumbing (compile-clean refactor)

1. Delete `pkg/tlscert/`.
2. Delete TLS fields from `server.Options`. Update `pkg/server/server.go::Run`
   to plain HTTP only.
3. Delete TLS flags from `pkg/commands/server/server.go`.
4. Delete `pendingCertPEM`, `tlsConfig`, etc. from `pkg/peer/client.go`.
5. Delete TLS branch in `pkg/peer/pty_manager.go::connectPTYWebSocket`.
6. Delete `TLSCertPEM`/`CACertPEM` fields and methods from
   `pkg/identity/peers.go`.
7. Delete `TrustCertificate.tsx` and references.
8. Run `go build ./...` until green. Run `go test ./...` and fix/delete
   broken tests (`pkg/peer/client_cert_test.go`, anything in `pkg/tlscert/`).

### Step 2 — Schema changes

1. Add `Address`, `Enabled`, `LastSeen` fields to `identity.Peer`.
2. Add `UpdateAddress`, `SetEnabled`, `UpdateLastSeen`, `RemoveByPublicKey`,
   `GetByFingerprint` methods.
3. Migration on load: if any peer has zero `Enabled` value AND the file
   previously existed, set `Enabled = true` and re-save. (Detect by reading
   raw JSON keys.)

### Step 3 — Delete pairing infrastructure

1. Delete `pkg/identity/pairing.go`.
2. Delete `pkg/commands/pair/`.
3. Delete `pkg/commands/peers/`.
4. Update `main.go`.
5. Remove `/api/pair` and `/api/pair/complete` routes.
6. Remove `PairingMgr` from `server.Options` and from `peer.Handler`.
7. Remove `HandlePairing` method.
8. `go build ./...` green.

### Step 4 — Shared session function

1. Create `pkg/peer/session.go` with `runSession`, `SessionDeps`, and
   sub-helpers extracted from both `client.go` and `handler.go`.
2. Migrate `handler.go::HandlePeer` to use `runSession`. Make the read
   loop dispatch via `handleSessionMessage`. Verify listener now handles
   `MsgPTYOpen/Close/Resize/SessionAction/RequestState`.
3. **Filter** `peer-state` snapshot to only include the local host
   (transitivity off).
4. Make `manager.go::GetHosts()` and `peer-state` snapshot use a new method
   `GetHostsForPeer(remotePeerID string)` that returns only local + the
   remote peer.

### Step 5 — LinkSupervisor

1. Create `pkg/peer/supervisor.go` with the supervisor + reconnector logic
   in §5.1.
2. Migrate the dial path from `client.go` into `supervisor.dialOnce`,
   calling `runSession` after auth completes.
3. Either delete `client.go` or trim it to just helpers used by both sides.
4. Wire supervisor into `pkg/commands/server/server.go`.

### Step 6 — Bootstrap endpoint

1. Create `pkg/peer/bootstrap.go` with `SendBootstrap`, `BootstrapRequest`,
   `BootstrapResponse`, `NormalizeAddress`.
2. Add `POST /api/peers/bootstrap` route (unauthenticated, password-gated).
   Handler verifies password, adds peer, returns identity.
3. Add `GET /api/peers`, `POST /api/peers`, `PATCH /api/peers/{fp}`,
   `POST /api/peers/{fp}/reconnect`, `DELETE /api/peers/{fp}` (all auth-gated).
4. POST /api/peers handler must call `SendBootstrap`, then
   `supervisor.AddPeer`.

### Step 7 — UI

1. Add Peers panel to `web/src/components/Settings.tsx`.
2. Add `ConnectPeerModal` component.
3. Wire to new endpoints. Poll `GET /api/peers` every 5 s while visible.

### Step 8 — Docs

1. Rewrite `docs/multi-host.md`.
2. Update `README.md` flags table.
3. Update `CHANGELOG.md` with breaking change note.

### Step 9 — End-to-end smoke test

On two machines (or two ports on one machine — e.g. 7654 and 7655):

1. Start both: `termyard server` (port 7654), `termyard server -p 7655`.
2. Set password on both via UI (`/setup` flow already exists).
3. From dashboard at :7654, connect to `localhost:7655` with the second
   password.
4. Verify:
   - Both dashboards show both nodes and their sessions.
   - Killing one process: the other shows it as offline within 30 s, then
     prunes after 5 min (existing `OfflineTimeout`).
   - Restarting: link auto-recovers within backoff window.
   - Toggling **Auto-reconnect off** on either side stops reconnection on
     the dialer; toggling on resumes.
   - Forgetting on one side closes the link and drops the peer. **Before**
     closing, the forgetting side sends `MsgForget` (new message type,
     payload `{}`) over the live link. The remote side, on receiving
     `MsgForget`, removes the peer from its own store and closes the
     connection. This avoids "unknown peer" auth-fail spam after one-sided
     forgets. Implement in `pkg/peer/protocol.go` (add the const + payload)
     and dispatch in the shared `handleSessionMessage` (§6). If the link is
     already down at the moment of forget, the message can't be sent —
     remote will continue to retry and eventually get an `auth-fail`
     `reason: "unknown peer"`, which it should treat as **non-terminal**
     (keep retrying — the user might re-pair later). It surfaces in the UI
     as `last_error: "peer no longer recognises us — forget?"`.

---

## 10. Edge cases & gotchas

1. **First-run password**: bootstrap rejects (503) if the target has no
   password set. UI should surface this clearly.
2. **Same-machine self-pair**: bootstrap handler must reject if
   `req.public_key == localIdentity.PublicKey`. Return 400 with message
   "cannot pair with self".
3. **Re-pair after key rotation**: identity is durable, but if a user
   nukes `~/.config/termyard/identity.json`, paired peers will get
   "unknown peer" on auth. The forget-on-fail strategy: if a dialer gets
   `auth-fail` with reason "unknown peer", it should keep retrying for a
   minute, then mark its local record as "stale" (status field) but **not**
   delete it — only the user can forget. UI shows "paired peer no longer
   recognises this machine — forget?".
4. **Address change**: peer's IP/hostname changed. The user re-runs Connect
   from the UI; bootstrap is idempotent on public key and just updates the
   address.
5. **Two nodes both behind NAT**: dialer selection now follows user intent
   (whoever clicked Connect). So the user must run Connect on the side that
   can reach the other. If neither side is reachable from the other,
   nothing works — that's a network problem, not a termyard problem. Document
   this in `docs/multi-host.md`.

6. **Two processes simultaneously bootstrap each other**: both
   POST `/api/peers/bootstrap` to each other at the same moment. Both
   handlers add a record with `InitiatedByUs = true` (each thinks it's the
   initiator because each side's user clicked Connect). Both reconnectors
   spawn; one of the `/ws/peer` connections wins via the
   `"already connected"` race-resolution in §3.2, the loser flips its
   record to `InitiatedByUs = false`. ✅
7. **Bootstrap during downtime**: if remote is down, POST `/api/peers`
   fails with network error. Nothing is added. UI shows error. User
   retries when remote is up. ✅
8. **Auto-reconnect off, but inbound still arrives**: the listener accepts
   any peer in its store regardless of `Enabled`. This is fine — the
   `Enabled` flag only governs **outbound** retries. (Document this.)
9. **WebSocket ping/pong timeouts**: keep existing 15 s ping / 30 s read
   deadline.
10. **Deep-link to a remote session**: existing `?host=<fp>` query param on
    `/ws/session` still works because routing is unchanged (`handleRemoteSession`
    in `pkg/server/server.go`). Verify it still uses `peerConn.Send <- pty-open`.
11. **Forget propagation**: when the user clicks Forget on peer P, the local
    backend (a) sends `MsgForget` on the live link (if any) before closing,
    (b) calls `supervisor.RemovePeer(P.publicKey)`, which cancels the dialer
    goroutine and removes from the store. On the remote side, the
    `MsgForget` handler removes its own record for us and tears down the
    connection. If the link was already down, no message is sent; the
    remote will retry, receive `auth-fail{reason:"unknown peer"}`, treat it
    as **non-terminal** (keep slow-retrying), and surface in UI as
    "peer no longer recognises us — forget?".

---

## 11. Test checklist

Add tests:

- `pkg/peer/supervisor_test.go`:
  - `TestDialerSelectionFollowsInitiator` — `InitiatedByUs=true` ⇒ dial; `false` ⇒ listen.
  - `TestSpawnLinkRespectsDisabled` — `Enabled=false` ⇒ no goroutine.
  - `TestRemovePeerCancelsGoroutine` — link goroutine exits within timeout.
  - `TestReconnectNowResetsBackoff` — forces a fresh dial.
  - `TestSimultaneousInitiateRaceResolved` — both sides start with `InitiatedByUs=true`; after the duplicate-connection collapse, exactly one side ends up with `InitiatedByUs=false` and the other stays `true`.
  - `TestAuthFailAlreadyConnectedIsTerminal` — dialer receiving `auth-fail{reason:"already connected"}` flips its own `InitiatedByUs` to `false` and stops retrying.

- `pkg/peer/bootstrap_test.go`:
  - `TestNormalizeAddress` — table-driven covering `host`, `host:port`, `scheme://host:port`, IPv6, malformed.
  - `TestSendBootstrap` — uses httptest server, asserts request shape.

- `pkg/server/server_test.go`:
  - `TestPostPeersBootstrapRejectsBadPassword` — 401.
  - `TestPostPeersBootstrapRejectsSelf` — 400.
  - `TestPostPeersBootstrapIdempotent` — second call with same pubkey just refreshes.
  - `TestGetPeers` — returns self + paired list.

Remove or rewrite:

- `pkg/peer/client_cert_test.go` — delete.
- Anything in `pkg/tlscert/*_test.go` — deleted with the package.

---

## 12. Rollback strategy

The whole change is one or more commits on a feature branch. Tag the pre-change
commit (e.g. `pre-symmetric-peering`). If anything blocks shipping:

```
git revert <range>          # or
git reset --hard pre-symmetric-peering
```

Existing user data (`peers.json`, `identity.json`, `auth.json`) remains
compatible with the old code because we only **added** fields to `Peer`. The
old code ignores them.

---

## 13. Out-of-band notes for the implementing agent

- **You may delete code aggressively.** Anything in `pkg/tlscert/` and
  pairing modules has no remaining callers after this plan.
- **Do not introduce new dependencies** unless absolutely required. Stick to
  `gorilla/websocket`, `go-chi/chi/v5`, `sirupsen/logrus`, `urfave/cli/v3`,
  `golang.org/x/crypto/bcrypt`. These are already present.
- **Plain HTTP everywhere.** Never construct `https://` or `wss://`. The user
  layers their own TLS (Caddy / Tailscale / nginx) if they want it.
- **No env-var fallbacks.** Settings live in `~/.config/termyard/peers.json`
  managed by the UI. The only persistent runtime config the CLI exposes is
  `--port`, `--socket`, `--discovery-interval`, `--no-control-mode`,
  `--no-auth`.
- **Backward compatibility**: a user upgrading from old termyard will find
  their old `peers.json` migrated automatically (Enabled defaults to true)
  and the new UI will list them. They will have to re-pair only if the old
  records relied on pinned TLS certs to reach a non-system-trusted host —
  with TLS gone entirely, the address either works on plain HTTP or doesn't.

---

## 14. Done criteria

Refactor is complete when **all** of these are true:

- [ ] `grep -ri 'tls\|cert\|--hub\|TERMYARD_TLS\|TERMYARD_HUB\|pairing\|TrustCertificate' pkg web/src main.go` returns nothing except docs/comments.
- [ ] `termyard server --help` lists only: `--port`, `--socket`, `--discovery-interval`, `--no-control-mode`, `--no-auth`.
- [ ] `termyard --help` lists only top-level commands: `server`, `notify`, `agent-setup`, `install`. (No `pair`, no `peers`.)
- [ ] Fresh install on two machines: connect via UI works first try.
- [ ] Killing one machine does not affect the other's local sessions.
- [ ] After bringing it back up, the link auto-recovers within 30 s.
- [ ] Existing `peers.json` from old install loads without error and shows in UI as enabled.
- [ ] `docs/multi-host.md` and `README.md` are updated.
- [ ] `go vet ./... && go test ./...` passes.
- [ ] `cd web && pnpm build` passes.
