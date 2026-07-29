/**
 * Path arithmetic for the side panel.
 *
 * Lives in lib/ rather than in the component so its tests do not have to load
 * the viewer package, and with it tiptap, mermaid and highlight.js, to check
 * string handling.
 */

/** Collapse . and .. segments. */
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

/**
 * Make a path from terminal output absolute.
 *
 * Every non-absolute form is joined against the cwd, not just ./ and ../ .
 * Bare relative paths like app/(public)/sign-in.tsx are the common shape in
 * terminal output.
 *
 * ~/ is left alone: only a shell can expand it, and guessing $HOME from the cwd
 * would be wrong for any session outside the user's home. FilePane reports that
 * case rather than guessing.
 */
export function resolveFilePath(path: string, cwd?: string): string {
  if (path.startsWith('/')) return normalizePath(path)
  if (path.startsWith('~/')) return path
  if (cwd && cwd.startsWith('/')) return normalizePath(cwd + '/' + path)
  return path
}

/** True when `abs` is `dir` itself or sits beneath it. */
export function isInside(dir: string, abs: string): boolean {
  const d = dir.replace(/\/+$/, '')
  // The trailing slash is load-bearing: a bare startsWith would treat
  // /home/sil/guppi-secrets as living inside /home/sil/guppi.
  return abs === d || abs.startsWith(d + '/')
}

/**
 * The root handed to wiki-viewer for the browse surface. It MUST contain the
 * file we want it to open: wiki-viewer relativizes against the root and answers
 * "Invalid path" when the file falls outside, which renders as its empty
 * "select a file to view or edit" screen.
 *
 * The session cwd is preferred because it yields a useful tree, but only when it
 * actually contains the file. A terminal sitting in one repo routinely prints
 * paths belonging to another.
 *
 * A root also has to be present at all: wiki-viewer grants a non-loopback parent
 * postMessage trust only when the host supplied one, and termyard's documented
 * deployment sits behind Tailscale or a reverse proxy.
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
