import { test, expect, Page, WebSocketRoute } from '@playwright/test'

/**
 * Canonical-schema smoke suite.
 *
 * Every stub in this file speaks the real wire protocol the production
 * frontend consumes:
 *   - GET  /api/state/bootstrap     (pkg/server/routes_state.go's BootstrapResponse)
 *   - WS   /ws/state                (pkg/ws/state_stream.go's catalog_snapshot /
 *                                     workspace_snapshot / catalog_owner_removed)
 *   - POST /api/state/session-commands (pkg/state/session_commands.go's
 *                                     create/kill/label/set_presentation actions)
 *
 * There is no tmux-shaped Session/Window/Pane fixture, no /api/session/new,
 * and no _compat wrapper anywhere in this file -- see
 * web/src/state/session/types.ts (LocalSessionRecord) and wireTypes.ts
 * (BootstrapResponse / StateStreamMessage) for the frozen shapes this
 * mirrors.
 */

// Realistic base32-lowercase OwnerID fixtures (same shape as
// testdata/session_ref_fixtures.json / multi-node.spec.ts's OWNER const).
const LOCAL_OWNER = 'ownerlocalfixture1234567a'
const REMOTE_OWNER = 'ownerremotefixture1234567b'
const REMOTE_FINGERPRINT = 'fp-remote-node-1'

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
  id: 'fp-local-node',
  name: 'Local Machine',
  local: true,
  online: true,
  owner_id: LOCAL_OWNER,
  sessions: [],
  last_seen: new Date().toISOString(),
}

type RawSessionRecord = {
  id: string
  owner: string
  ref: string
  phase: string
  desired: string
  revision: number
  created_at: string
  name?: string
  shell?: string
  cwd?: string
  agent_type?: string
  worktree_branch?: string
  schedule_id?: string
  hidden?: boolean
  background?: boolean
  generation?: string
}

/** Builds one canonical LocalSessionRecord on the wire -- direct fields, no _compat wrapper. */
function makeLocalSessionRecord(id: string, overrides: Partial<RawSessionRecord> = {}): RawSessionRecord {
  return {
    id,
    owner: LOCAL_OWNER,
    ref: `${id}:0.0`,
    phase: 'active',
    desired: 'run',
    revision: 1,
    created_at: new Date().toISOString(),
    name: id,
    shell: '/bin/bash',
    cwd: `/tmp/e2e/${id}`,
    agent_type: 'claude',
    generation: 'gen-1',
    ...overrides,
  }
}

type RawPaneNode =
  | { type: 'leaf'; ref: string }
  | { type: 'split'; id?: string; direction: 'h' | 'v'; ratio: number; first: RawPaneNode; second: RawPaneNode }

/** Removes the leaf matching `ref` from a raw PaneNode tree, collapsing an emptied split -- mirrors pkg/state's removeSessionFromWorkspacesLocked (see commit 0e5eeff). Returns null if the whole tree was that one leaf. */
function removeRefFromTree(node: RawPaneNode | null, ref: string): RawPaneNode | null {
  if (!node) return null
  if (node.type === 'leaf') return node.ref === ref ? null : node
  const first = removeRefFromTree(node.first, ref)
  const second = removeRefFromTree(node.second, ref)
  if (first && second) return { ...node, first, second }
  return first ?? second ?? null
}

/**
 * In-memory canonical state the fixture mutates as POST
 * /api/state/session-commands actions arrive, mirroring the server's
 * catalog.apply semantics closely enough for UI-level assertions (never a
 * substitute for the real pkg/state unit/integration tests).
 */
class FakeState {
  owner = LOCAL_OWNER
  revision = 1
  sessions = new Map<string, RawSessionRecord>()
  remoteSnapshots: Array<{ owner: string; revision: number; sessions: RawSessionRecord[] }> = []
  hosts: any[] = [LOCAL_HOST]
  workspaceRevision = 1
  tree: RawPaneNode | null = null
  activeKey: string | null = null
  private wsConns: WebSocketRoute[] = []

  seedLocal(rec: RawSessionRecord, tiled = true) {
    this.sessions.set(rec.id, rec)
    if (tiled) {
      this.tree = { type: 'leaf', ref: rec.ref }
      this.activeKey = rec.ref
    }
  }

  bootstrapRaw() {
    return {
      owner: this.owner,
      revision: this.revision,
      local: { owner: this.owner, revision: this.revision, sessions: Array.from(this.sessions.values()), layouts: [] },
      remote: this.remoteSnapshots,
      hosts: this.hosts,
      workspace: { id: 'layout-1', owner: this.owner, revision: this.workspaceRevision, tree: this.tree, active_key: this.activeKey ?? undefined },
      pending: [],
      pending_remote: [],
    }
  }

