import { useEffect, useRef, useState } from 'react'

export type WikiPanelStatus =
  | 'checking'
  | 'ready'
  | 'offline'
  | 'no-key'
  | 'bad-key'
  | 'bad-root'
  | 'unconfigured'
  | 'unresolved-path'

interface WikiPanelProps {
  wikiUrl: string
  filePath: string | null
  /**
   * Bumped on every open request, even when the path is unchanged. Without it a
   * second open of the same file is a no-op, because the send effect keys on the
   * resolved path. That is easy to hit now that one panel serves every pane:
   * open A from one pane, then A from another, and nothing would happen.
   */
  openNonce?: number
  /** Session cwd. Sent as wiki-viewer's ephemeral root so the file resolves
   *  regardless of which workspace wiki-viewer has active. */
  sessionCwd?: string
  /** Peer host ID. Threaded through to the grant call so remote files are
   *  relayed through the mesh control link. */
  hostId?: string
  onClose: () => void
}

/** Collapse . and .. segments. wiki-viewer normalizes too, but a path we hand
 *  out has to be comparable against the one we sent last. */
function normalizePath(path: string): string {
  const absolute = path.startsWith('/')
  const out: string[] = []
  for (const seg of path.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      // A leading .. on a relative path has nothing to pop and must be kept.
      if (out.length && out[out.length - 1] !== '..') out.pop()
      else if (!absolute) out.push('..')
      continue
    }
    out.push(seg)
  }
  return (absolute ? '/' : '') + out.join('/')
}

/**
 * Make a path from terminal output absolute, which is what wiki-viewer needs:
 * its navigateToPath treats a NON-absolute path as already root-relative, so a
 * relative path is silently reinterpreted against whatever root we happened to
 * send, and a relative path with no root at all is looked up inside
 * wiki-viewer's own configured workspace and reported as File not found.
 *
 * Every non-absolute form is joined against the cwd, not just ./ and ../ .
 * Bare relative paths like app/(public)/sign-in.tsx are the common shape in
 * terminal output and were previously passed through untouched.
 *
 * ~/ is left alone: only a shell can expand it, and guessing $HOME from the cwd
 * would be wrong for any session outside the user's home.
 */
export function resolveFilePath(path: string, cwd?: string): string {
  if (path.startsWith('/')) return normalizePath(path)
  if (path.startsWith('~/')) return path
  if (cwd && cwd.startsWith('/')) return normalizePath(cwd + '/' + path)
  return path
}

/** True when `abs` is `dir` itself or sits beneath it. */
function isInside(dir: string, abs: string): boolean {
  const d = dir.replace(/\/+$/, '')
  if (!d.startsWith('/')) return false
  // The trailing slash is load-bearing: a bare startsWith would treat
  // /home/sil/guppi-secrets as living inside /home/sil/guppi.
  return abs === d || abs.startsWith(d + '/')
}

/**
 * The root handed to wiki-viewer. It MUST contain the file: wiki-viewer
 * relativizes the path against the root and answers "Invalid path" when the
 * file falls outside, which the panel can only render as its empty
 * "select a file to view or edit" screen.
 *
 * The session cwd is preferred because it yields a useful tree, but it is only
 * used when it actually contains the file. A terminal sitting in one repo
 * routinely prints paths belonging to another, and passing that cwd anyway is
 * what produced the empty panel. The file's own directory is, by construction,
 * a root that contains it.
 *
 * A root also has to be present at all: wiki-viewer grants a non-loopback parent
 * postMessage trust only when the host supplied one, and termyard's documented
 * deployment sits behind Tailscale or a reverse proxy. A narrow root is a small
 * tree; no root is a panel where only the first file of a session works.
 */
export function pickEmbedRoot(resolvedPath: string | null, sessionCwd?: string): string | undefined {
  // Non-absolute (a bare name, or ~/ which only the shell can expand): nothing
  // reliable to derive, so keep the cwd.
  if (!resolvedPath || !resolvedPath.startsWith('/')) return sessionCwd
  if (sessionCwd && isInside(sessionCwd, resolvedPath)) return sessionCwd
  const cut = resolvedPath.lastIndexOf('/')
  // cut === 0 means a file directly in /. Rooting there would hand wiki-viewer
  // the whole filesystem to walk, so prefer the cwd even though it will not
  // resolve; an unbounded root is the worse failure.
  return cut > 0 ? resolvedPath.slice(0, cut) : sessionCwd
}

