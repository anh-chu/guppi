import { test, expect, Page, WebSocketRoute } from '@playwright/test'

/**
 * Multi-node v2 E2E suite (docs/v2-direct-cutover.md's proof-gate item
 * "UI correctness" + the acceptance criteria list attached to this task).
 *
 * Two of the four scenarios below (remote-catalog-visibility across two
 * real peered nodes, and v2-only-rejects-legacy-peer at handshake) are
 * `test.skip`ped with a concrete, verified blocker -- see the
 * "REAL TWO-PROCESS HARNESS" section for the investigation that produced
 * that blocker. The other two (retry idempotency, generation-change/
 * reconnect does not remount) are fully implemented against the existing
 * single-process Vite-dev-server + route-stub harness (same pattern as
 * baseline-render.spec.ts / smoke.spec.ts), extended with:
 *   - a GET /api/v2/bootstrap stub (the v2 equivalent of /api/sessions)
 *   - page.routeWebSocket('**\/ws/v2/state', ...) to fully mock the v2
 *     durable state stream without needing a real backend
 *   - page.route('**\/api/v2/session-commands', ...) to fully mock the v2
 *     command endpoint and observe retry behavior
 *
 * ---------------------------------------------------------------------
 * REAL TWO-PROCESS HARNESS: investigation and concrete blocker
 * ---------------------------------------------------------------------
 * A real two-node test was attempted first (not assumed impossible). Using
 * a built `termyard` binary, two full server processes were started on
 * different ports with isolated $HOME/$XDG_CONFIG_HOME/$XDG_DATA_HOME/
 * $XDG_STATE_HOME directories (pkg/config/config.go's Dir()/DataDir() and
 * pkg/pty/lifecycle.go's DefaultStateDir() all respect these env vars, so
 * config/auth/v2-store isolation between two instances on one machine
 * works fine). The full peer-pairing flow was driven end-to-end with curl
 * and verified to work: POST /api/auth/setup on each node, then POST
 * /api/peers {address, password} from node A's authenticated session
 * against node B -- node A's GET /api/peers came back with the peer at
 * status "connected" and node B's GET /api/peers showed status "listener"
 * within ~1.5s. So peer trust/bootstrap itself is NOT the blocker.
 *
 * The blocker is the session/PTY backend: pkg/pty/daemon.go's
 * DaemonConfig.SocketDir defaults to `/tmp/termyard-sessions-{uid}`, keyed
 * by OS user id, not by server instance/config dir, and the `server`
 * command has no flag/env var to override it (only the internal
 * `sessiondaemon` child process accepts --socket-dir, and only when a
 * parent explicitly passes a non-default one, which pkg/commands/server
 * never does). Confirmed live: GET /api/hosts on either of the two
 * differently-configured, differently-ported test instances returned the
 * exact same set of real tmux/daemon sessions already running on the host
 * under this OS user -- i.e. "node A" and "node B" are not actually two
 * independent local session backends, they are two HTTP/WS front ends onto
 * the SAME shared daemon registry. A test that creates a session "on node
 * B" and asserts it becomes visible "on node A" would be trivially true
 * for the wrong reason (it's the same underlying local daemon, not
 * genuine cross-node catalog sync over the peer WS link) -- and would
 * also be unsafe to run on a shared dev/CI machine, since it would list
 * and could kill real, unrelated tmux sessions belonging to that user.
 *
 * Fixing this needs one of: (a) a `--socket-dir`/`TERMYARD_SESSION_SOCKET_DIR`
 * override plumbed from the `server` command down into the Registry it
 * constructs (mirroring the child daemon's existing --socket-dir flag), or
 * (b) running each node as a distinct OS user/container. Neither exists
 * today, so the two true multi-process tests below are `test.skip` with
 * this comment rather than faked. This is the concrete, sourced gap that
 * should be closed (see docs/v2-direct-cutover.md's preflight checklist)
 * before Task 15.
 */

const OWNER = 'owner-node-a'

function makeSessionRaw(id: string, generation: string, name = id) {
  return {
    id,
    owner: OWNER,
    ref: `${id}:0.0`,
    phase: 'active',
    desired: 'run',
    revision: 1,
    created_at: new Date().toISOString(),
    _compat: {
      name,
      shell: '/bin/bash',
      cwd: '/tmp/e2e',
      generation,
    },
  }
}

function makeWorkspaceRaw(sessionId: string, revision = 1) {
  return {
    id: 'layout-1',
    owner: OWNER,
    revision,
    tree: { type: 'leaf', ref: `${sessionId}:0.0` },
    active_key: `${sessionId}:0.0`,
  }
}

