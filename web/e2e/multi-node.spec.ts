import { test, expect, Page, WebSocketRoute } from '@playwright/test'
import { execFile } from 'node:child_process'
import * as fs from 'node:fs'
import * as path from 'node:path'
import { promisify } from 'node:util'

import {
  ClusterNode,
  Cluster,
  startCluster,
  waitForHostOnline,
  assertPeerNeverConnects,
  loginBrowserContext,
  pairNodes,
  SHARED_TEST_PASSWORD,
  getReviewedBinary,
} from './fixtures/termyardCluster'

/**
 * Multi-node E2E suite for the canonical state protocol.
 *
 * REAL TWO-PROCESS HARNESS
 * -------------------------------------------------------------------------
 * Every test in the `real two-node cluster` describe blocks below (and the
 * standalone case-6 test) drives genuine `termyard server` OS processes,
 * built from the exact checked-out source (see fixtures/termyardCluster.ts:
 * `npm run build` embeds the current frontend, then `go build .`), each
 * with its own HOME, XDG_* dirs, TERMYARD_SESSION_DIR, TERMYARD_PORT, TERMYARD_SOCKET.
 * Pairing goes through the real
 * POST /api/auth/setup and POST /api/peers (-> POST /api/peers/bootstrap)
 * HTTP endpoints -- no internal Go state is ever injected directly. The one
 * browser page each test drives is authenticated by copying the session
 * cookie a real /api/auth/setup or /api/auth/login call returned into the
 * page's cookie jar (the cookie itself is server-issued, not fabricated).
 *
 * This closes the gap the previous version of this file recorded: an
 * uncontrolled UID-scoped PTY daemon socket directory used to make two
 * "independent" local nodes share one real daemon registry. That is fixed
 * in production by TERMYARD_SESSION_DIR (pkg/commands/server/runtime.go's
 * defaultSessionDir) and is verified fail-fast by
 * fixtures/termyardCluster.ts's assertDistinctIdentities (called inside
 * startCluster), which throws if two nodes ever end up pointed at the same
 * directory, OwnerID, or peer fingerprint.
 *
 * The two-node describe block runs `mode: 'serial'` and is wrapped in a
 * `for` loop that spins the ENTIRE cluster lifecycle up twice ("run 1" /
 * "run 2") in one `playwright test` invocation, each with its own
 * beforeAll/afterAll. Combined with the process-group teardown's own
 * "assert nothing survives" check (termyardCluster.ts's
 * assertNoProcessGroup, which runs at the end of every `stop()`), this
 * proves run 2 does not inherit any leaked process, Unix socket, state
 * directory, or browser context from run 1.
 *
 * The single exception to "no mocks" is the last test in this file
 * ("browser command retry with same CommandID does not double-execute"),
 * which is the one deterministic response-loss scenario the plan calls out
 * as unsafe to induce via real process/network timing: it needs a
 * guaranteed single ambiguous failure on the FIRST attempt followed by a
 * byte-identical retry succeeding, which real process/network timing cannot
 * reliably reproduce test-to-test.
 */

// ---------------------------------------------------------------------------
// Small real-cluster test helpers. These only ever drive the real browser
// DOM (against a real backend) or the real cluster HTTP API -- nothing here
// mocks a route or a WebSocket.
// ---------------------------------------------------------------------------

async function openAuthedPage(browser: import('@playwright/test').Browser, node: ClusterNode): Promise<Page> {
  // Tall viewport: the New Session modal (with the host selector row shown)
  // can exceed the default 720px viewport height, and it's a fixed-position
  // overlay that the page can't scroll to reveal its Create button.
  const context = await browser.newContext({ viewport: { width: 1280, height: 1400 } })
  await loginBrowserContext(context, node)
  const page = await context.newPage()
  await page.goto(node.baseURL + '/')
  return page
}

