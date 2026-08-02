import type { Session } from '../hooks/useSessions'
import type { GroupRecordMap } from '../hooks/useGroupSync'
import {
  type PaneTree,
  findLeaf,
  getLeaves,
  removeLeaf,
  replaceLeaf,
  splitLeaf,
  insertBesideLeaf,
  swapLeaves,
  movePane,
  popOut,
} from '../lib/paneTree'
import { generateKeyBetween } from 'fractional-indexing'

export type View = 'overview' | 'session' | 'settings'

export type WikiTarget = {
  path: string | null
  cwd?: string
  hostId?: string
  session?: string
  nonce: number
}

export interface WorkspaceView {
  currentView: View
  settingsOpen: boolean
  paneTree: PaneTree | null
  activeKey: string | null
  singleView: string | null
  activeGroupId: string
}

export interface WikiState {
  target: WikiTarget | null
  history: WikiTarget[]
}

export interface WorkspaceState {
  sessions: Session[]
  loading: boolean
  optimistic: Record<string, number>
  connection: {
    live: boolean
    livenessUnknown: boolean
  }
  transportGeneration: number
  groups: GroupRecordMap
  groupsLoaded: boolean
  view: WorkspaceView
  wiki: WikiState
}

export type WorkspaceAction =
  | { type: 'connection'; live: boolean; livenessUnknown: boolean }
  | { type: 'sessions/snapshot'; sessions: Session[]; generation: number; now: number }
  | { type: 'sessions/event'; event: SessionEvent; generation: number }
  | { type: 'optimistic/add'; session: Session; now?: number }
  | { type: 'optimistic/remove'; name: string; host?: string }
  | { type: 'sessions/remove'; key: string }
  | { type: 'groups/snapshot'; groups: GroupRecordMap; generation?: number }
  | { type: 'groups/delta'; groups: GroupRecordMap; generation?: number }
  | { type: 'view/select'; key: string }
  | { type: 'view/navigate'; view?: View; sessionKey?: string | null }
  | { type: 'view/openSettings'; open: boolean }
  | { type: 'view/setSingleView'; sessionKey: string | null }
  | { type: 'view/setCurrentView'; view: View }
  | { type: 'view/close'; sessionKey: string }
  | { type: 'view/removeFromLayout'; sessionKey: string }
  | { type: 'view/split'; targetKey: string; direction: 'h' | 'v'; newKey: string; newFirst?: boolean }
  | { type: 'view/move'; sourceKey: string; targetKey: string; edge: 'left' | 'right' | 'top' | 'bottom' }
  | { type: 'view/swap'; a: string; b: string }
  | { type: 'view/setPaneTree'; tree: PaneTree | null }
  | { type: 'view/setActiveGroup'; groupId: string; tree?: PaneTree | null; focusKey?: string | null }
  | { type: 'view/setActiveKey'; key: string | null }
  | { type: 'view/dissolveToSingle' }
  | { type: 'view/promoteNextGroup' }
  | { type: 'view/pruneMissing'; validKeys: string[]; now: number }
  | { type: 'rename'; oldKey: string; newKey: string }
  | { type: 'wiki/open'; target: WikiTarget }
  | { type: 'wiki/close' }
  | { type: 'restore'; snapshot: Partial<WorkspaceState> }
  | { type: 'legacy/migrate'; legacy: LegacyMigrationInput }

export interface SessionEvent {
  type: string
  session?: string
  host?: string
  data?: { new_name?: string } | null
}

export interface LegacyGroup {
  id: string
  tree: PaneTree
  activeKey?: string | null
  name?: string
}

export interface LegacyMigrationInput {
  groups: LegacyGroup[]
  order: string[]
  sessionOrder: string[]
}

const STUB_TTL_MS = 6000
const WIKI_HISTORY_MAX = 20

export function sessionKey(session: Session): string {
  return session.host ? `${session.host}/${session.name}` : session.name
}

