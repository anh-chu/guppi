/**
 * Typed v2 command client (Task 12).
 *
 * Wraps POST /api/v2/session-commands and POST /api/v2/workspace-commands
 * (pkg/server/routes_state_v2.go). Every command carries a caller-supplied
 * CommandID; the SAME id is reused on retry after an ambiguous network
 * failure (fetch throw / timeout / non-parseable response), so a retried
 * command that actually reached the server the first time is deduplicated
 * server-side by intent id rather than double-applied. Only ambiguous
 * failures are retried -- a well-formed error response (4xx JSON body) is
 * never retried, since the server has already told us definitively what
 * happened.
 */

import type { CommandID, LayoutID, OwnerID, SessionRef, SplitDirection } from './types'
import type { CommandResult, CommandResultWire, V2ErrorResponse } from './wireTypes'
import { decodeCommandResult, encodeSessionRefWire } from './wireCodec'

// SessionCommandAction mirrors pkg/state/session_commands.go's ExecuteSessionCommand
// switch: create/kill/label/recover/dismiss/retry are the only session-scoped
// actions the backend implements. Layout mutations (split/move/swap/pop_out/
// resize/rename/select) are NOT session commands server-side -- they are
// workspace commands (see WorkspaceCommandAction below), keyed by layout id
// rather than by session ref.
// The create variant's target/direction/new_first fields mirror
// pkg/state/session_commands.go's CreateParams: when target names an
// existing leaf in layout_id, the server places the new session by splitting
// that leaf, atomically, as part of the same create command -- this is the
// single-step replacement for the old create-then-split-command sequence
// (see V2CommandClient.createSession and App.tsx's handleCreateSession).
export type SessionCommandAction =
  | { action: 'create'; name?: string; shell?: string; cwd?: string; worktree_branch?: string; cols?: number; rows?: number; layout_id?: LayoutID; agent_type?: string; target?: SessionRef; direction?: SplitDirection; new_first?: boolean; target_owner?: OwnerID }
  | { action: 'kill' }
  | { action: 'label'; label: string }
  | { action: 'recover' }
  | { action: 'dismiss' }
  | { action: 'retry' }

// CreateSessionCommand is the create variant of SessionCommandAction. Create is
// the one session command that structurally carries NO SessionRef: the server
// assigns the SessionID (executeCreate calls NewSessionID on the zero ref), so
// sending any ref -- even a placeholder -- is wrong and is rejected by
// ParseSessionRef once wire-encoded ("missing session id"). Use
// V2CommandClient.createSession (which omits `ref` from the body), never
// sessionCommand, for creates.
export type CreateSessionCommand = Extract<SessionCommandAction, { action: 'create' }>

// WorkspaceCommandAction mirrors pkg/state/workspace.go's ApplyWorkspaceCommand
// switch (WorkspaceActionSplit/Move/Swap/PopOut/Resize/Select/ReorderLayouts/
// Present) exactly, including backend field names -- these are nested under
// `params` on the wire, not spread flat (see postWithRetry).
//
// NOTE: WorkspaceActionRename is intentionally NOT represented here. The
// backend unconditionally rejects it (ErrDeprecatedAction --
// pkg/state/workspace.go's WorkspaceActionRename case: "workspace 'rename'
// action is removed; use the session label command to change a session's
// display name instead"). Use SessionCommandAction's `label` action for all
// rename UI.
export type WorkspaceCommandAction =
  | { action: 'split'; target: SessionRef; direction: SplitDirection; new: SessionRef; new_first?: boolean; expected_revision?: number }
  | { action: 'move'; source: SessionRef; target: SessionRef; edge: string; expected_revision?: number }
  | { action: 'swap'; a: SessionRef; b: SessionRef; expected_revision?: number }
  | { action: 'pop_out'; ref: SessionRef; expected_revision?: number }
  | { action: 'remove'; ref: SessionRef; expected_revision?: number }
  | { action: 'resize'; split_id: string; ratio: number; expected_revision?: number }
  | { action: 'select'; ref: SessionRef; expected_revision?: number }

// encodeWorkspaceCommandAction re-encodes every nested SessionRef field on a
// WorkspaceCommandAction into its canonical wire string. This must cover
// every action variant that carries a SessionRef -- see
// pkg/state/workspace.go's workspace*Params structs, which this mirrors.
function encodeWorkspaceCommandAction(cmd: WorkspaceCommandAction): Record<string, unknown> {
  switch (cmd.action) {
    case 'split':
      return { ...cmd, target: encodeSessionRefWire(cmd.target), new: encodeSessionRefWire(cmd.new) }
    case 'move':
      return { ...cmd, source: encodeSessionRefWire(cmd.source), target: encodeSessionRefWire(cmd.target) }
    case 'swap':
      return { ...cmd, a: encodeSessionRefWire(cmd.a), b: encodeSessionRefWire(cmd.b) }
    case 'pop_out':
      return { ...cmd, ref: encodeSessionRefWire(cmd.ref) }
    case 'remove':
      return { ...cmd, ref: encodeSessionRefWire(cmd.ref) }
    case 'resize':
      return { ...cmd }
    case 'select':
      return { ...cmd, ref: encodeSessionRefWire(cmd.ref) }
  }
}

