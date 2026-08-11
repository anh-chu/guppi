/** Collapse . and .. segments. wiki-viewer normalizes too, but a path we hand
 *  out has to be comparable against the one we sent last. */
export function normalizePath(path: string): string {
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

/** True when `abs` is `dir` itself or sits beneath it. */
export function isInside(dir: string, abs: string): boolean {
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
 * A root also has to be present at all: the viewer refuses a request without one
 * rather than falling back to a previously used directory, which would render a
 * confidently wrong tree. A narrow root is a small tree; no root is a panel
 * where only the first file of a session works.
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

/**
 * Build a same-origin wiki-viewer embed URL. root is always present.
 * embed=1 and chrome=1 are always set. file is omitted when null.
 */
export function buildWikiSrc({ root, file }: { root: string; file?: string | null }): string {
  const params = new URLSearchParams()
  params.set('embed', '1')
  params.set('chrome', '1')
  params.set('root', root)
  if (file) params.set('file', file)
  return `/wiki?${params.toString()}`
}