export function parseSessionKey(key: string): { host: string; name: string } {
  const idx = key.indexOf('/')
  if (idx === -1) return { host: '', name: key }
  return { host: key.substring(0, idx), name: key.substring(idx + 1) }
}

export interface WorkspaceStateWithStreaks extends WorkspaceState {
  missingStreaks: Record<string, MissingStreak>
}

export function createInitialWorkspaceState(input?: {
  view?: Partial<WorkspaceView>
  activeGroupId?: string
}): WorkspaceStateWithStreaks {
  const groupId = input?.activeGroupId ?? randomId()
  return {
    sessions: [],
    loading: true,
    optimistic: {},
    connection: {
      live: false,
      livenessUnknown: true,
    },
    transportGeneration: 0,
    groups: {},
    groupsLoaded: false,
    missingStreaks: {},
    view: {
      currentView: 'overview',
      settingsOpen: false,
      paneTree: null,
      activeKey: null,
      singleView: null,
      activeGroupId: groupId,
      ...input?.view,
    },
    wiki: {
      target: null,
      history: [],
    },
  }
}

function randomId() {
  return Math.random().toString(36).slice(2)
}

function matchesKey(s: Session, key: string): boolean {
  return sessionKey(s) === key
}

function matchesNameHost(s: Session, name: string, host?: string): boolean {
  if (host === undefined) return s.name === name
  return s.name === name && (s.host || '') === host
}

function reconcileSnapshot(
  state: WorkspaceState,
  sessions: Session[],
  now: number,
): Session[] {
  const confirmed = new Set(sessions.map(s => s.name))
  const nextOptimistic: Record<string, number> = {}
  for (const [name, insertedAt] of Object.entries(state.optimistic)) {
    if (confirmed.has(name) || now - insertedAt > STUB_TTL_MS) continue
    nextOptimistic[name] = insertedAt
  }
  const kept = state.sessions.filter(s => nextOptimistic[s.name] !== undefined)
  const merged = [...kept]
  for (const s of sessions) {
    const idx = merged.findIndex(x => x.name === s.name && (x.host || '') === (s.host || ''))
    if (idx === -1) merged.push(s)
    else merged[idx] = s
  }
  return merged
}

function upsertSessionByKey(sessions: Session[], session: Session): Session[] {
  const idx = sessions.findIndex(s => s.name === session.name && (s.host || '') === (session.host || ''))
  if (idx === -1) return [...sessions, session]
  const copy = sessions.slice()
  copy[idx] = session
  return copy
}

function removeSessionByKey(sessions: Session[], key: string): Session[] {
  return sessions.filter(s => sessionKey(s) !== key)
}

function applyRenameToWiki(wiki: WikiState, oldKey: string, newKey: string): WikiState {
  const { host: oldHost, name: oldName } = parseSessionKey(oldKey)
  const { host: newHost, name: newName } = parseSessionKey(newKey)
  const rebuild = (t: WikiTarget | null): WikiTarget | null => {
    if (!t || t.session !== oldName || (t.hostId ?? '') !== oldHost) return t
    return { ...t, session: newName, hostId: newHost || undefined }
  }
  return {
    target: rebuild(wiki.target),
    history: wiki.history.map(rebuild).filter((t): t is WikiTarget => t !== null),
  }
}

function selectViewKey(view: WorkspaceView, key: string): WorkspaceView {
  if (view.paneTree && findLeaf(view.paneTree, key)) {
    return { ...view, currentView: 'session', singleView: null, activeKey: key }
  }
  return { ...view, currentView: 'session', singleView: key }
}

function navigateTo(view: WorkspaceView, targetView?: View, sessionKey?: string | null): WorkspaceView {
  if (targetView === 'settings') {
    return { ...view, currentView: 'overview', settingsOpen: true }
  }
  if (sessionKey) return selectViewKey(view, sessionKey)
  return { ...view, currentView: targetView ?? 'overview' }
}

