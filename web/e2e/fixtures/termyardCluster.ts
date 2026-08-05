import { ChildProcess, spawn } from 'node:child_process'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import * as fs from 'node:fs'
import * as net from 'node:net'
import * as os from 'node:os'
import * as path from 'node:path'
import type { BrowserContext } from '@playwright/test'

const execFileAsync = promisify(execFile)

// repo root: web/e2e/fixtures -> web/e2e -> web -> <repo root>
const __filenameESM = new URL(import.meta.url).pathname
const __dirnameESM = path.dirname(__filenameESM)
const REPO_ROOT = path.resolve(__dirnameESM, '..', '..', '..')
const WEB_DIR = path.join(REPO_ROOT, 'web')
const EMBEDDED_DIST_DIR = path.join(REPO_ROOT, 'pkg', 'server', 'dist')

const SHARED_TEST_PASSWORD = 'e2e-cluster-password-123'

// ---------------------------------------------------------------------------
// One reviewed binary, built once per Playwright process from the exact
// checked-out source (frontend build embedded via pkg/server/embed.go, then
// `go build`). Cached at module scope so every test/spec in the same worker
// process reuses the identical binary instead of rebuilding per test.
// ---------------------------------------------------------------------------
let binaryPromise: Promise<string> | null = null

export function getReviewedBinary(): Promise<string> {
  if (!binaryPromise) {
    binaryPromise = buildBinary()
  }
  return binaryPromise
}

async function buildBinary(): Promise<string> {
  // Rebuild the embedded frontend so the binary reflects the exact working
  // tree, not a stale artifact. `tsc && vite build` writes into
  // pkg/server/dist (see web/vite.config.ts's outDir), which pkg/server's
  // //go:embed dist/* picks up on the next `go build`.
  await execFileAsync('npm', ['run', 'build'], { cwd: WEB_DIR, timeout: 180_000 })

  if (!fs.existsSync(path.join(EMBEDDED_DIST_DIR, 'index.html'))) {
    throw new Error(
      `frontend build did not produce ${EMBEDDED_DIST_DIR}/index.html -- termyard binary would serve a stale/empty UI`,
    )
  }

  const outDir = fs.mkdtempSync(path.join(os.tmpdir(), 'termyard-e2e-bin-'))
  const binPath = path.join(outDir, 'termyard')
  await execFileAsync('go', ['build', '-o', binPath, '.'], { cwd: REPO_ROOT, timeout: 180_000 })

  if (!fs.existsSync(binPath)) {
    throw new Error(`go build did not produce ${binPath}`)
  }
  return binPath
}

// ---------------------------------------------------------------------------
// Port allocation
// ---------------------------------------------------------------------------

function getFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer()
    srv.unref()
    srv.on('error', reject)
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address()
      const port = typeof addr === 'object' && addr ? addr.port : 0
      srv.close(() => {
        if (!port) reject(new Error('failed to allocate free port'))
        else resolve(port)
      })
    })
  })
}

// ---------------------------------------------------------------------------
// Node lifecycle
// ---------------------------------------------------------------------------

export interface ClusterNodeOptions {
  /** Node label used only for logs/dirs, e.g. "A" or "B". */
  name: string
  /** Root directory for this node's isolated HOME/session-dir/logs. */
  rootDir: string
  /** Binary to spawn. */
  binaryPath: string
  /** When false, TERMYARD_V2_STATE is left unset (legacy-only mode). */
  v2?: boolean
}

export class ClusterNode {
  readonly name: string
  readonly port: number
  readonly baseURL: string
  readonly homeDir: string
  readonly sessionDir: string
  readonly socketPath: string
  readonly rootDir: string
  readonly v2: boolean
  readonly binaryPath: string

  proc: ChildProcess | null = null
  sessionCookie: string | null = null
  fingerprint: string | null = null
  ownerId: string | null = null

  private stdoutChunks: string[] = []
  private stderrChunks: string[] = []
  private outStream: fs.WriteStream
  private errStream: fs.WriteStream