  registerSocket(ws: WebSocketRoute) {
    this.wsConns.push(ws)
    ws.onClose(() => {
      this.wsConns = this.wsConns.filter(w => w !== ws)
    })
  }

  broadcastCatalog() {
    const snapshot = { owner: this.owner, revision: this.revision, sessions: Array.from(this.sessions.values()), layouts: [] }
    for (const ws of this.wsConns) {
      ws.send(JSON.stringify({ type: 'catalog_snapshot', is_local: true, snapshot }))
    }
  }

  broadcastWorkspace() {
    const workspace = { id: 'layout-1', owner: this.owner, revision: this.workspaceRevision, tree: this.tree, active_key: this.activeKey ?? undefined }
    for (const ws of this.wsConns) {
      ws.send(JSON.stringify({ type: 'workspace_snapshot', workspace }))
    }
  }

  /** Applies one POST /api/state/session-commands body, mirroring pkg/state/session_commands.go's action switch. */
  applyCommand(body: { id: string; ref?: string; action: string; params?: any }): { status: number; result: any } {
    const { action, params } = body

    if (action === 'create') {
      this.revision += 1
      const id = `sess-${this.revision}`
      const rec = makeLocalSessionRecord(id, {
        name: params?.name || id,
        cwd: params?.cwd || '/tmp/e2e',
        agent_type: params?.agent_type || 'claude',
        schedule_id: params?.schedule_id,
      })
      if (params?.cwd && String(params.cwd).includes('__forceError')) {
        return { status: 400, result: { code: 'invalid_input', field: 'cwd', message: 'cwd does not exist' } }
      }
      this.sessions.set(id, rec)
      this.tree = { type: 'leaf', ref: rec.ref }
      this.activeKey = rec.ref
      this.broadcastCatalog()
      this.broadcastWorkspace()
      return { status: 200, result: { id: body.id, ref: rec.ref, display_name: rec.name, accepted: true } }
    }

    const sessionId = body.ref ? body.ref.split(':')[0].split('/').pop()! : ''
    const rec = this.sessions.get(sessionId)
    if (!rec) {
      return { status: 404, result: { code: 'not_found', field: 'ref.session', message: `session "${sessionId}" not found` } }
    }

    if (action === 'label') {
      this.revision += 1
      rec.name = params.label
      this.broadcastCatalog()
      return { status: 200, result: { id: body.id, ref: rec.ref, display_name: rec.name, accepted: true } }
    }

    if (action === 'kill') {
      this.revision += 1
      this.sessions.delete(sessionId)
      this.tree = removeRefFromTree(this.tree, rec.ref)
      if (this.activeKey === rec.ref) this.activeKey = null
      this.broadcastCatalog()
      this.broadcastWorkspace()
      return { status: 200, result: { id: body.id, ref: rec.ref, accepted: true } }
    }

    if (action === 'set_presentation') {
      this.revision += 1
      const backgrounding = params?.background === true && !rec.background
      if (params?.hidden !== undefined) rec.hidden = params.hidden
      if (params?.background !== undefined) rec.background = params.background
      if (backgrounding) {
        // Atomic with the flag flip on the real server -- see commit
        // 0e5eeff "fix(state): background sessions atomically with
        // workspace removal".
        this.workspaceRevision += 1
        this.tree = removeRefFromTree(this.tree, rec.ref)
        if (this.activeKey === rec.ref) this.activeKey = null
      }
      this.broadcastCatalog()
      if (backgrounding) this.broadcastWorkspace()
      return { status: 200, result: { id: body.id, ref: rec.ref, display_name: rec.name, accepted: true } }
    }

    return { status: 400, result: { code: 'invalid_input', field: 'action', message: `unsupported action "${action}"` } }
  }
}