function closePane(view: WorkspaceView, sessionKey: string): WorkspaceView {
  if (!view.paneTree) return view
  const nextTree = removeLeaf(view.paneTree, sessionKey)
  if (nextTree === null) {
    return { ...view, paneTree: null, activeKey: null }
  }
  const nextActive =
    sessionKey === view.activeKey ? getLeaves(nextTree)[0] ?? null : view.activeKey
  return { ...view, paneTree: nextTree, activeKey: nextActive }
}

function removeFromLayout(view: WorkspaceView, sessionKey: string): WorkspaceView {
  const next = closePane(view, sessionKey)
  return { ...next, singleView: next.singleView === sessionKey ? null : next.singleView }
}

function splitPane(
  view: WorkspaceView,
  targetKey: string,
  direction: 'h' | 'v',
  newKey: string,
  newFirst?: boolean,
): WorkspaceView {
  if (!view.paneTree) {
    if (targetKey) {
      const base = popOut(targetKey)
      if (base.type === 'leaf' && base.sessionKey === targetKey) {
        return {
          ...view,
          paneTree: splitLeaf(base, targetKey, direction, newKey),
          activeKey: newKey,
          currentView: 'session',
          singleView: null,
        }
      }
    }
    return { ...view, paneTree: popOut(newKey), activeKey: newKey, currentView: 'session', singleView: null }
  }
  if (findLeaf(view.paneTree, newKey)) {
    return { ...view, activeKey: newKey, currentView: 'session', singleView: null }
  }
  if (findLeaf(view.paneTree, targetKey)) {
    const nextTree = newFirst
      ? insertBesideLeaf(view.paneTree, targetKey, direction, newKey, true)
      : splitLeaf(view.paneTree, targetKey, direction, newKey)
    return { ...view, paneTree: nextTree, activeKey: newKey, currentView: 'session', singleView: null }
  }
  return { ...view, currentView: 'session', singleView: newKey }
}

function movePaneView(
  view: WorkspaceView,
  sourceKey: string,
  targetKey: string,
  edge: 'left' | 'right' | 'top' | 'bottom',
): WorkspaceView {
  if (!view.paneTree) return view
  const nextTree = movePane(view.paneTree, sourceKey, targetKey, edge)
  return { ...view, paneTree: nextTree }
}

function swapPaneView(view: WorkspaceView, a: string, b: string): WorkspaceView {
  if (!view.paneTree) return view
  return { ...view, paneTree: swapLeaves(view.paneTree, a, b) }
}

interface MissingStreak {
  count: number
  last: number
}

