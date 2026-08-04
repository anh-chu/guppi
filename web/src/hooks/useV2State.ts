/**
 * React binding for the v2 normalized state store (Task 12, first slice).
 *
 * Owns one V2Store + one StateStreamClient + one V2CommandClient for the
 * lifetime of the component tree it's mounted under. Bootstraps from
 * GET /api/v2/bootstrap, then subscribes to /ws/v2/state; every message
 * (catalog or workspace snapshot) replaces the corresponding projection in
 * the store via the store's own generation/revision acceptance rule -- this
 * hook does no additional staleness bookkeeping of its own.
 *
 * Disabled by default; only constructed/mounted when the v2 feature flag is
 * on (see src/lib/featureFlags.ts). Callers should not mount this hook
 * unconditionally in a component that is also active on the legacy path.
 */

import { useEffect, useMemo, useRef, useSyncExternalStore } from 'react'
import { V2Store, type V2StoreState } from '../state/v2/store'
import { StateStreamClient } from '../state/v2/stateStream'
import { V2CommandClient, type SessionCommandAction, type WorkspaceCommandAction } from '../state/v2/commands'
import { paneNodeToPaneTree, sessionRefToKey } from '../state/v2/paneTreeAdapter'
import type { SessionRef, LayoutID } from '../state/v2/types'
import type { V2BootstrapResponse } from '../state/v2/wireTypes'
import type { PaneTree } from '../lib/paneTree'

export type UseV2StateOptions = {
  enabled: boolean
}

export type UseV2StateResult = {
  state: V2StoreState
  bootstrapped: boolean
  connected: boolean
  // Derived from state.workspace.record via paneNodeToPaneTree -- the single
  // active layout the stream tracks. No groups/singleView: the v2 path only
  // ever has one layout in view (see StateStreamHub's primaryLayout note).
  paneTree: PaneTree | null
  activeKey: string | null
  layoutId: LayoutID | null
  createSession: (params: {
    name?: string
    shell?: string
    cwd?: string
    worktreeBranch?: string
    layoutId?: LayoutID
    // Target host selected by the caller. Accepted here for forward
    // compatibility with the frontend API boundary, but NOT yet placed on
    // the wire -- see the TODO in createSession's implementation below.
    hostId?: string
  }) => Promise<unknown>
  sessionCommand: (ref: SessionRef, cmd: SessionCommandAction) => Promise<unknown>
  workspaceCommand: (layout: LayoutID, cmd: WorkspaceCommandAction) => Promise<unknown>
}

function wsURL(path: string): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}${path}`
}

export function useV2State({ enabled }: UseV2StateOptions): UseV2StateResult {
  const storeRef = useRef<V2Store | null>(null)
  if (storeRef.current === null) storeRef.current = new V2Store()
  const store = storeRef.current

  const commandClient = useMemo(() => new V2CommandClient(), [])

  const state = useSyncExternalStore(
    (listener) => store.subscribe(listener),
    () => store.getState(),
  )

  const bootstrappedRef = useRef(false)

  useEffect(() => {
    if (!enabled) return
    let disposed = false
    const generation = store.bumpConnectionGeneration()

    const client = new StateStreamClient({
      url: wsURL('/ws/v2/state'),
      callbacks: {
        onCatalog: (snapshot, gen) => {
          if (disposed) return
          store.replaceCatalog(snapshot, gen)
        },
        onWorkspace: (snapshot, gen) => {
          if (disposed) return
          store.replaceWorkspace(snapshot, gen)
        },
        onConnectionChange: (online) => {
          if (disposed) return
          store.setConnectionOnline(online)
        },
      },
    })

    async function bootstrapAndConnect() {
      try {
        const res = await fetch('/api/v2/bootstrap')
        if (res.ok && !disposed) {
          const body = (await res.json()) as V2BootstrapResponse
          store.replaceCatalog(
            { owner: body.owner, revision: body.revision, sessions: body.sessions, layouts: body.layouts },
            generation,
          )
          if (body.workspace) {
            store.replaceWorkspace(body.workspace, generation, body.presentations)
          }
        }
      } catch {
        // Bootstrap failure is not fatal: the stream client will retry the
        // socket independently and a later catalog snapshot will replace
        // whatever partial/absent state we have.
      }
      if (!disposed) client.start()
    }
    bootstrapAndConnect()
    bootstrappedRef.current = true

    return () => {
      disposed = true
      client.dispose()
    }
  }, [enabled, store])

  const createSession = useMemo(
    () =>
      async (params: {
        name?: string
        shell?: string
        cwd?: string
        worktreeBranch?: string
        layoutId?: LayoutID
        hostId?: string
      }) => {
        // TODO(v2 remote create): params.hostId identifies the caller's
        // selected target host, but the v2 session-command wire contract has
        // no field to carry it yet -- pkg/state/session_commands.go's
        // executeCreate hard-rejects any SessionRef.Owner that doesn't match
        // the local catalog owner (ref.Owner != s.owner -> ErrInvalidIdentity),
        // and the RemoteCreateCoordinator (pkg/state/remote_create.go,
        // ExecuteRemoteCreate) that would route this to the right peer is only
        // reachable from pkg/peer/session_state.go today, not from this HTTP
        // command path. Deliberately NOT placing hostId on the SessionRef sent
        // below: doing so would make every remote-host create fail with
        // invalid_identity instead of the current (also wrong, but at least
        // non-fatal) behavior of silently creating locally. Once the backend
        // exposes a target-owner field on this endpoint, wire params.hostId
        // into that field here.
        const ref: SessionRef = { owner: null, session: '', window: 0, pane: 0 }
        return commandClient.sessionCommand(ref, {
          action: 'create',
          name: params.name,
          shell: params.shell,
          cwd: params.cwd,
          worktree_branch: params.worktreeBranch,
          layout_id: params.layoutId,
        })
      },
    [commandClient],
  )

  const sessionCommand = useMemo(
    () => (ref: SessionRef, cmd: SessionCommandAction) => commandClient.sessionCommand(ref, cmd),
    [commandClient],
  )
  const workspaceCommand = useMemo(
    () => (layout: LayoutID, cmd: WorkspaceCommandAction) => commandClient.workspaceCommand(layout, cmd),
    [commandClient],
  )

  const paneTree = useMemo(
    () => (state.workspace.record ? paneNodeToPaneTree(state.workspace.record.tree) : null),
    [state.workspace.record],
  )
  const activeKey = useMemo(
    () => (state.workspace.record?.active_key ? sessionRefToKey(state.workspace.record.active_key) : null),
    [state.workspace.record],
  )
  const layoutId = state.workspace.layoutId

  return {
    state,
    bootstrapped: state.catalogBootstrapped,
    connected: state.connectionOnline,
    paneTree,
    activeKey,
    layoutId,
    createSession,
    sessionCommand,
    workspaceCommand,
  }
}