  private constructor(opts: ClusterNodeOptions & { port: number }) {
    this.name = opts.name
    this.port = opts.port
    this.baseURL = `http://127.0.0.1:${opts.port}`
    this.rootDir = opts.rootDir
    this.homeDir = path.join(opts.rootDir, 'home')
    this.v2 = opts.v2 !== false
    this.binaryPath = opts.binaryPath

    // Unix domain socket paths are capped by the kernel's sun_path buffer
    // (108 bytes on Linux, ~104 usable after the trailing NUL). Playwright's
    // own test-output directory names embed the full (sanitized) test
    // title -- e.g. "test-results/multi-node-real-two-node-c-<hash>-...
    // -chromium/cluster-run-1/node-b/sessions/<sessionid>.sock" -- which
    // alone is already well over 150 bytes, long before this node's own
    // per-session socket name is appended. Every session-daemon spawned
    // under such a path used to fail at its very first step (its own
    // control-socket bind()) with a silent, hard-to-diagnose
    // "bind: invalid argument" (EINVAL from a too-long sun_path), which the
    // daemon's parent (pkg/pty/registry_stable.go's Start) can only observe
    // as "daemon did not become ready" -- no amount of readiness-timeout or
    // retry tuning fixes an always-EINVAL bind. So TERMYARD_SESSION_DIR (and
    // the optional notify.sock) get a short, independent /tmp path here,
    // decoupled from Playwright's long, test-title-derived rootDir, which
    // stays in use for HOME/logs (never used as a socket path).
    this.sessionDir = fs.mkdtempSync(path.join(os.tmpdir(), `ty-e2e-${opts.name}-`))
    this.socketPath = path.join(this.sessionDir, 'notify.sock')

    fs.mkdirSync(this.homeDir, { recursive: true })
    fs.mkdirSync(path.join(this.homeDir, '.config'), { recursive: true })
    fs.mkdirSync(path.join(this.homeDir, '.local-share'), { recursive: true })
    fs.mkdirSync(path.join(this.homeDir, '.local-state'), { recursive: true })

    this.outStream = fs.createWriteStream(path.join(opts.rootDir, 'stdout.log'))
    this.errStream = fs.createWriteStream(path.join(opts.rootDir, 'stderr.log'))
  }

  static async start(opts: ClusterNodeOptions): Promise<ClusterNode> {
    const port = await getFreePort()
    const node = new ClusterNode({ ...opts, port })
    await node.spawnProcess()
    await node.waitForReady()
    return node
  }

  private async spawnProcess(): Promise<void> {
    const env: NodeJS.ProcessEnv = {
      // Keep PATH/TERM etc. from the host so the go binary and any child
      // sessiondaemon processes can still find a shell.
      PATH: process.env.PATH,
      TERM: process.env.TERM || 'xterm-256color',
      LANG: process.env.LANG,
      HOME: this.homeDir,
      XDG_CONFIG_HOME: path.join(this.homeDir, '.config'),
      XDG_DATA_HOME: path.join(this.homeDir, '.local-share'),
      XDG_STATE_HOME: path.join(this.homeDir, '.local-state'),
      TERMYARD_PORT: String(this.port),
      TERMYARD_SOCKET: this.socketPath,
      TERMYARD_SESSION_DIR: this.sessionDir,
    }
    if (this.v2) {
      env.TERMYARD_V2_STATE = '1'
    }

    this.proc = spawn(this.binaryPath, ['server'], {
      cwd: this.homeDir,
      env,
      // detached so the process becomes its own process-group leader; lets
      // teardown signal the whole group (this process plus any spawned
      // sessiondaemon children) at once via a negative pid.
      detached: true,
      stdio: ['ignore', 'pipe', 'pipe'],
    })

    this.proc.stdout?.on('data', (chunk: Buffer) => {
      this.stdoutChunks.push(chunk.toString('utf8'))
      this.outStream.write(chunk)
    })
    this.proc.stderr?.on('data', (chunk: Buffer) => {
      this.stderrChunks.push(chunk.toString('utf8'))
      this.errStream.write(chunk)
    })
  }