async function installBackendStubs(
  page: Page,
  state: FakeState,
  opts: { toolEvents?: any[]; schedules?: any[] } = {},
) {
  await page.route('**/api/**', async (route, request) => {
    const url = new URL(request.url())
    const p = url.pathname
    const method = request.method()

    if (p === '/api/auth/status') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ auth_required: true, needs_setup: false }) })
    }
    if (p === '/api/auth/check') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ authenticated: true }) })
    }
    if (p === '/api/preferences') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DEFAULT_PREFS) })
    }
    if (p === '/api/hosts') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(state.hosts) })
    }
    if (p === '/api/state/bootstrap') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(state.bootstrapRaw()) })
    }
    if (p === '/api/tool-events') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(opts.toolEvents ?? []) })
    }
    if (p === '/api/schedules') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(opts.schedules ?? []) })
    }
    if (p === '/api/state/session-commands' && method === 'POST') {
      const body = JSON.parse(request.postData() || '{}')
      const { status, result } = state.applyCommand(body)
      return route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(result) })
    }
    if (p.startsWith('/api/')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    }
    return route.continue()
  })

  await page.routeWebSocket('**/ws/state', (ws) => {
    state.registerSocket(ws)
    ws.send(JSON.stringify({ type: 'catalog_snapshot', is_local: true, snapshot: { owner: state.owner, revision: state.revision, sessions: Array.from(state.sessions.values()), layouts: [] } }))
    if (state.tree) {
      ws.send(JSON.stringify({ type: 'workspace_snapshot', workspace: { id: 'layout-1', owner: state.owner, revision: state.workspaceRevision, tree: state.tree, active_key: state.activeKey ?? undefined } }))
    }
    for (const snap of state.remoteSnapshots) {
      ws.send(JSON.stringify({ type: 'catalog_snapshot', is_local: false, snapshot: snap }))
    }
  })

  // /ws/events (tool-event/activity live stream) is not needed for these
  // smoke scenarios -- leave it unstubbed; useWebSocket degrades to
  // connected=null without it, which none of these assertions depend on.
}