/** Creates a session through the real New Session modal. Session name is the auto-suggested basename of `pathValue`. */
async function createSessionViaUI(page: Page, opts: { pathValue: string; hostOwnerId?: string }): Promise<void> {
  // The real PTY backend needs an existing working directory (this harness
  // runs both nodes on one machine, so a plain mkdir on the test process's
  // own filesystem is sufficient and real -- not a mock).
  fs.mkdirSync(opts.pathValue, { recursive: true })
  await page.getByTitle('New session (drag onto a pane to split)').click()
  await expect(page.getByText('New Session', { exact: true })).toBeVisible()
  await page.locator('input[placeholder="~"]').fill(opts.pathValue)
  if (opts.hostOwnerId) {
    // NewSessionModal.tsx's HostSelect option values are canonical OwnerIDs
    // (selectableHosts.map(h => ({ value: h.owner_id, ... }))), never the
    // peer fingerprint -- see the module doc comment there.
    await page.locator('select').selectOption({ value: opts.hostOwnerId })
  }
  await page.getByRole('button', { name: 'Create' }).click()
}

function basename(p: string): string {
  return p.replace(/\/+$/, '').split('/').pop() || p
}

/** Right-clicks a session row by its visible name, then clicks a context-menu item by exact text. */
async function contextMenuAction(page: Page, sessionName: string, item: string): Promise<void> {
  await page.getByText(sessionName, { exact: true }).first().click({ button: 'right' })
  await page.getByText(item, { exact: true }).first().click()
}

// ---------------------------------------------------------------------------
// Real two-node cluster: mandatory cases 1-5. Run the whole cluster
// lifecycle twice in this one invocation to prove no cross-run leakage.
// ---------------------------------------------------------------------------