  private async waitForReady(timeoutMs = 20_000): Promise<void> {
    const deadline = Date.now() + timeoutMs
    let lastErr: unknown = null
    while (Date.now() < deadline) {
      if (this.proc && this.proc.exitCode !== null) {
        throw new Error(
          `node ${this.name} exited during startup (code ${this.proc.exitCode})\n--- stderr ---\n${this.stderrTail()}`,
        )
      }
      try {
        const res = await fetch(`${this.baseURL}/api/version`, { signal: AbortSignal.timeout(1000) })
        if (res.ok) return
      } catch (err) {
        lastErr = err
      }
      await sleep(150)
    }
    throw new Error(
      `node ${this.name} did not become ready within ${timeoutMs}ms: ${String(lastErr)}\n--- stderr ---\n${this.stderrTail()}`,
    )
  }

  stdoutTail(maxChars = 4000): string {
    return this.stdoutChunks.join('').slice(-maxChars)
  }

  stderrTail(maxChars = 4000): string {
    return this.stderrChunks.join('').slice(-maxChars)
  }

  /** Real HTTP call against this node, forwarding the stored session cookie. */
  async api(pathname: string, init: RequestInit = {}): Promise<Response> {
    const headers = new Headers(init.headers)
    if (this.sessionCookie && !headers.has('Cookie')) {
      headers.set('Cookie', `termyard_session=${this.sessionCookie}`)
    }
    if (init.body && !headers.has('Content-Type')) {
      headers.set('Content-Type', 'application/json')
    }
    return fetch(`${this.baseURL}${pathname}`, { ...init, headers })
  }

  async apiJSON<T = unknown>(pathname: string, init: RequestInit = {}): Promise<T> {
    const res = await this.api(pathname, init)
    if (!res.ok) {
      const body = await res.text().catch(() => '')
      throw new Error(`${init.method || 'GET'} ${pathname} on node ${this.name} failed: ${res.status} ${body}`)
    }
    return (await res.json()) as T
  }

  /** Real POST /api/auth/setup. Stores the resulting session cookie. */
  async authSetup(password: string): Promise<void> {
    const res = await this.api('/api/auth/setup', {
      method: 'POST',
      body: JSON.stringify({ password }),
    })
    if (!res.ok) {
      const body = await res.text().catch(() => '')
      throw new Error(`auth setup failed on node ${this.name}: ${res.status} ${body}`)
    }
    const setCookie = res.headers.get('set-cookie') || ''
    const match = /termyard_session=([^;]+)/.exec(setCookie)
    if (!match) {
      throw new Error(`auth setup on node ${this.name} did not return a session cookie`)
    }
    this.sessionCookie = match[1]
  }

  /** Real GET /api/peers. Populates fingerprint. */
  async refreshSelf(): Promise<{ name: string; fingerprint: string; public_key: string }> {
    const data = await this.apiJSON<{ self: { name: string; fingerprint: string; public_key: string } }>('/api/peers')
    this.fingerprint = data.self.fingerprint
    return data.self
  }

  /** Real GET /api/v2/bootstrap. Populates ownerId as a side effect. */
  async bootstrap(): Promise<any> {
    const data = await this.apiJSON<any>('/api/v2/bootstrap')
    if (data && data.owner) this.ownerId = data.owner
    return data
  }

  /** Real GET /api/hosts. */
  async hosts(): Promise<Array<Record<string, any>>> {
    return this.apiJSON('/api/hosts')
  }

  /** Real GET /api/peers (peer-link status view, not the bootstrap catalog). */
  async peers(): Promise<{ self: any; peers: any[] }> {
    return this.apiJSON('/api/peers')
  }

  /**
   * Real POST /api/v2/session-commands {action:'create'} against THIS
   * node's own catalog (no target_owner) -- the local-create code path,
   * distinct from the cross-node remote-create RPC. Used to seed a real
   * session directly on a node when a test's precondition is "a session
   * exists and is authoritatively owned by this node", without depending
   * on cross-node create working.
   */
  async createLocalSession(name: string, cwd: string, shell = 'bash'): Promise<any> {
    // The real PTY backend needs an existing working directory (unlike the
    // route-stubbed harness, which never actually spawns a process).
    fs.mkdirSync(cwd, { recursive: true })
    return this.apiJSON('/api/v2/session-commands', {
      method: 'POST',
      body: JSON.stringify({ action: 'create', params: { name, shell, cwd } }),
    })
  }