test.describe('smoke: canonical schema', () => {
  test('creating a session succeeds and renders its pane', async ({ page }) => {
    const state = new FakeState()
    await installBackendStubs(page, state)

    await page.goto('/')
    await page.getByTitle('New session (drag onto a pane to split)').click()
    await expect(page.getByText('New Session', { exact: true })).toBeVisible()
    await page.locator('input[placeholder="~"]').fill('/tmp/e2e/created')
    await page.getByRole('button', { name: 'Create' }).click()

    await expect(page.locator('[data-pane-key]')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText('created', { exact: true }).first()).toBeVisible()
  })

  test('a rejected create shows the server error instead of silently closing', async ({ page }) => {
    const state = new FakeState()
    await installBackendStubs(page, state)

    await page.goto('/')
    await page.getByTitle('New session (drag onto a pane to split)').click()
    await expect(page.getByText('New Session', { exact: true })).toBeVisible()
    await page.locator('input[placeholder="~"]').fill('/tmp/e2e/__forceError')
    await page.getByRole('button', { name: 'Create' }).click()

    await expect(page.getByText(/cwd does not exist|error/i).first()).toBeVisible({ timeout: 10_000 })
    // The modal must still be open -- a rejected create is not treated as success.
    await expect(page.getByText('New Session', { exact: true })).toBeVisible()
  })

  test('renaming the selected session keeps it selected under its new name (selection is ref-stable, not name-stable)', async ({ page }) => {
    const state = new FakeState()
    state.seedLocal(makeLocalSessionRecord('sess-stable', { name: 'before-rename' }))
    await installBackendStubs(page, state)

    await page.goto('/session/sess-stable')
    await expect(page.locator('[data-pane-key="sess-stable"]')).toBeVisible({ timeout: 10_000 })

    await page.getByText('before-rename', { exact: true }).first().click({ button: 'right' })
    await page.getByText('Rename', { exact: true }).first().click()
    const renameInput = page.locator('input:focus')
    await renameInput.fill('after-rename')
    await renameInput.press('Enter')

    await expect(page.getByText('after-rename', { exact: true }).first()).toBeVisible({ timeout: 10_000 })
    // Still the same pane/session identity (keyed by SessionID, never by name)
    // and the URL (which encodes the SessionID, not the display name) is unchanged.
    await expect(page.locator('[data-pane-key="sess-stable"]')).toBeVisible()
    await expect(page).toHaveURL(/session\/sess-stable/)
  })

  test('a session created by a schedule renders inside its schedule group, not the main list', async ({ page }) => {
    const state = new FakeState()
    state.seedLocal(makeLocalSessionRecord('sess-scheduled', { name: 'nightly-build', schedule_id: 'sched-1' }), false)
    await installBackendStubs(page, state, { schedules: [{ id: 'sched-1', name: 'Nightly Build' }] })

    await page.goto('/')
    await expect(page.locator('[data-schedule-id="sched-1"]')).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('[data-schedule-id="sched-1"]')).toContainText(/Nightly Build|sched-1/)
    // Must not also render as a top-level, ungrouped session row.
    await expect(page.locator('li[data-session-key="sess-scheduled"]:not([data-schedule-id] li[data-session-key="sess-scheduled"])')).toHaveCount(0)
  })

  test('backgrounding a tiled local session removes its pane and lists it under Background', async ({ page }) => {
    const state = new FakeState()
    const bg = makeLocalSessionRecord('sess-bg', { name: 'bg-target' })
    const keep = makeLocalSessionRecord('sess-keep', { name: 'keep-target' })
    state.sessions.set(bg.id, bg)
    state.sessions.set(keep.id, keep)
    // A real two-leaf split so backgrounding one leaf is a well-defined
    // sibling-promotion, not an empty-tree edge case.
    state.tree = { type: 'split', id: 'split-1', direction: 'h', ratio: 0.5, first: { type: 'leaf', ref: bg.ref }, second: { type: 'leaf', ref: keep.ref } }
    state.activeKey = bg.ref
    await installBackendStubs(page, state)

    await page.goto('/session/sess-bg')
    await expect(page.locator('[data-pane-key="sess-bg"]')).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('[data-pane-key="sess-keep"]')).toBeVisible({ timeout: 10_000 })

    await page.getByText('bg-target', { exact: true }).first().click({ button: 'right' })
    await page.getByText('Background', { exact: true }).first().click()

    // The backgrounded pane must be gone (the server removes it from the
    // layout atomically with the Background flag flip -- commit 0e5eeff);
    // its sibling remains, promoted to the sole leaf.
    await expect(page.locator('[data-pane-key="sess-bg"]')).not.toBeVisible({ timeout: 10_000 })
    await expect(page.locator('[data-pane-key="sess-keep"]')).toBeVisible()
    // Session must now render in the Background section.
    await expect(page.getByText('Background', { exact: true })).toBeVisible()
    await expect(page.getByText('bg-target', { exact: true })).toBeVisible()
  })

  test('a remote session whose host is offline still renders, marked offline', async ({ page }) => {
    const state = new FakeState()
    state.hosts.push({
      id: REMOTE_FINGERPRINT,
      name: 'Remote Box',
      local: false,
      online: false,
      owner_id: REMOTE_OWNER,
      sessions: [],
      last_seen: new Date().toISOString(),
    })
    state.remoteSnapshots.push({
      owner: REMOTE_OWNER,
      revision: 1,
      sessions: [{ ...makeLocalSessionRecord('sess-remote', { name: 'remote-agent' }), owner: REMOTE_OWNER, ref: `${REMOTE_OWNER}/sess-remote:0.0` }],
    })
    await installBackendStubs(page, state)

    await page.goto('/')
    const row = page.locator('[data-session-key]').filter({ hasText: 'remote-agent' })
    await expect(row).toBeVisible({ timeout: 10_000 })
    await expect(row).toContainText('Offline')
  })

  test('clicking a remote waiting-event alert navigates to and renders that remote session', async ({ page }) => {
    const state = new FakeState()
    state.hosts.push({
      id: REMOTE_FINGERPRINT,
      name: 'Remote Box',
      local: false,
      online: true,
      owner_id: REMOTE_OWNER,
      sessions: [],
      last_seen: new Date().toISOString(),
    })
    state.remoteSnapshots.push({
      owner: REMOTE_OWNER,
      revision: 1,
      sessions: [{ ...makeLocalSessionRecord('sess-remote-alert', { name: 'remote-waiting' }), owner: REMOTE_OWNER, ref: `${REMOTE_OWNER}/sess-remote-alert:0.0` }],
    })
    const toolEvents = [{
      tool: 'claude',
      status: 'waiting',
      host: REMOTE_FINGERPRINT,
      host_name: 'Remote Box',
      session: 'remote-waiting',
      session_id: 'sess-remote-alert',
      window: 0,
      pane: '',
      message: 'needs input',
      timestamp: new Date().toISOString(),
    }]
    await installBackendStubs(page, state, { toolEvents })

    await page.goto('/')
    // useToolEvents normalizes an event's key using useHosts' hostIndex at
    // ingestion time and only re-normalizes on its next poll once that
    // index updates (see useToolEvents.ts's normalizeToolEvent) -- so wait
    // for the alert to actually report "Waiting" (proof the host->owner
    // correlation has landed) before clicking it, instead of racing it.
    const alert = page.locator('header button', { hasText: 'Waiting' })
    await expect(alert).toBeVisible({ timeout: 15_000 })
    await expect(alert).toContainText('remote-waiting')
    await alert.click()

    await expect(page).toHaveURL(new RegExp(`session/${REMOTE_OWNER}/sess-remote-alert`))
    await expect(page.locator(`[data-pane-key="${REMOTE_OWNER}/sess-remote-alert"]`)).toBeVisible({ timeout: 10_000 })
  })
})
