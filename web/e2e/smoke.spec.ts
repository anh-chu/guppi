import { test, expect, Page } from '@playwright/test'

const LOCAL_HOST = {
  id: 'local',
  name: 'Local Machine',
  local: true,
  online: true,
  sessions: [],
  last_seen: new Date().toISOString(),
}

function makeSession(name: string, host = 'local', agentType = 'claude') {
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
    agent_type: agentType,
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
            current_command: agentType,
            current_path: '/tmp/e2e',
            pid: 1234,
          },
        ],
      },
    ],
  }
}

const DEFAULT_PREFS = {
  terminal: {
    font_size: 13,
    font_family: 'Space Mono',
    scrollback: 50000,
    renderer: 'webgl',
    unicode_graphemes: false,
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

async function installBackendStubs(
  page: Page,
  {
    needsSetup = false,
    authenticated = false,
    sessions,
    captures,
  }: {
    needsSetup?: boolean
    authenticated?: boolean
    sessions?: any[]
    captures?: { sessionNewBodies?: any[] }
  } = {},
) {
  const localSessions = sessions ?? []
  const capturedSessionNewBodies = captures?.sessionNewBodies

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
        body: JSON.stringify({ auth_required: true, needs_setup: needsSetup }),
      })
    }

    if (path === '/api/auth/check') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ authenticated }),
      })
    }

    if (path === '/api/auth/login' && method === 'POST') {
      return route.fulfill({ status: 200 })
    }

    if (path === '/api/auth/setup' && method === 'POST') {
      return route.fulfill({ status: 200 })
    }

    if (path === '/api/auth/logout' && method === 'POST') {
      return route.fulfill({ status: 200 })
    }

    if (path === '/api/preferences') {
      const body = method === 'PUT' ? await json() : DEFAULT_PREFS
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ ...DEFAULT_PREFS, ...body }),
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
        body: JSON.stringify(localSessions),
      })
    }

    if (path === '/api/session-order') {
      const data = method === 'POST' ? await json() : {}
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(data),
      })
    }

    if (path === '/api/groups') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({}),
      })
    }

    if (path === '/api/session-attrs') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ background: [], hidden: [], schedule_ids: {} }),
      })
    }

    if (path === '/api/crashed-sessions') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      })
    }

    if (path === '/api/stats') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          sessions: {
            total: localSessions.length,
            attached: 0,
            detached: 0,
          },
          windows: 0,
          panes: 0,
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
          current_version: '4.2.3',
          latest_version: '4.2.3',
          update_available: false,
          channel: 'stable',
        }),
      })
    }

    if (path === '/api/agent-status') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          agents: [
            {
              name: 'Claude',
              key: 'claude',
              installed: false,
              configured: false,
            },
          ],
          setup_command: 'echo install-stub',
        }),
      })
    }

    if (path === '/api/schedules') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      })
    }

    if (path === '/api/tool-events') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      })
    }

    if (path === '/api/active-turns') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      })
    }

    if (path === '/api/activity') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      })
    }

    if (path === '/api/session/new' && method === 'POST') {
      const body = await json()
      capturedSessionNewBodies?.push(body)
      const record = makeSession(body.name, body.host || undefined, body.agent_type || body.command || 'claude')
      localSessions.push(record)
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ name: body.name }),
      })
    }

    if (path === '/api/session/kill' && method === 'POST') {
      const body = await json()
      const index = localSessions.findIndex(
        (s) => s.name === body.name && (s.host ?? '') === (body.host ?? ''),
      )
      if (index !== -1) localSessions.splice(index, 1)
      return route.fulfill({ status: 204 })
    }

    if (path === '/api/session/display-name' && method === 'POST') {
      return route.fulfill({ status: 204 })
    }

    if (path === '/api/session/regenerate-name' && method === 'POST') {
      return route.fulfill({ status: 204 })
    }

    if (path === '/api/session/select-window' && method === 'POST') {
      return route.fulfill({ status: 204 })
    }

    if (path === '/api/group/name' && method === 'POST') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ name: 'group' }),
      })
    }

    // Catch any other backend calls so stubbed tests never see auth-401 flips.
    if (path.startsWith('/api/')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      })
    }

    return route.continue()
  })
}

test('setup page loads and initialises', async ({ page }) => {
  await installBackendStubs(page, { needsSetup: true, sessions: [] })
  await page.goto('/')
  await expect(page.getByText('Set a password to initialize')).toBeVisible()
  await page.getByPlaceholder('Choose password').fill('password123')
  await page.getByPlaceholder('Confirm password').fill('password123')
  await page.getByRole('button', { name: 'Set Password' }).click()
  await expect(page.getByText('Agent Setup')).toBeVisible()
})

