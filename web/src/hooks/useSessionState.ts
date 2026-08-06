/**
 * React binding for the canonical normalized state store (Task 12, first slice).
 *
 * Owns one SessionStore + one StateStreamClient + one CommandClient for the
 * lifetime of the component tree it's mounted under. Bootstraps from
 * GET /api/state/bootstrap, then subscribes to /ws/state; every message
 * (catalog or workspace snapshot) replaces the corresponding projection in
 * the store via the store's own generation/revision acceptance rule -- this
 * hook does no additional staleness bookkeeping of its own.
 *
 * This is the only state path; it is mounted unconditionally from
 * SessionApp.
 */

import { useEffect, useMemo, useRef, useSyncExternalStore } from 'react'
import { SessionStore, type SessionStoreState } from '../state/session/store'
import { StateStreamClient } from '../state/session/stateStream'
import { CommandClient, type SessionCommandAction, type WorkspaceCommandAction } from '../state/session/commands'
import { paneNodeToPaneTree } from '../state/session/paneTreeAdapter'
import { decodeBootstrapResponse } from '../state/session/wireCodec'
import type { SessionRef, LayoutID, SplitDirection } from '../state/session/types'
import type { CommandResult, BootstrapResponse } from '../state/session/wireTypes'
import type { HostSnapshot } from '../state/session/wireTypes'
import type { PaneTree } from '../lib/paneTree'
import {
  selectHostByOwner,
  selectHostByPeer,
  selectLocalHost,
} from '../state/session/store'

export type UseSessionStateResult = {
  state: SessionStoreState
  bootstrapped: boolean
  connected: boolean
  // Derived from state.workspace.record via paneNodeToPaneTree -- the single
  // active layout the stream tracks. No groups/singleView: this path only
  // ever has one layout in view (see StateStreamHub's primaryLayout note).
  paneTree: PaneTree | null
  layoutId: LayoutID | null
  // Convenience accessors for host snapshots
  hosts: HostSnapshot[]
  selectHostByOwner: (owner: string) => HostSnapshot | undefined
  selectHostByPeer: (peer_id: string) => HostSnapshot | undefined
  selectLocalHost: () => HostSnapshot | undefined
  createSession: (params: {
    name?: string
    shell?: string
    cwd?: string
    worktreeBranch?: string
    layoutId?: LayoutID
    // Agent preset id selected in the New Session modal, sent on the wire
    // as the top-level create param `agent_type` (see
    // pkg/state/session_commands.go's CreateParams).
    agentType?: string
    // Target host (owner OwnerID) selected by the caller in the New
    // Session modal. Sent as the top-level target_owner wire field; the
    // server routes the create through peer.Manager.RequestRemoteCreate when
    // this differs from the local owner. Omitted means "create locally".
    targetOwner?: string
    // When set (together with layoutId), the new session is placed by
    // splitting the target leaf in one atomic step on the server -- see
    // CreateParams in pkg/state/session_commands.go. This replaces the old
    // create-then-separate-split-command sequence, which could place the
    // new leaf twice (once via create's default placement, once via the
    // follow-up split) and have the split rejected as a duplicate leaf.
    splitTarget?: SessionRef
    splitDirection?: SplitDirection
    splitNewFirst?: boolean
  }) => Promise<CommandResult>
  sessionCommand: (ref: SessionRef, cmd: SessionCommandAction) => Promise<CommandResult>
  workspaceCommand: (layout: LayoutID, cmd: WorkspaceCommandAction) => Promise<unknown>
}

