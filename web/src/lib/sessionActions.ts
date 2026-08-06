// Shared session action calls used by both the Sidebar context menu and the
// Overview SessionActionsMenu. Pure API wrappers: callers own their own UI
// state (rename input, naming spinner, kill confirms, optimistic removal).
//
// Routed through the canonical POST /api/state/session-commands command
// client (state/session/commands.ts) -- the legacy /api/session/display-name,
// /api/session/regenerate-name and /api/session/kill REST routes were
// deleted server-side (Task 7). There is no server-side replacement for AI
// (re)naming; that feature was removed along with the old routes.

import { CommandClient } from '../state/session/commands'
import type { SessionRef } from '../state/session/types'

const client = new CommandClient()

// Sets the friendly display label only; the underlying session name is
// left untouched (renaming it would break session keys, attachment, and agent
// hooks). The new label arrives via the websocket state update.
export async function renameSession(ref: SessionRef, label: string): Promise<void> {
  try {
    await client.sessionCommand(ref, { action: 'label', label })
  } catch (err) {
    console.error('Failed to rename session:', err)
  }
}

export async function killSession(ref: SessionRef, removeWorktree?: boolean): Promise<void> {
  try {
    await client.sessionCommand(ref, { action: 'kill', remove_worktree: removeWorktree || undefined })
  } catch (err) {
    console.error('Failed to kill session:', err)
  }
}
