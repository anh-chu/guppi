// @vitest-environment jsdom
import { renderHook, act } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { useReducer } from 'react'
import { useWikiController } from './useWikiController'
import {
  workspaceReducer,
  createInitialWorkspaceState,
  type WikiTarget,
} from '../state/workspaceReducer'

function renderController(enabled = true) {
  return renderHook(() => {
    const [state, dispatch] = useReducer(
      workspaceReducer,
      createInitialWorkspaceState(),
    )
    const workspace = {
      state,
      actions: {
        openWiki: (target: WikiTarget) =>
          dispatch({ type: 'wiki/open', target }),
        closeWiki: () => dispatch({ type: 'wiki/close' }),
      },
    }
    return useWikiController(workspace, enabled)
  })
}

describe('useWikiController', () => {
  it('returns false from openFile and has no target when disabled', () => {
    const { result } = renderController(false)
    expect(result.current.openFile('/a')).toBe(false)
    expect(result.current.target).toBeNull()
    expect(result.current.canGoBack).toBe(false)
  })

  it('openFile bumps nonce and opens the wiki', () => {
    const { result } = renderController(true)
    act(() => {
      result.current.openFile('/a.ts', '/cwd', 'host-1', 'my-session')
    })
    expect(result.current.target).toEqual({
      path: '/a.ts',
      cwd: '/cwd',
      hostId: 'host-1',
      session: 'my-session',
      nonce: 1,
    })

    act(() => {
      result.current.openFile('/b.ts')
    })
    expect(result.current.target?.path).toBe('/b.ts')
    expect(result.current.target?.nonce).toBe(2)
  })

  it('closePanel closes the wiki', () => {
    const { result } = renderController(true)
    act(() => result.current.openFile('/a.ts'))
    expect(result.current.target).not.toBeNull()

    act(() => result.current.closePanel())
    expect(result.current.target).toBeNull()
  })

  it('togglePanel opens and closes the wiki', () => {
    const { result } = renderController(true)
    expect(result.current.target).toBeNull()

    act(() => result.current.togglePanel('/cwd'))
    expect(result.current.target).toEqual({
      path: null,
      cwd: '/cwd',
      nonce: 1,
    })

    act(() => result.current.togglePanel())
    expect(result.current.target).toBeNull()
  })

  it('goBack navigates history', () => {
    const { result } = renderController(true)
    act(() => result.current.openFile('/first.ts', '/cwd'))
    act(() => result.current.openFile('/second.ts'))

    expect(result.current.target?.path).toBe('/second.ts')
    expect(result.current.canGoBack).toBe(true)

    act(() => result.current.goBack())
    expect(result.current.target?.path).toBe('/first.ts')
    expect(result.current.history[1].path).toBe('/second.ts')
  })
})