function wsURL(path: string): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}${path}`
}

export function useSessionState(): UseSessionStateResult {
  const storeRef = useRef<SessionStore | null>(null)
  if (storeRef.current === null) storeRef.current = new SessionStore()
  const store = storeRef.current

  const commandClient = useMemo(() => new CommandClient(), [])

  const state = useSyncExternalStore(
    (listener) => store.subscribe(listener),
    () => store.getState(),
  )

  const bootstrappedRef = useRef(false)

  useEffect(() => {
    let disposed = false
    const generation = store.bumpConnectionGeneration()

    const client = new StateStreamClient({
      url: wsURL('/ws/state'),
      callbacks: {
        onCatalog: (snapshot, gen, isLocal) => {
          if (disposed) return
          store.replaceCatalog(snapshot, gen, isLocal)
        },
        onCatalogRemoved: (owner) => {
          if (disposed) return
          store.removeOwnerCatalog(owner)
        },
        onWorkspace: (snapshot, gen) => {
          if (disposed) return
          store.replaceWorkspace(snapshot, gen)
        },
        onHosts: (snapshots) => {
          if (disposed) return
          store.replaceHosts(snapshots)
        },
        onRuntime: (snapshots) => {
          if (disposed) return
          store.updateRuntimeSnapshots(snapshots)
        },
        onConnectionChange: (online) => {
          if (disposed) return
          store.setConnectionOnline(online)
        },
      },
    })

    async function bootstrapAndConnect() {
      try {
        const res = await fetch('/api/state/bootstrap')
        if (res.ok && !disposed) {
          const rawBody = await res.json()
          const body: BootstrapResponse = decodeBootstrapResponse(rawBody)
          store.replaceCatalog(body.local, generation, true)
          for (const remoteSnapshot of body.remote ?? []) {
            store.replaceCatalog(remoteSnapshot, generation, false)
          }
          if (body.workspace) {
            store.replaceWorkspace(body.workspace, generation)
          }
          if (body.hosts) {
            store.replaceHosts(body.hosts)
          }
          // Populate initial runtime snapshots from bootstrap
          const allRuntimeSnapshots: any[] = []
          if (body.runtime) {
            for (const ownerRuntime of body.runtime) {
              allRuntimeSnapshots.push(...(ownerRuntime.snapshots ?? []))
            }
          }
          if (allRuntimeSnapshots.length > 0) {
            store.updateRuntimeSnapshots(allRuntimeSnapshots)
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
  }, [store])

  const createSession = useMemo(
    () =>
      async (params: {
        name?: string
        shell?: string
        cwd?: string
        worktreeBranch?: string
        layoutId?: LayoutID
        agentType?: string
        targetOwner?: string
        splitTarget?: SessionRef
        splitDirection?: SplitDirection
        splitNewFirst?: boolean
      }) => {
        // params.targetOwner identifies the caller's selected target host and is
        // sent as the top-level target_owner wire field (see
        // v2SessionCommandRequest in pkg/server/routes_state_v2.go); when set
        // to a different owner than this node's own, the server forwards the
        // create through peer.Manager.RequestRemoteCreate (the same
        // RemoteCreateCoordinator/RPC path pkg/peer/session_state.go already
        // used for peer-originated creates) instead of running it locally.
        // Omitted (undefined) means "create locally", unchanged from before.
        //
        // Note: a create carries NO SessionRef on the wire -- the server
        // assigns the SessionID (executeCreate synthesizes one when ref is
        // absent). Never send a placeholder ref here; see
        // CommandClient.createSession in state/session/commands.ts.
        return commandClient.createSession({
          action: 'create',
          name: params.name,
          shell: params.shell,
          cwd: params.cwd,
          worktree_branch: params.worktreeBranch,
          layout_id: params.layoutId,
          agent_type: params.agentType,
          target: params.splitTarget,
          direction: params.splitDirection,
          new_first: params.splitNewFirst,
          target_owner: params.targetOwner,
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
  const layoutId = state.workspace.layoutId

  const hosts = state.hosts.hosts
  const hostsState = state.hosts

  return {
    state,
    bootstrapped: state.catalogBootstrapped,
    connected: state.connectionOnline,
    paneTree,
    layoutId,
    hosts,
    selectHostByOwner: (owner: string) => selectHostByOwner(hostsState, owner),
    selectHostByPeer: (peer_id: string) => selectHostByPeer(hostsState, peer_id),
    selectLocalHost: () => selectLocalHost(hostsState),
    createSession,
    sessionCommand,
    workspaceCommand,
  }
}
