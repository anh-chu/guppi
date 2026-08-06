import { useRef, useCallback, useMemo } from 'react'
import type { WikiTarget, WikiState } from '../state/wiki'

type WorkspaceLike = {
  state: { wiki: WikiState }
  actions: { openWiki: (target: WikiTarget) => void; closeWiki: () => void }
}

export interface WikiController {
  target: WikiTarget | null
  history: WikiTarget[]
  /** Try to open `path` in the wiki panel. Returns false when the panel is disabled. */
  openFile: (path: string, cwd?: string, hostId?: string, sessionName?: string) => boolean
  closePanel: () => void
  togglePanel: (cwd?: string) => void
  goBack: () => void
  canGoBack: boolean
}

export function useWikiController(workspace: WorkspaceLike, wikiEnabled: boolean): WikiController {
  const { state, actions } = workspace
  const nonceRef = useRef(0)
  const targetRef = useRef(state.wiki.target)
  targetRef.current = state.wiki.target

  const makeNonce = useCallback(() => ++nonceRef.current, [])

  const openFile = useCallback(
    (path: string, cwd?: string, hostId?: string, sessionName?: string): boolean => {
      if (!wikiEnabled) return false
      actions.openWiki({ path, cwd, nonce: makeNonce(), hostId, session: sessionName })
      return true
    },
    [actions, makeNonce, wikiEnabled],
  )

  const closePanel = useCallback(() => {
    actions.closeWiki()
  }, [actions])

  const togglePanel = useCallback(
    (cwd?: string) => {
      if (targetRef.current) {
        actions.closeWiki()
      } else {
        actions.openWiki({ path: null, cwd, nonce: makeNonce() })
      }
    },
    [actions, makeNonce],
  )

  const goBack = useCallback(() => {
    const previous = state.wiki.history[1]
    if (!previous) return
    actions.openWiki({ ...previous, nonce: makeNonce() })
  }, [actions, makeNonce, state.wiki.history])

  const canGoBack = state.wiki.history.length > 0

  return useMemo(
    () => ({
      target: state.wiki.target,
      history: state.wiki.history,
      openFile,
      closePanel,
      togglePanel,
      goBack,
      canGoBack,
    }),
    [canGoBack, closePanel, goBack, openFile, state.wiki.history, state.wiki.target, togglePanel],
  )
}