function makeBootstrapRaw(sessionId: string, generation: string) {
  return {
    owner: OWNER,
    revision: 1,
    local: { owner: OWNER, revision: 1, sessions: [makeSessionRaw(sessionId, generation)], layouts: [] },
    remote: [],
    hosts: [],
    workspace: makeWorkspaceRaw(sessionId),
    presentations: [],
    pending: [],
    pending_remote: [],
  }
}

const DEFAULT_PREFS = {
  terminal: {
    font_size: 13,
    font_family: 'Space Mono',
    scrollback: 50000,
    renderer: 'dom',
    unicode_graphemes: false,
    predictive_echo: false,
  },
  theme: 'dark',
  sidebar: { default_collapsed: false, collapse_mode: 'small' },
  default_view: 'overview',
  notifications: { statuses: ['waiting', 'stuck', 'error', 'completed'] },
  agent_banner: { auto_dismiss_seconds: 0 },
  lock_timeout_minutes: 30,
  fullscreen_hide_alerts: true,
  default_agent: 'claude',
  wiki_disabled: false,
  ai_naming: { enabled: false, endpoint: '', api_key: '', model: 'gpt-4o-mini' },
}

const LOCAL_HOST = {
  id: 'local',
  name: 'Local Machine',
  local: true,
  online: true,
  sessions: [],
  last_seen: new Date().toISOString(),
}

/** Base stubs every v2 test needs: auth, prefs, hosts, and a generic /api/* catch-all. */
async function installBaseStubs(page: Page) {
  await page.route('**/api/**', async (route, request) => {
    const url = new URL(request.url())
    const path = url.pathname

    if (path === '/api/auth/status') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ auth_required: true, needs_setup: false }),
      })
    }
    if (path === '/api/auth/check') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ authenticated: true }),
      })
    }
    if (path === '/api/preferences') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DEFAULT_PREFS) })
    }
    if (path === '/api/hosts') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([LOCAL_HOST]) })
    }
    // Generic catch-all for every other legacy/shared endpoint AppV2's
    // shared hooks still call (crashed-sessions, update, tool-events,
    // active-turns, activity, stats, schedules, ...). None of these carry
    // v2 catalog/workspace data, so an empty array/object is always a valid
    // stub response for them in this suite.
    if (path.startsWith('/api/')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    }
    return route.continue()
  })
}

async function installV2Bootstrap(page: Page, bootstrapRaw: unknown) {
  await page.route('**/api/v2/bootstrap', async (route) => {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(bootstrapRaw) })
  })
}

async function enableV2Flag(page: Page) {
  await page.addInitScript(() => {
    window.localStorage.setItem('termyard.v2State', '1')
  })
}

/**
 * Mocks /ws/v2/state entirely (no real backend). `onConnection` is called
 * once per socket the browser opens (including reconnects after a forced
 * close), receiving the server-side WebSocketRoute and a 1-based connection
 * index, so a test can send different frames on each successive connection
 * (e.g. to simulate a reconnect after a generation change).
 */
type V2SocketHandle = {
  /** 1-based count of connections opened so far (bumped on every reconnect). */
  connectionCount: () => number
  /** Closes the currently-open connection (triggers the client's reconnect logic). */
  closeCurrent: () => Promise<void>
}

async function installV2StateSocket(
  page: Page,
  onConnection: (ws: WebSocketRoute, connectionIndex: number) => void,
): Promise<V2SocketHandle> {
  let count = 0
  let current: WebSocketRoute | null = null
  await page.routeWebSocket('**/ws/v2/state', (ws) => {
    count += 1
    current = ws
    ws.onClose(() => {
      if (current === ws) current = null
    })
    onConnection(ws, count)
  })
  return {
    connectionCount: () => count,
    closeCurrent: async () => {
      if (current) await current.close()
    },
  }
}

function sendCatalogSnapshot(ws: WebSocketRoute, sessionId: string, generation: string, revision = 1) {
  ws.send(
    JSON.stringify({
      type: 'catalog_snapshot',
      is_local: true,
      snapshot: { owner: OWNER, revision, sessions: [makeSessionRaw(sessionId, generation)], layouts: [] },
    }),
  )
}

function sendWorkspaceSnapshot(ws: WebSocketRoute, sessionId: string, revision = 1) {
  ws.send(JSON.stringify({ type: 'workspace_snapshot', workspace: makeWorkspaceRaw(sessionId, revision) }))
}

// ---------------------------------------------------------------------------
// Test 1 & 2: real two-node scenarios. See file header for the concrete,
// verified blocker (shared UID-scoped PTY daemon socket dir). Left as
// documented test.skip rather than a fabricated pass.
// ---------------------------------------------------------------------------