  /** Real POST /api/auth/login (used after a restart invalidates the in-memory session). */
  async login(password: string): Promise<void> {
    const res = await this.api('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    })
    if (!res.ok) {
      const body = await res.text().catch(() => '')
      throw new Error(`login failed on node ${this.name}: ${res.status} ${body}`)
    }
    const setCookie = res.headers.get('set-cookie') || ''
    const match = /termyard_session=([^;]+)/.exec(setCookie)
    if (!match) {
      throw new Error(`login on node ${this.name} did not return a session cookie`)
    }
    this.sessionCookie = match[1]
  }

  /**
   * Stops (if running) and respawns this node from the exact same binary,
   * port, HOME, and session/socket directories -- a real process restart,
   * not a fresh node. Callers must re-authenticate afterward (the in-memory
   * SessionManager does not survive a restart) via `login`.
   */
  async restart(): Promise<void> {
    if (this.proc && this.proc.exitCode === null) {
      await this.stop()
    }
    this.sessionCookie = null
    this.stdoutChunks = []
    this.stderrChunks = []
    this.outStream = fs.createWriteStream(path.join(this.rootDir, `stdout.restart-${Date.now()}.log`))
    this.errStream = fs.createWriteStream(path.join(this.rootDir, `stderr.restart-${Date.now()}.log`))
    await this.spawnProcess()
    await this.waitForReady()
  }

  async stop(): Promise<void> {
    const proc = this.proc
    if (!proc || proc.pid === undefined || proc.exitCode !== null) return

    const pid = proc.pid
    const exited = new Promise<void>((resolve) => {
      proc.once('exit', () => resolve())
    })

    const trySignal = (sig: NodeJS.Signals) => {
      try {
        // Negative pid signals the whole process group (proc was spawned
        // detached, so pid === pgid).
        process.kill(-pid, sig)
      } catch {
        try {
          process.kill(pid, sig)
        } catch {
          /* already gone */
        }
      }
    }

    trySignal('SIGTERM')
    const termOk = await raceTimeout(exited, 5000)
    if (!termOk) {
      trySignal('SIGKILL')
      await raceTimeout(exited, 5000)
    }

    this.outStream.end()
    this.errStream.end()

    await assertNoProcessGroup(pid, this.name)
  }

  /** Removes this node's short-path /tmp session-socket directory (see the
   * constructor's comment on why it is decoupled from rootDir). Only safe to
   * call once this node is permanently done (final teardown): `restart()`
   * calls `stop()` and then respawns reusing the SAME sessionDir so a
   * restarted node can still reach its pre-restart session-daemon sockets
   * -- this must never run as part of a plain `stop()`/`restart()` cycle. */
  disposeSessionDir(): void {
    fs.rmSync(this.sessionDir, { recursive: true, force: true })
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function raceTimeout(p: Promise<void>, ms: number): Promise<boolean> {
  let done = false
  p.then(() => {
    done = true
  })
  const start = Date.now()
  while (!done && Date.now() - start < ms) {
    await sleep(50)
  }
  return done
}

/** Fails loudly if any process remains in pid's process group after teardown. */
async function assertNoProcessGroup(pid: number, nodeName: string): Promise<void> {
  try {
    // pgrep -g <pgid> lists surviving members of the process group. On a
    // clean teardown this must be empty.
    const { stdout } = await execFileAsync('pgrep', ['-g', String(pid)])
    const survivors = stdout.split('\n').map((l) => l.trim()).filter(Boolean)
    if (survivors.length > 0) {
      throw new Error(
        `teardown leaked ${survivors.length} process(es) in node ${nodeName}'s process group (pgid ${pid}): ${survivors.join(', ')}`,
      )
    }
  } catch (err: any) {
    // pgrep exits 1 with no output when nothing matches -- that's success.
    if (err && typeof err.code === 'number' && err.code === 1) return
    if (err instanceof Error && err.message.startsWith('teardown leaked')) throw err
    // pgrep not installed or other unexpected error: don't silently pass a
    // leak check we couldn't actually run.
    throw new Error(`could not verify no-leak for node ${nodeName}: ${String(err)}`)
  }
}

// ---------------------------------------------------------------------------
// Pairing (real HTTP, never internal Go state injection)
// ---------------------------------------------------------------------------

/**
 * Pairs `dialer` -> `listener` via the real POST /api/peers flow (which
 * itself performs a real POST /api/peers/bootstrap handshake against the
 * listener, per pkg/server/peers.go's handlePostPeers/handlePeersBootstrap).
 * Both nodes must already have `password` set via authSetup.
 */
export async function pairNodes(dialer: ClusterNode, listener: ClusterNode, password: string): Promise<void> {
  await dialer.refreshSelf()
  await listener.refreshSelf()

  const res = await dialer.api('/api/peers', {
    method: 'POST',
    body: JSON.stringify({
      address: `127.0.0.1:${listener.port}`,
      password,
      auto_reconnect: true,
    }),
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`pairing ${dialer.name} -> ${listener.name} failed: ${res.status} ${body}`)
  }
}

/** Polls GET /api/peers on `node` until the peer with `fingerprint` reaches one of `statuses`. */
export async function waitForPeerStatus(
  node: ClusterNode,
  fingerprint: string,
  statuses: string[],
  timeoutMs = 15_000,
): Promise<any> {
  const deadline = Date.now() + timeoutMs
  let lastSnap: any = null
  while (Date.now() < deadline) {
    const data = await node.peers()
    const snap = (data.peers || []).find((p: any) => p.fingerprint === fingerprint)
    lastSnap = snap
    if (snap && statuses.includes(snap.status)) return snap
    await sleep(200)
  }
  throw new Error(
    `node ${node.name}: peer ${fingerprint} never reached status in [${statuses.join(', ')}] within ${timeoutMs}ms (last: ${JSON.stringify(lastSnap)})`,
  )
}

/** Polls GET /api/hosts on `node` until a host with `fingerprint` reports online === wantOnline. */
export async function waitForHostOnline(
  node: ClusterNode,
  fingerprint: string,
  wantOnline: boolean,
  timeoutMs = 15_000,
): Promise<Record<string, any>> {
  const deadline = Date.now() + timeoutMs
  let last: any = null
  while (Date.now() < deadline) {
    const hosts = await node.hosts()
    const h = hosts.find((x) => x.id === fingerprint || x.ID === fingerprint)
    last = h
    const online = h ? h.online ?? h.Online : undefined
    if (h && online === wantOnline) return h
    await sleep(250)
  }
  throw new Error(
    `node ${node.name}: host ${fingerprint} never reported online=${wantOnline} within ${timeoutMs}ms (last: ${JSON.stringify(last)})`,
  )
}

/**
 * Watches `node`'s view of `fingerprint` for `windowMs` and fails if the
 * link ever reaches 'connected'. Used to prove a capability-incompatible
 * peer is rejected before any state/command participation, rather than
 * merely asserting a single snapshot (which could race a slow dial).
 */
export async function assertPeerNeverConnects(node: ClusterNode, fingerprint: string, windowMs = 8_000): Promise<string[]> {
  const deadline = Date.now() + windowMs
  const seenStatuses = new Set<string>()
  while (Date.now() < deadline) {
    const data = await node.peers()
    const snap = (data.peers || []).find((p: any) => p.fingerprint === fingerprint)
    if (snap) {
      seenStatuses.add(snap.status)
      if (snap.status === 'connected') {
        throw new Error(
          `node ${node.name}: peer ${fingerprint} reached status 'connected' -- capability gate did not reject it`,
        )
      }
    }
    await sleep(300)
  }
  return Array.from(seenStatuses)
}

// ---------------------------------------------------------------------------
// Identity divergence assertion (fail fast if the harness accidentally
// shares state between the two "independent" nodes).
// ---------------------------------------------------------------------------

export async function assertDistinctIdentities(a: ClusterNode, b: ClusterNode): Promise<void> {
  const [selfA, selfB] = await Promise.all([a.refreshSelf(), b.refreshSelf()])
  if (selfA.fingerprint === selfB.fingerprint) {
    throw new Error(`nodes A and B share one peer fingerprint (${selfA.fingerprint}) -- identity not isolated`)
  }
  if (selfA.public_key === selfB.public_key) {
    throw new Error('nodes A and B share one identity keypair -- identity not isolated')
  }

  const [bootA, bootB] = await Promise.all([a.bootstrap(), b.bootstrap()])
  if (!bootA.owner || !bootB.owner) {
    throw new Error(`one or both nodes did not return a v2 OwnerID from /api/v2/bootstrap (A=${bootA.owner}, B=${bootB.owner})`)
  }
  if (bootA.owner === bootB.owner) {
    throw new Error(`nodes A and B share one v2 OwnerID (${bootA.owner}) -- v2 catalog not isolated`)
  }

  if (path.resolve(a.sessionDir) === path.resolve(b.sessionDir)) {
    throw new Error('nodes A and B share one daemon socket directory (TERMYARD_SESSION_DIR) -- PTY registries not isolated')
  }
  const sessionDirRealA = fs.realpathSync(a.sessionDir)
  const sessionDirRealB = fs.realpathSync(b.sessionDir)
  if (sessionDirRealA === sessionDirRealB) {
    throw new Error('nodes A and B resolve to one real daemon socket directory -- PTY registries not isolated')
  }

  const v2StateDirA = path.join(a.homeDir, '.local-share', 'termyard', 'v2')
  const v2StateDirB = path.join(b.homeDir, '.local-share', 'termyard', 'v2')
  if (path.resolve(v2StateDirA) === path.resolve(v2StateDirB)) {
    throw new Error('nodes A and B share one v2 state document directory -- state not isolated')
  }
}

// ---------------------------------------------------------------------------
// Whole-cluster convenience wrapper
// ---------------------------------------------------------------------------

export interface Cluster {
  a: ClusterNode
  b: ClusterNode
  password: string
  stopAll(): Promise<void>
}

export interface StartClusterOptions {
  rootDir: string
  /** Whether node B starts in legacy (non-v2) mode. Default true (v2). */
  bV2?: boolean
  /** Whether node A starts in legacy (non-v2) mode. Default true (v2). */
  aV2?: boolean
}

export async function startCluster(opts: StartClusterOptions): Promise<Cluster> {
  const binaryPath = await getReviewedBinary()
  const [a, b] = await Promise.all([
    ClusterNode.start({ name: 'A', rootDir: path.join(opts.rootDir, 'node-a'), binaryPath, v2: opts.aV2 }),
    ClusterNode.start({ name: 'B', rootDir: path.join(opts.rootDir, 'node-b'), binaryPath, v2: opts.bV2 }),
  ])

  await Promise.all([a.authSetup(SHARED_TEST_PASSWORD), b.authSetup(SHARED_TEST_PASSWORD)])

  await assertDistinctIdentities(a, b)

  await pairNodes(a, b, SHARED_TEST_PASSWORD)

  return {
    a,
    b,
    password: SHARED_TEST_PASSWORD,
    async stopAll() {
      await Promise.all([a.stop(), b.stop()])
      a.disposeSessionDir()
      b.disposeSessionDir()
    },
  }
}

/** Injects a's stored session cookie into a Playwright BrowserContext so the browser is authenticated without driving the login UI (the cookie itself was issued by a real /api/auth/setup call). */
export async function loginBrowserContext(context: BrowserContext, node: ClusterNode): Promise<void> {
  if (!node.sessionCookie) throw new Error(`node ${node.name} has no session cookie yet -- call authSetup first`)
  await context.addCookies([
    {
      name: 'termyard_session',
      value: node.sessionCookie,
      url: node.baseURL,
    },
  ])
  await context.addInitScript(() => {
    window.localStorage.setItem('termyard.v2State', '1')
  })
}

export { SHARED_TEST_PASSWORD }