export function workspaceReducer(
  stateArg: WorkspaceStateWithStreaks,
  action: WorkspaceAction,
): WorkspaceStateWithStreaks {
  let state = stateArg

  // Transport generation gate: ignore stale transport actions.
  if ('generation' in action && typeof action.generation === 'number') {
    if (action.generation < state.transportGeneration) {
      return state
    }
    state = { ...state, transportGeneration: action.generation }
  }

  switch (action.type) {
    case 'connection': {
      return {
        ...state,
        connection: { live: action.live, livenessUnknown: action.livenessUnknown },
      }
    }

    case 'sessions/snapshot': {
      const merged = reconcileSnapshot(state, action.sessions, action.now)
      const notEmpty = action.sessions.length > 0
      const connection = notEmpty
        ? { ...state.connection, livenessUnknown: false }
        : state.connection
      return {
        ...state,
        sessions: merged,
        loading: false,
        connection,
      }
    }

    case 'sessions/event': {
      const { event } = action
      if (event.type === 'session-added') {
        const name = event.session || ''
        const existing = state.sessions.find(s => s.name === name && (s.host || '') === (event.host || ''))
        if (existing) return state
        const stub = state.sessions.find(
          s => s.name === name && (s.host || '') === (event.host || '') && !s.id,
        )
        if (stub) return state
        return state
      }
      if (event.type === 'session-removed') {
        const key = event.host ? `${event.host}/${event.session}` : event.session || ''
        return {
          ...state,
          sessions: removeSessionByKey(state.sessions, key),
        }
      }
      if (event.type === 'session-renamed') {
        const oldName = event.session || ''
        const newName = event.data?.new_name || ''
        if (!oldName || !newName) return state
        const oldKey = event.host ? `${event.host}/${oldName}` : oldName
        const newKey = event.host ? `${event.host}/${newName}` : newName
        const idx = state.sessions.findIndex(s => sessionKey(s) === oldKey)
        if (idx === -1) return state
        const renamed = { ...state.sessions[idx], name: newName }
        const sessions = [...state.sessions.slice(0, idx), renamed, ...state.sessions.slice(idx + 1)]
        let view = state.view
        if (view.paneTree && findLeaf(view.paneTree, oldKey)) {
          if (findLeaf(view.paneTree, newKey)) {
            view = { ...view, paneTree: removeLeaf(view.paneTree, oldKey) ?? view.paneTree }
          } else {
            view = { ...view, paneTree: replaceLeaf(view.paneTree, oldKey, newKey) }
          }
        }
        view = {
          ...view,
          activeKey: view.activeKey === oldKey ? newKey : view.activeKey,
          singleView: view.singleView === oldKey ? newKey : view.singleView,
        }
        return {
          ...state,
          sessions,
          view,
          wiki: applyRenameToWiki(state.wiki, oldKey, newKey),
        }
      }
      if (event.type === 'sessions-changed') {
        return state
      }
      return state
    }

    case 'optimistic/add': {
      const session = action.session
      const nextOptimistic = { ...state.optimistic, [session.name]: action.now ?? Date.now() }
      return {
        ...state,
        optimistic: nextOptimistic,
        sessions: upsertSessionByKey(state.sessions, session),
      }
    }

    case 'optimistic/remove': {
      const nextOptimistic = { ...state.optimistic }
      delete nextOptimistic[action.name]
      return {
        ...state,
        optimistic: nextOptimistic,
        sessions: state.sessions.filter(s => !matchesNameHost(s, action.name, action.host)),
      }
    }

    case 'sessions/remove': {
      return { ...state, sessions: removeSessionByKey(state.sessions, action.key) }
    }

    case 'groups/snapshot':
    case 'groups/delta': {
      return {
        ...state,
        groups: { ...state.groups, ...action.groups },
        groupsLoaded: true,
      }
    }

    case 'view/select': {
      return { ...state, view: selectViewKey(state.view, action.key) }
    }

    case 'view/navigate': {
      return { ...state, view: navigateTo(state.view, action.view, action.sessionKey) }
    }

    case 'view/openSettings': {
      return { ...state, view: { ...state.view, settingsOpen: action.open } }
    }

    case 'view/setSingleView': {
      return { ...state, view: { ...state.view, singleView: action.sessionKey } }
    }

    case 'view/setCurrentView': {
      return { ...state, view: { ...state.view, currentView: action.view } }
    }

    case 'view/close': {
      return { ...state, view: closePane(state.view, action.sessionKey) }
    }

    case 'view/removeFromLayout': {
      return { ...state, view: removeFromLayout(state.view, action.sessionKey) }
    }

    case 'view/split': {
      return {
        ...state,
        view: splitPane(state.view, action.targetKey, action.direction, action.newKey, action.newFirst),
      }
    }

    case 'view/move': {
      return { ...state, view: movePaneView(state.view, action.sourceKey, action.targetKey, action.edge) }
    }

    case 'view/swap': {
      return { ...state, view: swapPaneView(state.view, action.a, action.b) }
    }

    case 'view/setPaneTree': {
      return { ...state, view: { ...state.view, paneTree: action.tree } }
    }

    case 'view/setActiveGroup': {
      const { groupId, tree, focusKey } = action
      return {
        ...state,
        view: {
          ...state.view,
          activeGroupId: groupId,
          paneTree: tree ?? state.view.paneTree,
          activeKey:
            focusKey !== undefined
              ? focusKey
              : state.view.activeGroupId === groupId
                ? state.view.activeKey
                : null,
          singleView: null,
          currentView: 'session',
        },
      }
    }

    case 'view/setActiveKey': {
      return { ...state, view: { ...state.view, activeKey: action.key } }
    }

    case 'view/dissolveToSingle': {
      const leaves = state.view.paneTree ? getLeaves(state.view.paneTree) : []
      if (leaves.length !== 1) return state
      return {
        ...state,
        view: {
          ...state.view,
          paneTree: null,
          singleView: leaves[0] ?? null,
          activeKey: null,
          currentView: 'session',
        },
      }
    }

    case 'view/promoteNextGroup': {
      const entries = Object.entries(state.groups).sort(([, a], [, b]) => {
        const ar = a.rank ?? ''
        const br = b.rank ?? ''
        if (!ar && br) return 1
        if (ar && !br) return -1
        if (ar !== br) return ar.localeCompare(br)
        return 0
      })
      const next = entries.find(([id]) => id !== state.view.activeGroupId)
      if (!next) {
        return {
          ...state,
          view: {
            ...state.view,
            paneTree: null,
            activeKey: null,
            singleView: null,
            currentView: 'overview',
          },
        }
      }
      const [nextId, nextGroup] = next
      const leaves = getLeaves(nextGroup.tree)
      return {
        ...state,
        view: {
          ...state.view,
          activeGroupId: nextId,
          paneTree: nextGroup.tree,
          activeKey: leaves[0] ?? null,
          singleView: null,
          currentView: 'session',
        },
      }
    }

    case 'view/pruneMissing': {
      const { validKeys, now } = action
      const live = state.connection.live && !state.connection.livenessUnknown
      if (!state.view.paneTree) {
        return {
          ...state,
          view: {
            ...state.view,
            singleView: validKeys.includes(state.view.singleView ?? '') ? state.view.singleView : null,
          },
          missingStreaks: {},
        }
      }
      const streaks = { ...(state.missingStreaks || {}) }
      const leaves = getLeaves(state.view.paneTree)
      const toRemove: string[] = []
      for (const key of leaves) {
        if (validKeys.includes(key)) {
          delete streaks[key]
          continue
        }
        const prev = streaks[key]
        if (!prev) {
          streaks[key] = { count: 1, last: now }
          continue
        }
        if (now - prev.last < 1000) continue
        if (prev.count + 1 >= 2) {
          if (live) toRemove.push(key)
          delete streaks[key]
        } else {
          streaks[key] = { count: prev.count + 1, last: now }
        }
      }
      if (toRemove.length === 0) {
        return {
          ...state,
          view: {
            ...state.view,
            singleView: validKeys.includes(state.view.singleView ?? '') ? state.view.singleView : null,
          },
          missingStreaks: streaks,
        }
      }
      let tree: PaneTree | null = state.view.paneTree
      for (const key of toRemove) {
        if (tree === null) break
        tree = removeLeaf(tree, key)
      }
      const remainingLeaves = tree ? getLeaves(tree) : []
      const activeKey =
        tree && state.view.activeKey && toRemove.includes(state.view.activeKey)
          ? remainingLeaves[0] ?? null
          : state.view.activeKey
      return {
        ...state,
        view: {
          ...state.view,
          paneTree: tree,
          activeKey,
          singleView: validKeys.includes(state.view.singleView ?? '') ? state.view.singleView : null,
        },
        missingStreaks: streaks,
      }
    }

    case 'rename': {
      const { oldKey, newKey } = action
      if (!oldKey || !newKey || oldKey === newKey) return state
      let sessions = state.sessions
      const idx = state.sessions.findIndex(s => sessionKey(s) === oldKey)
      if (idx !== -1) {
        sessions = [
          ...state.sessions.slice(0, idx),
          { ...state.sessions[idx], name: parseSessionKey(newKey).name },
          ...state.sessions.slice(idx + 1),
        ]
      }
      let view = state.view
      if (view.paneTree && findLeaf(view.paneTree, oldKey)) {
        if (findLeaf(view.paneTree, newKey)) {
          view = { ...view, paneTree: removeLeaf(view.paneTree, oldKey) ?? view.paneTree }
        } else {
          view = { ...view, paneTree: replaceLeaf(view.paneTree, oldKey, newKey) }
        }
      }
      view = {
        ...view,
        activeKey: view.activeKey === oldKey ? newKey : view.activeKey,
        singleView: view.singleView === oldKey ? newKey : view.singleView,
      }
      return {
        ...state,
        sessions,
        view,
        wiki: applyRenameToWiki(state.wiki, oldKey, newKey),
      }
    }

    case 'wiki/open': {
      const history = [action.target, ...state.wiki.history.filter(t => t.path !== action.target.path)].slice(0, WIKI_HISTORY_MAX)
      return { ...state, wiki: { target: action.target, history } }
    }

    case 'wiki/close': {
      return { ...state, wiki: { ...state.wiki, target: null } }
    }

    case 'restore': {
      const s = action.snapshot
      return {
        ...state,
        ...s,
        view: { ...state.view, ...(s.view ?? {}) },
        wiki: { ...state.wiki, ...(s.wiki ?? {}) },
      }
    }

    case 'legacy/migrate': {
      return state
    }

    default:
      return state
  }
}