test.describe('real two-node peer scenarios (blocked)', () => {
  test.skip(
    true,
    'Blocked: pkg/pty/daemon.go SocketDir defaults to /tmp/termyard-sessions-{uid} ' +
      '(keyed by OS user, not by server instance) and the `server` command has no ' +
      'flag/env to override it. Two termyard processes on one machine as the same ' +
      'user share the exact same local session registry, so they cannot be used as ' +
      'two independently-backed nodes to prove cross-node catalog sync -- and doing ' +
      'so would also read/mutate real tmux sessions on a shared dev/CI machine. ' +
      'Needs a --socket-dir/TERMYARD_SESSION_SOCKET_DIR override on the server ' +
      'command (or per-node OS users/containers) before this can be implemented for ' +
      'real. Peer pairing itself (POST /api/auth/setup + POST /api/peers) was ' +
      'verified working end-to-end with curl against two isolated-config real ' +
      'server processes during this investigation, so that part is not the blocker.',
  )
  test('session created on node B becomes visible in node A browser', async () => {
    // Intentionally not implemented -- see test.skip reason above.
  })

  test.skip(
    true,
    'Same blocker as above: exercising pkg/peer/protocol.go\'s requiresV2Peer/' +
      'peerCapsSatisfyV2 handshake gate for real needs two real server processes ' +
      'that pair over /ws/peer, one built with TERMYARD_V2_STATE=1 (v2-only) and ' +
      'one without (legacy-only, i.e. built from a commit/flag that never ' +
      'constructs V2CommandSvc). That part alone is reachable, but this suite runs ' +
      'a single build of the binary, so "legacy-only" would have to be simulated by ' +
      'a second real process anyway. This test also does not depend on the ' +
      'session-backend isolation gap above (peer handshake happens before any ' +
      'session-level data is exchanged) -- it is left skipped for a narrower reason: ' +
      'there is no harness support today for running two differently-flagged ' +
      'server binaries side by side. A future harness change (spawn a real server ' +
      'child process per node, one with and one without TERMYARD_V2_STATE=1, pair ' +
      'them via the curl flow proven above, then assert GET /api/peers reports the ' +
      'v2-only side rejecting the legacy side) would close this out directly.',
  )
  test('v2-only node rejects legacy-only peer at handshake', async () => {
    // Intentionally not implemented -- see test.skip reason above.
  })
})

// ---------------------------------------------------------------------------
// Test 3: browser command retry with the same CommandID does not double-execute.
// ---------------------------------------------------------------------------

