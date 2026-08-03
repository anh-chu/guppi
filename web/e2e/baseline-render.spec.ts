import { test, expect, Page } from '@playwright/test'

const LOCAL_HOST = {
  id: 'local',
  name: 'Local Machine',
  local: true,
  online: true,
  sessions: [],
  last_seen: new Date().toISOString(),
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

function makeSession(name: string, host = 'local') {
  const now = new Date().toISOString()
  return {
    id: host ? `${host}/${name}` : name,
    name,
    host,
    host_name: 'Local Machine',
    host_online: true,
    backend: 'daemon',
    created: now,
    attached: true,
    last_activity: now,
    project_path: '/tmp/e2e',
    agent_type: 'claude',
    user_prompt: '',
    prompt_preview: '',
    last_agent_message: '',
    windows: [
      {
        id: `w-${host ? `${host}-` : ''}${name}`,
        session_id: name,
        name: 'shell',
        index: 0,
        active: true,
        layout: 'tiled',
        panes: [
          {
            id: `p-${host ? `${host}-` : ''}${name}`,
            window_id: `w-${host ? `${host}-` : ''}${name}`,
            session_id: name,
            index: 0,
            active: true,
            width: 80,
            height: 24,
            current_command: 'claude',
            current_path: '/tmp/e2e',
            pid: 1234,
          },
        ],
      },
    ],
  }
}

async function installBackendStubs(page: Page, sessions: any[]) {
  await page.route('**/api/**', async (route, request) => {
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname

    const json = async () => {
      const body = request.postData()
      return body ? JSON.parse(body) : {}
    }

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
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(DEFAULT_PREFS),
      })
    }

    if (path === '/api/hosts') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([LOCAL_HOST]),
      })
    }

    if (path === '/api/sessions') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(sessions),
      })
    }

    if (path === '/api/session-order') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
    }

    if (path === '/api/groups') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
    }

    if (path === '/api/session-attrs') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ background: [], hidden: [], schedule_ids: {} }),
      })
    }

    if (path === '/api/crashed-sessions') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    }

    if (path === '/api/stats') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          sessions: { total: sessions.length, attached: sessions.length, detached: 0 },
          windows: sessions.length,
          panes: sessions.length,
          agent_panes: 0,
          agents: { active: 0, waiting: 0, stuck: 0, error: 0 },
          processes: [],
        }),
      })
    }

    if (path === '/api/update') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          current_version: '4.3.0',
          latest_version: '4.3.0',
          update_available: false,
          channel: 'stable',
        }),
      })
    }

    if (path === '/api/tool-events') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    }

    if (path === '/api/active-turns') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    }

    if (path === '/api/activity') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    }

    if (path.startsWith('/api/')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    }

    return route.continue()
  })
}

async function measureRender(page: Page, count: number) {
  const sessions = Array.from({ length: count }, (_, i) =>
    makeSession(`sess-${i.toString().padStart(4, '0')}`)
  )

  await installBackendStubs(page, sessions)

  const start = Date.now()
  await page.goto('/')

  const lastKey = `local/sess-${(count - 1).toString().padStart(4, '0')}`
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
