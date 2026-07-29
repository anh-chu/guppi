/**
 * Getting the bytes for one file. Nothing here knows how a file is rendered.
 *
 * Rendering lives in @wiki-viewer/viewer, reached through the single boundary in
 * components/FileViewerBoundary.tsx. This module answers only two questions:
 * does a kind need text or a URL, and how do we get either one out of
 * termyard's own capability endpoints.
 *
 * The reason this runs on termyard's origin is the whole point of the design: a
 * same-origin <img src="/file?token=..."> carries termyard's session cookie, so
 * images, PDFs and video work with no auth changes at all. A viewer on another
 * origin cannot authenticate to /file and so can never show real bytes; it can
 * only be handed text over postMessage.
 */

/**
 * The kinds @wiki-viewer/viewer can render. Declared here rather than imported
 * so this module and its tests stay free of the package; FileViewerBoundary
 * proves at compile time that the package cannot return a kind missing here.
 */
export type FileKind =
  | 'markdown'
  | 'csv'
  | 'pdf'
  | 'html'
  | 'image'
  | 'media'
  | 'text'
  | 'source'
  | 'binary'

/**
 * Kinds the viewer renders from a string. Everything else is handed a URL and
 * fetched by the browser, which is what makes range requests (video seeking,
 * incremental PDF) work.
 */
const TEXT_KINDS: ReadonlySet<FileKind> = new Set<FileKind>([
  'markdown',
  'csv',
  'html',
  'text',
  'source',
])

export function needsText(kind: FileKind): boolean {
  return TEXT_KINDS.has(kind)
}

/**
 * Mirrors maxFileReadSize in pkg/peer/session.go. A file on another host is
 * relayed whole over the control link, so this is a hard ceiling there. Locally
 * /file streams and has no cap, but a text file larger than this still has no
 * business being turned into a JS string.
 */
export const FILE_SIZE_LIMIT_BYTES = 10 * 1024 * 1024

/** A failure that belongs to ONE file. Never promote this to panel state. */
export interface FileLoadFailure {
  title: string
  detail: string
}

export interface FileLoadSuccess {
  kind: FileKind
  filename: string
  /** Set for text kinds. */
  content?: string
  /** Set for asset kinds. Points at /file?token=, same origin. */
  assetUrl?: string
}

export type FileLoadResult =
  | { ok: true; value: FileLoadSuccess }
  | { ok: false; error: FileLoadFailure }

export function basename(path: string): string {
  const leaf = path.split('/').pop()
  return leaf ? leaf : path
}

function tooLarge(bytes: number): FileLoadFailure {
  return {
    title: 'File too large',
    detail: `${(bytes / (1024 * 1024)).toFixed(1)} MB exceeds the 10 MB limit for reading a file as text.`,
  }
}

/**
 * Turn a grant rejection into something a person can act on.
 *
 * The bodies are plain text from http.Error. handleRemoteFileGrant forwards a
 * peer's reason verbatim as "remote file: <reason>", which is how the 10 MB
 * relay ceiling reaches us: peer/session.go answers "file too large" before it
 * sends anything, so there is no partial transfer to detect, only this string.
 */
export function classifyGrantFailure(status: number, body: string, remote: boolean): FileLoadFailure {
  const text = body.trim()
  if (/file too large/i.test(text)) {
    return {
      title: 'File too large',
      detail: remote
        ? 'Files on another host are relayed over the control link, which is capped at 10 MB.'
        : 'The file exceeds the 10 MB read limit.',
    }
  }
  if (/peer not connected|peer send queue full/i.test(text)) {
    return {
      title: 'Host not reachable',
      detail: 'termyard has no working control link to the host holding this file.',
    }
  }
  if (status === 404) {
    if (remote) {
      return { title: 'File not found', detail: `The host answered: ${text || 'not found'}` }
    }
    return {
      title: 'File not found',
      // A local miss answers the bare string "not found", which only repeats the
      // title back at the user, so say something that adds information instead.
      detail: text && !/^not found$/i.test(text) ? text : 'The path does not exist.',
    }
  }
  if (status === 400) {
    return { title: 'Cannot open this path', detail: text || 'The server rejected the path.' }
  }
  if (status === 401 || status === 403) {
    return {
      title: 'Not authorized',
      detail: 'The termyard session is no longer valid. Reload the page and try again.',
    }
  }
  return {
    title: 'Could not open file',
    detail: text ? `HTTP ${status}: ${text}` : `HTTP ${status}`,
  }
}