test('browser command retry with same CommandID does not double-execute', async ({ page }) => {
  const sessionId = 'sess-retry'
  const generation = 'gen-1'

  await enableV2Flag(page)
  await installBaseStubs(page)
  await installV2Bootstrap(page, makeBootstrapRaw(sessionId, generation))

  let latestWs: WebSocketRoute | null = null
  await installV2StateSocket(page, (ws) => {
    latestWs = ws
    ws.onClose(() => {
      if (latestWs === ws) latestWs = null
    })
    sendCatalogSnapshot(ws, sessionId, generation)
    sendWorkspaceSnapshot(ws, sessionId)
  })

  // Capture every session-command POST body so we can assert the retry
  // reuses the exact same `id` (this is the browser-level contract that
  // makes server-side CommandReceipt dedup by intent id actually work --
  // see pkg/state/INVARIANTS.md's Commands section; the server-side dedup
  // itself is unit-tested in Go, this proves the browser side of the
  // contract at the E2E level).
  const seenBodies: Array<{ id: string; action: string }> = []
  const idAttempts = new Map<string, number>()
  await page.route('**/api/v2/session-commands', async (route, request) => {
    const body = JSON.parse(request.postData() || '{}')
    seenBodies.push({ id: body.id, action: body.action })
    const attempt = (idAttempts.get(body.id) ?? 0) + 1
    idAttempts.set(body.id, attempt)

    if (attempt === 1) {
      // Simulate an ambiguous network failure (e.g. connection reset) --
      // the one class of failure the client is allowed to retry.
      return route.abort('failed')
    }

    // Second attempt with the SAME id: succeed, and reflect the effect
    // (session killed) exactly once via a fresh catalog snapshot over the
    // (already-connected) mocked state stream. Note: killing a session only
    // removes it from the catalog -- removing its leaf from the workspace
    // tree is a separate, explicit "remove" workspace command (see
    // App.tsx's onClose handler), so the workspace snapshot here
    // deliberately keeps the same tree; only the catalog's sessions list
    // empties.
    if (latestWs) {
      latestWs.send(
        JSON.stringify({
          type: 'catalog_snapshot',
          is_local: true,
          snapshot: { owner: OWNER, revision: 2, sessions: [], layouts: [] },
        }),
      )
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
  })

  await page.goto('/session/x')
  await expect(page.locator(`[data-pane-key="${sessionId}"]`)).toBeVisible({ timeout: 15000 })

  // Trigger a kill via the sidebar's session-actions menu (same "Kill" ->
  // "Confirm kill?" two-step pattern used by smoke.spec.ts and
  // Sidebar.test.tsx's v2Mode kill test).
  await page.getByText(sessionId).first().click({ button: 'right' })
  await page.locator('text=Kill').first().click()
  await page.locator('text=Confirm kill?').first().click()

  // Wait until the retried (2nd) attempt has actually gone out.
  await expect.poll(() => seenBodies.length, { timeout: 10000 }).toBeGreaterThanOrEqual(2)

  expect(seenBodies).toHaveLength(2)
  expect(seenBodies[0].action).toBe('kill')
  expect(seenBodies[1].action).toBe('kill')
  // The core assertion: retry reused the identical CommandID, not a new one.
  expect(seenBodies[1].id).toBe(seenBodies[0].id)

  // And the effect only ever lands once: the session disappears from the
  // catalog-backed sidebar exactly once (not toggled/duplicated) once the
  // successful attempt's catalog update arrives -- not twice, which is what
  // a double-execution bug (e.g. a second, independently-generated command
  // id also reaching the server) would look like.
  await expect(page.locator(`[data-session-key="${sessionId}"]`)).toHaveCount(0)

  // No third attempt was ever made -- the client's retry budget is bounded
  // and this specific retry succeeded on attempt 2, so nothing should keep
  // retrying in the background.
  await page.waitForTimeout(500)
  expect(seenBodies).toHaveLength(2)
})

// ---------------------------------------------------------------------------
// Test 4: generation change (reconnect) does not visibly remount the terminal.
// ---------------------------------------------------------------------------

test('generation change on reconnect does not remount the terminal', async ({ page }) => {
  const sessionId = 'sess-stable'
  const generation = 'gen-1' // daemon binding generation: unchanged across the reconnect

  await enableV2Flag(page)
  await installBaseStubs(page)
  await installV2Bootstrap(page, makeBootstrapRaw(sessionId, generation))

  const socket = await installV2StateSocket(page, (ws) => {
    // Every connection (including the reconnect) gets a complete snapshot,
    // mirroring the real StateStreamHub's connect-order guarantee (see
    // pkg/ws/state_stream.go). Same sessionId/ownerId/generation triple on
    // both connections -- only the WS connection generation changes.
    sendCatalogSnapshot(ws, sessionId, generation)
    sendWorkspaceSnapshot(ws, sessionId)
  })

  await page.goto('/session/x')
  await expect(page.locator(`[data-pane-key="${sessionId}"]`)).toBeVisible({ timeout: 15000 })

  // Tag the actual xterm DOM root terminalPool reuses across checkouts
  // (see web/src/lib/terminalPool.ts's transferNode/checkout) so we can
  // prove afterwards it is the SAME node, not a freshly mounted one. This is
  // the E2E-level equivalent of the unit-level identity assertions in
  // web/src/lib/terminalPool.test.ts ("terminal instance identity survives
  // both rename and generation-change events").
  const tagged = await page.evaluate((key) => {
    const el = document.querySelector(`[data-pane-key="${key}"] .xterm`) as HTMLElement | null
    if (!el) return false
    el.setAttribute('data-e2e-stable-node', 'yes')
    return true
  }, sessionId)
  expect(tagged).toBe(true)

  // Force the mocked socket closed to simulate a dropped connection; the
  // real StateStreamClient (web/src/state/v2/stateStream.ts) auto-reconnects
  // with backoff and bumps its internal generation counter on the new
  // connect() call -- exactly the "generation change (recovery/reconnect)"
  // scenario this test targets.
  const connectionsBefore = socket.connectionCount()
  await socket.closeCurrent()
  await expect.poll(() => socket.connectionCount(), { timeout: 10000 }).toBeGreaterThan(connectionsBefore)

  // The pane is still present (no unmount/error state)...
  await expect(page.locator(`[data-pane-key="${sessionId}"]`)).toBeVisible()
  // ...and the tagged DOM node is still the exact one from before the
  // reconnect: terminalPool did not tear down and recreate the terminal
  // just because the WS connection generation changed.
  await expect(
    page.locator(`[data-pane-key="${sessionId}"] .xterm[data-e2e-stable-node="yes"]`),
  ).toBeVisible()
})