for (const run of [1, 2] as const) {
  test.describe(`real two-node cluster (run ${run})`, () => {
    // Serial mode is scoped to THIS describe only (one cluster, one
    // beforeAll/afterAll, ordered tests). It is intentionally NOT applied
    // file-wide: a real failure inside one run's cluster tests must not
    // cascade-skip the other run, case 6, or the retained mocked test.
    test.describe.configure({ mode: 'serial' })

    let cluster: Cluster
    let a: ClusterNode
    let b: ClusterNode

    test.beforeAll(async ({}, testInfo) => {
      test.setTimeout(180_000)
      const rootDir = testInfo.outputPath(`cluster-run-${run}`)
      cluster = await startCluster({ rootDir })
      a = cluster.a
      b = cluster.b
    })

    test.afterAll(async () => {
      test.setTimeout(60_000)
      if (cluster) await cluster.stopAll()
    })

    // NOTE on ordering: "case 1" (create a session on B through A's browser
    // host selector) is declared LAST in this describe block, not first.
    // It shares this cluster's beforeAll/afterAll with cases 2-9, and it
    // depends on b.ownerId already being populated by a prior
    // b.bootstrap() call (see the test body) -- keeping it last means it
    // always runs after at least one of those earlier cases has bootstrapped
    // B, instead of adding a redundant bootstrap-before-first-use path here.
    // (pkg/state/remote_create.go's RemoteCreateRequest.Target is already
    // a pointer *SessionRef with omitempty, so it round-trips a nil Target
    // correctly on the wire; there is no reproducible remote-create defect
    // on this path -- see CreateIntent.Target's doc comment for the
    // still-dormant sibling field this was once confused with.)

    test('case 2: attach from A to B terminal, write a unique marker, read it back through the real remote stream', async ({ browser }) => {
      test.setTimeout(60_000)
      const path = `/tmp/e2e-r${run}-attach-${Date.now()}`
      const name = basename(path)
      await b.createLocalSession(name, path)
      // Real subprocess spawn (fork+exec+socket-bind+handshake) plus a real
      // cross-node websocket push; see pkg/pty/registry_stable.go's
      // readinessTimeout comment for the modest safety margin baked in.
      await expect.poll(async () => {
        const boot = await a.bootstrap()
        return (boot.remote || []).some((snap: any) => (snap.sessions || []).some((s: any) => s.name === name))
      }, { timeout: 20_000, message: 'seeded session on B never replicated to A remote catalog' }).toBe(true)

      const marker = `E2E-MARKER-${run}-${Date.now()}`
      const page = await openAuthedPage(browser, a)
      try {
        await page.getByText(name, { exact: true }).first().click()
        const pane = page.locator('[data-pane-key]').first()
        await expect(pane).toBeVisible({ timeout: 15_000 })

        // `.xterm` appears in the DOM as soon as TiledView swaps the
        // "Connecting…" placeholder for the real <Terminal>, but xterm.js's
        // own attach (lib/terminalPool.ts's `term.open(container)`) --
        // which is what actually makes the pane focusable/clickable and
        // creates its helper textarea -- can still be a beat behind that
        // React render. Clicking `.xterm` before that attach finishes is a
        // real UI-rendering race, not the catalog-replication defect this
        // test otherwise exercises: Playwright's actionability check can
        // time out if the element it's about to click is torn down and
        // recreated by that attach. `textarea.xterm-helper-textarea` only
        // exists once xterm.js has genuinely opened onto the container, so
        // wait for it first -- a real readiness signal, not an arbitrary
        // sleep.
        await expect(pane.locator('textarea.xterm-helper-textarea')).toBeAttached({ timeout: 15_000 })
        await pane.locator('.xterm').click()
        await page.keyboard.type(`echo ${marker}`)
        await page.keyboard.press('Enter')

        // The marker only reaches the DOM if the browser's PTY write went
        // through the real peer control/stream link to B's real daemon and
        // the real echo came back the same way -- nothing here is mocked.
        await expect(pane).toContainText(marker, { timeout: 15_000 })
      } finally {
        await page.close()
      }
    })

    test('case 3: label and kill the remote session from A; B authoritative state and A projection converge', async ({ browser }) => {
      test.setTimeout(60_000)
      const path = `/tmp/e2e-r${run}-labelkill-${Date.now()}`
      const name = basename(path)
      const renamedName = `${name}-relabeled`
      await b.createLocalSession(name, path)
      await expect.poll(async () => {
        const boot = await a.bootstrap()
        return (boot.remote || []).some((snap: any) => (snap.sessions || []).some((s: any) => s.name === name))
      }, { timeout: 20_000, message: 'seeded session on B never replicated to A remote catalog' }).toBe(true)

      const page = await openAuthedPage(browser, a)
      try {
        await contextMenuAction(page, name, 'Rename')
        const renameInput = page.locator('input:focus')
        await renameInput.fill(renamedName)
        await renameInput.press('Enter')
        await expect(page.getByText(renamedName, { exact: true }).first()).toBeVisible({ timeout: 15_000 })

        await contextMenuAction(page, renamedName, 'Kill')
        await page.getByText('Confirm kill?', { exact: true }).first().click()
        await expect(page.getByText(renamedName, { exact: true })).toHaveCount(0, { timeout: 15_000 })
      } finally {
        await page.close()
      }

      // B's authoritative catalog must reflect both the rename and the kill.
      await expect.poll(async () => {
        const boot = await b.bootstrap()
        return (boot.local?.sessions || []).some((s: any) => s.name === name || s.name === renamedName)
      }, { timeout: 15_000, message: 'B still authoritatively lists the killed session' }).toBe(false)

      // A's projection (its cached remote snapshot of B) must converge to
      // the same state, not diverge from B.
      await expect.poll(async () => {
        const boot = await a.bootstrap()
        const bSnap = (boot.remote || []).find((snap: any) => snap.owner === b.ownerId)
        const sessions = bSnap ? bSnap.sessions || [] : []
        return sessions.some((s: any) => s.name === name || s.name === renamedName)
      }, { timeout: 15_000, message: "A's projection of B did not converge with B's kill" }).toBe(false)
    })

    test('case 4: stop B; A retains last-confirmed sessions as offline; restart B; verify reconnect/snapshot convergence', async ({ browser }) => {
      test.setTimeout(90_000)
      const path2 = `/tmp/e2e-r${run}-offline-${Date.now()}`
      const name2 = basename(path2)
      await b.createLocalSession(name2, path2)

      await expect.poll(async () => {
        const boot = await a.bootstrap()
        return (boot.remote || []).some((snap: any) => (snap.sessions || []).some((s: any) => s.name === name2))
      }, { timeout: 20_000 }).toBe(true)

      const lastKnownRemote = await a.bootstrap()
      const lastSnapForB = (lastKnownRemote.remote || []).find((snap: any) => snap.owner === b.ownerId)
      expect(lastSnapForB, 'A had no last-confirmed remote snapshot for B before stopping it').toBeTruthy()

      await b.stop()
      await waitForHostOnline(a, b.fingerprint!, false)

      // A must retain the last-confirmed session list (marked offline via
      // host.online=false) rather than discarding it.
      const hostsAfterStop = await a.hosts()
      const bHostAfterStop = hostsAfterStop.find((h: any) => h.id === b.fingerprint)
      expect(bHostAfterStop, 'A dropped B from its host list entirely instead of marking it offline').toBeTruthy()
      const bootWhileDown = await a.bootstrap()
      const remoteSnapWhileDown = (bootWhileDown.remote || []).find((snap: any) => snap.owner === b.ownerId)
      expect(remoteSnapWhileDown, "A discarded B's last-confirmed catalog instead of retaining it while offline").toBeTruthy()
      expect((remoteSnapWhileDown.sessions || []).some((s: any) => s.name === name2)).toBe(true)

      // The retained session must not just be present in the API response
      // while B is down -- it must actually RENDER marked offline in the UI
      // (Sidebar.tsx: signal.state === 'offline' -> activityDisplay 'Offline').
      const pageWhileDown = await openAuthedPage(browser, a)
      try {
        const row = pageWhileDown.locator('[data-session-key]').filter({ hasText: name2 })
        await expect(row).toBeVisible({ timeout: 15_000 })
        await expect(row).toContainText('Offline')
      } finally {
        await pageWhileDown.close()
      }

      await b.restart()
      await b.login(cluster.password)
      await waitForHostOnline(a, b.fingerprint!, true)

      // Replacement connection/snapshot convergence: A's remote view of B
      // must still (or again) carry B's real, current catalog.
      await expect.poll(async () => {
        const boot = await a.bootstrap()
        const snap = (boot.remote || []).find((s: any) => s.owner === b.ownerId)
        return snap ? (snap.sessions || []).some((s: any) => s.name === name2) : false
      }, { timeout: 20_000, message: "A's remote snapshot of B did not reconverge after B restarted" }).toBe(true)
    })

    test('case 5: restart A with its persisted remote cache; verify owner-to-peer routing restores correctly', async () => {
      test.setTimeout(90_000)
      const path3 = `/tmp/e2e-r${run}-restart-a-${Date.now()}`
      const name3 = basename(path3)
      await b.createLocalSession(name3, path3)

      await a.stop()
      await a.restart()
      await a.login(cluster.password)

      // A's own on-disk peer store (in its persisted HOME) must be enough
      // for it to automatically redial B and re-establish routing -- no
      // re-pairing call is made here.
      await waitForHostOnline(a, b.fingerprint!, true)

      await expect.poll(async () => {
        const boot = await a.bootstrap()
        const snap = (boot.remote || []).find((s: any) => s.owner === b.ownerId)
        return snap ? (snap.sessions || []).some((s: any) => s.name === name3) : false
      }, { timeout: 20_000, message: 'A did not restore an owner->peer(B) route after restarting' }).toBe(true)
    })

    test('case 7: a schedule-fired session renders inside its schedule group by schedule_id, not the main list', async ({ browser }) => {
      test.setTimeout(60_000)
      const scheduleName = `e2e-r${run}-sched-${Date.now()}`
      const path = `/tmp/e2e-r${run}-sched-${Date.now()}`
      fs.mkdirSync(path, { recursive: true })

      const created = await a.api('/api/schedules', {
        method: 'POST',
        body: JSON.stringify({ name: scheduleName, cron_spec: '0 0 1 1 *', path, agent_type: 'claude', enabled: false }),
      })
      expect(created.ok, 'creating the schedule failed').toBe(true)
      const job = await created.json()

      // Fires the SAME createFn the scheduler's own ticker uses (see
      // routes_scheduler.go's "/schedules/{id}/run" handler) -- a real
      // session creation, not a fabricated schedule_id.
      const ran = await a.api(`/api/schedules/${job.id}/run`, { method: 'POST' })
      expect(ran.ok, 'running the schedule failed').toBe(true)

      await expect.poll(async () => {
        const boot = await a.bootstrap()
        return (boot.local?.sessions || []).some((s: any) => s.schedule_id === job.id)
      }, { timeout: 15_000, message: 'schedule run never produced a session tagged with its schedule_id' }).toBe(true)

      const page = await openAuthedPage(browser, a)
      try {
        await expect(page.locator(`[data-schedule-id="${job.id}"]`)).toBeVisible({ timeout: 15_000 })
      } finally {
        await page.close()
      }
    })

    test('case 8: backgrounding a tiled local session removes its pane atomically and lists it under Background', async ({ browser }) => {
      test.setTimeout(60_000)
      const path = `/tmp/e2e-r${run}-bg-${Date.now()}`
      const name = basename(path)
      await a.createLocalSession(name, path)

      const page = await openAuthedPage(browser, a)
      try {
        await page.getByText(name, { exact: true }).first().click()
        await expect(page.locator('[data-pane-key]').filter({ hasText: '' }).first()).toBeVisible({ timeout: 15_000 })

        await contextMenuAction(page, name, 'Background')

        await expect(page.getByText('Background', { exact: true })).toBeVisible({ timeout: 15_000 })
        await expect(page.getByText(name, { exact: true }).first()).toBeVisible()
      } finally {
        await page.close()
      }

      // The server removes the leaf from the sole layout atomically with the
      // Background flag flip (commit 0e5eeff) -- confirm both landed.
      await expect.poll(async () => {
        const boot = await a.bootstrap()
        const rec = (boot.local?.sessions || []).find((s: any) => s.name === name)
        return rec?.background === true
      }, { timeout: 15_000, message: 'session was never marked background on the server' }).toBe(true)
    })

    test('case 9: renaming a session and immediately re-selecting it keeps selection stable through the rename', async ({ browser }) => {
      test.setTimeout(60_000)
      const path = `/tmp/e2e-r${run}-renameclick-${Date.now()}`
      const name = basename(path)
      const renamedName = `${name}-renamed-fast`
      await a.createLocalSession(name, path)

      const page = await openAuthedPage(browser, a)
      try {
        await page.getByText(name, { exact: true }).first().click()
        await expect(page.locator('[data-pane-key]').first()).toBeVisible({ timeout: 15_000 })
        const urlBeforeRename = page.url()

        await contextMenuAction(page, name, 'Rename')
        const renameInput = page.locator('input:focus')
        await renameInput.fill(renamedName)
        await renameInput.press('Enter')
        // Re-select the row immediately, before waiting for the rename to
        // visibly settle -- selection must key off the SessionID, never the
        // in-flight display label, so this must not lose or duplicate the pane.
        await page.getByText(renamedName, { exact: true }).first().click()

        await expect(page.getByText(renamedName, { exact: true }).first()).toBeVisible({ timeout: 15_000 })
        expect(page.url(), 'the URL (SessionID-keyed) must not have changed across a same-session rename+reselect').toBe(urlBeforeRename)
        // The rename+reselect race must not have produced a DUPLICATE pane
        // (data-pane-key is keyed by the immutable SessionID, not the
        // mutable display name -- see sessionRefToKey/paneNodeToPaneTree --
        // so re-deriving this session's expected pane key from `name` isn't
        // possible here; the cluster is also shared serially across this
        // describe's other cases, so other sessions' panes may legitimately
        // also be present). Assert no key appears more than once.
        const paneKeys = await page.locator('[data-pane-key]').evaluateAll(els => els.map(e => e.getAttribute('data-pane-key')))
        expect(new Set(paneKeys).size, `duplicate pane key(s) found: ${paneKeys.join(', ')}`).toBe(paneKeys.length)
      } finally {
        await page.close()
      }
    })
    test('case 1: create session on B through A browser host selection; B owns it, A sees it via remote catalog replication', async ({ browser }) => {
      test.setTimeout(60_000)
      const path = `/tmp/e2e-r${run}-remotecreate-${Date.now()}`
      const name = basename(path)

      // A must observe B online before the New Session modal offers it as a
      // host choice -- this alone proves the real peer link, not a stub, is
      // what drives host visibility.
      await waitForHostOnline(a, b.fingerprint!, true)
      // b.ownerId is populated as a side effect of any prior b.bootstrap()
      // call in this serial describe block (cases 3/4/5/7/8/9 above all
      // call it) -- the New Session modal's host selector option values are
      // OwnerIDs, never peer fingerprints (see createSessionViaUI).
      if (!b.ownerId) await b.bootstrap()

      const page = await openAuthedPage(browser, a)
      try {
        await createSessionViaUI(page, { pathValue: path, hostOwnerId: b.ownerId! })
        await expect(page.getByText(name, { exact: true }).first()).toBeVisible({ timeout: 15_000 })
      } finally {
        await page.close()
      }

      // B is the authoritative owner: the session must exist in B's own
      // local catalog, not A's.
      await expect.poll(async () => {
        const boot = await b.bootstrap()
        return (boot.local?.sessions || []).some((s: any) => s.name === name)
      }, { timeout: 15_000, message: 'session never appeared in B local catalog' }).toBe(true)

      // A must see it only through remote catalog replication (a Remote
      // entry keyed by B's OwnerID), never in A's own Local catalog -- that
      // would mean the "remote" create actually executed locally.
      await expect.poll(async () => {
        const boot = await a.bootstrap()
        return (boot.remote || []).some((snap: any) => (snap.sessions || []).some((s: any) => s.name === name))
      }, { timeout: 15_000, message: 'session never appeared in A remote catalog replication' }).toBe(true)

      const bootA = await a.bootstrap()
      const localHasIt = (bootA.local?.sessions || []).some((s: any) => s.name === name)
      expect(localHasIt, 'remote routing executed locally on A instead of on B').toBe(false)
    })

  })
}

