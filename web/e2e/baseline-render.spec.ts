import { test, expect, Page } from '@playwright/test'

/**
 * Baseline render-time smoke: canonical schema, N local sessions, no split
 * layout (each session lives as a standalone leaf so the test only measures
 * list/sidebar render cost, not workspace-tree traversal). Mirrors the wire
 * shapes documented in web/e2e/smoke.spec.ts -- GET /api/state/bootstrap and
 * WS /ws/state, no tmux-shaped Session/Window/Pane, no _compat wrapper.
 */

const OWNER = 'ownerbaselinefixture1234567'

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
  owner_id: OWNER,
  sessions: [],
  last_seen: new Date().toISOString(),
}

function makeSessionRecord(id: string) {
  return {
    id,
    owner: OWNER,
    ref: `${id}:0.0`,
    phase: 'active',
    desired: 'run',
    revision: 1,
    created_at: new Date().toISOString(),
    name: id,
    shell: '/bin/bash',
    cwd: '/tmp/e2e',
    agent_type: 'claude',
    generation: 'gen-1',
  }
}

async function installBackendStubs(page: Page, sessions: ReturnType<typeof makeSessionRecord>[]) {
  const bootstrap = {
    owner: OWNER,
    revision: 1,
    local: { owner: OWNER, revision: 1, sessions, layouts: [] },
    remote: [],
    hosts: [LOCAL_HOST],
    // No tiled workspace -- every session renders through the sidebar/list
    // only, which is what this test measures.
    workspace: undefined,
    pending: [],
    pending_remote: [],
  }

  await page.route('**/api/**', async (route, request) => {
    const url = new URL(request.url())
    const path = url.pathname

    if (path === '/api/auth/status') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ auth_required: true, needs_setup: false }) })
    }
    if (path === '/api/auth/check') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ authenticated: true }) })
    }
    if (path === '/api/preferences') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DEFAULT_PREFS) })
    }
    if (path === '/api/hosts') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([LOCAL_HOST]) })
    }
    if (path === '/api/state/bootstrap') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(bootstrap) })
    }
    if (path.startsWith('/api/')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    }
    return route.continue()
  })

  await page.routeWebSocket('**/ws/state', (ws) => {
    ws.send(JSON.stringify({ type: 'catalog_snapshot', is_local: true, snapshot: { owner: OWNER, revision: 1, sessions, layouts: [] } }))
  })
}

async function measureRender(page: Page, count: number) {
  const sessions = Array.from({ length: count }, (_, i) =>
    makeSessionRecord(`sess-${i.toString().padStart(4, '0')}`)
  )

  await installBackendStubs(page, sessions)

  const start = Date.now()
  await page.goto('/')

  const lastKey = `sess-${(count - 1).toString().padStart(4, '0')}`
  await expect(page.locator(`[data-session-key="${lastKey}"]`)).toBeVisible({ timeout: 60000 })

  const elapsed = Date.now() - start
  test.info().annotations.push({
    type: 'render-time',
    description: `${count} sessions: ${elapsed.toFixed(1)}ms`,
  })
  return elapsed
}

test('baseline render time: 100 sessions', async ({ page }) => {
  const ms = await measureRender(page, 100)
  expect(ms).toBeGreaterThan(0)
  // eslint-disable-next-line no-console
  console.log(`baseline 100-session render time: ${ms.toFixed(1)}ms`)
})

test('baseline render time: 500 sessions', async ({ page }) => {
  const ms = await measureRender(page, 500)
  expect(ms).toBeGreaterThan(0)
  // eslint-disable-next-line no-console
  console.log(`baseline 500-session render time: ${ms.toFixed(1)}ms`)
})