/** Same-origin URL for a granted token. The session cookie rides along. */
export function fileUrlForToken(token: string): string {
  return `/file?token=${encodeURIComponent(token)}`
}

/**
 * Mint a capability token for one path.
 *
 * Called per open rather than once per panel: fileGrantTTL is 5 minutes
 * (pkg/server/server.go), so a token minted when the panel opened is dead long
 * before a user gets back to it.
 */
export async function mintFileToken(
  path: string,
  hostId?: string,
  signal?: AbortSignal,
): Promise<{ token: string } | { error: FileLoadFailure }> {
  let qs = `path=${encodeURIComponent(path)}`
  if (hostId) qs += `&host=${encodeURIComponent(hostId)}`
  let res: Response
  try {
    res = await fetch(`/file/grant?${qs}`, { method: 'POST', signal })
  } catch {
    return {
      error: { title: 'Could not open file', detail: 'Network error while requesting a read grant.' },
    }
  }
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    return { error: classifyGrantFailure(res.status, body, !!hostId) }
  }
  const data = (await res.json().catch(() => null)) as { token?: string } | null
  if (!data || !data.token) {
    return { error: { title: 'Could not open file', detail: 'The grant response carried no token.' } }
  }
  return { token: data.token }
}

/** Fetch a granted file as text, refusing oversized bodies before decoding. */
export async function fetchFileText(
  token: string,
  signal?: AbortSignal,
): Promise<{ content: string } | { error: FileLoadFailure }> {
  let res: Response
  try {
    res = await fetch(fileUrlForToken(token), { signal })
  } catch {
    return { error: { title: 'File read failed', detail: 'Network error while fetching the contents.' } }
  }
  if (!res.ok) {
    if (res.status === 403) {
      return {
        error: {
          title: 'Read grant expired',
          detail: 'Grants last 5 minutes. Reload to mint a fresh one.',
        },
      }
    }
    return { error: { title: 'File read failed', detail: `HTTP ${res.status}` } }
  }
  // Check the declared size first: it is free, and it is the difference between
  // a message and a wedged tab on a multi-gigabyte log.
  const declared = Number(res.headers.get('Content-Length'))
  if (Number.isFinite(declared) && declared > FILE_SIZE_LIMIT_BYTES) {
    return { error: tooLarge(declared) }
  }
  const buf = await res.arrayBuffer()
  if (buf.byteLength > FILE_SIZE_LIMIT_BYTES) return { error: tooLarge(buf.byteLength) }
  return { content: new TextDecoder().decode(buf) }
}

/**
 * Load one file for the viewer: mint, then either fetch text or hand back a URL.
 *
 * Deliberately has no memory of previous calls. A guard that remembered would
 * silently ignore the second file a user opens, which is exactly the bug this
 * shape exists to prevent.
 */
export async function loadFile(args: {
  path: string
  kind: FileKind
  hostId?: string
  signal?: AbortSignal
}): Promise<FileLoadResult> {
  const { path, kind, hostId, signal } = args
  const filename = basename(path)

  const minted = await mintFileToken(path, hostId, signal)
  if ('error' in minted) return { ok: false, error: minted.error }

  if (!needsText(kind)) {
    return { ok: true, value: { kind, filename, assetUrl: fileUrlForToken(minted.token) } }
  }

  const read = await fetchFileText(minted.token, signal)
  if ('error' in read) return { ok: false, error: read.error }
  return { ok: true, value: { kind, filename, content: read.content } }
}
