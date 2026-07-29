import { useEffect, useState, type ReactElement } from 'react'

import { basename, loadFile, needsText, type FileKind, type FileLoadFailure } from '../lib/fileSource'
import type { MountedFileViewerProps } from './FileViewerBoundary'

/**
 * The viewer is reached through a dynamic import so that tiptap, mermaid, katex
 * and highlight.js stay out of termyard's main bundle. Statically importing the
 * boundary put roughly a megabyte of renderer into the first paint of every
 * session, including sessions that never open a file.
 *
 * Memoized at module scope: the chunk is fetched once per page load, not once
 * per file, so opening the second file is not slower for no reason.
 */
let viewerModule: Promise<typeof import('./FileViewerBoundary')> | null = null
function loadViewerModule() {
  if (!viewerModule) viewerModule = import('./FileViewerBoundary')
  return viewerModule
}

interface FilePaneProps {
  /** Absolute path, or a non-absolute one we could not resolve. */
  path: string | null
  /** Peer host holding the file. Absent means this machine. */
  hostId?: string
  /** Bumped on every open request, so re-opening the same file reloads it. */
  openNonce?: number
  /** Bumped by the panel's Reload button. Re-mints the grant. */
  reloadSeq: number
}

type PaneState =
  | { phase: 'empty' }
  | { phase: 'loading' }
  | {
      phase: 'ready'
      View: (props: MountedFileViewerProps) => ReactElement
      kind: FileKind
      filename: string
      content?: string
      assetUrl?: string
    }
  | { phase: 'error'; error: FileLoadFailure }

/**
 * One file, rendered natively on termyard's own origin.
 *
 * All of this component's failure modes are LOCAL to it. A bad path, a dead host
 * or a chunk that will not load renders a card in here and touches nothing
 * outside, which is the fix for the shape that bricked the panel before: per-file
 * errors were written into the container's status, the status decided whether the
 * iframe rendered at all, and so one unreadable file tore down the surface every
 * later file needed.
 */
export function FilePane({ path, hostId, openNonce, reloadSeq }: FilePaneProps) {
  const [state, setState] = useState<PaneState>({ phase: 'empty' })

  useEffect(() => {
    if (!path) {
      setState({ phase: 'empty' })
      return
    }
    // Only a shell can expand ~, and guessing $HOME from a session cwd is wrong
    // for any session outside the user's home. Say so rather than minting a
    // grant that will come back as a confusing 404.
    if (!path.startsWith('/')) {
      setState({
        phase: 'error',
        error: {
          title: 'Could not resolve this path',
          detail: path.startsWith('~/')
            ? `${path} needs shell expansion. Open it with an absolute path.`
            : `${path} is relative and the session has no known directory to resolve it against.`,
        },
      })
      return
    }

    const controller = new AbortController()
    let cancelled = false
    setState({ phase: 'loading' })

    void (async () => {
      let viewer: typeof import('./FileViewerBoundary')
      try {
        viewer = await loadViewerModule()
      } catch {
        // A failed chunk fetch is usually a stale index after a deploy. Let the
        // next attempt retry rather than caching the rejection forever.
        viewerModule = null
        if (cancelled) return
        setState({
          phase: 'error',
          error: { title: 'Viewer failed to load', detail: 'Reload termyard and try again.' },
        })
        return
      }
      if (cancelled) return

      let kind: FileKind
      try {
        kind = viewer.fileKindOf(basename(path))
      } catch {
        setState({
          phase: 'error',
          error: { title: 'Could not classify this file', detail: 'The viewer rejected the filename.' },
        })
        return
      }

      const result = await loadFile({ path, kind, hostId, signal: controller.signal })
      if (cancelled) return
      if (!result.ok) {
        setState({ phase: 'error', error: result.error })
        return
      }
      setState({
        phase: 'ready',
        View: viewer.MountedFileViewer,
        kind: result.value.kind,
        filename: result.value.filename,
        content: result.value.content,
        assetUrl: result.value.assetUrl,
      })
    })()

    return () => {
      cancelled = true
      controller.abort()
    }
    // openNonce and reloadSeq are here on purpose. Without openNonce, opening
    // the same path a second time is a no-op; without reloadSeq, an expired
    // grant can never be replaced.
  }, [path, hostId, openNonce, reloadSeq])

  if (state.phase === 'empty') {
    return (
      <div className="flex flex-col items-center justify-center flex-1 gap-2 px-6 text-center">
        <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/40">
          <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/>
        </svg>
        <p className="text-[12px] text-mute">No file selected</p>
        <p className="text-[11px] text-mute/60">Click a file path in a terminal to view it here.</p>
      </div>
    )
  }

  if (state.phase === 'loading') {
    return (
      <div className="flex items-center justify-center flex-1 px-6">
        <p className="text-[11px] text-mute/60">Loading…</p>
      </div>
    )
  }

  if (state.phase === 'error') {
    return (
      <div className="flex flex-col items-center justify-center flex-1 gap-2 px-6 text-center">
        <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
          <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
        </svg>
        <p className="text-[12px] text-mute">{state.error.title}</p>
        <p className="text-[11px] text-mute/60 break-words">{state.error.detail}</p>
      </div>
    )
  }

  return (
    <>
      <state.View
        kind={state.kind}
        filename={state.filename}
        content={state.content}
        assetUrl={state.assetUrl}
      />
      {!needsText(state.kind) && (
        // The URL in the DOM stops working after fileGrantTTL (5 minutes), and
        // nothing in here can observe the media element failing, so name the
        // remedy up front instead of leaving a broken player unexplained.
        <p className="px-3 py-1 text-[10px] text-mute/50 border-t border-hairline shrink-0">
          Read access expires after 5 minutes. Use Reload if this stops loading.
        </p>
      )}
    </>
  )
}
