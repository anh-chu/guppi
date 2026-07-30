export type ArtifactKind = 'image' | 'text' | 'other'

const IMAGE_EXTENSIONS = new Set(['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp'])
const TEXT_EXTENSIONS = new Set([
  'md', 'txt', 'log', 'json', 'yaml', 'yml', 'diff', 'patch', 'csv', 'tsv',
  'ts', 'tsx', 'js', 'jsx', 'go', 'py', 'sh', 'toml', 'ini', 'conf', 'xml',
  'html', 'htm',
])

export const ARTIFACT_PREVIEW_TEXT_LIMIT_CHARS = 2000
export const ARTIFACT_PREVIEW_TEXT_LIMIT_LINES = 40

function getArtifactExtension(path: string, name?: string): string {
  const leaf = (name || path).split('/').pop() || path
  const idx = leaf.lastIndexOf('.')
  if (idx < 0 || idx === leaf.length - 1) return ''
  return leaf.slice(idx + 1).toLowerCase()
}

export function getArtifactKind(path: string, name?: string): ArtifactKind {
  const ext = getArtifactExtension(path, name)
  if (IMAGE_EXTENSIONS.has(ext)) return 'image'
  if (TEXT_EXTENSIONS.has(ext)) return 'text'
  return 'other'
}

export async function grantArtifactToken(path: string, session: string, signal?: AbortSignal, host?: string): Promise<string> {
  let qs = `path=${encodeURIComponent(path)}&session=${encodeURIComponent(session)}`
  if (host) qs += `&host=${encodeURIComponent(host)}`
  const res = await fetch(`/file/grant?${qs}`, { method: 'POST', signal })
  if (!res.ok) {
    throw new Error(`HTTP ${res.status}`)
  }
  const data: { token?: string } = await res.json()
  if (!data.token) {
    throw new Error('Missing token')
  }
  return data.token
}

export function truncateArtifactText(text: string): { preview: string; truncated: boolean } {
  const normalized = text.replace(/\r\n/g, '\n')
  let preview = normalized
  let truncated = false
  if (preview.length > ARTIFACT_PREVIEW_TEXT_LIMIT_CHARS) {
    preview = preview.slice(0, ARTIFACT_PREVIEW_TEXT_LIMIT_CHARS)
    truncated = true
  }
  const lines = preview.split('\n')
  if (lines.length > ARTIFACT_PREVIEW_TEXT_LIMIT_LINES) {
    preview = lines.slice(0, ARTIFACT_PREVIEW_TEXT_LIMIT_LINES).join('\n')
    truncated = true
  }
  return { preview, truncated }
}