export function WikiPanel({ wikiUrl, filePath, openNonce, sessionCwd, hostId, onClose }: WikiPanelProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [status, setStatus] = useState<WikiPanelStatus>('checking')
  const [embedSrc, setEmbedSrc] = useState<string | null>(null)
  const [width, setWidth] = useState(480)
  const dragRef = useRef<{ startX: number; startWidth: number } | null>(null)

  // Content mode flag — read once per mount so flipping it mid-flight does not
  // produce a half-initialized panel. Default (unset) is the legacy path.
  const contentMode = typeof localStorage !== 'undefined' && localStorage.getItem('termyard.wikiContentMode') === '1'

  const resolvedPath = contentMode ? null : (filePath ? resolveFilePath(filePath, sessionCwd) : null)

  const effectiveRoot = contentMode ? undefined : pickEmbedRoot(resolvedPath, sessionCwd)

  // Latest resolved path, read by the embed-url effect without making it a
  // dependency. Changing the file must NOT rebuild the iframe URL — that would
  // reload the iframe on every click. Subsequent files go over postMessage.
  const resolvedPathRef = useRef<string | null>(resolvedPath)
  resolvedPathRef.current = resolvedPath

  // The file baked into the current embed URL, consumed once on first load.
  const bakedPathRef = useRef<string | null>(null)

  // In content mode the path is never baked into the URL; the iframe loads a
  // stub. All files arrive via postMessage, including the first.
  const bakedPath = contentMode ? null : bakedPathRef.current

  // The origin the embed URL resolves to, derived from the loaded iframe so it
  // is always the real wiki-viewer origin regardless of redirects. Used for
  // origin-verifying postMessage and for sendOpenFile's targetOrigin.
  const wikiOriginRef = useRef<string>(wikiUrl)

  // ---- Content-mode state ----
  const contentReadyRef = useRef(false)
  const pendingContentRef = useRef<{ path: string; nonce: number } | null>(null)
  // A failed fetch is about ONE file, not about the panel. Keeping it out of
  // `status` is what lets the iframe stay mounted: status drives whether the
  // iframe renders at all, so routing errors through it unmounted the stub,
  // dropped the pushed content, and left every later file unable to open.
  const [contentError, setContentError] = useState<{ title: string; detail: string } | null>(null)

  // Resolve status and build the embed URL. Keyed on the root only: the root is
  // what determines which wiki-viewer instance scope we need.
  useEffect(() => {
    let cancelled = false
    const controller = new AbortController()
    setStatus('checking')
    setEmbedSrc(null)
    contentReadyRef.current = false

    // A path we could not make absolute means no reliable root either, and
    // loading rootless is the wrong kind of failure: wiki-viewer resolves the
    // path inside its OWN configured workspace and renders its empty state, so
    // the panel looks like it simply ignored the click. Say so instead.
    if (!contentMode && resolvedPathRef.current && !resolvedPathRef.current.startsWith('/')) {
      setStatus('unresolved-path')
      return
    }

    ;(async () => {
      try {
        const hres = await fetch('/api/wiki/health', { signal: controller.signal })
        if (!hres.ok) throw new Error('health')
        const health: {
          reachable?: boolean
          has_key?: boolean
          auth_ok?: boolean
          configured?: boolean
        } = await hres.json()
        if (cancelled) return

        if (!health.reachable) { setStatus('offline'); return }
        if (!health.has_key) { setStatus('no-key'); return }
        if (!health.auth_ok) { setStatus('bad-key'); return }
        // wiki-viewer's own root config only matters when we are NOT supplying a
        // root. With a root, its workspace state is irrelevant by design.
        if (!contentMode && !effectiveRoot && !health.configured) { setStatus('unconfigured'); return }

        const params = new URLSearchParams()
        if (contentMode) {
          params.set('mode', 'content')
        } else {
          if (effectiveRoot) params.set('root', effectiveRoot)
          if (resolvedPathRef.current) params.set('file', resolvedPathRef.current)
        }

        const eres = await fetch('/api/wiki/embed-url?' + params.toString(), {
          signal: controller.signal,
        })
        if (cancelled) return

        if (!eres.ok) {
          const body: { error?: string } = await eres.json().catch(() => ({}))
          switch (body.error) {
            case 'api_key_required': setStatus('no-key'); break
            case 'root_requires_api_key': setStatus('no-key'); break
            case 'key_rejected': setStatus('bad-key'); break
            case 'root_not_found':
            case 'root_not_a_directory':
            // Any other rejection of the root from wiki-viewer, passed through
            // verbatim so a new code on their side degrades to the right shape
            // rather than the wrong one.
            case 'root_rejected': setStatus('bad-root'); break
            case 'wiki_unreachable': setStatus('offline'); break
            default: setStatus('unconfigured')
          }
          return
        }

        const { url } = (await eres.json()) as { url: string }
        if (cancelled) return
        // wiki-viewer resolves file= during the initial load, so the first paint
        // already shows this file. Remember it so we don't immediately postMessage
        // a navigation to the file that is already open.
        if (!contentMode) {
          bakedPathRef.current = resolvedPathRef.current
        }
        // Capture the origin from the returned URL for postMessage verification.
        try { wikiOriginRef.current = new URL(url).origin } catch { /* keep the raw wikiUrl fallback */ }
        // Deliberately not logged: this URL carries the API key.
        setEmbedSrc(url)
        setStatus('ready')
      } catch {
        if (!cancelled) setStatus('offline')
      }
    })()

    return () => { cancelled = true; controller.abort() }
    // In content mode the embed URL has no root dependency — it is always the
    // stub. Rebuild only when the mode or wikiUrl changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [contentMode ? wikiUrl : effectiveRoot])

  // "Open in wiki-viewer" (new tab) must reuse the server-built URL, because that
  // is the only thing carrying ?root= and the API key — the frontend cannot
  // rebuild either, since the key is masked in the preferences GET.
  //
  // This link used to hand-build `${wikiUrl}/?file=<abs path>`, which dropped
  // both. wiki-viewer then fell back to the most-recent registered workspace,
  // could not relativize an absolute path outside it, and rendered its empty
  // "select a file to view or edit" screen. Unauthenticated it 307s to /signin.
  //
  // embed=1 is deliberately KEPT. wiki-viewer's middleware validates ?api_key=
  // only inside `if (isEmbed)`, so dropping embed=1 makes the key (and the
  // embed cookie) ignored and the navigation 307s to /signin. Verified: with
  // embed=1 it is 200, without it is 307 even with a valid key and cookie.
  // That coupling is intentional on their side — the api-key identity is
  // synthetic and honoring it outside embed mode would expose Settings, which
  // displays the API key itself.
  //
  // chrome=1 is set again here rather than inherited: a full tab must show the
  // sidebar regardless of what the panel currently asks for, so this link does
  // not silently lose the chrome if the embed default changes.
  // Auth stays gated on embed=1; only the chrome moved.
  //
  // Only file= is re-pointed, at the currently shown path: embedSrc carries the
  // file baked in at load time, which goes stale after postMessage navigation.
  // In content mode this link is hidden — the stub page has no sidebar to show.
  const externalHref = contentMode ? null : (() => {
    if (!embedSrc) return null
    try {
      const u = new URL(embedSrc, window.location.origin)
      u.searchParams.set('chrome', '1')
      if (resolvedPath) u.searchParams.set('file', resolvedPath)
      return u.toString()
    } catch {
      return null
    }
  })()

  // ---- Content-mode file fetch ----
  const fetchAndSendContent = (file: string) => {
    const origin = wikiOriginRef.current
    ;(async () => {
      try {
        // Grant a short-lived capability token for this path.
        let grantQS = `path=${encodeURIComponent(file)}`
        if (hostId) grantQS += `&host=${encodeURIComponent(hostId)}`
        const grantRes = await fetch(`/file/grant?${grantQS}`, { method: 'POST' })
        if (!grantRes.ok) {
          setContentError({ title: 'Could not open file', detail: `Grant returned HTTP ${grantRes.status}` })
          return
        }
        const grantData: { token?: string; error?: string } = await grantRes.json().catch(() => ({}))
        if (!grantData.token) {
          setContentError({ title: 'Could not open file', detail: 'Grant response missing token' })
          return
        }

        // Fetch the file through the capability token.
        const ac = new AbortController()
        const timer = setTimeout(() => ac.abort(), 10_000)
        let fileRes: Response
        try {
          fileRes = await fetch(`/file?token=${encodeURIComponent(grantData.token)}`, { signal: ac.signal })
        } finally {
          clearTimeout(timer)
        }
        if (ac.signal.aborted) {
          setContentError({ title: 'File read timed out', detail: 'No response within 10s' })
          return
        }
        if (!fileRes.ok) {
          setContentError({ title: 'File read failed', detail: `HTTP ${fileRes.status}` })
          return
        }

        const text = await fileRes.text()
        // Mirror the server's remote-read cap so the user gets a message instead
        // of a wedged tab. Measured in bytes, not string length, because a
        // multi-byte file is larger on the wire than its character count.
        const bytes = new TextEncoder().encode(text).length
        if (bytes > 10 * 1024 * 1024) {
          setContentError({
            title: 'File too large',
            detail: `${(bytes / (1024 * 1024)).toFixed(1)} MB exceeds the 10 MB limit`,
          })
          return
        }

        // Push to the embedded stub page.
        const win = iframeRef.current?.contentWindow
        if (!win) {
          setContentError({ title: 'Viewer not ready', detail: 'The embedded viewer went away before content arrived' })
          return
        }
        win.postMessage({ type: 'render-content', path: file, content: text }, origin)
        setContentError(null)
      } catch {
        setContentError({ title: 'File read failed', detail: 'Network error while fetching contents' })
      }
    })()
  }

  // ---- Message listener ----
  useEffect(() => {
    const onMessage = (e: MessageEvent) => {
      // Reject messages from origins we do not trust. wikiOriginRef is set from
      // the embed URL response before the iframe loads, so by the time the stub
      // posts embed-doc-ready the origin is known.
      const trusted = wikiOriginRef.current
      if (e.origin !== new URL(trusted, window.location.origin).origin) return

      if (e.data?.type === 'embed-doc-ready') {
        contentReadyRef.current = true
        const pending = pendingContentRef.current
        pendingContentRef.current = null
        if (pending) fetchAndSendContent(pending.path)
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
    // Re-bind if the embed URL changes (new wikiOrigin). Deliberately narrow:
    // the listener itself has no state deps, only the trusted origin changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [embedSrc])

  // ---- Content-mode: flush pending target on ready, handle new files ----
  // In content mode every file open (including the first) goes through
  // fetchAndSendContent. If the iframe has not signalled embed-doc-ready yet,
  // queue it. If it already has, send immediately.
  // openNonce gates the effect, so the same file clicked twice still sends.
  useEffect(() => {
    if (!contentMode) return
    // Gated on embedSrc rather than status: an error opening one file must not
    // stop the next one from opening.
    if (!filePath || !embedSrc) return
    if (contentReadyRef.current) {
      fetchAndSendContent(filePath)
    } else {
      pendingContentRef.current = { path: filePath, nonce: openNonce ?? 0 }
    }
    // openNonce triggers re-send of the same path. filePath alone is not enough
    // because the same file from two different panes has the same path.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filePath, openNonce, embedSrc, contentMode])

  // ---- Legacy-mode send, iframe load, and drag handlers ----

  // Send the open-file message, queueing if the iframe has not loaded yet.
  const [iframeLoaded, setIframeLoaded] = useState(false)
  const pendingPathRef = useRef<string | null>(null)

  const sendOpenFile = (resolved: string) => {
    const win = iframeRef.current?.contentWindow
    if (!win) return
    win.postMessage({ type: 'open-file', path: resolved }, wikiOriginRef.current)
  }

  // Legacy effect: when the file changes, send it to the already-loaded iframe.
  useEffect(() => {
    if (contentMode) return
    if (!resolvedPath || status !== 'ready') return
    if (iframeLoaded) sendOpenFile(resolvedPath)
    else pendingPathRef.current = resolvedPath
    // openNonce belongs HERE and must never reach the embed-url effect's
    // [effectiveRoot] deps: this one re-sends a message, that one would rebuild
    // the iframe on every click and defeat the whole point of the shared panel.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [resolvedPath, openNonce, status, iframeLoaded, wikiUrl])

  const handleIframeLoad = () => {
    setIframeLoaded(true)
    const pending = pendingPathRef.current
    pendingPathRef.current = null
    // Consume the baked path exactly once: a later click on the same file must
    // still send, since by then the user has navigated away from it.
    const baked = bakedPathRef.current
    bakedPathRef.current = null
    if (pending && pending !== baked) sendOpenFile(pending)
  }

  // A new embed URL means a fresh document; the load gate must reset with it.
  useEffect(() => { setIframeLoaded(false) }, [embedSrc])

  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      const drag = dragRef.current
      if (!drag) return
      const delta = drag.startX - e.clientX
      setWidth(Math.max(320, Math.min(900, drag.startWidth + delta)))
    }
    const onUp = () => { dragRef.current = null }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
  }, [])

  return (
    <div className="flex flex-row h-full shrink-0 border-l border-hairline" style={{ width }}>
      <div
        className="w-1 cursor-col-resize bg-transparent hover:bg-primary/30 transition-colors shrink-0"
        onMouseDown={e => {
          e.preventDefault()
          dragRef.current = { startX: e.clientX, startWidth: width }
        }}
      />

      <div className="flex flex-col flex-1 min-w-0 bg-canvas">
        <div className="flex items-center justify-between px-3 py-1.5 border-b border-hairline shrink-0">
          <span className="text-[11px] font-bold uppercase tracking-widest text-mute truncate">
            {resolvedPath ? resolvedPath.split('/').pop() : filePath ? filePath.split('/').pop() : 'Files'}
          </span>
          <div className="flex items-center gap-2 shrink-0">
            {externalHref && (
              <a
                href={externalHref}
                target="_blank"
                rel="noreferrer"
                title="Open in wiki-viewer"
                className="p-1 text-mute hover:text-primary transition-colors"
              >
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/>
                </svg>
              </a>
            )}
            <button
              onClick={onClose}
              title="Close panel"
              className="p-1 text-mute hover:text-primary transition-colors"
            >
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                <line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
              </svg>
            </button>
          </div>
        </div>

        {status === 'offline' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
            </svg>
            <p className="text-[12px] text-mute">wiki-viewer is not running</p>
            <p className="text-[11px] text-mute/60">
              Start it with <code className="font-mono bg-surface px-1 py-0.5 rounded text-[10px]">npx wiki-viewer</code>
            </p>
          </div>
        )}

        {status === 'no-key' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/>
            </svg>
            <p className="text-[12px] text-mute">API key required</p>
            <p className="text-[11px] text-mute/60">
              Viewing files from this session needs wiki-viewer's API key. Copy it from
              wiki-viewer's Settings → API Access, then paste it into Settings → Integrations.
            </p>
            <a href={`${wikiUrl}/settings`} target="_blank" rel="noreferrer" className="text-[11px] text-primary hover:underline">
              Open wiki-viewer Settings →
            </a>
          </div>
        )}

        {status === 'bad-key' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/>
            </svg>
            <p className="text-[12px] text-mute">API key rejected</p>
            <p className="text-[11px] text-mute/60">
              wiki-viewer did not accept the stored key. It may have been rotated — copy the
              current one and update Settings → Integrations.
            </p>
          </div>
        )}

        {status === 'bad-root' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><line x1="12" y1="11" x2="12" y2="14"/>
            </svg>
            <p className="text-[12px] text-mute">Session directory unavailable</p>
            <p className="text-[11px] text-mute/60 font-mono break-all">{effectiveRoot}</p>
            <p className="text-[11px] text-mute/60">It no longer exists, or is not a directory.</p>
          </div>
        )}

        {status === 'unresolved-path' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
            </svg>
            <p className="text-[12px] text-mute">Cannot locate this file</p>
            <p className="text-[11px] text-mute/60 font-mono break-all">{filePath}</p>
            <p className="text-[11px] text-mute/60">
              The path is relative and this session reports no working directory, so there is
              nothing to resolve it against. Opening it in a new tab still works.
            </p>
          </div>
        )}

        {status === 'unconfigured' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/>
            </svg>
            <p className="text-[12px] text-mute">wiki-viewer needs a root directory</p>
            <a href={wikiUrl} target="_blank" rel="noreferrer" className="text-[11px] text-primary hover:underline">
              Open wiki-viewer to configure →
            </a>
          </div>
        )}

        {status === 'checking' && (
          <div className="flex items-center justify-center flex-1 gap-2 text-mute/50">
            <span className="inline-block h-3 w-3 rounded-full border-2 border-current border-t-transparent animate-spin" />
            <span className="text-[11px]">Connecting…</span>
          </div>
        )}

        {/* Sits above the iframe rather than replacing it, so a file that fails
            to load does not throw away the file the user is already reading. */}
        {contentError && (
          <div className="shrink-0 border-b border-hairline bg-warning/10 px-3 py-2">
            <p className="text-[11px] text-mute">{contentError.title}</p>
            <p className="text-[11px] text-mute/60 font-mono break-all">{filePath}</p>
            <p className="text-[11px] text-mute/60">{contentError.detail}</p>
          </div>
        )}

        {status === 'ready' && embedSrc && (
          <iframe
            ref={iframeRef}
            src={embedSrc}
            className="flex-1 w-full border-none min-h-0"
            title="wiki-viewer"
            referrerPolicy="no-referrer"
            onLoad={handleIframeLoad}
          />
        )}
      </div>
    </div>
  )
}
