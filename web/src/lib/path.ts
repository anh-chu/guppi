// Leaf segment of a filesystem path, with home dirs collapsed to ~.
export function pathLeaf(path?: string): string {
  if (!path) return ''
  const trimmed = path.replace(/[\\/]+$/, '')
  if (/^(\/home\/[^/]+|\/Users\/[^/]+|\/root)$/.test(trimmed)) return '~'
  const parts = trimmed.split(/[\\/]/)
  return parts[parts.length - 1] || trimmed
}

// Project label for sidebar grouping: shows the trailing two path segments
// (parent/leaf) so generic leaves like "app" or "web" stay distinguishable,
// unless the parent is the home root or the user's home dir (then just leaf).
export function projectLabel(path?: string): string {
  if (!path) return 'Sessions'
  const trimmed = path.replace(/[\\/]+$/, '')
  if (/^(\/home\/[^/]+|\/Users\/[^/]+|\/root)$/.test(trimmed)) return '~'
  const parts = trimmed.split(/[\\/]/).filter(Boolean)
  if (parts.length <= 1) return parts[0] || trimmed
  const leaf = parts[parts.length - 1]
  const parent = parts[parts.length - 2]
  const grand = parts[parts.length - 3]
  if (/^(home|Users|root)$/.test(parent)) return leaf
  if (grand && /^(home|Users)$/.test(grand)) return leaf
  return `${parent}/${leaf}`
}
