interface CacheEntry { exists: boolean; expires: number }
const cache = new Map<string, CacheEntry>()
const TTL_MS = 30_000

/**
 * Ask the server whether `path` resolves to an existing file, so the
 * terminal's file-link highlighter can skip paths it already knows are dead
 * ends. Hits GET /file/exists, which is read-only: unlike /file/grant it
 * mints no capability token, just an os.Stat behind the same path
 * resolution (relative paths against the session's active pane cwd, ~/
 * against the server's home dir).
 *
 * Fails open (reports "exists") on network errors or when the session
 * lives on a remote host, so a flaky connection or an unreachable peer
 * degrades to the old "highlight everything" behavior rather than hiding
 * every link in the terminal.
 */
export async function checkPathExists(path: string, session?: string, hostId?: string): Promise<boolean> {
  const key = `${hostId || ''}\u0000${session || ''}\u0000${path}`
  const now = Date.now()
  const cached = cache.get(key)
  if (cached && cached.expires > now) return cached.exists

  try {
    const qs = new URLSearchParams({ path, session: session || '', host: hostId || '' })
    const res = await fetch(`/file/exists?${qs}`)
    const exists = res.ok ? Boolean((await res.json()).exists) : true
    cache.set(key, { exists, expires: now + TTL_MS })
    return exists
  } catch {
    return true
  }
}