test('login page loads and signs in', async ({ page }) => {
  await installBackendStubs(page, { sessions: [] })
  await page.goto('/')
  await expect(page.getByText('Enter Workspace')).toBeVisible()
  await page.getByPlaceholder('Password').fill('password123')
  await page.getByRole('button', { name: 'Enter Workspace' }).click()
  await expect(page.getByText('No sessions found')).toBeVisible()
})

test('overview page loads', async ({ page }) => {
  await installBackendStubs(page, { authenticated: true, sessions: [] })
  await page.goto('/')
  await expect(page.getByText('No sessions found')).toBeVisible()
  await expect(page.getByText('Termyard')).toBeVisible()
})

test('creates and kills a session', async ({ page }) => {
  const sessions: any[] = []
  await installBackendStubs(page, { authenticated: true, sessions })

  await page.goto('/')
  await expect(page.getByText('No sessions found')).toBeVisible()

  await page.getByTitle('New session (drag onto a pane to split)').click()
  await expect(page.getByText('New Session')).toBeVisible()

  await page.locator('input[placeholder="~"]').fill('/tmp/e2e-smoke')
  await page.getByRole('button', { name: 'Create' }).click()

  await expect(page).toHaveURL(/session\/.*\/e2e-smoke/)

  await page.goto('/')
  await expect(page.getByText('e2e-smoke').first()).toBeVisible()

  await page.getByText('e2e-smoke').first().click({ button: 'right' })
  await page.locator('text=Kill').first().click()
  await page.locator('text=Confirm kill?').first().click()

  await expect(page.getByText('No sessions found')).toBeVisible()
  expect(sessions).toHaveLength(0)
})

test('reconnects and restores persisted view', async ({ page }) => {
  const sessions = [makeSession('persisted')]
  await installBackendStubs(page, { authenticated: true, sessions })

  await page.goto('/')
  await expect(page.getByText('persisted').first()).toBeVisible()
  await page.getByText('persisted').first().click()
  await expect(page).toHaveURL(/session\/local\/persisted/)

  const collapsedBefore = await page.evaluate(() =>
    localStorage.getItem('termyard:sidebar-collapsed'),
  )
  expect(collapsedBefore).not.toBe('true')

  await page.getByTitle('Collapse sidebar').click()

  const collapsedAfter = await page.evaluate(() =>
    localStorage.getItem('termyard:sidebar-collapsed'),
  )
  expect(collapsedAfter).toBe('true')

  // In single-view session mode the active-key localStorage item may be
  // absent, but the URL itself is the persisted navigation state.
  const activeGroupId = await page.evaluate(() =>
    localStorage.getItem('termyard:active-group-id'),
  )
  expect(activeGroupId).toBeTruthy()

  await page.reload()
  await expect(page).toHaveURL(/session\/local\/persisted/)

  const collapsedReload = await page.evaluate(() =>
    localStorage.getItem('termyard:sidebar-collapsed'),
  )
  expect(collapsedReload).toBe('true')
})

test('drags New session onto a local pane and persists the split after reload', async ({ page }) => {
  const sessions = [makeSession('existing', 'local')]
  const sessionNewBodies: any[] = []
  await installBackendStubs(page, { authenticated: true, sessions, captures: { sessionNewBodies } })

  await page.goto('/session/local/existing')
  await expect(page).toHaveURL(/session\/local\/existing/)

  await expect(page.locator('[data-session-key="local/existing"]')).toBeVisible()

  const responsePromise = page.waitForResponse('**/api/session/new')
  await page.evaluate(() => {
    const source = document.querySelector('[title="New session (drag onto a pane to split)"]') as HTMLElement | null
    const target = document.querySelector('[data-drop-zone="main"]') as HTMLElement | null
    if (!source || !target) throw new Error('missing drag source or drop zone')

    const dt = new DataTransfer()
    dt.setData('application/x-termyard-new-session', '1')
    dt.effectAllowed = 'copy'

    const rect = target.getBoundingClientRect()
    const x = rect.left + rect.width / 2
    const y = rect.top + rect.height / 2

    source.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, dataTransfer: dt, clientX: 0, clientY: 0 }))
    target.dispatchEvent(new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer: dt, clientX: x, clientY: y }))
    target.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt, clientX: x, clientY: y }))
    source.dispatchEvent(new DragEvent('dragend', { bubbles: true, cancelable: true, dataTransfer: dt }))
  })
  await responsePromise

  expect(sessionNewBodies).toHaveLength(1)
  const body = sessionNewBodies[0]
  expect(body.host).toBe('local')
  expect(body.path).toBe('/tmp/e2e')
  expect(body).not.toHaveProperty('backend')

  await expect(page.locator('[data-pane-key="local/existing"]')).toBeVisible()
  await expect(page.locator('[data-pane-key="local/shell"]')).toBeVisible()

  await page.reload()

  await expect(page.locator('[data-pane-key="local/existing"]')).toBeVisible()
  await expect(page.locator('[data-pane-key="local/shell"]')).toBeVisible()
})
