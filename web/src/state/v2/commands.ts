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

import type { CommandID, LayoutID, SessionRef, SplitDirection } from './types'
import type { V2ErrorResponse } from './wireTypes'

// SessionCommandAction mirrors pkg/state/session_commands.go's ExecuteSessionCommand
// switch: create/kill/label/recover/dismiss/retry are the only session-scoped
// actions the backend implements. Layout mutations (split/move/swap/pop_out/
// resize/rename/select) are NOT session commands server-side -- they are
// workspace commands (see WorkspaceCommandAction below), keyed by layout id
// rather than by session ref.
export type SessionCommandAction =
  | { action: 'create'; name?: string; shell?: string; cwd?: string; worktree_branch?: string; cols?: number; rows?: number; layout_id?: LayoutID; agent_type?: string }
  | { action: 'kill' }
  | { action: 'label'; label: string }
  | { action: 'recover' }
  | { action: 'dismiss' }
  | { action: 'retry' }

// WorkspaceCommandAction mirrors pkg/state/workspace.go's ApplyWorkspaceCommand
// switch (WorkspaceActionSplit/Move/Swap/PopOut/Resize/Rename/Select/
// ReorderLayouts/Present) exactly, including backend field names -- these are
// nested under `params` on the wire, not spread flat (see postWithRetry).
export type WorkspaceCommandAction =
  | { action: 'split'; target: SessionRef; direction: SplitDirection; new: SessionRef; new_first?: boolean; expected_revision?: number }
  | { action: 'move'; source: SessionRef; target: SessionRef; edge: string; expected_revision?: number }
  | { action: 'swap'; a: SessionRef; b: SessionRef; expected_revision?: number }
  | { action: 'pop_out'; ref: SessionRef; expected_revision?: number }
  | { action: 'remove'; ref: SessionRef; expected_revision?: number }
  | { action: 'resize'; split_id: string; ratio: number; expected_revision?: number }
  | { action: 'rename'; old: SessionRef; new: SessionRef; expected_revision?: number }
  | { action: 'select'; ref: SessionRef; expected_revision?: number }

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

  async sessionCommand(ref: SessionRef, cmd: SessionCommandAction, id?: CommandID): Promise<unknown> {
    const commandId = id ?? this.genId()
    const { action, ...params } = cmd
    return postWithRetry(
      '/api/v2/session-commands',
      { id: commandId, ref, action, params },
      { fetchImpl: this.fetchImpl, maxRetries: this.maxRetries, retryDelayMs: this.retryDelayMs },
    )
  }

  async workspaceCommand(layout: LayoutID, cmd: WorkspaceCommandAction, id?: CommandID): Promise<unknown> {
    const commandId = id ?? this.genId()
    const { action, ...params } = cmd
    return postWithRetry(
      '/api/v2/workspace-commands',
      { id: commandId, layout, action, params },
      { fetchImpl: this.fetchImpl, maxRetries: this.maxRetries, retryDelayMs: this.retryDelayMs },
    )
  }
}