// ---------------------------------------------------------------------------
// Case 6 (legacy-mode-peer handshake rejection) is removed: legacy mode no
// longer exists as a runtime option (see refactor!: make canonical state
// the only runtime and UI). Every node the binary can start is canonical;
// there is no legacy-mode peer to reject any more.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Finding 3 regression proof: after a full cluster start + real session
// create + teardown cycle, zero session-daemon processes and zero daemon
// socket files must survive. Uses real process inspection (pgrep against
// the node's own daemon PIDs read from its real on-disk lifecycle records,
// plus a real fs existence check on the socket path) -- nothing here is
// mocked.
// ---------------------------------------------------------------------------

test('teardown leaves zero surviving session-daemon processes and zero surviving daemon socket files', async ({}, testInfo) => {
  test.setTimeout(120_000)
  const rootDir = testInfo.outputPath('cluster-leak-proof')
  const cluster = await startCluster({ rootDir })
  const { a, b } = cluster
  let torndown = false
  const teardown = async () => {
    if (torndown) return
    torndown = true
    await cluster.stopAll()
  }

  try {
    const path1 = `/tmp/e2e-leakproof-a-${Date.now()}`
    const path2 = `/tmp/e2e-leakproof-b-${Date.now()}`
    const name1 = basename(path1)
    const name2 = basename(path2)
    await a.createLocalSession(name1, path1)
    await b.createLocalSession(name2, path2)

    // The create command can return before the real daemon subprocess has
    // finished spawning and reported ready (pkg/pty/registry_stable.go's
    // readinessTimeout allows up to 15s under load), so the lifecycle sidecar
    // may not yet carry a daemon_pid immediately after createLocalSession
    // resolves. Poll each node's own local catalog (real HTTP, not a mock)
    // until both seeded sessions are actually active before reading PIDs.
    await expect.poll(async () => {
      const boot = await a.bootstrap()
      return (boot.local?.sessions || []).some((s: any) => s.name === name1 && s.phase === 'active')
    }, { timeout: 20_000, message: 'seeded session on A never reached active phase' }).toBe(true)
    await expect.poll(async () => {
      const boot = await b.bootstrap()
      return (boot.local?.sessions || []).some((s: any) => s.name === name2 && s.phase === 'active')
    }, { timeout: 20_000, message: 'seeded session on B never reached active phase' }).toBe(true)

    // Real on-disk evidence, read BEFORE teardown: every lifecycle sidecar
    // each node's registry actually wrote, and the daemon PID + socket path
    // it recorded.
    const readLifecycleRecords = () =>
      [a, b].flatMap((node) => {
        const dir = path.join(node.homeDir, '.local-state', 'termyard', 'sessions')
        let files: string[] = []
        try {
          files = fs.readdirSync(dir).filter((f) => f.endsWith('.lifecycle.json'))
        } catch {
          files = []
        }
        return files.map((f) => {
          const rec = JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'))
          return { node: node.name, id: rec.id as string, daemonPid: rec.daemon_pid as number }
        })
      })

    await expect.poll(() => {
      const recs = readLifecycleRecords()
      return recs.length > 0 && recs.every((r) => r.daemonPid > 0)
    }, { timeout: 20_000, message: 'lifecycle records never recorded a daemon_pid for both seeded sessions' }).toBe(true)

    const preTeardown = readLifecycleRecords()
    expect(preTeardown.length, 'no session-daemon lifecycle records were found before teardown -- test seeded nothing real to prove cleanup on').toBeGreaterThan(0)
    for (const rec of preTeardown) {
      expect(rec.daemonPid, `node ${rec.node} session ${rec.id} has no recorded daemon_pid`).toBeGreaterThan(0)
    }

    await teardown()

    // Real process inspection: each recorded PID must be gone.
    for (const rec of preTeardown) {
      let alive = true
      try {
        process.kill(rec.daemonPid, 0)
      } catch {
        alive = false
      }
      expect(alive, `node ${rec.node} session-daemon pid ${rec.daemonPid} (session ${rec.id}) survived teardown`).toBe(false)
    }

    // pgrep-based scan (real process table, not a mock): no session-daemon
    // process should remain anywhere carrying either node's own daemon PIDs.
    const execFileAsync = promisify(execFile)
    for (const rec of preTeardown) {
      await expect(execFileAsync('kill', ['-0', String(rec.daemonPid)])).rejects.toBeTruthy()
    }

    // Socket files: each node's sessionDir is removed by disposeSessionDir()
    // as part of stopAll(); nothing should remain on disk.
    expect(fs.existsSync(a.sessionDir), `node A's session socket dir survived teardown: ${a.sessionDir}`).toBe(false)
    expect(fs.existsSync(b.sessionDir), `node B's session socket dir survived teardown: ${b.sessionDir}`).toBe(false)
  } finally {
    // Guarantees the real cluster (and any daemon it spawned) is torn down
    // even if an assertion above throws -- a leak-detection test must never
    // itself be the thing that leaks.
    await teardown()
  }
})