// Legacy migration planner. Pure: given local legacy data and current server
// groups/order, return the sync operations needed to upload missing ranks.
export function planLegacyMigration(
  legacy: LegacyMigrationInput,
  currentGroups: GroupRecordMap,
  currentSessionOrder: Record<string, string>,
  activeGroupId: string,
  activeTree?: PaneTree | null,
  activeName?: string,
): {
  groupRanks: { id: string; rank: string; tree?: PaneTree; name?: string }[]
  sessionRanks: { key: string; rank: string }[]
  order: string[]
} {
  const orderIds =
    Array.isArray(legacy.order) && legacy.order.length > 0
      ? legacy.order.filter((id): id is string => typeof id === 'string')
      : [activeGroupId, ...legacy.groups.map(g => g.id)]
  const uniqueOrder = orderIds.filter((id, idx, all) => id && all.indexOf(id) === idx)
  const legacyById = new Map(legacy.groups.map(g => [g.id, g]))

  const groupRanks: { id: string; rank: string; tree?: PaneTree; name?: string }[] = []
  let prevRank: string | null = null
  for (const id of uniqueOrder) {
    const serverGroup = currentGroups[id]
    if (serverGroup?.rank) {
      prevRank = serverGroup.rank
      continue
    }
    const rank = generateKeyBetween(prevRank, null)
    const localGroup =
      id === activeGroupId && activeTree
        ? { id, tree: activeTree, name: activeName }
        : legacyById.get(id)
    groupRanks.push({
      id,
      rank,
      ...(localGroup?.tree ? { tree: localGroup.tree } : {}),
      ...(localGroup?.name ? { name: localGroup.name } : {}),
    })
    prevRank = rank
  }

  const sessionRanks: { key: string; rank: string }[] = []
  let prevSessionRank: string | null = null
  const sessionIds = Array.isArray(legacy.sessionOrder)
    ? legacy.sessionOrder.filter((id): id is string => typeof id === 'string')
    : []
  for (const key of sessionIds) {
    if (currentSessionOrder[key]) {
      prevSessionRank = currentSessionOrder[key]
      continue
    }
    const rank = generateKeyBetween(prevSessionRank, null)
    sessionRanks.push({ key, rank })
    prevSessionRank = rank
  }

  return { groupRanks, sessionRanks, order: uniqueOrder }
}