export class V2CommandError extends Error {
  code: V2ErrorResponse['code']
  field?: string
  constructor(resp: V2ErrorResponse) {
    super(resp.message)
    this.code = resp.code
    this.field = resp.field
  }
}

// Thrown when a command exhausts its retries without a definitive
// (successful or well-formed-error) server response.
export class V2CommandNetworkError extends Error {
  cause: unknown
  constructor(message: string, cause: unknown) {
    super(message)
    this.cause = cause
  }
}

export type V2CommandClientOptions = {
  fetchImpl?: typeof fetch
  genId?: () => CommandID
  maxRetries?: number
  retryDelayMs?: number
}

function defaultGenId(): CommandID {
  if (typeof crypto !== 'undefined' && crypto.randomUUID) return crypto.randomUUID()
  return `cmd-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

async function postWithRetry(
  url: string,
  body: unknown,
  opts: Required<Pick<V2CommandClientOptions, 'fetchImpl' | 'maxRetries' | 'retryDelayMs'>>,
): Promise<unknown> {
  let lastErr: unknown
  for (let attempt = 0; attempt <= opts.maxRetries; attempt++) {
    try {
      const res = await opts.fetchImpl(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (res.ok) {
        if (res.status === 204) return null
        return await res.json().catch(() => null)
      }
      // Definitive (non-ambiguous) server error: parse and throw immediately,
      // never retried -- the server has already told us what happened.
      const parsed = await res.json().catch(() => null)
      if (parsed && typeof parsed.code === 'string') {
        throw new V2CommandError(parsed as V2ErrorResponse)
      }
      throw new V2CommandNetworkError(`unexpected response ${res.status}`, parsed)
    } catch (err) {
      if (err instanceof V2CommandError) throw err
      lastErr = err
      if (attempt < opts.maxRetries) {
        if (opts.retryDelayMs > 0) {
          await new Promise((r) => setTimeout(r, opts.retryDelayMs))
        }
        continue
      }
    }
  }
  throw new V2CommandNetworkError('command failed after retries', lastErr)
}

/**
 * Typed client for the two v2 command endpoints. One instance per app
 * (or per test) is fine -- it is stateless aside from injected options.
 */
export class V2CommandClient {
  private readonly fetchImpl: typeof fetch
  private readonly genId: () => CommandID
  private readonly maxRetries: number
  private readonly retryDelayMs: number

  constructor(options: V2CommandClientOptions = {}) {
    this.fetchImpl = options.fetchImpl ?? fetch
    this.genId = options.genId ?? defaultGenId
    this.maxRetries = options.maxRetries ?? 2
    this.retryDelayMs = options.retryDelayMs ?? 0
  }

  async sessionCommand(ref: SessionRef, cmd: SessionCommandAction, id?: CommandID): Promise<CommandResult> {
    const commandId = id ?? this.genId()
    const { action, ...params } = cmd
    const raw = await postWithRetry(
      '/api/v2/session-commands',
      { id: commandId, ref: encodeSessionRefWire(ref), action, params },
      { fetchImpl: this.fetchImpl, maxRetries: this.maxRetries, retryDelayMs: this.retryDelayMs },
    )
    return decodeCommandResult(raw as CommandResultWire)
  }

  /**
   * Posts a create-session command. Unlike sessionCommand, this NEVER includes
   * a `ref` member: a create cannot know its SessionID before the server
   * assigns one, so the wire body is exactly { id, action: 'create', params }.
   * If cmd carries a `target`, it is re-encoded to its canonical wire string
   * (mirroring encodeWorkspaceCommandAction's split case below) so the server
   * places the new session atomically via that target, instead of the caller
   * having to issue a separate split command afterward.
   */
  async createSession(cmd: CreateSessionCommand, id?: CommandID): Promise<CommandResult> {
    const commandId = id ?? this.genId()
    // target_owner is a TOP-LEVEL wire field (see v2SessionCommandRequest in
    // pkg/server/routes_state_v2.go), not part of params: it selects which
    // owner's catalog should execute the create (server-side remote-create
    // forwarding via peer.Manager.RequestRemoteCreate), so it must sit
    // alongside `action`, not nested inside the create's own params.
    const { action, target, target_owner, ...rest } = cmd
    const params = target !== undefined ? { ...rest, target: encodeSessionRefWire(target) } : rest
    const body: Record<string, unknown> = { id: commandId, action, params }
    if (target_owner) {
      body.target_owner = target_owner
    }
    const raw = await postWithRetry(
      '/api/v2/session-commands',
      body,
      { fetchImpl: this.fetchImpl, maxRetries: this.maxRetries, retryDelayMs: this.retryDelayMs },
    )
    return decodeCommandResult(raw as CommandResultWire)
  }

  async workspaceCommand(layout: LayoutID, cmd: WorkspaceCommandAction, id?: CommandID): Promise<unknown> {
    const commandId = id ?? this.genId()
    const { action, ...params } = encodeWorkspaceCommandAction(cmd)
    return postWithRetry(
      '/api/v2/workspace-commands',
      { id: commandId, layout, action, params },
      { fetchImpl: this.fetchImpl, maxRetries: this.maxRetries, retryDelayMs: this.retryDelayMs },
    )
  }
}