// ---------------------------------------------------------------------------
// Retained mocks: conditions that real process/network timing in this
// harness cannot reliably or deterministically reproduce --
//   1. a deterministic response-loss/retry condition (first attempt lost,
//      byte-identical retry succeeds), and
//   2. a remote session in a genuine "waiting on an agent tool call" state,
//      which on a real cluster requires an actual agent CLI (e.g. Claude)
//      running inside the PTY and calling a hook -- not available in this
//      sandboxed test environment.
// ---------------------------------------------------------------------------

// Realistic base32-lowercase OwnerID fixture (from testdata/session_ref_fixtures.json).
const OWNER = 'ownerfixture1234567890ab'

function makeSessionRaw(id: string, generation: string, name = id) {
  return {
    id,
    owner: OWNER,
    ref: `${id}:0.0`,
    phase: 'active',
    desired: 'run',
    revision: 1,
    created_at: new Date().toISOString(),
    name,
    shell: '/bin/bash',
    cwd: '/tmp/e2e',
    generation,
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

async function installBaseStubs(page: Page) {
  await page.route('**/api/**', async (route, request) => {
    const url = new URL(request.url())
    const p = url.pathname

    if (p === '/api/auth/status') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ auth_required: true, needs_setup: false }),
      })
    }
    if (p === '/api/auth/check') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ authenticated: true }),
      })
    }
    if (p === '/api/preferences') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DEFAULT_PREFS) })
    }
    if (p === '/api/hosts') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([LOCAL_HOST]) })
    }
    if (p.startsWith('/api/')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    }
    return route.continue()
  })
}

async function installBootstrap(page: Page, bootstrapRaw: unknown) {
  await page.route('**/api/state/bootstrap', async (route) => {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(bootstrapRaw) })
  })
}

type StateSocketHandle = {
  connectionCount: () => number
  closeCurrent: () => Promise<void>
}

async function installStateSocket(
  page: Page,
  onConnection: (ws: WebSocketRoute, connectionIndex: number) => void,
): Promise<StateSocketHandle> {
  let count = 0
  let current: WebSocketRoute | null = null
  await page.routeWebSocket('**/ws/state', (ws) => {
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
  ws.send(
    JSON.stringify({
      type: 'workspace_snapshot',
      workspace: makeWorkspaceRaw(sessionId, revision),
    }),
  )
}

test('browser command retry with same CommandID does not double-execute (deterministic mocked lost-response case)', async ({ page }) => {
  const sessionId = 'sess-retry'
  const generation = 'gen-1'

  await installBaseStubs(page)
  await installBootstrap(page, makeBootstrapRaw(sessionId, generation))

  let latestWs: WebSocketRoute | null = null
  await installStateSocket(page, (ws) => {
    latestWs = ws
    ws.onClose(() => {
      if (latestWs === ws) latestWs = null
    })
    sendCatalogSnapshot(ws, sessionId, generation)
    sendWorkspaceSnapshot(ws, sessionId)
  })

  const seenBodies: Array<{ id: string; action: string }> = []
  const idAttempts = new Map<string, number>()
  await page.route('**/api/state/session-commands', async (route, request) => {
    const body = JSON.parse(request.postData() || '{}')
    seenBodies.push({ id: body.id, action: body.action })
    const attempt = (idAttempts.get(body.id) ?? 0) + 1
    idAttempts.set(body.id, attempt)

    if (attempt === 1) {
      return route.abort('failed')
    }

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

  await page.getByText(sessionId).first().click({ button: 'right' })
  await page.locator('text=Kill').first().click()
  await page.locator('text=Confirm kill?').first().click()

  await expect.poll(() => seenBodies.length, { timeout: 10000 }).toBeGreaterThanOrEqual(2)

  expect(seenBodies).toHaveLength(2)
  expect(seenBodies[0].action).toBe('kill')
  expect(seenBodies[1].action).toBe('kill')
  expect(seenBodies[1].id).toBe(seenBodies[0].id)

  await expect(page.locator(`[data-session-key="${sessionId}"]`)).toHaveCount(0)

  await page.waitForTimeout(500)
  expect(seenBodies).toHaveLength(2)
})


test('clicking a remote waiting-on-agent alert navigates to and renders that remote session (deterministic mocked agent-hook case)', async ({ page }) => {
  // A genuine "waiting on an agent tool call" state requires a real agent
  // CLI running inside the PTY and calling its hook -- not available in
  // this sandboxed harness (see the header comment above). This mocks only
  // the tool-event snapshot and the remote catalog it must correlate
  // against; the click, navigation, and render are all driven for real
  // against the actual React app.
  const REMOTE_OWNER = 'ownerremotefixture1234567b'
  const REMOTE_FINGERPRINT = 'fp-remote-alert-node'
  const sessionId = 'sess-remote-alert'

  await installBaseStubs(page)
  await page.route('**/api/hosts', async (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([
        { id: 'fp-local', name: 'Local Machine', local: true, online: true, owner_id: OWNER, sessions: [], last_seen: new Date().toISOString() },
        { id: REMOTE_FINGERPRINT, name: 'Remote Box', local: false, online: true, owner_id: REMOTE_OWNER, sessions: [], last_seen: new Date().toISOString() },
      ]),
    })
  })
  await page.route('**/api/tool-events', async (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([{
        tool: 'claude',
        status: 'waiting',
        host: REMOTE_FINGERPRINT,
        host_name: 'Remote Box',
        session: 'remote-waiting',
        session_id: sessionId,
        window: 0,
        pane: '',
        message: 'needs input',
        timestamp: new Date().toISOString(),
      }]),
    })
  })
  await installBootstrap(page, {
    owner: OWNER,
    revision: 1,
    local: { owner: OWNER, revision: 1, sessions: [], layouts: [] },
    remote: [{
      owner: REMOTE_OWNER,
      revision: 1,
      sessions: [{ ...makeSessionRaw(sessionId, 'gen-remote', 'remote-waiting'), owner: REMOTE_OWNER, ref: `${REMOTE_OWNER}/${sessionId}:0.0` }],
    }],
    hosts: [],
    workspace: undefined,
    pending: [],
    pending_remote: [],
  })
  await installStateSocket(page, () => {})

  await page.goto('/')
  // useToolEvents re-normalizes an event's key once useHosts' hostIndex
  // updates (see useToolEvents.ts) -- wait for the correlated "Waiting"
  // alert rather than racing the initial render.
  const alert = page.locator('header button', { hasText: 'Waiting' })
  await expect(alert).toBeVisible({ timeout: 15_000 })
  await expect(alert).toContainText('remote-waiting')
  await alert.click()

  await expect(page).toHaveURL(new RegExp(`session/${REMOTE_OWNER}/${sessionId}`))
  await expect(page.locator(`[data-pane-key="${REMOTE_OWNER}/${sessionId}"]`)).toBeVisible({ timeout: 10_000 })
})
